package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
	"github.com/MarioPaez/VirginBot/vapi"
)

// Watches: a user watches a full class and the server books it as soon as a
// spot frees up. Every user shares Virgin's view of our IP, so polling is
// gentle and bounded: one call per club and day (whoever watches it), spaced
// out while the class is far, and at most watchPollsPerTick calls a minute in
// total.
const (
	maxWatchesPerUser = 3
	watchTick         = time.Minute
	watchPollsPerTick = 2
	watchFar          = 15 * time.Minute // poll period more than a day before the class
	watchMid          = 5 * time.Minute  // ... within the last day
	watchNear         = 2 * time.Minute  // ... within the last 3 hours (cancellations peak)
	watchMaxErrors    = 3                // consecutive failed bookings before giving up on a watch
)

// watchCutoff is how long before the class watching stops: booking later could
// leave the user with a class they can't cancel without a late-cancellation
// penalty. VIRGINBOT_WATCH_CUTOFF_MIN overrides it.
func watchCutoff() time.Duration {
	if n, err := strconv.Atoi(os.Getenv("VIRGINBOT_WATCH_CUTOFF_MIN")); err == nil && n >= 0 {
		return time.Duration(n) * time.Minute
	}
	return 2 * time.Hour
}

var (
	errWatchLimit = fmt.Errorf("you can watch up to %d classes at a time", maxWatchesPerUser)
	errWatchLate  = errors.New("too close to the class to watch it")
)

// watch is a class occurrence a user is waiting a spot for.
type watch struct {
	UserID  int64  `json:"-"`
	Name    string `json:"name"`
	Club    string `json:"club"`
	Date    string `json:"date"`
	Start   string `json:"start"`
	Created string `json:"created"`
}

func (w watch) key() string { return w.Date + "|" + w.Club + "|" + w.Name + "|" + w.Start }

// startOf is the class's start time, ok=false on malformed data.
func (s *Server) startOf(date, start string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+start, s.loc)
	return t, err == nil
}

// addWatch starts watching an occurrence (idempotent), enforcing the per-user
// cap and the cutoff.
func (s *Server) addWatch(userID int64, name, club, date, start string) error {
	at, ok := s.startOf(date, start)
	if !ok {
		return errors.New("invalid date or time")
	}
	if time.Now().After(at.Add(-watchCutoff())) {
		return errWatchLate
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM watches WHERE user_id = ? AND NOT (name = ? AND club = ? AND date = ? AND start = ?)`,
		userID, name, club, date, start).Scan(&n)
	if n >= maxWatchesPerUser {
		return errWatchLimit
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO watches (user_id, name, club, date, start, created) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, name, club, date, start, time.Now().Format(time.RFC3339))
	return err
}

func (s *Server) removeWatch(userID int64, name, club, date, start string) {
	if _, err := s.db.Exec(`DELETE FROM watches WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		userID, name, club, date, start); err != nil {
		log.Printf("remove watch u%d: %v", userID, err)
	}
}

// listWatches returns the watches of one user (userID > 0) or of everyone (0),
// oldest first (first come, first served when a spot frees up).
func (s *Server) listWatches(userID int64) []watch {
	q := `SELECT user_id, name, club, date, start, created FROM watches`
	var args []any
	if userID > 0 {
		q += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.db.Query(q+` ORDER BY created`, args...)
	if err != nil {
		log.Printf("list watches: %v", err)
		return []watch{}
	}
	defer rows.Close()
	out := []watch{}
	for rows.Next() {
		var w watch
		if rows.Scan(&w.UserID, &w.Name, &w.Club, &w.Date, &w.Start, &w.Created) == nil {
			out = append(out, w)
		}
	}
	return out
}

// markWatched flags the classes the user is watching (calendar overlay).
func (s *Server) markWatched(userID int64, classes []calendar.Class) {
	watched := map[string]bool{}
	for _, w := range s.listWatches(userID) {
		watched[w.key()] = true
	}
	for i := range classes {
		c := &classes[i]
		c.Watched = watched[c.Date+"|"+c.Club+"|"+c.Name+"|"+c.Start]
	}
}

// handleWatches: GET the user's watches, POST a class to watch, DELETE to stop.
func (s *Server) handleWatches(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	if r.Method == http.MethodGet {
		writeJSON(w, s.listWatches(userID))
		return
	}
	var req bookRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Name == "" || req.Club == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if s.isBooked(userID, req.Name, req.Club, req.Date, req.Start) {
			writeJSON(w, map[string]any{"ok": false, "error": "you already have this class booked"})
			return
		}
		if err := s.addWatch(userID, req.Name, req.Club, req.Date, req.Start); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "cutoffMin": int(watchCutoff().Minutes())})
	case http.MethodDelete:
		s.removeWatch(userID, req.Name, req.Club, req.Date, req.Start)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// RunWatches polls the watched classes and books the freed spots. Only where
