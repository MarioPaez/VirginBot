package automation

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

// Tiempos de la política de reserva. El plazo abre según la clase (calistenia 7
// días, solarium 2); las plazas liberadas aparecen sobre todo al abrir y a
// partir de 24h antes, así que en esos instantes sondeamos agresivamente.
const (
	slowInterval = 3 * time.Minute  // red de seguridad: pilla cancelaciones a cualquier hora
	hotInterval  = 8 * time.Second  // sondeo agresivo en las ventanas calientes
	hotLead      = 1 * time.Minute  // empezar a sondear 1 min antes del instante clave
	hotAfter     = 12 * time.Minute // seguir caliente 12 min después del instante clave
	horizonDays  = 8                // cubre la ventana más larga (calistenia, 7 días)
)

// Engine ejecuta las reglas de TODOS los usuarios con timing preciso. Las
// dependencias se inyectan por usuario para reservar con el cliente de cada uno.
type Engine struct {
	store    *Store
	fetchDay func(userID int64, date string) ([]calendar.Class, error) // fetch fresco de un día (autenticado, curado)
	book     func(userID int64, bookingID, center int) error
	notify   func(userID int64, subject, body string)
	loc      *time.Location

	mu    sync.Mutex
	state map[string]*occState // clave: userID|ruleID|fecha
}

type occState struct {
	booked       bool
	attempts     int
	lastError    string
	missNotified bool
	userID       int64
	name, club   string
	date, start  string
}

func NewEngine(
	store *Store,
	fetchDay func(int64, string) ([]calendar.Class, error),
	book func(int64, int, int) error,
	notify func(int64, string, string),
) *Engine {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		loc = time.UTC
	}
	if notify == nil {
		notify = func(int64, string, string) {}
	}
	return &Engine{store: store, fetchDay: fetchDay, book: book, notify: notify, loc: loc, state: map[string]*occState{}}
}

// occ es una ocurrencia concreta de una regla (un día concreto) de un usuario.
type occ struct {
	rule  Rule
	date  string
	start time.Time
}

func (o occ) stateKey() string {
	return fmt.Sprintf("%d|%s|%s", o.rule.UserID, o.rule.ID, o.date)
}

// fetchKey agrupa fetches por usuario+día (cada usuario ve su propio estado).
func (o occ) fetchKey() string {
	return fmt.Sprintf("%d|%s", o.rule.UserID, o.date)
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

// occurrences calcula, a partir de las reglas de todos los usuarios, las
// próximas ocurrencias no reservadas (sin tocar la red: solo por día de la
// semana y hora).
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

// cycle evalúa las ocurrencias. Si hotOnly, solo las que están en ventana
// caliente (para sondear rápido sin barrer todo). Agrupa el fetch por
// usuario+día para no duplicar scrapes.
func (e *Engine) cycle(now time.Time, hotOnly bool) {
	occs := e.occurrences(now)

	type ud struct {
		userID int64
		date   string
	}
	want := map[string]ud{}
	for _, o := range occs {
		if hotOnly && !e.isHot(o, now) {
			continue
		}
		want[o.fetchKey()] = ud{o.rule.UserID, o.date}
	}

	for _, k := range want {
		classes, err := e.fetchDay(k.userID, k.date)
		if err != nil {
			log.Printf("automation: fetch u%d %s: %v", k.userID, k.date, err)
			continue
		}
		for _, o := range occs {
			if o.rule.UserID == k.userID && o.date == k.date {
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
	if err := e.book(o.rule.UserID, c.BookingID, c.Center); err != nil {
		st.lastError = err.Error()
		log.Printf("automation: u%d %s %s @ %s → %v", o.rule.UserID, o.rule.Name, o.date, o.rule.Start, err)
		return
	}
	st.booked = true
	st.lastError = ""
	log.Printf("automation: ✓ u%d reservada %s %s %s @ %s", o.rule.UserID, o.rule.Name, o.rule.Club, o.date, o.rule.Start)
	e.notify(o.rule.UserID,
		fmt.Sprintf("VirginBot: ✓ reservada %s", o.rule.Name),
		fmt.Sprintf("¡Reserva conseguida! Te he apuntado automáticamente:\n\n%s\nIntentos: %d\n\nNos vemos en clase 💪\n",
			classLines(o.rule.Name, o.rule.Club, o.start), st.attempts),
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
		when, _ := time.ParseInLocation("2006-01-02 15:04", st.date+" "+st.start, e.loc)
		reason := st.lastError
		if reason == "" {
			reason = "no llegó a haber plaza reservable"
		}
		e.notify(st.userID,
			fmt.Sprintf("VirginBot: ✗ NO se pudo reservar %s", st.name),
			fmt.Sprintf("No he conseguido reservar esta clase:\n\n%s\nIntentos: %d\nÚltimo motivo: %s\n\nLa clase pudo llenarse antes de que se liberara una plaza.\n",
				classLines(st.name, st.club, when), st.attempts, reason),
		)
	}
}

// hotMoments son los instantes clave de una ocurrencia: la apertura del plazo
// (T - ventana de la clase) y T-24h (cancelaciones de última hora).
func (e *Engine) hotMoments(o occ) []time.Time {
	w := o.rule.OpensDaysBefore
	if w <= 0 {
		w = WindowDays(o.rule.Name)
	}
	return []time.Time{
		o.start.Add(-time.Duration(w) * 24 * time.Hour),
		o.start.Add(-24 * time.Hour),
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
		for _, m := range e.hotMoments(o) {
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
		if e.isHot(o, now) {
			return true
		}
	}
	return false
}

// isHot indica si `now` cae en la ventana caliente de alguno de los instantes clave.
func (e *Engine) isHot(o occ, now time.Time) bool {
	for _, m := range e.hotMoments(o) {
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
	key := o.stateKey()
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.state[key]
	if st == nil {
		st = &occState{userID: o.rule.UserID, name: o.rule.Name, club: o.rule.Club, date: o.date, start: o.rule.Start}
		e.state[key] = st
	}
	return st
}

var weekdaysIT = [...]string{"Domenica", "Lunedì", "Martedì", "Mercoledì", "Giovedì", "Venerdì", "Sabato"}
