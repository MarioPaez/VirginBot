package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
	"github.com/MarioPaez/VirginBot/vapi"
)

//go:embed web
var webFS embed.FS

const (
	defaultDays = 10
	maxDays     = 30
)

// Server serves the calendar (live, via vapi), bookings and automations. All user
// state is isolated by user_id. The calendar is NO longer cached in the DB: vapi
// accepts a range and the whole calendar is 2 calls; the browser keeps it in
// memory. Only these persist in the DB: users, sessions, automations and the
// bookings table (reconciled from vapi).
type Server struct {
	db       *sql.DB
	auth     *Auth
	accounts *account.Store
	clubs    []int
	keep     func(calendar.Class) bool
	store    *automation.Store
	loc      *time.Location
	sess     *sessions
	push     *push.Notifier // nil = Web Push disabled

	mu      sync.Mutex
	fetchIn map[string]bool // booking reconciliations in progress, key "bookings|userID"
}

func New(db *sql.DB, auth *Auth, accounts *account.Store, clubs []int, keep func(calendar.Class) bool, store *automation.Store) *Server {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	s := &Server{
		db: db, auth: auth, accounts: accounts, clubs: clubs, keep: keep, store: store, loc: loc,
		sess:    newSessions(db),
		fetchIn: map[string]bool{},
	}
	go func() {
		s.warm()
		s.dailyRefreshLoop()
	}()
	go s.waitlistLoop()
	return s
}

// warm preloads the registered users' bookings (so nextAttempt and the overlay
// are up to date from startup).
func (s *Server) warm() {
	for _, uid := range s.accounts.ListUserIDs() {
		s.fetchBookings(uid)
	}
}

// Waitlist sync: Virgin promotes people from the waitlist at any time (someone
// cancels), so users with a waitlist place are re-synced on their own to notice
// (and push) it soon. Closer to the class, cancellations are more frequent.
const (
	waitlistTick = 2 * time.Minute  // loop period, and the sync period near the class
	waitlistFar  = 10 * time.Minute // sync period otherwise
	waitlistNear = 3 * time.Hour    // "near the class"
)

func (s *Server) waitlistLoop() {
	last := map[int64]time.Time{}
	for {
		time.Sleep(waitlistTick)
		now := time.Now().In(s.loc)
		for uid, next := range s.nextWaitlistedClass(now) {
			every := waitlistFar
			if next.Sub(now) <= waitlistNear {
				every = waitlistTick
			}
			if now.Sub(last[uid]) >= every-time.Second {
				last[uid] = now
				s.fetchBookings(uid) // reconciling pushes "you're in" on a promotion
			}
		}
	}
}

