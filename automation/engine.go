package automation

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

// Las clases se pueden reservar desde 48h antes; si están llenas, suelen
// liberarse plazas a partir de 24h antes.
const (
	windowOpen   = 48 * time.Hour
	retryFrom    = 24 * time.Hour
	tickInterval = 2 * time.Minute
)

// Engine ejecuta las reglas: intenta reservar cada ocurrencia al abrir el plazo
// (48h antes) y reintenta desde 24h antes si estaba llena.
type Engine struct {
	store    *Store
	classes  func() ([]calendar.Class, error)  // calendario autenticado actual
	book     func(bookingID, center int) error // reserva (cliente autenticado)
	location *time.Location

	mu    sync.Mutex
	state map[string]*occState // clave: ruleID|fecha
}

type occState struct {
	phase1Done bool
	booked     bool
	attempts   int
	lastError  string
	lastTry    time.Time
}

// NewEngine crea el motor. `classes` debe devolver el calendario autenticado
// (con booked/bookingId/center); `book` reserva con el cliente autenticado.
func NewEngine(store *Store, classes func() ([]calendar.Class, error), book func(bookingID, center int) error) *Engine {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	return &Engine{store: store, classes: classes, book: book, location: loc, state: map[string]*occState{}}
}

// Run arranca el bucle periódico hasta que se cierre `stop`.
func (e *Engine) Run(stop <-chan struct{}) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	e.tick() // primera pasada inmediata
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.tick()
		}
	}
}

// tick evalúa todas las reglas una vez.
func (e *Engine) tick() {
	rules := e.store.List()
	if len(rules) == 0 {
		return
	}
	classes, err := e.classes()
	if err != nil {
		log.Printf("automation: no se pudo leer el calendario: %v", err)
		return
	}

	now := time.Now().In(e.location)
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, c := range classes {
			if !matches(r, c, e.location) {
				continue
			}
			e.handle(r, c, now)
		}
	}
}

// handle aplica la política 48h/24h a una ocurrencia concreta.
func (e *Engine) handle(r Rule, c calendar.Class, now time.Time) {
	st, err := time.ParseInLocation("2006-01-02 15:04", c.Date+" "+c.Start, e.location)
	if err != nil {
		return
	}

	key := r.ID + "|" + c.Date
	e.mu.Lock()
	stt := e.state[key]
	if stt == nil {
		stt = &occState{}
		e.state[key] = stt
	}
	e.mu.Unlock()

	if c.Booked {
		stt.booked = true
		return
	}
	if stt.booked || c.BookingID == 0 {
		return // ya reservada por nosotros, o sin botón reservable
	}

	opens := st.Add(-windowOpen)
	switch {
	case now.Before(opens):
		return // el plazo aún no ha abierto
	case now.After(st):
		return // la clase ya pasó
	case now.Before(st.Add(-retryFrom)):
		// Fase 1: entre 48h y 24h antes, un único intento al abrir el plazo.
		if !stt.phase1Done {
			stt.phase1Done = true
			e.attempt(r, c, stt)
		}
	default:
		// Fase 2: desde 24h antes, reintenta cada tick hasta conseguirlo.
		e.attempt(r, c, stt)
	}
}

// attempt intenta reservar y registra el resultado.
func (e *Engine) attempt(r Rule, c calendar.Class, stt *occState) {
	stt.attempts++
	stt.lastTry = time.Now()
	err := e.book(c.BookingID, c.Center)
	if err != nil {
		stt.lastError = err.Error()
		log.Printf("automation: %s %s %s @ %s → %v", r.Name, r.Club, c.Date, c.Start, err)
		return
	}
	stt.booked = true
	stt.lastError = ""
	log.Printf("automation: ✓ reservada %s %s %s @ %s", r.Name, r.Club, c.Date, c.Start)
}

// matches indica si una clase del calendario corresponde a una regla.
func matches(r Rule, c calendar.Class, loc *time.Location) bool {
	if !strings.EqualFold(c.Name, r.Name) || !strings.EqualFold(c.Club, r.Club) || c.Start != r.Start {
		return false
	}
	d, err := time.ParseInLocation("2006-01-02", c.Date, loc)
	if err != nil {
		return false
	}
	return int(d.Weekday()) == r.Weekday
}
