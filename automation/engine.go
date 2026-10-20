package automation

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
)

// Booking policy: PRECISE, not continuous polling. A class's slots open when the
// booking window opens (8 days before; solarium, 2), at the class
// time. So we only try at those instants —at the class time, each day from the
// window opening until 24h before— instead of hammering Virgin every few minutes
// (inefficient and a reason to get blocked).
// Trigger parameters. Virgin's window sometimes opens a few seconds AFTER the
// exact class time, so we allow an initial lead and several spaced attempts.
// Tunable via env (VIRGINBOT_ATTEMPTS/GAP_SEC/LEAD_SEC) without recompiling,
// because the real delay is empirical.
const (
	defaultAttempts = 9                // attempts per fire (~1 min window with the gaps below)
	defaultLeadSec  = 0                // lead after the hour before the 1st attempt (0 = at the exact hour)
	defaultGap1Sec  = 4                // wait between the 1st and 2nd attempt (short: in case it opens a few seconds late)
	defaultGapSec   = 8                // wait between the following attempts
	maxIdle         = 5 * time.Minute  // re-evaluate the schedule (no network) to pick up new rules
	triggerGrace    = 15 * time.Minute // a fire only counts if we're within <=15min of its time (doesn't recover old fires)
	horizonDays     = 9                // covers the longest window (8 days) with margin
	// Fires due at the same instant run concurrently (a fire without a spot takes
	// ~1 min of rounds: in series, the next user's opening fire would start late
	// and lose the spot), but capped so vapi never sees a burst.
	maxParallelFires = 4
)

// Engine runs the rules of ALL users with precise fires.
type Engine struct {
	store        *Store
	fetchDay     func(userID int64, club, date string) ([]calendar.Class, error) // fresh fetch of a club's day (authenticated, curated)
	book         func(userID int64, clubID, classID, sessionID int, date string) error
	push         func(userID int64, title, body, url string) // booking outcomes on the user's devices (SetPush); nil = silent
	lang         func(userID int64) string                   // user's language for notifications (it/es/en); nil = Default
	memberWindow func(userID int64) int                      // member's booking window in minutes (0 = unknown); nil = class windows only
	loc          *time.Location
	rounds       int           // attempts per fire
	lead         time.Duration // lead after the hour before the 1st attempt
	gap1         time.Duration // wait between the 1st and 2nd attempt
	gap          time.Duration // wait between the following attempts
	debug        bool          // VIRGINBOT_DEBUG: logs every evaluation (for local observation)

	onBooked func(userID int64, r Rule, date string) // a booking went through (history: booked by the bot)
	// onMissed runs after the last fire of an occurrence that's still full
	// (status "waitlist" or "full"); it returns true if it took over (joined the
	// waitlist, started a watch) and will tell the user itself.
	onMissed func(userID int64, r Rule, date, status string) bool

	lastCycle atomic.Int64 // unix time of the last completed scheduling cycle (health check)

	mu    sync.Mutex
	state map[string]*occState // key: userID|ruleID|date; persisted in the store
}

type occState struct {
	booked       bool
	waitlisted   bool // the spot went at the last second and Virgin waitlisted us instead: stop, they'll promote us
	attempts     int
	lastError    string
	missNotified bool
	lastFired    time.Time // last fire already executed (avoids repeating it)
	lastStatus   string    // class status seen in the last round (not persisted)
}

// ErrWaitlisted is what the book function returns when Virgin put the member on
// the class's waitlist instead of booking (the spot went between the calendar
// fetch and the POST).
type ErrWaitlisted struct{ Position int }

func (e ErrWaitlisted) Error() string {
	return fmt.Sprintf("joined the waitlist instead (position %d)", e.Position)
}

func NewEngine(
	store *Store,
	fetchDay func(int64, string, string) ([]calendar.Class, error),
	book func(int64, int, int, int, string) error,
	lang func(int64) string,
) *Engine {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	return &Engine{
		store: store, fetchDay: fetchDay, book: book, lang: lang, loc: loc,
		rounds: envInt("VIRGINBOT_ATTEMPTS", defaultAttempts),
		lead:   time.Duration(envInt("VIRGINBOT_LEAD_SEC", defaultLeadSec)) * time.Second,
		gap1:   time.Duration(envInt("VIRGINBOT_GAP1_SEC", defaultGap1Sec)) * time.Second,
		gap:    time.Duration(envInt("VIRGINBOT_GAP_SEC", defaultGapSec)) * time.Second,
		debug:  os.Getenv("VIRGINBOT_DEBUG") != "",
		state:  map[string]*occState{},
	}
}

