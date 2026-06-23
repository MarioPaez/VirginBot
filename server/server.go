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
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/notification"
)

//go:embed web
var webFS embed.FS

const (
	defaultDays = 10
	maxDays     = 30
)

// Server sirve el calendario (en vivo, vía vapi), reservas y automatizaciones.
// Todo el estado de usuario se aísla por user_id. El calendario ya NO se cachea
// en BD: vapi acepta rango y todo el calendario son 2 llamadas; el navegador lo
// guarda en memoria. Solo persisten en BD: usuarios, sesiones, automatizaciones
// y la tabla bookings (reconciliada desde vapi).
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
	fetchIn map[string]bool // reconciliaciones de reservas en curso, clave "bookings|userID"
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

// warm precarga las reservas de los usuarios registrados (para que nextAttempt y
// el overlay estén al día desde el arranque).
func (s *Server) warm() {
	for _, uid := range s.accounts.ListUserIDs() {
		s.fetchBookings(uid)
	}
}

// dailyRefreshLoop purga reservas pasadas y refresca las de cada usuario a las
// 00:00 (Italia).
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
	mux.HandleFunc("/api/book", s.requireSession(s.handleBook))
	mux.HandleFunc("/api/unbook", s.requireSession(s.handleUnbook))
	mux.HandleFunc("/api/automations", s.requireSession(s.handleAutomations))
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", noCache(http.FileServer(http.FS(static))))
	return mux
}

// dates devuelve las próximas `days` fechas (zona horaria de Italia).
func (s *Server) dates(days int) []string {
	now := time.Now().In(s.loc)
	out := make([]string, days)
	for i := 0; i < days; i++ {
		out[i] = now.AddDate(0, 0, i).Format("2006-01-02")
	}
	return out
}

func (s *Server) today() string { return time.Now().In(s.loc).Format("2006-01-02") }

// ---- calendario ----

type calendarDay struct {
	Date    string           `json:"date"`
	Classes []calendar.Class `json:"classes"`
}

// handleCalendar devuelve TODO el calendario (los próximos `days` días) en una
// respuesta: clases curadas, deduplicadas y con el overlay de reservas. Pocas
// llamadas a vapi; el navegador lo cachea en memoria.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	days := clampDays(r.URL.Query().Get("days"))
	now := time.Now().In(s.loc)

	s.fetchBookings(userID) // reservas frescas para el overlay
	classes, err := s.freshClasses(userID, now, now.AddDate(0, 0, days-1))
	if err != nil {
		http.Error(w, "no se pudo cargar el calendario: "+err.Error(), http.StatusBadGateway)
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
			continue // sin clases (agenda no publicada aún): no lo mostramos, como la app
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

// freshClasses descarga (vapi) las clases de todos los clubes en el rango, las
// cura (filtro keep), deduplica (una fila por club+clase+hora) y marca las
// reservadas (overlay).
func (s *Server) freshClasses(userID int64, start, end time.Time) ([]calendar.Class, error) {
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		return nil, err
	}
	var classes []calendar.Class
	for _, clubID := range s.clubs {
		cc, err := client.Classes(clubID, start, end)
		if err != nil {
			return nil, err
		}
		classes = append(classes, cc...)
	}
	return s.overlayBooked(userID, dedupe(s.curate(classes))), nil
}

// dedupe colapsa las instancias de una misma clase (mismo club, nombre, fecha y
// hora) en una sola fila —p. ej. el Solarium tiene muchas "camas", cada una una
// clase reservable distinta— prefiriendo la instancia más reservable.
func dedupe(classes []calendar.Class) []calendar.Class {
	rank := map[string]int{"bookable": 3, "waitlist": 2, "unavailable": 1}
	best := map[string]int{} // clave -> índice en out
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

// overlayBooked marca como reservadas (con los ids reales para cancelar) las
// clases que el usuario ya tiene en la tabla bookings.
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

// futureOnly descarta las clases cuya fecha+hora ya pasó.
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

// FreshDayFor obtiene un día (curado+dedup+overlay) en vivo para el motor.
func (s *Server) FreshDayFor(userID int64, date string) ([]calendar.Class, error) {
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return nil, err
	}
	return s.freshClasses(userID, day, day)
}

// ---- reservas (tabla, reconciliada desde vapi) ----

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

// triggerBookings reconcilia las reservas en 2º plano (una sola vez a la vez).
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

// fetchBookings reconcilia la tabla bookings del usuario con vapi (una llamada).
func (s *Server) fetchBookings(userID int64) {
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		log.Printf("reservas u%d: sin sesión: %v", userID, err)
		return
	}
	now := time.Now().In(s.loc)
	end := now.AddDate(0, 0, defaultDays)
	booked, err := client.MyBookings(now, end)
	if err != nil {
		log.Printf("reservas u%d fallido: %v", userID, err)
		return
	}
	s.reconcileBookings(userID, now.Format("2006-01-02"), end.Format("2006-01-02"), booked)
}

