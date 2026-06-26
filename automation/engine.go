package automation

import (
	"fmt"
	"log"
	"os"
	"strconv"
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
// Parámetros del disparo. El plazo de Virgin a veces se habilita unos segundos
// DESPUÉS de la hora exacta de la clase, así que damos un margen inicial y varios
// intentos espaciados. Tunables por env (VIRGINBOT_ATTEMPTS/GAP_SEC/LEAD_SEC) sin
// recompilar, porque el retardo real es empírico.
const (
	defaultAttempts = 9                // intentos por disparo (~1 min de ventana con los gaps de abajo)
	defaultLeadSec  = 0                // margen tras la hora antes del 1er intento (0 = a la hora exacta)
	defaultGap1Sec  = 4                // espera entre el 1er y el 2º intento (corta: por si abre con unos segundos de retraso)
	defaultGapSec   = 8                // espera entre los intentos siguientes
	maxIdle         = 5 * time.Minute  // re-evalúa la agenda (sin red) para captar reglas nuevas
	triggerGrace    = 15 * time.Minute // un disparo solo cuenta si estamos a <=15min de su hora (no recupera disparos viejos)
	horizonDays     = 8                // cubre la ventana más larga (calistenia, 7 días)
)

// Engine ejecuta las reglas de TODOS los usuarios con disparos precisos.
type Engine struct {
	store    *Store
	fetchDay func(userID int64, date string) ([]calendar.Class, error) // fetch fresco de un día (autenticado, curado)
	book     func(userID int64, clubID, classID, sessionID int, date string) error
	notify   func(userID int64, subject, body string)
	loc      *time.Location
	rounds   int           // intentos por disparo
	lead     time.Duration // margen tras la hora antes del 1er intento
	gap1     time.Duration // espera entre el 1er y el 2º intento
	gap      time.Duration // espera entre los intentos siguientes
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
	book func(int64, int, int, int, string) error,
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
		rounds: envInt("VIRGINBOT_ATTEMPTS", defaultAttempts),
		lead:   time.Duration(envInt("VIRGINBOT_LEAD_SEC", defaultLeadSec)) * time.Second,
		gap1:   time.Duration(envInt("VIRGINBOT_GAP1_SEC", defaultGap1Sec)) * time.Second,
		gap:    time.Duration(envInt("VIRGINBOT_GAP_SEC", defaultGapSec)) * time.Second,
		debug:  os.Getenv("VIRGINBOT_DEBUG") != "",
		state:  map[string]*occState{},
	}
}