// the engine runs (it books for real). Never returns.
func (s *Server) RunWatches() {
	lastPoll := map[string]time.Time{} // club|date → last fetch
	errs := map[string]int{}           // user|watch key → consecutive failed bookings
	for range time.Tick(watchTick) {
		now := time.Now().In(s.loc)
		for _, g := range s.dueWatchGroups(now, lastPoll) {
			s.pollWatchGroup(g.club, g.date, g.watches, errs)
		}
	}
}

// watchGroup is the watches on one club's day: a single fetch serves them all.
type watchGroup struct {
	club, date string
	every      time.Duration // poll period (the most urgent watch's)
	next       time.Time     // nearest class start
	watches    []watch
}

// dueWatchGroups expires the watches past their cutoff and returns the club-days
// to poll now (most urgent first, within the per-tick budget), stamping them in
// lastPoll.
func (s *Server) dueWatchGroups(now time.Time, lastPoll map[string]time.Time) []*watchGroup {
	groups := map[string]*watchGroup{}
	for _, w := range s.listWatches(0) {
		at, ok := s.startOf(w.Date, w.Start)
		if !ok || !now.Before(at.Add(-watchCutoff())) {
			s.expireWatch(w)
			continue
		}
		every := watchFar
		switch until := at.Sub(now); {
		case until <= 3*time.Hour:
			every = watchNear
		case until <= 24*time.Hour:
			every = watchMid
		}
		k := w.Club + "|" + w.Date
		g := groups[k]
		if g == nil {
			g = &watchGroup{club: w.Club, date: w.Date, every: every, next: at}
			groups[k] = g
		}
		g.every, g.next = min(g.every, every), minTime(g.next, at)
		g.watches = append(g.watches, w)
	}
	due := make([]*watchGroup, 0, len(groups))
	for k, g := range groups {
		if now.Sub(lastPoll[k]) >= g.every-time.Second {
			due = append(due, g)
		}
	}
	// Most overdue first (so no club-day starves while nearer ones keep coming
	// due), then the nearest class.
	overdue := func(g *watchGroup) time.Duration { return now.Sub(lastPoll[g.club+"|"+g.date]) - g.every }
	sort.Slice(due, func(i, j int) bool {
		if oi, oj := overdue(due[i]), overdue(due[j]); oi != oj {
			return oi > oj
		}
		return due[i].next.Before(due[j].next)
	})
	due = due[:min(len(due), watchPollsPerTick)]
	for _, g := range due {
		lastPoll[g.club+"|"+g.date] = now
	}
	for k := range lastPoll { // forget club-days nobody watches anymore
		if groups[k] == nil {
			delete(lastPoll, k)
		}
	}
	return due
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// pollWatchGroup fetches a club's day once (with the first watcher's session)
// and, if a watched class has a free spot, books it for its watchers in order.
func (s *Server) pollWatchGroup(club, date string, watches []watch, errs map[string]int) {
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return
	}
	clubID, ok := calendar.ClubIDByName(club)
	if !ok {
		return
	}
	// Any watcher's session can read the calendar: fall back to the next one if
	// a user's login fails (e.g. changed password).
	var classes []calendar.Class
	tried := map[int64]bool{}
	for _, w := range watches {
		if tried[w.UserID] {
			continue
		}
		tried[w.UserID] = true
		if classes, err = s.downloadClasses(w.UserID, []int{clubID}, day, day); err == nil {
			break
		}
		log.Printf("watch: fetching %s %s as u%d: %v", club, date, w.UserID, err)
	}
	if err != nil {
		return
	}
	for _, w := range watches {
		if s.isBooked(w.UserID, w.Name, w.Club, w.Date, w.Start) { // booked meanwhile (app, waitlist promotion)
			s.removeWatch(w.UserID, w.Name, w.Club, w.Date, w.Start)
			continue
		}
		beds := matchesWithStatus(classes, w.Name, w.Club, w.Start, w.Date, "bookable")
		if len(beds) == 0 {
			continue
		}
		// The bookings table may be stale (booked in the official app since):
		// refresh it before POSTing a booking the user may already hold.
		s.fetchBookings(w.UserID)
		if s.isBooked(w.UserID, w.Name, w.Club, w.Date, w.Start) {
			s.removeWatch(w.UserID, w.Name, w.Club, w.Date, w.Start)
			continue
		}
		k := fmt.Sprintf("%d|%s", w.UserID, w.key())
		if s.grabWatched(w, beds) {
			delete(errs, k)
			continue
		}
		// Virgin keeps refusing (overlapping booking, account issue…): give up
		// and tell the user instead of POSTing until the cutoff.
		if errs[k]++; errs[k] >= watchMaxErrors {
			delete(errs, k)
			s.removeWatch(w.UserID, w.Name, w.Club, w.Date, w.Start)
			s.notifyClass(w.UserID, w.Name, w.Club, w.Date, w.Start, "/", "push.watch.failed.title")
		}
	}
}

