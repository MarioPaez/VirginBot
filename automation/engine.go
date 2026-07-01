package automation

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
)

// Booking policy: PRECISE, not continuous polling. A class's slots open when the
// booking window opens (calisthenics 7 days before, solarium 2), at the class
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
	horizonDays     = 8                // covers the longest window (calisthenics, 7 days)
)

// Engine runs the rules of ALL users with precise fires.
type Engine struct {
	store    *Store
	fetchDay func(userID int64, date string) ([]calendar.Class, error) // fresh fetch of a day (authenticated, curated)
	book     func(userID int64, clubID, classID, sessionID int, date string) error
	notify   func(userID int64, subject, body string)
	lang     func(userID int64) string // user's language for emails (it/es/en); nil = Default
	loc      *time.Location
	rounds   int           // attempts per fire
	lead     time.Duration // lead after the hour before the 1st attempt
	gap1     time.Duration // wait between the 1st and 2nd attempt
	gap      time.Duration // wait between the following attempts
	debug    bool          // VIRGINBOT_DEBUG: logs every evaluation (for local observation)

	mu    sync.Mutex
	state map[string]*occState // key: userID|ruleID|date
}

type occState struct {
	booked       bool
	attempts     int
	lastError    string
	missNotified bool
	lastFired    time.Time // last fire already executed (avoids repeating it)
}

func NewEngine(
	store *Store,
	fetchDay func(int64, string) ([]calendar.Class, error),
	book func(int64, int, int, int, string) error,
	notify func(int64, string, string),
	lang func(int64) string,
) *Engine {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	if notify == nil {
		notify = func(int64, string, string) {}
	}
	return &Engine{
		store: store, fetchDay: fetchDay, book: book, notify: notify, lang: lang, loc: loc,
		rounds: envInt("VIRGINBOT_ATTEMPTS", defaultAttempts),
		lead:   time.Duration(envInt("VIRGINBOT_LEAD_SEC", defaultLeadSec)) * time.Second,
		gap1:   time.Duration(envInt("VIRGINBOT_GAP1_SEC", defaultGap1Sec)) * time.Second,
		gap:    time.Duration(envInt("VIRGINBOT_GAP_SEC", defaultGapSec)) * time.Second,
		debug:  os.Getenv("VIRGINBOT_DEBUG") != "",
		state:  map[string]*occState{},
	}
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
	rule  Rule
	date  string
	start time.Time
}

func (o occ) stateKey() string {
	return fmt.Sprintf("%d|%s|%s", o.rule.UserID, o.rule.ID, o.date)
}

func windowOf(o occ) int {
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
	for {
		now := time.Now().In(e.loc)
		occs := e.occurrences(now)
		if e.debug {
			log.Printf("[debug] %d pending occurrence(s)", len(occs))
		}
		for _, o := range occs {
			e.maybeFire(o, now)
		}
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
			start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.Start, e.loc)
			if err != nil || start.Before(now) {
				continue
			}
			o := occ{rule: r, date: date, start: start}
			if st := e.get(o.stateKey()); st != nil && st.booked {
				continue
			}
			out = append(out, o)
		}
	}
	return out
}

// maybeFire fires an attempt when due: there's a fire (class time) that's past
// and not yet executed. After the last fire (24h before) without success, it
// notifies by email.
func (e *Engine) maybeFire(o occ, now time.Time) {
	st := e.ensureState(o)
	if st.booked {
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

	// Fallback: rule created with the window already open and WITHOUT future fires
	// (in the last 24h before the class). Try once immediately, don't wait.
	if dueT.IsZero() && !hasFuture && st.lastFired.IsZero() && st.attempts == 0 {
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

	// If this was the last fire (at 24h) and it didn't succeed, notify once.
	last := o.start.Add(-24 * time.Hour)
	if !st.booked && !st.missNotified && !dueT.Before(last) {
		st.missNotified = true
		lang := e.langOf(o.rule.UserID)
		reason := st.lastError
		if reason == "" {
			reason = i18n.T(lang, "email.failed.noreason")
		}
		e.notify(o.rule.UserID,
			i18n.T(lang, "email.failed.subject", o.rule.Name),
			i18n.T(lang, "email.failed.body",
				classLines(lang, o.rule.Name, o.rule.Club, o.start), st.attempts, reason))
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
	for round := 0; round < e.rounds && !st.booked; round++ {
		classes, err := e.fetchDay(o.rule.UserID, o.date)
		if err != nil {
			st.lastError = err.Error()
			log.Printf("automation: %s — round %d/%d: ERROR fetching the calendar: %v", tag, round+1, e.rounds, err)
		} else {
			matches := findMatches(classes, o.rule)
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
					if err := e.book(o.rule.UserID, c.ClubID, c.ClassID, c.SessionID, o.date); err != nil {
						st.lastError = err.Error()
						log.Printf("automation: %s — round %d/%d bed %d/%d: booking FAILED: %v", tag, round+1, e.rounds, bi+1, len(beds), err)
						continue
					}
					st.booked = true
					log.Printf("automation: ✓ %s — BOOKED (bed %d/%d, attempt %d)", tag, bi+1, len(beds), st.attempts)
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
	// failure email and a retry that would double-book).
	if !st.booked && st.attempts > 0 {
		if classes, err := e.fetchDay(o.rule.UserID, o.date); err == nil {
			if bookedMatch(findMatches(classes, o.rule)) != nil {
				st.booked = true
				log.Printf("automation: ✓ %s — BOOKED (confirmed in final reconciliation, despite the API error)", tag)
				e.notifyBooked(o, st)
			}
		}
	}
	if !st.booked {
		log.Printf("automation: %s — fire finished WITHOUT booking after %d rounds (last reason: %s)", tag, e.rounds, st.lastError)
	}
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

// findMatches returns ALL instances matching the rule. The Solarium exposes
// several "beds" at the same time (each a distinct bookable class), so a rule can
// match more than one.
func findMatches(classes []calendar.Class, r Rule) []*calendar.Class {
	var out []*calendar.Class
	for i := range classes {
		c := &classes[i]
		if strings.EqualFold(c.Name, r.Name) && strings.EqualFold(c.Club, r.Club) && c.Start == r.Start {
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

// notifyBooked notifies the member of a successful booking (includes the attempt
// count if any: direct booking, confirmation after an API error, or reconciliation).
func (e *Engine) notifyBooked(o occ, st *occState) {
	lang := e.langOf(o.rule.UserID)
	body := i18n.T(lang, "email.booked.body", classLines(lang, o.rule.Name, o.rule.Club, o.start))
	if st.attempts > 0 {
		body += i18n.T(lang, "email.attempts", st.attempts)
	}
	body += i18n.T(lang, "email.booked.outro")
	e.notify(o.rule.UserID, i18n.T(lang, "email.booked.subject", o.rule.Name), body)
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