// nextWaitlistedClass maps each user with an upcoming waitlist place to the
// start of the nearest one. The rows are drained before returning: the pool has
// a single connection and the caller queries again.
func (s *Server) nextWaitlistedClass(now time.Time) map[int64]time.Time {
	out := map[int64]time.Time{}
	rows, err := s.db.Query(`SELECT user_id, date, start FROM bookings WHERE status = 'waitlisted' AND date >= ?`,
		now.Format("2006-01-02"))
	if err != nil {
		log.Printf("waitlist sync: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		var date, start string
		if rows.Scan(&uid, &date, &start) != nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", date+" "+start, s.loc)
		if err != nil || !t.After(now) {
			continue
		}
		if cur, ok := out[uid]; !ok || t.Before(cur) {
			out[uid] = t
		}
	}
	return out
}

// dailyRefreshLoop purges past bookings and refreshes each user's at 00:00
// (Italy).
func (s *Server) dailyRefreshLoop() {
	for {
		now := time.Now().In(s.loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 1)
		time.Sleep(time.Until(next))
		s.purgePastBookings()
		for _, uid := range s.accounts.ListUserIDs() {
			s.fetchBookings(uid)
		}
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/calendar", s.requireSession(s.handleCalendar))
	mux.HandleFunc("/api/bookings", s.requireSession(s.handleBookings))
	mux.HandleFunc("/api/bookings/refresh", s.requireSession(s.handleBookingsRefresh))
	mux.HandleFunc("/api/bookings/shared", s.requireSession(s.handleSharedBookings))
	mux.HandleFunc("/api/book", s.requireSession(s.handleBook))
	mux.HandleFunc("/api/unbook", s.requireSession(s.handleUnbook))
	mux.HandleFunc("/api/automations", s.requireSession(s.handleAutomations))
	mux.HandleFunc("/api/lang", s.requireSession(s.handleSetLang))
	mux.HandleFunc("/api/push", s.requireSession(s.handlePush))
	mux.HandleFunc("/api/push/subscribe", s.requireSession(s.handlePushSubscribe))
	mux.HandleFunc("/api/push/unsubscribe", s.requireSession(s.handlePushUnsubscribe))
	mux.HandleFunc("/api/push/test", s.requireSession(s.handlePushTest))
	mux.HandleFunc("/api/admin/users", s.requireAdmin(s.handleAdminUsers))
	mux.HandleFunc("/api/admin/automations", s.requireAdmin(s.handleAdminAutomations))
	mux.HandleFunc("/api/admin/bookings", s.requireAdmin(s.handleAdminBookings))
	// Not in Go's built-in MIME table; browsers want the proper type for the manifest.
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", noCache(http.FileServer(http.FS(static))))
	return mux
}

// dates returns the next `days` dates (Italy timezone).
func (s *Server) dates(days int) []string {
	now := time.Now().In(s.loc)
	out := make([]string, days)
	for i := 0; i < days; i++ {
		out[i] = now.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

func (s *Server) today() string { return time.Now().In(s.loc).Format("2006-01-02") }

// ---- calendar ----

type calendarDay struct {
	Date    string           `json:"date"`
	Classes []calendar.Class `json:"classes"`
}

// handleCalendar returns the WHOLE calendar (the next `days` days) in one
// response: curated, deduplicated classes with the bookings overlay. Few vapi
// calls; the browser caches it in memory.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	days := clampDays(r.URL.Query().Get("days"))
	now := time.Now().In(s.loc)

	// Bookings (for the overlay) and the clubs' classes are independent vapi
	// calls: fetch them concurrently; the overlay runs once both are in.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.fetchBookings(userID) }()
	classes, err := s.downloadClasses(userID, s.clubs, now, now.AddDate(0, 0, days-1))
	wg.Wait()
	if err == nil {
		classes = s.overlayBooked(userID, dedupe(classes))
	}
	if errors.Is(err, vapi.ErrBadCredentials) || errors.Is(err, errNoCreds) {
		// 401 sends the FE back to the login form: the only fix is a new password.
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if err != nil {
		http.Error(w, "couldn't load the calendar: "+err.Error(), http.StatusBadGateway)
		return
	}

	classes = s.futureOnly(classes)
	s.markOpening(classes, now)
	byDay := map[string][]calendar.Class{}
	for _, c := range classes {
		byDay[c.Date] = append(byDay[c.Date], c)
	}
	out := make([]calendarDay, 0, days)
	for _, d := range s.dates(days) {
		cs := byDay[d]
		if len(cs) == 0 {
			continue // no classes (schedule not published yet): we don't show it, like the app
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].Start != cs[j].Start {
				return cs[i].Start < cs[j].Start
			}
			return cs[i].Name < cs[j].Name
		})
		out = append(out, calendarDay{Date: d, Classes: cs})
	}
	writeJSON(w, map[string]any{"days": out})
}

// downloadClasses downloads (vapi) and curates (keep filter) the classes of the
// given clubs in the range, WITHOUT deduplicating or marking bookings: each
// caller decides whether to collapse the Solarium "beds" (web) or keep them all
// (engine). One request per club, concurrently (~0.7s each, ~0.5MB: vapi
// ignores the range and always returns ~10 days).
func (s *Server) downloadClasses(userID int64, clubs []int, start, end time.Time) ([]calendar.Class, error) {
	var classes []calendar.Class
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		perClub := make([][]calendar.Class, len(clubs))
		errs := make([]error, len(clubs))
		var wg sync.WaitGroup
		for i, clubID := range clubs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				perClub[i], errs[i] = client.Classes(clubID, start, end)
			}()
		}
		wg.Wait()
		// Joined so auth.Do still sees vapi.ErrUnauthorized and re-logs in.
		if err := errors.Join(errs...); err != nil {
			return err
		}
		classes = classes[:0]
		for _, cc := range perClub { // club order kept: deterministic output
			classes = append(classes, cc...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.curate(classes), nil
}

// dedupe collapses the instances of the same class (same club, name, date and
// time) into a single row —e.g. the Solarium has many "beds", each a distinct
// bookable class— preferring the most bookable instance. Spots and capacity are
// summed over the instances, so the row tells how many beds are free.
func dedupe(classes []calendar.Class) []calendar.Class {
	rank := map[string]int{"bookable": 4, "waitlist": 3, "full": 2, "unavailable": 1}
	best := map[string]int{} // key -> index in out
	var out []calendar.Class
	for _, c := range classes {
		k := c.Date + "|" + c.Club + "|" + c.Name + "|" + c.Start
		if i, ok := best[k]; ok {
			spots, capacity := out[i].Spots+c.Spots, out[i].Capacity+c.Capacity
			if rank[c.Status] > rank[out[i].Status] {
				out[i] = c
			}
			out[i].Spots, out[i].Capacity = spots, capacity
			continue
		}
		best[k] = len(out)
		out = append(out, c)
	}
	return out
}

// markOpening sets OpensAt on the classes that are unavailable only because
// their booking window hasn't opened yet (the engine's first fire: class time −
// window days), so the calendar can say when instead of "unavailable".
func (s *Server) markOpening(classes []calendar.Class, now time.Time) {
	for i := range classes {
		c := &classes[i]
		if c.Status != "unavailable" {
			continue
		}
		start, err := time.ParseInLocation("2006-01-02 15:04", c.Date+" "+c.Start, s.loc)
		if err != nil {
			continue
		}
		if open := start.AddDate(0, 0, -automation.WindowDays(c.Name)); open.After(now) {
			c.OpensAt = open.Format(time.RFC3339)
		}
	}
}

// overlayBooked marks the classes the user already has in the bookings table:
// booked, or waitlisted with their position (with the real ids to cancel/leave).
func (s *Server) overlayBooked(userID int64, classes []calendar.Class) []calendar.Class {
	for i := range classes {
		c := &classes[i]
		if b, ok := s.bookingRow(userID, c.Name, c.Club, c.Date, c.Start); ok {
			c.Status = b.Status
			c.Booked = b.Status == "booked"
			c.Waitlisted = b.Status == "waitlisted"
			c.WaitlistPos = b.WaitlistPos
			c.ClubID, c.ClassID, c.SessionID, c.BookingID = b.ClubID, b.ClassID, b.SessionID, b.BookingID
		}
	}
	return classes
}

// futureOnly drops classes whose date+time has already passed.
func (s *Server) futureOnly(classes []calendar.Class) []calendar.Class {
	now := time.Now().In(s.loc)
	out := make([]calendar.Class, 0, len(classes))
	for _, c := range classes {
		t, err := time.ParseInLocation("2006-01-02 15:04", c.Date+" "+c.Start, s.loc)
		if err != nil || !t.Before(now) {
			out = append(out, c)
		}
	}
	return out
}

// FreshDayFor fetches a live day of ONE club (the rule's / request's) for the
// engine and for booking, WITHOUT deduplicating: the engine needs to see ALL the
// Solarium "beds" (each a distinct bookable class) to try them until one accepts
// the booking. Only that club: this runs right when the window opens, and every
// extra club is ~0.7s before the POST. Unknown club name → every club.
func (s *Server) FreshDayFor(userID int64, club, date string) ([]calendar.Class, error) {
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return nil, err
	}
	clubs := s.clubs
	if id, ok := calendar.ClubIDByName(club); ok {
		clubs = []int{id}
	}
	classes, err := s.downloadClasses(userID, clubs, day, day)
	if err != nil {
		return nil, err
	}
	return s.overlayBooked(userID, classes), nil
}

// ---- bookings (table, reconciled from vapi) ----

func (s *Server) handleBookings(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	s.triggerBookings(userID)
	writeJSON(w, s.listBookings(userID))
}

func (s *Server) handleBookingsRefresh(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	s.purgePastBookings()
	s.fetchBookings(userID)
	writeJSON(w, s.listBookings(userID))
}

// triggerBookings reconciles the bookings in the background (only one at a time).
func (s *Server) triggerBookings(userID int64) {
	key := fmt.Sprintf("bookings|%d", userID)
	s.mu.Lock()
	if s.fetchIn[key] {
		s.mu.Unlock()
		return
	}
	s.fetchIn[key] = true
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.fetchIn, key); s.mu.Unlock() }()
		s.fetchBookings(userID)
	}()
}