func (s *Server) reconcileBookings(userID int64, start, end string, booked []calendar.Class) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("reconciliar reservas u%d: %v", userID, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bookings WHERE user_id = ? AND date >= ? AND date <= ?`, userID, start, end); err != nil {
		log.Printf("reconciliar (borrar) u%d: %v", userID, err)
		return
	}
	now := time.Now().Format(time.RFC3339)
	for _, c := range booked {
		if _, err := tx.Exec(
			`INSERT INTO bookings (user_id, name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id, created)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(user_id, name, club, date, start) DO NOTHING`,
			userID, c.Name, c.Club, c.Date, c.Start, c.End, c.Instructor, c.ClubID, c.ClassID, c.SessionID, c.BookingID, now); err != nil {
			log.Printf("reconciliar (insertar) u%d: %v", userID, err)
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

// listBookings devuelve las reservas futuras del usuario con forma de Class.
func (s *Server) listBookings(userID int64) []calendar.Class {
	rows, err := s.db.Query(
		`SELECT name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id
		 FROM bookings WHERE user_id = ? AND date >= ? ORDER BY date, start`, userID, s.today())
	if err != nil {
		log.Printf("listar reservas u%d: %v", userID, err)
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

// bookingInfo devuelve los ids de la reserva del socio para una ocurrencia.
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

// ---- reservas (acciones) ----

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
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		http.Error(w, "no autenticado: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := client.Book(req.ClubID, req.ClassID, req.SessionID, req.Date); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.InvalidateUser(userID)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleUnbook(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	req, ok := decodeBook(w, r)
	if !ok {
		return
	}
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		http.Error(w, "no autenticado: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := client.Cancel(req.SessionID, req.BookingID, req.ClubID, req.ClassID, req.Date); err != nil {
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
		http.Error(w, "petición inválida", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// ---- automatizaciones ----

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		rules := s.store.List(userID)
		out := make([]automationView, 0, len(rules))
		for _, rule := range rules {
			v := automationView{Rule: rule}
			if date, at, ok := s.nextAttempt(userID, rule); ok {
				v.NextClass = date
				v.NextAttempt = at.Format(time.RFC3339)
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
			http.Error(w, "fecha inválida", http.StatusBadRequest)
			return
		}
		rule, err := s.store.Add(userID, req.Name, req.Club, wd, req.Start)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		booked := s.tryBookNow(userID, req)
		if booked {
			when, _ := time.ParseInLocation("2006-01-02 15:04", req.Date+" "+req.Start, s.loc)
			s.emailUser(userID,
				fmt.Sprintf("VirginBot: ✓ reservada %s", rule.Name),
				fmt.Sprintf("¡Reserva conseguida al automatizar! Te he apuntado a:\n\nClase: %s\nClub: %s\nDía: %s\n\nLa automatización queda activa para reservar también las próximas semanas.\n",
					req.Name, req.Club, automation.FormatDateTimeIT(when)),
			)
		} else {
			s.emailUser(userID,
				fmt.Sprintf("VirginBot: automatización añadida — %s", rule.Name),
				fmt.Sprintf("Nueva automatización activada. Reservaré esta clase automáticamente cada semana:\n\n%s\n\nIntentaré reservar en cuanto abra el plazo (a la hora de la clase) y te avisaré del resultado.\n",
					rule.Summary(s.loc)),
			)
		}
		writeJSON(w, map[string]any{"rule": rule, "booked": booked})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		rule, found := s.store.Get(userID, id)
		if err := s.store.Remove(userID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if found {
			s.emailUser(userID,
				fmt.Sprintf("VirginBot: automatización quitada — %s", rule.Name),
				fmt.Sprintf("Has desactivado esta automatización. Ya no se reservará:\n\n%s\n",
					rule.Summary(s.loc)),
			)
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
	}
}

func (s *Server) tryBookNow(userID int64, req bookRequest) bool {
	if req.ClassID == 0 || req.SessionID == 0 || req.Booked {
		return false
	}
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		return false
	}
	if err := client.Book(req.ClubID, req.ClassID, req.SessionID, req.Date); err != nil {
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

// nextAttempt calcula la próxima ocurrencia NO reservada y el instante del
// próximo intento (siguiente disparo a la hora de la clase).
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

func (s *Server) emailUser(userID int64, subject, body string) {
	to := s.auth.Email(userID)
	go func() {
		if err := s.sender.Send(to, subject, body); err != nil {
			log.Printf("aviso email fallido (u%d): %v", userID, err)
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

// InvalidateUser reconcilia las reservas tras reservar/cancelar (el FE recarga el
// calendario, que se sirve en vivo).
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
		log.Printf("error escribiendo JSON: %v", err)
	}
}

// noCache evita que el navegador sirva una versión vieja del FE embebido.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