// SetPush sets how the booking outcomes reach the user (Web Push to their
// devices). Not set = no notifications.
func (e *Engine) SetPush(fn func(userID int64, title, body, url string)) { e.push = fn }

// SetMemberWindow sets where the engine reads each member's booking window, so
// it doesn't fire before the window opens for members without priority booking.
func (e *Engine) SetMemberWindow(fn func(userID int64) int) { e.memberWindow = fn }

// SetOnBooked sets what runs after each booking the engine makes.
func (e *Engine) SetOnBooked(fn func(userID int64, r Rule, date string)) { e.onBooked = fn }

// SetOnMissed sets what runs when an occurrence is still full at its last fire.
func (e *Engine) SetOnMissed(fn func(userID int64, r Rule, date, status string) bool) {
	e.onMissed = fn
}

// LastCycle is when the engine last completed a scheduling cycle (zero before
// the first one).
func (e *Engine) LastCycle() time.Time {
	if u := e.lastCycle.Load(); u != 0 {
		return time.Unix(u, 0)
	}
	return time.Time{}
}

// pushOutcome sends the short version of a booking outcome to the user's devices.
func (e *Engine) pushOutcome(o occ, url, titleKey string, args ...any) {
	if e.push == nil {
		return
	}
	lang := e.langOf(o.rule.UserID)
	e.push(o.rule.UserID, i18n.T(lang, titleKey, args...),
		i18n.T(lang, "push.class", o.rule.Name, o.rule.Club, i18n.FormatDateTime(lang, o.start)), url)
}

// envInt reads a positive integer from an environment variable, or returns def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// occ is a concrete occurrence of a rule (a specific day) for a user.
type occ struct {
	rule   Rule
	date   string
	start  time.Time
	window int // days before the class its booking opens for this member; 0 = the rule's
}

func (o occ) stateKey() string {
	return fmt.Sprintf("%d|%s|%s", o.rule.UserID, o.rule.ID, o.date)
}

func windowOf(o occ) int {
	if o.window > 0 {
		return o.window
	}
	if o.rule.OpensDaysBefore > 0 {
		return o.rule.OpensDaysBefore
	}
	return WindowDays(o.rule.Name)
}

// triggers are the instants at which we try to book the occurrence: at the class
// time, each day from the window opening (T - window) until 24h before.
func (o occ) triggers() []time.Time {
	w := windowOf(o)
	ts := make([]time.Time, 0, w)
	for k := w; k >= 1; k-- { // T-w, T-(w-1), ..., T-1  (ascending)
		ts = append(ts, o.start.AddDate(0, 0, -k)) // AddDate keeps the wall-clock time even across DST changes
	}
	return ts
}

// Run sleeps until the next fire and only then touches the network. Between fires
// it wakes at most every maxIdle to pick up new rules (without calling Virgin).
func (e *Engine) Run(stop <-chan struct{}) {
	if e.store != nil {
		loaded := e.store.loadStates(time.Now().In(e.loc).Format("2006-01-02"))
		e.mu.Lock()
		for k, st := range loaded {
			e.state[k] = st
		}
		e.mu.Unlock()
		log.Printf("automation: restored the state of %d occurrence(s)", len(loaded))
	}
	for {
		now := time.Now().In(e.loc)
		occs := e.occurrences(now)
		if e.debug {
			log.Printf("[debug] %d pending occurrence(s)", len(occs))
		}
		e.fireDue(occs, now)
		e.lastCycle.Store(time.Now().Unix())
		sleep := e.untilNext(now, occs)
		if e.debug {
			log.Printf("[debug] next attempt in %s", sleep.Round(time.Second))
		}
		select {
		case <-stop:
			return
		case <-time.After(sleep):
		}
	}
}

