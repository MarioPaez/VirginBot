package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/booking"
	"github.com/MarioPaez/VirginBot/calendar"
)

//go:embed web
var webFS embed.FS

const (
	defaultDays   = 10
	maxDays       = 30
	cacheTTL      = 5 * time.Minute
	refreshPeriod = 4 * time.Minute
	maxConcurrent = 5 // días en paralelo (más satura el backend remoto)
)

// Server sirve el calendario (caché por día), reservas y automatizaciones.
type Server struct {
	auth     *Auth
	clubs    []string
	classIDs []string
	keep     func(calendar.Class) bool
	store    *automation.Store
	loc      *time.Location
	plain    *http.Client
	sem      chan struct{} // limita fetches concurrentes
	sess     *sessions

	mu       sync.Mutex
	days     map[string]dayEntry // calendario curado, por fecha YYYY-MM-DD
	daysIn   map[string]bool
	booked   map[string]dayEntry // reservas (escaneo completo), por fecha
	bookedIn map[string]bool
}

type dayEntry struct {
	classes   []calendar.Class
	fetchedAt time.Time
}

func New(auth *Auth, clubs, classIDs []string, keep func(calendar.Class) bool, store *automation.Store) *Server {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	s := &Server{
		auth: auth, clubs: clubs, classIDs: classIDs, keep: keep, store: store, loc: loc,
		plain:    &http.Client{Timeout: 45 * time.Second},
		sem:      make(chan struct{}, maxConcurrent),
		sess:     newSessions(),
		days:     map[string]dayEntry{},
		daysIn:   map[string]bool{},
		booked:   map[string]dayEntry{},
		bookedIn: map[string]bool{},
	}
	go func() {
		s.client() // login una vez para calentar la sesión www antes de precargar
		s.warm()
		for range time.Tick(refreshPeriod) {
			s.warm()
		}
	}()
	return s
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
	mux.HandleFunc("/api/bookingday", s.requireSession(s.handleBookingDay))
	mux.HandleFunc("/api/book", s.requireSession(s.handleBook))
	mux.HandleFunc("/api/unbook", s.requireSession(s.handleUnbook))
	mux.HandleFunc("/api/automations", s.requireSession(s.handleAutomations))
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(static)))
	return withCORS(mux)
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

// warm precarga el calendario curado. Carga HOY primero y espera a que esté
// listo antes de lanzar el resto, para que el primer día (lo que ve el usuario
// al abrir) tenga toda la banda y aparezca cuanto antes.
func (s *Server) warm() {
	dates := s.dates(defaultDays)
	if len(dates) == 0 {
		return
	}
	s.ensureDay(dates[0])
	s.waitDay(dates[0])
	for _, d := range dates[1:] {
		s.ensureDay(d)
	}
}

// waitDay bloquea hasta que el día esté cacheado (o hasta un límite razonable).
func (s *Server) waitDay(date string) {
	for i := 0; i < 60; i++ {
		s.mu.Lock()
		_, ok := s.days[date]
		s.mu.Unlock()
		if ok {
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
	date := r.URL.Query().Get("date")
	if !validDate(date) {
		http.Error(w, "fecha inválida", http.StatusBadRequest)
		return
	}
	entry, ready := s.ensureDay(date)
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, dayResponse{Date: date, Ready: false})
		return
	}
	writeJSON(w, dayResponse{Date: date, Ready: true, Classes: entry.classes})
}

func (s *Server) handleBookingDay(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if !validDate(date) {
		http.Error(w, "fecha inválida", http.StatusBadRequest)
		return
	}
	entry, ready := s.ensureBookingDay(date)
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, dayResponse{Date: date, Ready: false})
		return
	}
	writeJSON(w, dayResponse{Date: date, Ready: true, Classes: entry.classes})
}

// ---- caché por día (calendario curado) ----

