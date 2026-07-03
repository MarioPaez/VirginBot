package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
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
	"github.com/MarioPaez/VirginBot/notification"
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
	sender   notification.Sender

	mu      sync.Mutex
	fetchIn map[string]bool // booking reconciliations in progress, key "bookings|userID"
}

func New(db *sql.DB, auth *Auth, accounts *account.Store, clubs []int, keep func(calendar.Class) bool, store *automation.Store, sender notification.Sender) *Server {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	if sender == nil {
		sender = notification.NoOp{}
	}
	s := &Server{
		db: db, auth: auth, accounts: accounts, clubs: clubs, keep: keep, store: store, loc: loc,
		sess:    newSessions(db),
		sender:  sender,
		fetchIn: map[string]bool{},
	}
	go func() {
		s.warm()
		s.dailyRefreshLoop()
	}()
	return s
}

// warm preloads the registered users' bookings (so nextAttempt and the overlay
// are up to date from startup).
func (s *Server) warm() {
	for _, uid := range s.accounts.ListUserIDs() {
		s.fetchBookings(uid)
	}
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
	mux.HandleFunc("/api/admin/users", s.requireAdmin(s.handleAdminUsers))
	mux.HandleFunc("/api/admin/automations", s.requireAdmin(s.handleAdminAutomations))
	mux.HandleFunc("/api/admin/bookings", s.requireAdmin(s.handleAdminBookings))
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

	s.fetchBookings(userID) // fresh bookings for the overlay
	classes, err := s.freshClasses(userID, now, now.AddDate(0, 0, days-1))
	if err != nil {
		http.Error(w, "couldn't load the calendar: "+err.Error(), http.StatusBadGateway)
		return
	}

	byDay := map[string][]calendar.Class{}
	for _, c := range s.futureOnly(classes) {
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

// downloadClasses downloads (vapi) and curates (keep filter) the classes of all
// clubs in the range, WITHOUT deduplicating or marking bookings: each caller
// decides whether to collapse the Solarium "beds" (web) or keep them all (engine).
func (s *Server) downloadClasses(userID int64, start, end time.Time) ([]calendar.Class, error) {
	var classes []calendar.Class
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		classes = classes[:0]
		for _, clubID := range s.clubs {
			cc, err := client.Classes(clubID, start, end)
			if err != nil {
				return err
			}
			classes = append(classes, cc...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.curate(classes), nil
}

// freshClasses prepares the calendar for the web: deduplicates (one row per
// club+class+time) and marks the booked ones (overlay).
func (s *Server) freshClasses(userID int64, start, end time.Time) ([]calendar.Class, error) {
	classes, err := s.downloadClasses(userID, start, end)
	if err != nil {
		return nil, err
	}
	return s.overlayBooked(userID, dedupe(classes)), nil
}

// dedupe collapses the instances of the same class (same club, name, date and
// time) into a single row —e.g. the Solarium has many "beds", each a distinct
// bookable class— preferring the most bookable instance.
func dedupe(classes []calendar.Class) []calendar.Class {
	rank := map[string]int{"bookable": 3, "waitlist": 2, "unavailable": 1}
	best := map[string]int{} // key -> index in out
	var out []calendar.Class
	for _, c := range classes {
		k := c.Date + "|" + c.Club + "|" + c.Name + "|" + c.Start
		if i, ok := best[k]; ok {
			if rank[c.Status] > rank[out[i].Status] {
				out[i] = c
			}
			continue
		}
		best[k] = len(out)
		out = append(out, c)
	}
	return out
}

// overlayBooked marks as booked (with the real ids to cancel) the classes the
// user already has in the bookings table.
func (s *Server) overlayBooked(userID int64, classes []calendar.Class) []calendar.Class {
	for i := range classes {
		c := &classes[i]
		if clubID, classID, sessionID, bid, ok := s.bookingInfo(userID, c.Name, c.Club, c.Date, c.Start); ok {
			c.Booked = true
			c.Status = "booked"
			c.ClubID, c.ClassID, c.SessionID, c.BookingID = clubID, classID, sessionID, bid
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

// FreshDayFor fetches a live day for the engine, WITHOUT deduplicating: the
// engine needs to see ALL the Solarium "beds" (each a distinct bookable class) to
// try them until one accepts the booking.
func (s *Server) FreshDayFor(userID int64, date string) ([]calendar.Class, error) {
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return nil, err
	}
	classes, err := s.downloadClasses(userID, day, day)
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

func (s *Server) reconcileBookings(userID int64, start, end string, booked []calendar.Class) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("reconcile bookings u%d: %v", userID, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bookings WHERE user_id = ? AND date >= ? AND date <= ?`, userID, start, end); err != nil {
		log.Printf("reconcile (delete) u%d: %v", userID, err)
		return
	}
	now := time.Now().Format(time.RFC3339)
	for _, c := range booked {
		if _, err := tx.Exec(
			`INSERT INTO bookings (user_id, name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id, created)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, name, club, date, start) DO NOTHING`,
			userID, c.Name, c.Club, c.Date, c.Start, c.End, c.Instructor, c.ClubID, c.ClassID, c.SessionID, c.BookingID, now); err != nil {
			log.Printf("reconcile (insert) u%d: %v", userID, err)
			return
		}
	}
	tx.Commit()
}

func (s *Server) deleteBooking(userID int64, name, club, date, start string) {
	s.db.Exec(`DELETE FROM bookings WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		userID, name, club, date, start)
}

func (s *Server) purgePastBookings() {
	s.db.Exec(`DELETE FROM bookings WHERE date < ?`, s.today())
}

// listBookings returns the user's future bookings as Class values.
func (s *Server) listBookings(userID int64) []calendar.Class {
	rows, err := s.db.Query(
		`SELECT name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id
		 FROM bookings WHERE user_id = ? AND date >= ? ORDER BY date, start`, userID, s.today())
	if err != nil {
		log.Printf("list bookings u%d: %v", userID, err)
		return []calendar.Class{}
	}
	defer rows.Close()
	out := []calendar.Class{}
	for rows.Next() {
		var c calendar.Class
		if err := rows.Scan(&c.Name, &c.Club, &c.Date, &c.Start, &c.End, &c.Instructor, &c.ClubID, &c.ClassID, &c.SessionID, &c.BookingID); err != nil {
			continue
		}
		c.Booked = true
		c.Status = "booked"
		out = append(out, c)
	}
	return s.futureOnly(out)
}

// bookingInfo returns the member's booking ids for an occurrence.
func (s *Server) bookingInfo(userID int64, name, club, date, start string) (clubID, classID, sessionID, bookingID int, ok bool) {
	err := s.db.QueryRow(
		`SELECT club_id, class_id, session_id, booking_id FROM bookings WHERE user_id=? AND name=? AND club=? AND date=? AND start=?`,
		userID, name, club, date, start).Scan(&clubID, &classID, &sessionID, &bookingID)
	return clubID, classID, sessionID, bookingID, err == nil
}

func (s *Server) isBooked(userID int64, name, club, date, start string) bool {
	_, _, _, _, ok := s.bookingInfo(userID, name, club, date, start)
	return ok
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
	classes, err := s.FreshDayFor(userID, req.Date)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	beds := bookableMatches(classes, req.Name, req.Club, req.Start, req.Date)
	if len(beds) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "no bookable slot anymore; reload the calendar"})
		return
	}
	// Try each instance until the POST accepts (Solarium: several "beds"; normal
	// class: only one).
	var bookErr error
	for _, c := range beds {
		bookErr = s.auth.Do(userID, func(client *vapi.Client) error {
			return client.Book(c.ClubID, c.ClassID, c.SessionID, req.Date)
		})
		if bookErr == nil {
			break
		}
	}
	if bookErr != nil {
		writeJSON(w, map[string]any{"ok": false, "error": bookErr.Error()})
		return
	}
	s.InvalidateUser(userID)
	writeJSON(w, map[string]any{"ok": true})
}

// bookableMatches returns the bookable instances (status bookable, valid ids)
// matching name+club+date+start of the already-downloaded day. Re-resolving this
// way avoids booking a stale sessionId the FE sent from its cache. The date check
// guards against vapi bleeding a neighboring day's session into a single-day
// query (same name/club/start every day, e.g. the Solarium).
func bookableMatches(classes []calendar.Class, name, club, start, date string) []calendar.Class {
	var out []calendar.Class
	for _, c := range classes {
		if strings.EqualFold(c.Name, name) && strings.EqualFold(c.Club, club) && c.Start == start && c.Date == date &&
			c.Status == "bookable" && c.ClassID != 0 && c.SessionID != 0 {
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
	if wd, ok := weekdayOf(req.Date); ok {
		s.store.RemoveMatching(userID, req.Name, req.Club, wd, req.Start)
	}
	s.InvalidateUser(userID)
	writeJSON(w, map[string]any{"ok": true})
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
		booked := s.tryBookNow(userID, req)
		lang := s.langOf(userID)
		if booked {
			when, _ := time.ParseInLocation("2006-01-02 15:04", req.Date+" "+req.Start, s.loc)
			block := i18n.T(lang, "block.class", req.Name, req.Club, i18n.FormatDateTime(lang, when))
			s.emailUser(userID,
				i18n.T(lang, "email.booked.subject", rule.Name),
				i18n.T(lang, "email.bookednow.body", block))
		} else {
			s.emailUser(userID,
				i18n.T(lang, "email.added.subject", rule.Name),
				i18n.T(lang, "email.added.body", rule.Summary(lang, s.loc)))
		}
		writeJSON(w, map[string]any{"rule": rule, "booked": booked})
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
		rule, found := s.store.Get(userID, id)
		if err := s.store.Remove(userID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if found {
			lang := s.langOf(userID)
			s.emailUser(userID,
				i18n.T(lang, "email.removed.subject", rule.Name),
				i18n.T(lang, "email.removed.body", rule.Summary(lang, s.loc)))
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) tryBookNow(userID int64, req bookRequest) bool {
	if req.ClassID == 0 || req.SessionID == 0 || req.Booked {
		return false
	}
	err := s.auth.Do(userID, func(client *vapi.Client) error {
		return client.Book(req.ClubID, req.ClassID, req.SessionID, req.Date)
	})
	if err != nil {
		return false
	}
	s.InvalidateUser(userID)
	return true
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
		if s.isBooked(userID, r.Name, r.Club, date, r.Start) {
			continue
		}
		for k := w; k >= 1; k-- {
			if t := start.AddDate(0, 0, -k); t.After(now) {
				return date, t, true
			}
		}
		if !now.Before(start.AddDate(0, 0, -w)) {
			return date, now, true
		}
	}
	return "", time.Time{}, false
}

// ---- helpers ----

// langOf returns the user's language (it/es/en), with fallback to Default.
func (s *Server) langOf(userID int64) i18n.Lang {
	return i18n.Coerce(s.accounts.Lang(userID))
}

func (s *Server) emailUser(userID int64, subject, body string) {
	to := s.auth.Email(userID)
	go func() {
		if err := s.sender.Send(to, subject, body); err != nil {
			log.Printf("email notification failed (u%d): %v", userID, err)
		}
	}()
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