// occurrences computes, across every user's rules, the upcoming unbooked
// occurrences (without touching the network: only by weekday and time).
func (e *Engine) occurrences(now time.Time) []occ {
	var out []occ
	for _, r := range e.store.ListAll() {
		if !r.Enabled {
			continue
		}
		for d := 0; d <= horizonDays; d++ {
			day := now.AddDate(0, 0, d)
			if int(day.Weekday()) != r.Weekday {
				continue
			}
			date := day.Format("2006-01-02")
			if !r.Covers(date) { // one-off rule: only its own week
				continue
			}
			start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.Start, e.loc)
			if err != nil || start.Before(now) {
				continue
			}
			o := occ{rule: r, date: date, start: start}
			if e.memberWindow != nil {
				o.window = min(windowOf(o), EffectiveWindow(r.Name, e.memberWindow(r.UserID)))
			}
			if st := e.get(o.stateKey()); st != nil && (st.booked || st.waitlisted) {
				continue
			}
			if e.store.Skipped(r.UserID, r.ID, date) { // cancelled by the user: don't re-book
				continue
			}
			out = append(out, o)
		}
	}
	return out
}

// fireDue runs maybeFire for every occurrence, the due ones concurrently (at
// most maxParallelFires at a time), and waits for all of them.
func (e *Engine) fireDue(occs []occ, now time.Time) {
	sem := make(chan struct{}, maxParallelFires)
	var wg sync.WaitGroup
	for _, o := range occs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			e.maybeFire(o, now)
		}()
	}
	wg.Wait()
}

// maybeFire fires an attempt when due: there's a fire (class time) that's past
// and not yet executed. After the last fire (24h before) without success, it
// notifies the user.
func (e *Engine) maybeFire(o occ, now time.Time) {
	st := e.ensureState(o)
	if st.booked || st.waitlisted {
		return
	}
	trs := o.triggers()

	// Most recent past fire not yet executed (if we skipped several while stopped,
	// run only one to catch up).
	var dueT time.Time
	hasFuture := false
	for _, t := range trs {
		if t.After(now) {
			hasFuture = true
		} else if t.After(st.lastFired) && now.Sub(t) <= triggerGrace {
			dueT = t // recently due (at its time) and not yet executed; doesn't recover old fires
		}
	}

	// Fallback: rule JUST created with the window already open and WITHOUT future
	// fires (in the last 24h before the class). Try once immediately, don't wait.
	// Gated on the rule's age because occState is in memory: without it every
	// restart would re-fire it.
	if dueT.IsZero() && !hasFuture && st.lastFired.IsZero() && st.attempts == 0 &&
		now.Sub(o.rule.Created) <= triggerGrace {
		open := o.start.AddDate(0, 0, -windowOf(o))
		if !now.Before(open) && now.Before(o.start) {
			dueT = now
		}
	}
	if dueT.IsZero() {
		return
	}

	log.Printf("automation: u%d ▶ FIRE %s · %s @ %s (class on %s) — attempting to book",
		o.rule.UserID, o.rule.Name, o.rule.Club, o.rule.Start, o.date)
	e.attempt(o, st)
	st.lastFired = dueT

	// If this was the last fire (at 24h) and it didn't succeed, notify once —
	// unless the user asked to fall back to the waitlist or a watch and that
	// took over.
	last := o.start.Add(-24 * time.Hour)
	if !st.booked && !st.waitlisted && !st.missNotified && !dueT.Before(last) {
		st.missNotified = true
		full := st.lastStatus == "waitlist" || st.lastStatus == "full"
		if !(full && e.onMissed != nil && e.onMissed(o.rule.UserID, o.rule, o.date, st.lastStatus)) {
			e.pushOutcome(o, "/?view=automations", "push.failed.title")
		}
	}
	e.persist(o, st)
}

// persist saves the occurrence's state (survives restarts and deploys).
func (e *Engine) persist(o occ, st *occState) {
	if e.store != nil {
		e.store.saveState(o.stateKey(), o.date, *st)
	}
}

// record logs the outcome of a fire.
func (e *Engine) record(o occ, outcome, detail string) {
	if e.store != nil {
		e.store.RecordAttempt(Attempt{UserID: o.rule.UserID, Name: o.rule.Name, Club: o.rule.Club,
			Date: o.date, Start: o.rule.Start, Source: "engine", Outcome: outcome, Detail: detail})
	}
}