// grabWatched tries the free beds for one watcher until one accepts; false if
// every booking call failed.
func (s *Server) grabWatched(w watch, beds []calendar.Class) bool {
	attempt := automation.Attempt{UserID: w.UserID, Name: w.Name, Club: w.Club, Date: w.Date, Start: w.Start, Source: "watch"}
	var lastErr error
	for _, c := range beds {
		var res vapi.BookResult
		lastErr = s.auth.Do(w.UserID, func(client *vapi.Client) error {
			var err error
			res, err = client.Book(c.ClubID, c.ClassID, c.SessionID, w.Date)
			return err
		})
		if lastErr != nil {
			continue
		}
		s.removeWatch(w.UserID, w.Name, w.Club, w.Date, w.Start)
		s.InvalidateUser(w.UserID)
		if res.Waitlisted { // the spot went between the fetch and the POST
			log.Printf("watch: u%d %s %s %s — waitlisted at %d", w.UserID, w.Name, w.Date, w.Start, res.Position)
			attempt.Outcome, attempt.Detail = automation.OutcomeWaitlisted, fmt.Sprintf("position %d", res.Position)
			s.store.RecordAttempt(attempt)
			s.notifyClass(w.UserID, w.Name, w.Club, w.Date, w.Start, "/?view=bookings", "push.waitlisted.title", res.Position)
			return true
		}
		log.Printf("watch: ✓ u%d %s · %s %s %s — a spot freed up: BOOKED", w.UserID, w.Name, w.Club, w.Date, w.Start)
		attempt.Outcome = automation.OutcomeBooked
		s.store.RecordAttempt(attempt)
		s.MarkBotBooked(w.UserID, w.Name, w.Club, w.Date, w.Start)
		s.notifyClass(w.UserID, w.Name, w.Club, w.Date, w.Start, "/?view=bookings", "push.watch.booked.title")
		return true
	}
	log.Printf("watch: u%d %s %s %s — booking failed: %v", w.UserID, w.Name, w.Date, w.Start, lastErr)
	attempt.Outcome, attempt.Detail = automation.OutcomeError, lastErr.Error()
	s.store.RecordAttempt(attempt)
	return false
}

// expireWatch ends a watch that reached its cutoff without a spot.
func (s *Server) expireWatch(w watch) {
	s.removeWatch(w.UserID, w.Name, w.Club, w.Date, w.Start)
	if s.isBooked(w.UserID, w.Name, w.Club, w.Date, w.Start) {
		return
	}
	log.Printf("watch: u%d %s %s %s — no spot freed up before the cutoff", w.UserID, w.Name, w.Date, w.Start)
	s.store.RecordAttempt(automation.Attempt{UserID: w.UserID, Name: w.Name, Club: w.Club, Date: w.Date, Start: w.Start,
		Source: "watch", Outcome: automation.OutcomeMissed})
	s.notifyClass(w.UserID, w.Name, w.Club, w.Date, w.Start, "/", "push.watch.missed.title")
}

