package automation

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

// Política de reserva: PRECISA, no por sondeo continuo. Las plazas de una clase
// se habilitan cuando abre el plazo (calistenia 7 días antes, solarium 2), a la
// hora de la clase. Por eso solo intentamos en esos instantes —a la hora de la
// lección, cada día desde la apertura hasta 24h antes— en vez de machacar a
// Virgin cada pocos minutos (ineficiente y motivo de bloqueo).
const (
	attemptRounds = 2                // intentos por disparo (el 2º cubre un fallo transitorio o un retardo en abrir)
	attemptGap    = 8 * time.Second  // espera entre intentos del mismo disparo
	maxIdle       = 5 * time.Minute  // re-evalúa la agenda (sin red) para captar reglas nuevas
	triggerGrace  = 15 * time.Minute // un disparo solo cuenta si estamos a <=15min de su hora (no recupera disparos viejos)
	horizonDays   = 8                // cubre la ventana más larga (calistenia, 7 días)
)

// Engine ejecuta las reglas de TODOS los usuarios con disparos precisos.
type Engine struct {
	store    *Store
	fetchDay func(userID int64, date string) ([]calendar.Class, error) // fetch fresco de un día (autenticado, curado)
	book     func(userID int64, bookingID, center int) error
	notify   func(userID int64, subject, body string)
	loc      *time.Location
	gap      time.Duration // espera entre rondas de un intento (configurable en tests)
	debug    bool          // VIRGINBOT_DEBUG: loguea cada evaluación (para observar en local)

	mu    sync.Mutex
	state map[string]*occState // clave: userID|ruleID|fecha
}

