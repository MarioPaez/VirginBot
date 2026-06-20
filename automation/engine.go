package automation

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/notification"
)

// Tiempos de la política de reserva. El plazo abre 48h antes; las plazas
// liberadas aparecen sobre todo al abrir y a partir de 24h antes, así que en
// esos instantes sondeamos agresivamente.
const (
	slowInterval = 3 * time.Minute  // red de seguridad: pilla cancelaciones a cualquier hora
	hotInterval  = 8 * time.Second  // sondeo agresivo en las ventanas calientes
	hotLead      = 1 * time.Minute  // empezar a sondear 1 min antes del instante clave
	hotAfter     = 12 * time.Minute // seguir caliente 12 min después del instante clave
	horizonDays  = 6
)

// Engine ejecuta las reglas con timing preciso.
type Engine struct {
	store    *Store
	fetchDay func(date string) ([]calendar.Class, error) // fetch fresco de un día (autenticado, curado)
	book     func(bookingID, center int) error
	notify   notification.Notifier
	loc      *time.Location

	mu    sync.Mutex
	state map[string]*occState // clave: ruleID|fecha
}

type occState struct {
	booked       bool
	attempts     int
	lastError    string
	missNotified bool
	name, club   string
	date, start  string
}

func NewEngine(store *Store, fetchDay func(string) ([]calendar.Class, error), book func(int, int) error, notify notification.Notifier) *Engine {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	if notify == nil {
		notify = notification.NoOp{}
	}
	return &Engine{store: store, fetchDay: fetchDay, book: book, notify: notify, loc: loc, state: map[string]*occState{}}
}

// occ es una ocurrencia concreta de una regla (un día concreto).
type occ struct {
	rule  Rule
	date  string
	start time.Time
}

// Run arranca el bucle adaptativo: duerme hasta el siguiente instante clave y
// sondea rápido en las ventanas calientes, con un repaso lento periódico.
func (e *Engine) Run(stop <-chan struct{}) {
	lastSlow := time.Time{}
	for {
		now := time.Now().In(e.loc)
		if now.Sub(lastSlow) >= slowInterval {
			e.cycle(now, false)
			lastSlow = now
		} else if e.hotActive(now) {
			e.cycle(now, true)
		}
		select {
		case <-stop:
			return
		case <-time.After(e.untilNext(time.Now().In(e.loc), lastSlow)):
		}
	}
}

// occurrences calcula, a partir de las reglas, las próximas ocurrencias no
// reservadas (sin tocar la red: solo por día de la semana y hora).
func (e *Engine) occurrences(now time.Time) []occ {
	var out []occ
	for _, r := range e.store.List() {
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
			if st := e.get(r.ID + "|" + date); st != nil && st.booked {
				continue
			}
			out = append(out, occ{rule: r, date: date, start: start})
		}
	}
	return out
}

// cycle evalúa las ocurrencias. Si hotOnly, solo las que están en ventana
// caliente (para sondear rápido sin barrer todo).
func (e *Engine) cycle(now time.Time, hotOnly bool) {
	occs := e.occurrences(now)

	dates := map[string]bool{}
	for _, o := range occs {
		if hotOnly && !isHot(o.start, now) {
			continue
		}
		dates[o.date] = true
	}

	for date := range dates {
		classes, err := e.fetchDay(date)
		if err != nil {
			log.Printf("automation: fetch %s: %v", date, err)
			continue
		}
		for _, o := range occs {
			if o.date == date {
				e.handle(o, classes)
			}
		}
	}

	if !hotOnly {
		e.notifyMisses(now)
	}
}

