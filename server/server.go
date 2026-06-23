package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/booking"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/notification"
)

//go:embed web
var webFS embed.FS

const (
	defaultDays   = 10
	maxDays       = 30
	dayTTL        = 6 * time.Hour // si un día cacheado es más viejo, se refresca en 2º plano
	emptyTTL      = 1 * time.Hour // días sin clases (agenda no publicada aún) se reintentan antes
	maxConcurrent = 5             // días en paralelo (más satura el backend remoto)
)

// Server sirve el calendario (cacheado en SQLite por usuario y día), reservas y
// automatizaciones. Todo el estado de usuario se aísla por user_id.
type Server struct {
	db       *sql.DB
	auth     *Auth
	accounts *account.Store
	clubs    []string
	classIDs []string
	keep     func(calendar.Class) bool
	store    *automation.Store
	loc      *time.Location
	sem      chan struct{} // limita fetches concurrentes (global)
	sess     *sessions
	sender   notification.Sender

	mu      sync.Mutex
	fetchIn map[string]bool // fetches en curso, clave "kind|userID|date"
}

type dayEntry struct {
	classes   []calendar.Class
	fetchedAt time.Time
}

func New(db *sql.DB, auth *Auth, accounts *account.Store, clubs, classIDs []string, keep func(calendar.Class) bool, store *automation.Store, sender notification.Sender) *Server {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	if sender == nil {
		sender = notification.NoOp{}
	}
	s := &Server{
		db: db, auth: auth, accounts: accounts, clubs: clubs, classIDs: classIDs, keep: keep, store: store, loc: loc,
		sem:     make(chan struct{}, maxConcurrent),
		sess:    newSessions(db),
		sender:  sender,
		fetchIn: map[string]bool{},
	}
	go func() {
		s.warm()
		s.dailyRefreshLoop() // refresca todo el calendario cada día a las 00:00
	}()
	return s
}

// dailyRefreshLoop recarga el calendario completo una vez al día (00:00 Italia)
// y purga las reservas ya pasadas. Durante el día se sirve desde la BD.
func (s *Server) dailyRefreshLoop() {
	for {
		now := time.Now().In(s.loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 1)
		time.Sleep(time.Until(next))
		log.Println("refresco diario del calendario")
		s.purgePastBookings()
		s.forceRefresh()
	}
}

// forceRefresh re-descarga el calendario de todos los usuarios registrados.
func (s *Server) forceRefresh() {
	s.purgePastBookings()
	for _, uid := range s.accounts.ListUserIDs() {
		s.forceRefreshUser(uid)
	}
}