func (s *Server) ensureDay(date string) (dayEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.days[date]; ok && time.Since(e.fetchedAt) < cacheTTL {
		return e, true
	}
	if !s.daysIn[date] {
		s.daysIn[date] = true
		go s.fetchDay(date)
	}
	if e, ok := s.days[date]; ok {
		return e, true
	}
	return dayEntry{}, false
}

func (s *Server) fetchDay(date string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	day, _ := time.ParseInLocation("2006-01-02", date, s.loc)
	classes, err := calendar.FetchDay(s.client(), s.clubs, s.classIDs, day)

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.daysIn, date)
	if err != nil {
		log.Printf("día %s fallido: %v", date, err)
		return
	}
	s.days[date] = dayEntry{classes: s.curate(classes), fetchedAt: time.Now()}
}

// ---- caché por día (reservas: escaneo completo sin filtro de clase) ----

func (s *Server) ensureBookingDay(date string) (dayEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.booked[date]; ok && time.Since(e.fetchedAt) < cacheTTL {
		return e, true
	}
	if !s.bookedIn[date] {
		s.bookedIn[date] = true
		go s.fetchBookingDay(date)
	}
	if e, ok := s.booked[date]; ok {
		return e, true
	}
	return dayEntry{}, false
}

func (s *Server) fetchBookingDay(date string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	day, _ := time.ParseInLocation("2006-01-02", date, s.loc)
	classes, err := calendar.FetchDay(s.client(), s.clubs, nil, day) // nil = todas las clases

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bookedIn, date)
	if err != nil {
		log.Printf("reservas %s fallido: %v", date, err)
		return
	}
	booked := make([]calendar.Class, 0)
	for _, c := range classes {
		if c.Booked {
			booked = append(booked, c)
		}
	}
	s.booked[date] = dayEntry{classes: booked, fetchedAt: time.Now()}
}

// FreshDay obtiene un día curado SIN pasar por la caché (lo usa el motor para
// sondear en tiempo real durante las ventanas calientes).
func (s *Server) FreshDay(date string) ([]calendar.Class, error) {
	day, err := time.ParseInLocation("2006-01-02", date, s.loc)
	if err != nil {
		return nil, err
	}
	classes, err := calendar.FetchDay(s.client(), s.clubs, s.classIDs, day)
	if err != nil {
		return nil, err
	}
	return s.curate(classes), nil
}

// ---- reservas ----

type bookRequest struct {
	BookingID int    `json:"bookingId"`
	Center    int    `json:"center"`
	Name      string `json:"name"`
	Club      string `json:"club"`
	Date      string `json:"date"`
	Start     string `json:"start"`
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBook(w, r)
	if !ok {
		return
	}
	client, err := s.auth.Client()
	if err != nil {
		http.Error(w, "no autenticado: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := booking.Book(client, req.BookingID, req.Center); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.Invalidate()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleUnbook(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBook(w, r)
	if !ok {
		return
	}
	client, err := s.auth.Client()
	if err != nil {
		http.Error(w, "no autenticado: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := booking.Unbook(client, req.BookingID, req.Center); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if wd, ok := weekdayOf(req.Date); ok {
		s.store.RemoveMatching(req.Name, req.Club, wd, req.Start)
	}
	s.Invalidate()
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
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.List())
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
		rule, err := s.store.Add(req.Name, req.Club, wd, req.Start)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rule)
	case http.MethodDelete:
		if err := s.store.Remove(r.URL.Query().Get("id")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
	}
}

// ---- helpers ----

// client devuelve el cliente autenticado, o el público si el login falla.
func (s *Server) client() *http.Client {
	if c, err := s.auth.Client(); err == nil {
		return c
	}
	return s.plain
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

// invalidate vacía las cachés tras reservar/cancelar y reprecarga.
func (s *Server) Invalidate() {
	s.mu.Lock()
	s.days = map[string]dayEntry{}
	s.booked = map[string]dayEntry{}
	s.mu.Unlock()
	go s.warm()
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

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