// envInt lee un entero positivo de una variable de entorno, o devuelve def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
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

	log.Printf("automation: u%d ▶ DISPARO %s · %s @ %s (clase del %s) — intentando reservar",
		o.rule.UserID, o.rule.Name, o.rule.Club, o.rule.Start, o.date)
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
	tag := fmt.Sprintf("u%d %s %s @ %s", o.rule.UserID, o.rule.Name, o.date, o.rule.Start)
	if e.lead > 0 {
		log.Printf("automation: %s — esperando %s tras la hora (el plazo a veces se habilita unos segundos tarde)", tag, e.lead)
		time.Sleep(e.lead)
	}
	for round := 0; round < e.rounds && !st.booked; round++ {
		classes, err := e.fetchDay(o.rule.UserID, o.date)
		if err != nil {
			st.lastError = err.Error()
			log.Printf("automation: %s — ronda %d/%d: ERROR al consultar el calendario: %v", tag, round+1, e.rounds, err)
		} else {
			matches := findMatches(classes, o.rule)
			switch {
			case len(matches) == 0:
				st.lastError = "no está en el calendario aún"
				log.Printf("automation: %s — ronda %d/%d: la clase aún no está en el calendario", tag, round+1, e.rounds)
			case bookedMatch(matches) != nil:
				st.booked = true
				if st.attempts > 0 {
					// Habíamos intentado reservar: la reserva SÍ cuajó (aunque la API
					// devolviera error). Lo confirmamos como éxito.
					log.Printf("automation: ✓ %s — RESERVADA (confirmada tras %d intento(s), pese a error de la API)", tag, st.attempts)
					e.notifyBooked(o, st)
				} else {
					log.Printf("automation: %s — ya estaba reservada, nada que hacer", tag)
				}
				return
			default:
				// El Solarium expone varias "camas" reservables a la misma hora.
				// Probamos las que el calendario marca reservables, una a una, hasta
				// que el POST acepte: ese estado a veces miente (TOO_LATE_TO_BOOK).
				beds := bookableBeds(matches)
				if len(beds) == 0 {
					st.lastError = "no reservable (status=" + matches[0].Status + ")"
					log.Printf("automation: %s — ronda %d/%d: no reservable (status=%q, p. ej. llena o plazo no abierto)", tag, round+1, e.rounds, matches[0].Status)
					break
				}
				log.Printf("automation: %s — ronda %d/%d: %d cama(s) RESERVABLE(s), probando…", tag, round+1, e.rounds, len(beds))
				for bi, c := range beds {
					st.attempts++
					log.Printf("automation: %s — ronda %d/%d cama %d/%d (classId=%d sessionId=%d), reservando…", tag, round+1, e.rounds, bi+1, len(beds), c.ClassID, c.SessionID)
					if err := e.book(o.rule.UserID, c.ClubID, c.ClassID, c.SessionID, o.date); err != nil {
						st.lastError = err.Error()
						log.Printf("automation: %s — ronda %d/%d cama %d/%d: FALLÓ la reserva: %v", tag, round+1, e.rounds, bi+1, len(beds), err)
						continue
					}
					st.booked = true
					log.Printf("automation: ✓ %s — RESERVADA (cama %d/%d, intento %d)", tag, bi+1, len(beds), st.attempts)
					e.notifyBooked(o, st)
					return
				}
			}
		}
		if round < e.rounds-1 {
			wait := e.gap // 7s entre los intentos siguientes
			if round == 0 {
				wait = e.gap1 // 5s entre el 1º y el 2º
			}
			if wait > 0 {
				log.Printf("automation: %s — esperando %s para la ronda %d/%d", tag, wait, round+2, e.rounds)
				time.Sleep(wait)
			}
		}
	}
	// Reconciliación final: si en la ÚLTIMA ronda el book devolvió error pero la
	// reserva cuajó, no hay ronda siguiente que lo detecte vía overlay. Volvemos a
	// consultar una vez para confirmar antes de dar el disparo por fallido (evita el
	// email de fallo falso y un reintento que reservaría dos veces).
	if !st.booked && st.attempts > 0 {
		if classes, err := e.fetchDay(o.rule.UserID, o.date); err == nil {
			if bookedMatch(findMatches(classes, o.rule)) != nil {
				st.booked = true
				log.Printf("automation: ✓ %s — RESERVADA (confirmada en reconciliación final, pese a error de la API)", tag)
				e.notifyBooked(o, st)
			}
		}
	}
	if !st.booked {
		log.Printf("automation: %s — disparo terminado SIN reservar tras %d rondas (último motivo: %s)", tag, e.rounds, st.lastError)
	}
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

// findMatches devuelve TODAS las instancias que casan con la regla. El Solarium
// expone varias "camas" a la misma hora (cada una una clase reservable distinta),
// así que una regla puede casar con más de una.
func findMatches(classes []calendar.Class, r Rule) []*calendar.Class {
	var out []*calendar.Class
	for i := range classes {
		c := &classes[i]
		if strings.EqualFold(c.Name, r.Name) && strings.EqualFold(c.Club, r.Club) && c.Start == r.Start {
			out = append(out, c)
		}
	}
	return out
}

// bookedMatch devuelve la primera instancia ya reservada por el socio, si la hay
// (basta una: todas las camas comparten nombre+club+fecha+hora en el overlay).
func bookedMatch(matches []*calendar.Class) *calendar.Class {
	for _, c := range matches {
		if c.Booked {
			return c
		}
	}
	return nil
}

// bookableBeds filtra las instancias que el calendario marca reservables y con
// ids válidos. El motor las prueba en orden hasta que el POST de reserva acepte.
func bookableBeds(matches []*calendar.Class) []*calendar.Class {
	var out []*calendar.Class
	for _, c := range matches {
		if c.Status == "bookable" && c.ClassID != 0 && c.SessionID != 0 {
			out = append(out, c)
		}
	}
	return out
}

// notifyBooked avisa al socio de una reserva conseguida (incluye los intentos si
// los hubo: reserva directa, confirmación tras error de la API o reconciliación).
func (e *Engine) notifyBooked(o occ, st *occState) {
	body := fmt.Sprintf("¡Reserva conseguida! Te he apuntado automáticamente:\n\n%s\n",
		classLines(o.rule.Name, o.rule.Club, o.start))
	if st.attempts > 0 {
		body += fmt.Sprintf("Intentos: %d\n", st.attempts)
	}
	body += "\nNos vemos en clase 💪\n"
	e.notify(o.rule.UserID, fmt.Sprintf("VirginBot: ✓ reservada %s", o.rule.Name), body)
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