// forceRefreshUser re-descarga (sin borrar primero) el calendario curado y el
// de reservas de un usuario. Si una descarga falla, se conservan los datos
// anteriores en la BD en vez de quedarnos con el calendario vacío.
func (s *Server) forceRefreshUser(userID int64) {
	for _, d := range s.dates(defaultDays) {
		s.triggerDay(userID, d, false)
		s.triggerDay(userID, d, true)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Públicas: login y estado de sesión.
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Protegidas: requieren sesión.
	mux.HandleFunc("/api/dates", s.requireSession(s.handleDates))
	mux.HandleFunc("/api/day", s.requireSession(s.handleDay))
	mux.HandleFunc("/api/bookings", s.requireSession(s.handleBookings))
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

// warm precarga el calendario curado de los usuarios ya registrados. Para cada
// uno carga HOY primero y espera antes del resto, para que el primer día (lo que
// ve al abrir) aparezca cuanto antes.
func (s *Server) warm() {
	dates := s.dates(defaultDays)
	if len(dates) == 0 {
		return
	}
	for _, uid := range s.accounts.ListUserIDs() {
		s.ensureDay(uid, dates[0])
		s.waitDay(uid, dates[0])
		for _, d := range dates[1:] {
			s.ensureDay(uid, d)
		}
		// Reservas: dispara el escaneo (rellena la tabla bookings).
		for _, d := range dates {
			s.triggerDay(uid, d, true)
		}
	}
}

// waitDay bloquea hasta que el día esté cacheado en la BD (o un límite).
func (s *Server) waitDay(userID int64, date string) {
	for i := 0; i < 60; i++ {
		if _, _, ok := s.getDayCache("curated", date, userID); ok {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ---- endpoints calendario ----

func (s *Server) handleDates(w http.ResponseWriter, r *http.Request) {
	days := clampDays(r.URL.Query().Get("days"))
	writeJSON(w, map[string]any{"dates": s.dates(days)})
}

type dayResponse struct {
	Date    string           `json:"date"`
	Ready   bool             `json:"ready"`
	Classes []calendar.Class `json:"classes"`
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	date := r.URL.Query().Get("date")
	if !validDate(date) {
		http.Error(w, "fecha inválida", http.StatusBadRequest)
		return
	}
	entry, ready := s.ensureDay(userID, date)
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, dayResponse{Date: date, Ready: false})
		return
	}
	writeJSON(w, dayResponse{Date: date, Ready: true, Classes: entry.classes})
}

// handleBookings devuelve las reservas futuras del usuario desde la BD (al
// instante; un escaneo en 2º plano las mantiene consistentes con Virgin).
func (s *Server) handleBookings(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	// Asegura que haya un escaneo reciente en marcha para reconciliar.
	go func() {
		for _, d := range s.dates(defaultDays) {
			s.triggerDay(userID, d, true)
		}
	}()
	writeJSON(w, s.listBookings(userID))
}

// ---- caché por día (SQLite, por usuario) ----

func (s *Server) ensureDay(userID int64, date string) (dayEntry, bool) {
	if cj, ts, ok := s.getDayCache("curated", date, userID); ok {
		classes := decodeClasses(cj)
		ttl := dayTTL
		if len(classes) == 0 {
			ttl = emptyTTL // probablemente agenda no publicada: reintenta antes
		}
		if time.Since(ts) >= ttl {
			s.triggerDay(userID, date, false)
		}
		return dayEntry{classes: classes, fetchedAt: ts}, true
	}
	s.triggerDay(userID, date, false)
	return dayEntry{}, false
}

// triggerDay lanza (una sola vez) el fetch de un día en segundo plano.
func (s *Server) triggerDay(userID int64, date string, booked bool) {
	kind := "curated"
	if booked {
		kind = "booked"
	}
	key := fmt.Sprintf("%s|%d|%s", kind, userID, date)
	s.mu.Lock()
	if s.fetchIn[key] {
		s.mu.Unlock()
		return
	}
	s.fetchIn[key] = true
	s.mu.Unlock()

	if booked {
		go s.fetchBookingDay(userID, date, key)
	} else {
		go s.fetchDay(userID, date, key)
	}
}

func (s *Server) fetchDay(userID int64, date, key string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	defer s.clearFetch(key)

	client, err := s.auth.ClientFor(userID)
	if err != nil {
		log.Printf("día u%d %s: sin sesión: %v", userID, date, err)
		return
	}
	day, _ := time.ParseInLocation("2006-01-02", date, s.loc)
	classes, err := calendar.FetchDay(client, s.clubs, s.classIDs, day)
	if err != nil {
		log.Printf("día u%d %s fallido: %v", userID, date, err)
		return
	}
	s.setDayCache("curated", date, userID, s.curate(classes))
}

func (s *Server) fetchBookingDay(userID int64, date, key string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	defer s.clearFetch(key)

	client, err := s.auth.ClientFor(userID)
	if err != nil {
		log.Printf("reservas u%d %s: sin sesión: %v", userID, date, err)
		return
	}
	day, _ := time.ParseInLocation("2006-01-02", date, s.loc)
	classes, err := calendar.FetchDay(client, s.clubs, nil, day) // nil = todas las clases
	if err != nil {
		log.Printf("reservas u%d %s fallido: %v", userID, date, err)
		return
	}
	booked := make([]calendar.Class, 0)
	for _, c := range classes {
		if c.Booked {
			booked = append(booked, c)
		}
	}
	s.setDayCache("booked", date, userID, booked)
	s.reconcileBookings(userID, date, booked)
}

func (s *Server) clearFetch(key string) {
	s.mu.Lock()
	delete(s.fetchIn, key)
	s.mu.Unlock()
}

// getDayCache lee un día cacheado de la BD.
func (s *Server) getDayCache(kind, date string, userID int64) (classesJSON string, fetchedAt time.Time, ok bool) {
	var ts int64
	err := s.db.QueryRow(`SELECT classes, fetched_at FROM day_cache WHERE kind = ? AND date = ? AND user_id = ?`,
		kind, date, userID).Scan(&classesJSON, &ts)
	if err != nil {
		return "", time.Time{}, false
	}
	return classesJSON, time.Unix(ts, 0), true
}

// setDayCache escribe un día en la BD.
func (s *Server) setDayCache(kind, date string, userID int64, classes []calendar.Class) {
	b, err := json.Marshal(classes)
	if err != nil {
		return
	}
	_, err = s.db.Exec(
		`INSERT INTO day_cache (kind, date, user_id, classes, fetched_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(kind, date, user_id) DO UPDATE SET classes = excluded.classes, fetched_at = excluded.fetched_at`,
		kind, date, userID, string(b), time.Now().Unix())
	if err != nil {
		log.Printf("guardar caché %s u%d %s: %v", kind, userID, date, err)
	}
}

func decodeClasses(j string) []calendar.Class {
	var classes []calendar.Class
	json.Unmarshal([]byte(j), &classes)
	return classes
}

// FreshDayFor obtiene un día curado SIN pasar por la caché (lo usa el motor para
// sondear en tiempo real durante las ventanas calientes), con el cliente del usuario.
func (s *Server) FreshDayFor(userID int64, date string) ([]calendar.Class, error) {
	client, err := s.auth.ClientFor(userID)
	if err != nil {
		return nil, err
	}
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return nil, err
	}
	classes, err := calendar.FetchDay(client, s.clubs, s.classIDs, day)
	if err != nil {
		return nil, err
	}
	return s.curate(classes), nil
}

// ---- tabla bookings (reservas persistentes y consistentes) ----

// upsertBooking añade o actualiza una reserva del usuario.
func (s *Server) upsertBooking(userID int64, c calendar.Class) {
	_, err := s.db.Exec(
		`INSERT INTO bookings (user_id, name, club, date, start, end_time, center, booking_id, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, name, club, date, start)
		 DO UPDATE SET end_time = excluded.end_time, center = excluded.center, booking_id = excluded.booking_id`,
		userID, c.Name, c.Club, c.Date, c.Start, c.End, c.Center, c.BookingID, time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("guardar reserva u%d %s %s: %v", userID, c.Name, c.Date, err)
	}
}

// deleteBooking elimina una reserva concreta del usuario.
func (s *Server) deleteBooking(userID int64, name, club, date, start string) {
	s.db.Exec(`DELETE FROM bookings WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		userID, name, club, date, start)
}

// reconcileBookings sincroniza la tabla bookings de un usuario para una fecha
// con lo que Virgin reporta como reservado: reemplaza las filas de ese día por
// las halladas (capta también cambios hechos desde la app oficial).
func (s *Server) reconcileBookings(userID int64, date string, booked []calendar.Class) {
	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("reconciliar reservas u%d %s: %v", userID, date, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bookings WHERE user_id = ? AND date = ?`, userID, date); err != nil {
		log.Printf("reconciliar (borrar) u%d %s: %v", userID, date, err)
		return
	}
	now := time.Now().Format(time.RFC3339)
	for _, c := range booked {
		if _, err := tx.Exec(
			`INSERT INTO bookings (user_id, name, club, date, start, end_time, center, booking_id, created)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, c.Name, c.Club, date, c.Start, c.End, c.Center, c.BookingID, now); err != nil {
			log.Printf("reconciliar (insertar) u%d %s: %v", userID, date, err)
			return
		}
	}
	tx.Commit()
}

// purgePastBookings elimina las reservas cuya fecha ya pasó (de todos los usuarios).
func (s *Server) purgePastBookings() {
	s.db.Exec(`DELETE FROM bookings WHERE date < ?`, s.today())
}

// listBookings devuelve las reservas futuras del usuario con forma de Class
// (para reutilizar el render del frontend).
func (s *Server) listBookings(userID int64) []calendar.Class {
	rows, err := s.db.Query(
		`SELECT name, club, date, start, end_time, center, booking_id
		 FROM bookings WHERE user_id = ? AND date >= ? ORDER BY date, start`, userID, s.today())
	if err != nil {
		log.Printf("listar reservas u%d: %v", userID, err)
		return []calendar.Class{}
	}
	defer rows.Close()
	out := []calendar.Class{}
	for rows.Next() {
		var c calendar.Class
		if err := rows.Scan(&c.Name, &c.Club, &c.Date, &c.Start, &c.End, &c.Center, &c.BookingID); err != nil {
			continue
		}
		c.Booked = true
		c.Status = "booked"
		out = append(out, c)
	}
	return out
}

// ---- reservas (acciones) ----

type bookRequest struct {
	BookingID int    `json:"bookingId"`
	Center    int    `json:"center"`
	Name      string `json:"name"`
	Club      string `json:"club"`
	Date      string `json:"date"`
	Start     string `json:"start"`
	End       string `json:"end"`
}

func (req bookRequest) asClass() calendar.Class {
	return calendar.Class{
		Name: req.Name, Club: req.Club, Date: req.Date, Start: req.Start, End: req.End,
		Center: req.Center, BookingID: req.BookingID,
	}
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
	if err := booking.Book(client, req.BookingID, req.Center); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.upsertBooking(userID, req.asClass())
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
	if err := booking.Unbook(client, req.BookingID, req.Center); err != nil {
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
		writeJSON(w, s.store.List(userID))
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
		s.emailUser(userID,
			fmt.Sprintf("VirginBot: automatización añadida — %s", rule.Name),
			fmt.Sprintf("Nueva automatización activada. Reservaré esta clase automáticamente cada semana:\n\n%s\n\nLo intentaré en cuanto abra el plazo y te avisaré por aquí del resultado.\n",
				rule.Summary(s.loc)),
		)
		writeJSON(w, rule)
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

// ---- helpers ----

// emailUser envía un aviso al email del usuario (no bloquea).
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

// InvalidateUser refresca el calendario del usuario tras reservar/cancelar
// (re-descarga sin borrar) y fuerza re-login en la próxima petición de cliente.
func (s *Server) InvalidateUser(userID int64) {
	s.auth.InvalidateUser(userID)
	go s.forceRefreshUser(userID)
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

func validDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
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

// noCache evita que el navegador sirva una versión vieja del FE embebido (los
// ficheros de embed.FS no llevan modtime, así que sin esto el navegador cachea
// por heurística y no recoge los cambios del front).
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