type occState struct {
	booked       bool
	attempts     int
	lastError    string
	missNotified bool
	lastFired    time.Time // último disparo ya ejecutado (evita repetir el mismo)
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
	return &Engine{
		store: store, fetchDay: fetchDay, book: book, notify: notify, loc: loc,
		gap:   attemptGap,
		debug: os.Getenv("VIRGINBOT_DEBUG") != "",
		state: map[string]*occState{},
	}
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

func windowOf(o occ) int {
	if o.rule.OpensDaysBefore > 0 {
		return o.rule.OpensDaysBefore
	}
	return WindowDays(o.rule.Name)
}

// triggers son los instantes en que intentamos reservar la ocurrencia: a la hora
// de la clase, cada día desde la apertura del plazo (T - ventana) hasta 24h antes.
func (o occ) triggers() []time.Time {
	w := windowOf(o)
	ts := make([]time.Time, 0, w)
	for k := w; k >= 1; k-- { // T-w, T-(w-1), ..., T-1  (ascendente)
		ts = append(ts, o.start.AddDate(0, 0, -k)) // AddDate mantiene la hora de pared aunque cambie el horario (DST)
	}
	return ts
}

// Run duerme hasta el próximo disparo y solo entonces toca la red. Entre disparos
// despierta como mucho cada maxIdle para captar reglas nuevas (sin llamar a Virgin).
func (e *Engine) Run(stop <-chan struct{}) {
	for {
		now := time.Now().In(e.loc)
		occs := e.occurrences(now)
		if e.debug {
			log.Printf("[debug] %d ocurrencia(s) pendientes", len(occs))
		}
		for _, o := range occs {
			e.maybeFire(o, now)
		}
		sleep := e.untilNext(now, occs)
		if e.debug {
			log.Printf("[debug] próximo intento en %s", sleep.Round(time.Second))
		}
		select {
		case <-stop:
			return
		case <-time.After(sleep):
		}
	}
}

// occurrences calcula, de las reglas de todos los usuarios, las próximas
// ocurrencias no reservadas (sin tocar la red: solo por día de la semana y hora).
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

// maybeFire dispara un intento si toca: hay un disparo (hora de la clase) vencido
// y aún no ejecutado. Tras el último disparo (24h antes) sin éxito, avisa por email.
func (e *Engine) maybeFire(o occ, now time.Time) {
	st := e.ensureState(o)
	if st.booked {
		return
	}
	trs := o.triggers()

	// Disparo vencido más reciente aún no ejecutado (si nos saltamos varios por
	// estar parados, ejecuta solo uno para ponernos al día).
	var dueT time.Time
	hasFuture := false
	for _, t := range trs {
		if t.After(now) {
			hasFuture = true
		} else if t.After(st.lastFired) && now.Sub(t) <= triggerGrace {
			dueT = t // vencido hace poco (a su hora) y aún no ejecutado; no recupera disparos viejos
		}
	}

	// Fallback: regla creada con el plazo ya abierto y SIN disparos futuros (en las
	// últimas 24h antes de la clase). Intenta una vez de inmediato, no esperar.
	if dueT.IsZero() && !hasFuture && st.lastFired.IsZero() && st.attempts == 0 {
		open := o.start.AddDate(0, 0, -windowOf(o))
		if !now.Before(open) && now.Before(o.start) {
			dueT = now
		}
	}
	if dueT.IsZero() {
		return
	}

	e.attempt(o, st)
	st.lastFired = dueT

	// Si ya fue el último disparo (a 24h) y no se consiguió, avisa una sola vez.
	last := o.start.Add(-24 * time.Hour)
	if !st.booked && !st.missNotified && !dueT.Before(last) {
		st.missNotified = true
		reason := st.lastError
		if reason == "" {
			reason = "no llegó a haber plaza reservable"
		}
		e.notify(o.rule.UserID,
			fmt.Sprintf("VirginBot: ✗ NO se pudo reservar %s", o.rule.Name),
			fmt.Sprintf("No he conseguido reservar esta clase:\n\n%s\nIntentos: %d\nÚltimo motivo: %s\n\nLo intenté a la hora de la clase desde que abrió el plazo hasta 24h antes.\n",
				classLines(o.rule.Name, o.rule.Club, o.start), st.attempts, reason))
	}
}

// attempt hace hasta attemptRounds rondas (fetch + reserva) espaciadas attemptGap.
// La 2ª ronda cubre un fallo transitorio o un pequeño retardo en que Virgin marque
// la clase reservable justo al abrir el plazo.
func (e *Engine) attempt(o occ, st *occState) {
	for round := 0; round < attemptRounds && !st.booked; round++ {
		classes, err := e.fetchDay(o.rule.UserID, o.date)
		if err != nil {
			st.lastError = err.Error()
			log.Printf("automation: u%d fetch %s: %v", o.rule.UserID, o.date, err)
		} else {
			c := findMatch(classes, o.rule)
			if e.debug {
				e.logClass(o, c)
			}
			switch {
			case c == nil:
				st.lastError = "no está en el calendario aún"
			case c.Booked:
				st.booked = true
				return
			case c.Status == "bookable" && c.BookingID != 0:
				st.attempts++
				if err := e.book(o.rule.UserID, c.BookingID, c.Center); err != nil {
					st.lastError = err.Error()
					log.Printf("automation: u%d book %s %s: %v", o.rule.UserID, o.rule.Name, o.date, err)
				} else {
					st.booked = true
					log.Printf("automation: ✓ u%d reservada %s %s %s @ %s",
						o.rule.UserID, o.rule.Name, o.rule.Club, o.date, o.rule.Start)
					e.notify(o.rule.UserID,
						fmt.Sprintf("VirginBot: ✓ reservada %s", o.rule.Name),
						fmt.Sprintf("¡Reserva conseguida! Te he apuntado automáticamente:\n\n%s\nIntentos: %d\n\nNos vemos en clase 💪\n",
							classLines(o.rule.Name, o.rule.Club, o.start), st.attempts))
					return
				}
			default:
				st.lastError = "no reservable (status=" + c.Status + ")"
			}
		}
		if round < attemptRounds-1 && e.gap > 0 {
			time.Sleep(e.gap)
		}
	}
}

func (e *Engine) logClass(o occ, c *calendar.Class) {
	if c == nil {
		log.Printf("[debug] u%d %s %s @ %s — no está en el calendario aún", o.rule.UserID, o.rule.Name, o.date, o.rule.Start)
		return
	}
	log.Printf("[debug] u%d %s %s @ %s — status=%q booked=%v bookingId=%d",
		o.rule.UserID, o.rule.Name, o.date, o.rule.Start, c.Status, c.Booked, c.BookingID)
}

// untilNext devuelve cuánto dormir: hasta el próximo disparo, o maxIdle si está
// más lejos (para refrescar la agenda y captar reglas nuevas sin tocar la red).
func (e *Engine) untilNext(now time.Time, occs []occ) time.Duration {
	var next time.Time
	for _, o := range occs {
		for _, t := range o.triggers() {
			if t.After(now) && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
	}
	sleep := maxIdle
	if !next.IsZero() {
		if d := next.Sub(now); d < sleep {
			sleep = d
		}
	}
	if sleep < time.Second {
		sleep = time.Second
	}
	return sleep
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
		st = &occState{}
		e.state[key] = st
	}
	return st
}

var weekdaysIT = [...]string{"Domenica", "Lunedì", "Martedì", "Mercoledì", "Giovedì", "Venerdì", "Sabato"}