// notifyClass pushes a title (i18n key + args) with the class line as body.
func (s *Server) notifyClass(userID int64, name, club, date, start, url, titleKey string, args ...any) {
	if s.push == nil {
		return
	}
	lang := s.langOf(userID)
	when, _ := s.startOf(date, start)
	go s.push.Notify(userID, push.Message{
		Title: i18n.T(lang, titleKey, args...),
		Body:  i18n.T(lang, "push.class", name, club, i18n.FormatDateTime(lang, when)),
		URL:   url,
	})
}

// MarkBotBooked remembers that VirginBot (engine or watch) made this booking,
// for the history stats.
func (s *Server) MarkBotBooked(userID int64, name, club, date, start string) {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO bot_bookings (user_id, name, club, date, start) VALUES (?, ?, ?, ?, ?)`,
		userID, name, club, date, start); err != nil {
		log.Printf("mark bot booking u%d: %v", userID, err)
	}
	// Booked after its reminder time (e.g. the engine's last fire at 24 h with a
	// 24 h reminder): "still going?" right after "booked" would be noise.
	if h := s.accounts.ReminderHours(userID); h > 0 {
		if at, ok := s.startOf(date, start); ok && !time.Now().Before(at.Add(-time.Duration(h)*time.Hour)) {
			s.db.Exec(`INSERT OR IGNORE INTO reminders_sent (user_id, name, club, date, start) VALUES (?, ?, ?, ?, ?)`,
				userID, name, club, date, start)
		}
	}
}

// ---- engine fallback: an automated class still full at its last attempt ----

// Full-class fallbacks a user can choose (users.full_action).
const (
	fullNotify   = ""         // just tell me I didn't get it
	fullWaitlist = "waitlist" // join the class's waitlist
	fullWatch    = "watch"    // keep watching for a freed spot
)

// OnEngineMissed applies the user's fallback when an automated class is still
// full at the engine's last fire. Reports whether it took over (and notified).
func (s *Server) OnEngineMissed(userID int64, r automation.Rule, date, status string) bool {
	switch s.accounts.FullAction(userID) {
	case fullWaitlist:
		if status != "waitlist" { // waitlist full or closed: nothing to join
			return false
		}
		return s.joinWaitlist(userID, r, date)
	case fullWatch:
		if err := s.addWatch(userID, r.Name, r.Club, date, r.Start); err != nil {
			log.Printf("engine fallback u%d: can't watch %s %s: %v", userID, r.Name, date, err)
			return false
		}
		s.notifyClass(userID, r.Name, r.Club, date, r.Start, "/", "push.watch.started.title")
		return true
	}
	return false
}

// joinWaitlist puts the user on the occurrence's waitlist (the booking call on a
// full class with an open waitlist does exactly that).
func (s *Server) joinWaitlist(userID int64, r automation.Rule, date string) bool {
	classes, err := s.FreshDayFor(userID, r.Club, date)
	if err != nil {
		return false
	}
	for _, c := range matchesWithStatus(classes, r.Name, r.Club, r.Start, date, "waitlist") {
		var res vapi.BookResult
		err := s.auth.Do(userID, func(client *vapi.Client) error {
			var err error
			res, err = client.Book(c.ClubID, c.ClassID, c.SessionID, date)
			return err
		})
		if err != nil {
			log.Printf("engine fallback u%d: joining the waitlist of %s %s: %v", userID, r.Name, date, err)
			continue
		}
		s.InvalidateUser(userID)
		if res.Waitlisted {
			s.notifyClass(userID, r.Name, r.Club, date, r.Start, "/?view=bookings", "push.waitlisted.title", res.Position)
		} else { // a spot freed up in the meantime: booked
			s.MarkBotBooked(userID, r.Name, r.Club, date, r.Start)
			s.notifyClass(userID, r.Name, r.Club, date, r.Start, "/?view=bookings", "push.booked.title")
		}
		return true
	}
	return false
}