// attempt runs up to e.rounds rounds (fetch + book) spaced by e.gap. The 2nd
// round covers a transient failure or a small delay in Virgin marking the class
// bookable right as the window opens.
func (e *Engine) attempt(o occ, st *occState) {
	tag := fmt.Sprintf("u%d %s %s @ %s", o.rule.UserID, o.rule.Name, o.date, o.rule.Start)
	if e.lead > 0 {
		log.Printf("automation: %s — waiting %s after the hour (the window sometimes opens a few seconds late)", tag, e.lead)
		time.Sleep(e.lead)
	}
	failed := false // a calendar or booking call errored (vs. simply not bookable)
	for round := 0; round < e.rounds && !st.booked; round++ {
		classes, err := e.fetchDay(o.rule.UserID, o.rule.Club, o.date)
		if err != nil {
			st.lastError = err.Error()
			failed = true
			log.Printf("automation: %s — round %d/%d: ERROR fetching the calendar: %v", tag, round+1, e.rounds, err)
		} else {
			matches := findMatches(classes, o.rule, o.date)
			if len(matches) > 0 {
				st.lastStatus = bestStatus(matches)
			}
			switch {
			case len(matches) == 0:
				st.lastError = "not in the calendar yet"
				log.Printf("automation: %s — round %d/%d: the class isn't in the calendar yet", tag, round+1, e.rounds)
			case bookedMatch(matches) != nil:
				st.booked = true
				if st.attempts > 0 {
					// We had tried to book: the booking DID go through (even though the
					// API returned an error). We confirm it as a success.
					log.Printf("automation: ✓ %s — BOOKED (confirmed after %d attempt(s), despite the API error)", tag, st.attempts)
					e.record(o, OutcomeBooked, "confirmed after an API error")
					e.notifyBooked(o, st)
				} else {
					log.Printf("automation: %s — already booked, nothing to do", tag)
				}
				return
			default:
				// The Solarium exposes several bookable "beds" at the same time. We try
				// the ones the calendar marks bookable, one by one, until the POST
				// accepts: that status sometimes lies (TOO_LATE_TO_BOOK).
				beds := bookableBeds(matches)
				if len(beds) == 0 {
					st.lastError = "not bookable (status=" + matches[0].Status + ")"
					log.Printf("automation: %s — round %d/%d: not bookable (status=%q, e.g. full or window not open)", tag, round+1, e.rounds, matches[0].Status)
					break
				}
				log.Printf("automation: %s — round %d/%d: %d bookable bed(s), trying…", tag, round+1, e.rounds, len(beds))
				for bi, c := range beds {
					st.attempts++
					log.Printf("automation: %s — round %d/%d bed %d/%d (classId=%d sessionId=%d), booking…", tag, round+1, e.rounds, bi+1, len(beds), c.ClassID, c.SessionID)
					err := e.book(o.rule.UserID, c.ClubID, c.ClassID, c.SessionID, o.date)
					var wl ErrWaitlisted
					if errors.As(err, &wl) {
						st.waitlisted = true
						log.Printf("automation: %s — the spot went: WAITLISTED at position %d, stopping", tag, wl.Position)
						e.record(o, OutcomeWaitlisted, fmt.Sprintf("position %d", wl.Position))
						e.pushOutcome(o, "/?view=bookings", "push.waitlisted.title", wl.Position)
						return
					}
					if err != nil {
						st.lastError = err.Error()
						failed = true
						log.Printf("automation: %s — round %d/%d bed %d/%d: booking FAILED: %v", tag, round+1, e.rounds, bi+1, len(beds), err)
						continue
					}
					st.booked = true
					log.Printf("automation: ✓ %s — BOOKED (bed %d/%d, attempt %d)", tag, bi+1, len(beds), st.attempts)
					e.record(o, OutcomeBooked, fmt.Sprintf("round %d", round+1))
					e.notifyBooked(o, st)
					return
				}
			}
		}
		if round < e.rounds-1 {
			wait := e.gap // 7s between the following attempts
			if round == 0 {
				wait = e.gap1 // 5s between the 1st and 2nd
			}
			if wait > 0 {
				log.Printf("automation: %s — waiting %s for round %d/%d", tag, wait, round+2, e.rounds)
				time.Sleep(wait)
			}
		}
	}
	// Final reconciliation: if on the LAST round book returned an error but the
	// booking went through, there's no next round to detect it via overlay. We
	// query once more to confirm before declaring the fire failed (avoids a false
	// failure notification and a retry that would double-book).
	if !st.booked && st.attempts > 0 {
		if classes, err := e.fetchDay(o.rule.UserID, o.rule.Club, o.date); err == nil {
			if bookedMatch(findMatches(classes, o.rule, o.date)) != nil {
				st.booked = true
				log.Printf("automation: ✓ %s — BOOKED (confirmed in final reconciliation, despite the API error)", tag)
				e.record(o, OutcomeBooked, "confirmed in the final reconciliation")
				e.notifyBooked(o, st)
			}
		}
	}
	if !st.booked {
		log.Printf("automation: %s — fire finished WITHOUT booking after %d rounds (last reason: %s)", tag, e.rounds, st.lastError)
		outcome := OutcomeUnavailable
		if failed {
			outcome = OutcomeError
		}
		e.record(o, outcome, st.lastError)
	}
}