// fetchBookings reconciles the user's bookings table with vapi (one call).
func (s *Server) fetchBookings(userID int64) {
	now := time.Now().In(s.loc)
	end := now.AddDate(0, 0, defaultDays)
	var booked []calendar.Class
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		var err error
		booked, err = client.MyBookings(now, end)
		return err
	})
	if err != nil {
		log.Printf("bookings u%d failed: %v", userID, err)
		return
	}
	s.reconcileBookings(userID, now.Format("2006-01-02"), end.Format("2006-01-02"), booked)
}

// reconcileBookings replaces the user's bookings in [start, end] with what Virgin
// reports (active bookings and waitlist places) and returns the occurrences that
// went from the waitlist to booked (already notified).
func (s *Server) reconcileBookings(userID int64, start, end string, booked []calendar.Class) (promoted []calendar.Class) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("reconcile bookings u%d: %v", userID, err)
		return nil
	}
	defer tx.Rollback()
	// Occurrences the user was waitlisted for: if one is now an active booking,
	// they got in. Read through the tx (the pool has a single connection).
	wasWaitlisted := map[string]bool{}
	if rows, err := tx.Query(`SELECT name, club, date, start FROM bookings
		 WHERE user_id = ? AND date >= ? AND date <= ? AND status = 'waitlisted'`, userID, start, end); err == nil {
		for rows.Next() {
			var n, c, d, st string
			if rows.Scan(&n, &c, &d, &st) == nil {
				wasWaitlisted[n+"|"+c+"|"+d+"|"+st] = true
			}
		}
		rows.Close()
	}
	if _, err := tx.Exec(`DELETE FROM bookings WHERE user_id = ? AND date >= ? AND date <= ?`, userID, start, end); err != nil {
		log.Printf("reconcile (delete) u%d: %v", userID, err)
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	for _, c := range booked {
		status := "booked"
		if c.Waitlisted {
			status = "waitlisted"
		} else if wasWaitlisted[c.Name+"|"+c.Club+"|"+c.Date+"|"+c.Start] {
			promoted = append(promoted, c)
		}
		// An active booking wins over a waitlist place for the same occurrence.
		if _, err := tx.Exec(
			`INSERT INTO bookings (user_id, name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id, status, waitlist_pos, created)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, name, club, date, start) DO UPDATE SET
			   end_time = excluded.end_time, instructor = excluded.instructor, club_id = excluded.club_id,
			   class_id = excluded.class_id, session_id = excluded.session_id, booking_id = excluded.booking_id,
			   status = excluded.status, waitlist_pos = excluded.waitlist_pos
			 WHERE bookings.status <> 'booked'`,
			userID, c.Name, c.Club, c.Date, c.Start, c.End, c.Instructor, c.ClubID, c.ClassID, c.SessionID, c.BookingID,
			status, c.WaitlistPos, now); err != nil {
			log.Printf("reconcile (insert) u%d: %v", userID, err)
			return nil
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("reconcile (commit) u%d: %v", userID, err)
		return nil
	}
	for _, c := range promoted {
		s.notifyPromoted(userID, c)
	}
	return promoted
}

// notifyPromoted tells the user they moved from the waitlist into the class.
func (s *Server) notifyPromoted(userID int64, c calendar.Class) {
	log.Printf("waitlist: u%d got into %s · %s %s %s", userID, c.Name, c.Club, c.Date, c.Start)
	if s.push == nil {
		return
	}
	lang := s.langOf(userID)
	when, _ := time.ParseInLocation("2006-01-02 15:04", c.Date+" "+c.Start, s.loc)
	go s.push.Notify(userID, push.Message{
		Title: i18n.T(lang, "push.promoted.title"),
		Body:  i18n.T(lang, "push.class", c.Name, c.Club, i18n.FormatDateTime(lang, when)),
		URL:   "/?view=bookings",
	})
}

func (s *Server) deleteBooking(userID int64, name, club, date, start string) {
	s.db.Exec(`DELETE FROM bookings WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		userID, name, club, date, start)
}

func (s *Server) purgePastBookings() {
	s.db.Exec(`DELETE FROM bookings WHERE date < ?`, s.today())
	if err := s.store.PurgeSkips(s.today()); err != nil {
		log.Printf("purge skips: %v", err)
	}
}

// unskip clears a cancelled-occurrence mark once the user books or automates it again.
func (s *Server) unskip(userID int64, req bookRequest) {
	if wd, ok := weekdayOf(req.Date); ok {
		if err := s.store.Unskip(userID, req.Name, req.Club, wd, req.Start, req.Date); err != nil {
			log.Printf("unskip u%d %s %s: %v", userID, req.Name, req.Date, err)
		}
	}
}

// listBookings returns the user's future bookings as Class values.
func (s *Server) listBookings(userID int64) []calendar.Class {
	rows, err := s.db.Query(
		`SELECT name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id, status, waitlist_pos
		 FROM bookings WHERE user_id = ? AND date >= ? ORDER BY date, start`, userID, s.today())
	if err != nil {
		log.Printf("list bookings u%d: %v", userID, err)
		return []calendar.Class{}
	}
	defer rows.Close()
	out := []calendar.Class{}
	for rows.Next() {
		var c calendar.Class
		if err := rows.Scan(&c.Name, &c.Club, &c.Date, &c.Start, &c.End, &c.Instructor, &c.ClubID, &c.ClassID, &c.SessionID, &c.BookingID,
			&c.Status, &c.WaitlistPos); err != nil {
			continue
		}
		c.Booked = c.Status == "booked"
		c.Waitlisted = c.Status == "waitlisted"
		out = append(out, c)
	}
	return s.futureOnly(out)
}

// bookingRow returns the member's booking (active or waitlist place) for an
// occurrence, with the ids needed to cancel / leave it.
func (s *Server) bookingRow(userID int64, name, club, date, start string) (b calendar.Class, ok bool) {
	err := s.db.QueryRow(
		`SELECT club_id, class_id, session_id, booking_id, status, waitlist_pos
		 FROM bookings WHERE user_id=? AND name=? AND club=? AND date=? AND start=?`,
		userID, name, club, date, start).Scan(&b.ClubID, &b.ClassID, &b.SessionID, &b.BookingID, &b.Status, &b.WaitlistPos)
	return b, err == nil
}

// isBooked reports an ACTIVE booking (a waitlist place doesn't count: the
// engine must still try to book that occurrence).
func (s *Server) isBooked(userID int64, name, club, date, start string) bool {
	b, ok := s.bookingRow(userID, name, club, date, start)
	return ok && b.Status == "booked"
}

// ---- bookings (actions) ----

type bookRequest struct {
	ClubID    int    `json:"clubId"`
	ClassID   int    `json:"classId"`
	SessionID int    `json:"sessionId"`
	BookingID int    `json:"bookingId"`
	Name      string `json:"name"`
	Club      string `json:"club"`
	Date      string `json:"date"`
	Start     string `json:"start"`
	End       string `json:"end"`
	Booked    bool   `json:"booked"`
	Status    string `json:"status"`
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	req, ok := decodeBook(w, r)
	if !ok {
		return
	}
	// Re-resolve the ids against the FRESH calendar: the ones the FE sends may be
	// stale (the browser caches the calendar and Virgin reschedules the session →
	// new sessionId), which causes TOO_EARLY/TOO_LATE_TO_BOOK when booking an
	// occurrence that's no longer the one for that day.
	classes, err := s.FreshDayFor(userID, req.Club, req.Date)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// A free spot first; if the class is full, the same call joins its waitlist.
	beds := matchesWithStatus(classes, req.Name, req.Club, req.Start, req.Date, "bookable")
	if len(beds) == 0 {
		beds = matchesWithStatus(classes, req.Name, req.Club, req.Start, req.Date, "waitlist")
	}
	if len(beds) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "no bookable slot anymore; reload the calendar"})
		return
	}
	// Try each instance until the POST accepts (Solarium: several "beds"; normal
	// class: only one).
	var res vapi.BookResult
	var bookErr error
	for _, c := range beds {
		bookErr = s.auth.Do(userID, func(client *vapi.Client) error {
			var err error
			res, err = client.Book(c.ClubID, c.ClassID, c.SessionID, req.Date)
			return err
		})
		if bookErr == nil {
			break
		}
	}
	if bookErr != nil {
		writeJSON(w, map[string]any{"ok": false, "error": bookErr.Error()})
		return
	}
	s.unskip(userID, req)
	s.InvalidateUser(userID)
	if res.Waitlisted {
		writeJSON(w, map[string]any{"ok": true, "waitlisted": true, "position": res.Position})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// matchesWithStatus returns the instances with the given status and valid ids
// matching name+club+date+start of the already-downloaded day. Re-resolving this
// way avoids booking a stale sessionId the FE sent from its cache. The date check
// guards against vapi bleeding a neighboring day's session into a single-day
// query (same name/club/start every day, e.g. the Solarium).
func matchesWithStatus(classes []calendar.Class, name, club, start, date, status string) []calendar.Class {
	var out []calendar.Class
	for _, c := range classes {
		if strings.EqualFold(c.Name, name) && strings.EqualFold(c.Club, club) && c.Start == start && c.Date == date &&
			c.Status == status && c.ClassID != 0 && c.SessionID != 0 {
			out = append(out, c)
		}
	}
	return out
}

func (s *Server) handleUnbook(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	req, ok := decodeBook(w, r)
	if !ok {
		return
	}
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		return client.Cancel(req.SessionID, req.BookingID, req.ClubID, req.ClassID, req.Date)
	})
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.deleteBooking(userID, req.Name, req.Club, req.Date, req.Start)
	// Keep the (recurring) rule but don't let the engine re-book this date; the FE
	// tells the user (skipped=true).
	skipped := false
	if wd, ok := weekdayOf(req.Date); ok {
		if skipped, err = s.store.Skip(userID, req.Name, req.Club, wd, req.Start, req.Date); err != nil {
			log.Printf("skip u%d %s %s: %v", userID, req.Name, req.Date, err)
		}
	}
	s.InvalidateUser(userID)
	writeJSON(w, map[string]any{"ok": true, "skipped": skipped})
}

func decodeBook(w http.ResponseWriter, r *http.Request) (bookRequest, bool) {
	var req bookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// ---- automations ----

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		rules := s.store.List(userID)
		out := make([]automationView, 0, len(rules))
		for _, rule := range rules {
			v := automationView{Rule: rule}
			if rule.Enabled { // paused rules don't run: we don't compute the next attempt
				if date, at, ok := s.nextAttempt(userID, rule); ok {
					v.NextClass = date
					v.NextAttempt = at.Format(time.RFC3339)
				}
			}
			out = append(out, v)
		}
		writeJSON(w, out)
	case http.MethodPost:
		req, ok := decodeBook(w, r)
		if !ok {
			return
		}
		wd, ok := weekdayOf(req.Date)
		if !ok {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}
		rule, err := s.store.Add(userID, req.Name, req.Club, wd, req.Start)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.unskip(userID, req)                        // automating this occurrence again overrides a past cancellation
		booked, waitPos := s.tryBookNow(userID, req) // the FE shows the outcome right away
		writeJSON(w, map[string]any{"rule": rule, "booked": booked, "waitlistPos": waitPos})
	case http.MethodPatch:
		id := r.URL.Query().Get("id")
		if _, found := s.store.Get(userID, id); !found {
			http.Error(w, "automation not found", http.StatusNotFound)
			return
		}
		enabled := r.URL.Query().Get("enabled") == "1"
		if err := s.store.SetEnabled(userID, id, enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := s.store.Remove(userID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// tryBookNow books the clicked occurrence right away when it has a free spot.
// Only then: on a full class the booking call would silently join the waitlist.
// If the spot went in the meantime and Virgin waitlisted it, it reports the
// position instead of a booking.
func (s *Server) tryBookNow(userID int64, req bookRequest) (booked bool, waitlistPos int) {
	if req.ClassID == 0 || req.SessionID == 0 || req.Booked || req.Status != "bookable" {
		return false, 0
	}
	var res vapi.BookResult
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		var err error
		res, err = client.Book(req.ClubID, req.ClassID, req.SessionID, req.Date)
		return err
	})
	if err != nil {
		return false, 0
	}
	s.InvalidateUser(userID)
	if res.Waitlisted {
		return false, res.Position
	}
	return true, 0
}

type automationView struct {
	automation.Rule
	NextClass   string `json:"nextClass"`
	NextAttempt string `json:"nextAttempt"`
}

// nextAttempt computes the next UNBOOKED occurrence and the instant of the next
// attempt (the next fire at the class time).
func (s *Server) nextAttempt(userID int64, r automation.Rule) (occDate string, attempt time.Time, ok bool) {
	now := time.Now().In(s.loc)
	w := r.OpensDaysBefore
	if w <= 0 {
		w = automation.WindowDays(r.Name)
	}
	for d := 0; d <= 14; d++ {
		day := now.AddDate(0, 0, d)
		if int(day.Weekday()) != r.Weekday {
			continue
		}
		date := day.Format("2006-01-02")
		start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.Start, s.loc)
		if err != nil || !start.After(now) {
			continue
		}
		if s.isBooked(userID, r.Name, r.Club, date, r.Start) || s.store.Skipped(userID, r.ID, date) {
			continue
		}
		// An occurrence with no fire left (inside its last 24h) gets no more
		// attempts from the engine: show the next one that still has fires.
		for k := w; k >= 1; k-- {
			if t := start.AddDate(0, 0, -k); t.After(now) {
				return date, t, true
			}
		}
	}
	return "", time.Time{}, false
}

// ---- helpers ----

// langOf returns the user's language (it/es/en), with fallback to Default.
func (s *Server) langOf(userID int64) i18n.Lang {
	return i18n.Coerce(s.accounts.Lang(userID))
}

func (s *Server) curate(classes []calendar.Class) []calendar.Class {
	if s.keep == nil {
		return classes
	}
	out := make([]calendar.Class, 0, len(classes))
	for _, c := range classes {
		if s.keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// InvalidateUser reconciles the bookings after booking/cancelling (the FE reloads
// the calendar, which is served live).
func (s *Server) InvalidateUser(userID int64) {
	s.fetchBookings(userID)
}

func clampDays(raw string) int {
	d, err := strconv.Atoi(raw)
	if err != nil || d < 1 {
		return defaultDays
	}
	if d > maxDays {
		return maxDays
	}
	return d
}

func weekdayOf(date string) (int, bool) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 0, false
	}
	return int(t.Weekday()), true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("error writing JSON: %v", err)
	}
}

// noCache prevents the browser from serving a stale version of the embedded FE.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
