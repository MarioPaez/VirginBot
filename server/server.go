package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/booking"
	"github.com/MarioPaez/VirginBot/calendar"
)

var errWarming = errors.New("calendario aún cargando")

//go:embed web
var webFS embed.FS

const (
	defaultDays   = 10
	maxDays       = 30
	cacheTTL      = 5 * time.Minute
	refreshPeriod = 4 * time.Minute
)

// Server sirve el calendario, reservas y automatizaciones.
type Server struct {
	auth     *Auth
	clubs    []string
	classIDs []string
	keep     func(calendar.Class) bool
	store    *automation.Store

	plain *http.Client // cliente sin auth, para listar si el login falla

	mu       sync.Mutex
	cache    map[int]cacheEntry
	inflight map[int]bool
}

type cacheEntry struct {
	classes   []calendar.Class
	fetchedAt time.Time
}

// New crea el servidor y precarga la caché.
func New(auth *Auth, clubs, classIDs []string, keep func(calendar.Class) bool, store *automation.Store) *Server {
	s := &Server{
		auth:     auth,
		clubs:    clubs,
		classIDs: classIDs,
		keep:     keep,
		store:    store,
		plain:    &http.Client{Timeout: 45 * time.Second},
		cache:    make(map[int]cacheEntry),
		inflight: make(map[int]bool),
	}
	s.ensure(defaultDays)
	go func() {
		for range time.Tick(refreshPeriod) {
			s.refresh(defaultDays)
		}
	}()
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/classes", s.handleClasses)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/bookings", s.handleBookings)
	mux.HandleFunc("/api/book", s.handleBook)
	mux.HandleFunc("/api/unbook", s.handleUnbook)
	mux.HandleFunc("/api/automations", s.handleAutomations)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(static)))
	return withCORS(mux)
}

// ---- calendario ----

type statusResponse struct {
	Ready     bool      `json:"ready"`
	Count     int       `json:"count"`
	FetchedAt time.Time `json:"fetchedAt,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	entry, ready := s.ensure(clampDays(r.URL.Query().Get("days")))
	writeJSON(w, statusResponse{Ready: ready, Count: len(entry.classes), FetchedAt: entry.fetchedAt})
}

type classesResponse struct {
	FetchedAt time.Time        `json:"fetchedAt"`
	Count     int              `json:"count"`
	Classes   []calendar.Class `json:"classes"`
}

func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	days := clampDays(r.URL.Query().Get("days"))
	entry, ready := s.ensure(days)
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, statusResponse{Ready: false})
		return
	}

	q := strings.ToLower(r.URL.Query().Get("q"))
	club := strings.ToLower(r.URL.Query().Get("club"))
	filtered := make([]calendar.Class, 0, len(entry.classes))
	for _, c := range entry.classes {
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
			continue
		}
		if club != "" && !strings.Contains(strings.ToLower(c.Club), club) {
			continue
		}
		filtered = append(filtered, c)
	}
	writeJSON(w, classesResponse{FetchedAt: entry.fetchedAt, Count: len(filtered), Classes: filtered})
}

// handleBookings devuelve las clases ya reservadas (de la caché autenticada).
func (s *Server) handleBookings(w http.ResponseWriter, _ *http.Request) {
	entry, ready := s.ensure(defaultDays)
	if !ready {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, statusResponse{Ready: false})
		return
	}
	var booked []calendar.Class
	for _, c := range entry.classes {
		if c.Booked {
			booked = append(booked, c)
		}
	}
	writeJSON(w, classesResponse{FetchedAt: entry.fetchedAt, Count: len(booked), Classes: booked})
}

// Classes expone el calendario autenticado cacheado (para el motor).
func (s *Server) Classes() ([]calendar.Class, error) {
	entry, ready := s.ensure(defaultDays)
	if !ready {
		return nil, errWarming
	}
	return entry.classes, nil
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
	var req bookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "petición inválida", http.StatusBadRequest)
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
	s.invalidate()
	writeJSON(w, map[string]any{"ok": true})
}

// handleUnbook cancela la reserva y, si la clase tenía automatización, la quita
// para que no se vuelva a reservar.
func (s *Server) handleUnbook(w http.ResponseWriter, r *http.Request) {
	var req bookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "petición inválida", http.StatusBadRequest)
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
	s.invalidate()
	writeJSON(w, map[string]any{"ok": true})
}

// ---- automatizaciones ----

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.List())
	case http.MethodPost:
		var req bookRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "petición inválida", http.StatusBadRequest)
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

// ---- caché ----

func (s *Server) ensure(days int) (cacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.cache[days]; ok && time.Since(e.fetchedAt) < cacheTTL {
		return e, true
	}
	if !s.inflight[days] {
		s.inflight[days] = true
		go s.fetch(days)
	}
	if e, ok := s.cache[days]; ok {
		return e, true
	}
	return cacheEntry{}, false
}

func (s *Server) refresh(days int) {
	s.mu.Lock()
	if s.inflight[days] {
		s.mu.Unlock()
		return
	}
	s.inflight[days] = true
	s.mu.Unlock()
	s.fetch(days)
}

func (s *Server) fetch(days int) {
	start := time.Now()
	// Cliente autenticado para ver el estado de reserva; si falla, lista público.
	client := s.plain
	if c, err := s.auth.Client(); err == nil {
		client = c
	} else {
		log.Printf("sin sesión www (calendario sin estado de reserva): %v", err)
	}

	classes, err := calendar.FetchRange(client, s.clubs, s.classIDs, time.Now(), days)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, days)
	if err != nil {
		log.Printf("carga de %d días fallida: %v", days, err)
		return
	}
	classes = s.curate(classes)
	s.cache[days] = cacheEntry{classes: classes, fetchedAt: time.Now()}
	log.Printf("caché de %d días lista (%d clases) en %s", days, len(classes), time.Since(start).Round(time.Millisecond))
}

// invalidate marca la caché como caduca para refrescar el estado de reserva.
func (s *Server) invalidate() {
	s.mu.Lock()
	s.cache = make(map[int]cacheEntry)
	s.mu.Unlock()
	go s.refresh(defaultDays)
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

// ---- helpers ----

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

// weekdayOf devuelve el día de la semana (0=Domingo) de una fecha YYYY-MM-DD.
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