// bestStatus is the most bookable status among a class's instances (beds).
func bestStatus(matches []*calendar.Class) string {
	rank := map[string]int{"booked": 5, "bookable": 4, "waitlist": 3, "full": 2}
	best := matches[0].Status
	for _, c := range matches[1:] {
		if rank[c.Status] > rank[best] {
			best = c.Status
		}
	}
	return best
}

// untilNext returns how long to sleep: until the next fire, or maxIdle if it's
// farther away (to refresh the schedule and pick up new rules without touching
// the network).
func (e *Engine) untilNext(now time.Time, occs []occ) time.Duration {
	var next time.Time
	for _, o := range occs {
		for _, t := range o.triggers() {
			if t.After(now) && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
	}
	sleep := maxIdle
	if !next.IsZero() {
		if d := next.Sub(now); d < sleep {
			sleep = d
		}
	}
	if sleep < time.Second {
		sleep = time.Second
	}
	return sleep
}

// findMatches returns ALL instances matching the rule on the given date. The
// Solarium exposes several "beds" at the same time (each a distinct bookable
// class), so a rule can match more than one. The date check is deliberate and
// NOT redundant with fetchDay's range: vapi has been observed to bleed a
// neighboring day's session into a single-day query (same name/club/start every
// day), which would otherwise get booked under the wrong date.
func findMatches(classes []calendar.Class, r Rule, date string) []*calendar.Class {
	var out []*calendar.Class
	for i := range classes {
		c := &classes[i]
		if c.Date == date && strings.EqualFold(c.Name, r.Name) && strings.EqualFold(c.Club, r.Club) && c.Start == r.Start {
			out = append(out, c)
		}
	}
	return out
}

// bookedMatch returns the first instance already booked by the member, if any
// (one is enough: all beds share name+club+date+time in the overlay).
func bookedMatch(matches []*calendar.Class) *calendar.Class {
	for _, c := range matches {
		if c.Booked {
			return c
		}
	}
	return nil
}

// bookableBeds filters the instances the calendar marks bookable and with valid
// ids. The engine tries them in order until the booking POST accepts.
func bookableBeds(matches []*calendar.Class) []*calendar.Class {
	var out []*calendar.Class
	for _, c := range matches {
		if c.Status == "bookable" && c.ClassID != 0 && c.SessionID != 0 {
			out = append(out, c)
		}
	}
	return out
}

// langOf resolves the user's language (it/es/en) with fallback to Default.
func (e *Engine) langOf(userID int64) i18n.Lang {
	if e.lang == nil {
		return i18n.Default
	}
	return i18n.Coerce(e.lang(userID))
}

// notifyBooked tells the member about a successful booking (direct, confirmed
// after an API error, or found in the final reconciliation).
func (e *Engine) notifyBooked(o occ, st *occState) {
	if e.onBooked != nil {
		e.onBooked(o.rule.UserID, o.rule, o.date)
	}
	e.pushOutcome(o, "/?view=bookings", "push.booked.title")
}

// ---- state ----

func (e *Engine) get(key string) *occState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state[key]
}

func (e *Engine) ensureState(o occ) *occState {
	key := o.stateKey()
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.state[key]
	if st == nil {
		st = &occState{}
		e.state[key] = st
	}
	return st
}