// handle intenta reservar una ocurrencia si el sitio la marca reservable.
func (e *Engine) handle(o occ, classes []calendar.Class) {
	st := e.ensureState(o)
	if st.booked {
		return
	}
	c := findMatch(classes, o.rule)
	if c == nil {
		return
	}
	if c.Booked {
		st.booked = true
		return
	}
	if c.Status != "bookable" || c.BookingID == 0 {
		return
	}

	st.attempts++
	if err := e.book(c.BookingID, c.Center); err != nil {
		st.lastError = err.Error()
		log.Printf("automation: %s %s @ %s → %v", o.rule.Name, o.date, o.rule.Start, err)
		return
	}
	st.booked = true
	st.lastError = ""
	log.Printf("automation: ✓ reservada %s %s %s @ %s", o.rule.Name, o.rule.Club, o.date, o.rule.Start)
	e.send(
		fmt.Sprintf("VirginBot: reservada %s", o.rule.Name),
		fmt.Sprintf("Reservada automáticamente:\n\n%s\n%s · %s %s\n", o.rule.Name, o.rule.Club, weekdayName(o.start), o.start.Format("02/01 15:04")),
	)
}

// notifyMisses avisa (una vez) de las ocurrencias cuyo inicio pasó sin lograr
// reservar, si llegamos a intentarlo.
func (e *Engine) notifyMisses(now time.Time) {
	e.mu.Lock()
	var miss []*occState
	for _, st := range e.state {
		if st.booked || st.missNotified || st.attempts == 0 {
			continue
		}
		start, err := time.ParseInLocation("2006-01-02 15:04", st.date+" "+st.start, e.loc)
		if err == nil && now.After(start) {
			st.missNotified = true
			miss = append(miss, st)
		}
	}
	e.mu.Unlock()

	for _, st := range miss {
		e.send(
			fmt.Sprintf("VirginBot: NO reservada %s", st.name),
			fmt.Sprintf("No se pudo reservar (%d intentos, último error: %s):\n\n%s\n%s · %s %s\n",
				st.attempts, st.lastError, st.name, st.club, st.date, st.start),
		)
	}
}

func (e *Engine) send(subject, body string) {
	if err := e.notify.Notify(subject, body); err != nil {
		log.Printf("automation: aviso email fallido: %v", err)
	}
}

// untilNext devuelve cuánto dormir: hotInterval si hay ventana caliente activa;
// si no, hasta el inicio de la próxima ventana caliente o el próximo repaso lento.
func (e *Engine) untilNext(now, lastSlow time.Time) time.Duration {
	if e.hotActive(now) {
		return hotInterval
	}
	next := slowInterval - now.Sub(lastSlow)
	for _, o := range e.occurrences(now) {
		for _, m := range []time.Time{o.start.Add(-48 * time.Hour), o.start.Add(-24 * time.Hour)} {
			startAt := m.Add(-hotLead)
			if d := startAt.Sub(now); d > 0 && d < next {
				next = d
			}
		}
	}
	if next < hotInterval {
		next = hotInterval
	}
	return next
}

func (e *Engine) hotActive(now time.Time) bool {
	for _, o := range e.occurrences(now) {
		if isHot(o.start, now) {
			return true
		}
	}
	return false
}

// isHot indica si `now` cae en la ventana caliente alrededor de T-48h o T-24h.
func isHot(start, now time.Time) bool {
	for _, m := range []time.Time{start.Add(-48 * time.Hour), start.Add(-24 * time.Hour)} {
		if !now.Before(m.Add(-hotLead)) && !now.After(m.Add(hotAfter)) {
			return true
		}
	}
	return false
}

func findMatch(classes []calendar.Class, r Rule) *calendar.Class {
	for i := range classes {
		c := &classes[i]
		if strings.EqualFold(c.Name, r.Name) && strings.EqualFold(c.Club, r.Club) && c.Start == r.Start {
			return c
		}
	}
	return nil
}

// ---- estado ----

func (e *Engine) get(key string) *occState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state[key]
}

func (e *Engine) ensureState(o occ) *occState {
	key := o.rule.ID + "|" + o.date
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.state[key]
	if st == nil {
		st = &occState{name: o.rule.Name, club: o.rule.Club, date: o.date, start: o.rule.Start}
		e.state[key] = st
	}
	return st
}

var weekdaysIT = [...]string{"Domenica", "Lunedì", "Martedì", "Mercoledì", "Giovedì", "Venerdì", "Sabato"}

func weekdayName(t time.Time) string { return weekdaysIT[int(t.Weekday())] }
