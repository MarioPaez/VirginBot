package automation

import (
	"fmt"
	"log"
	"time"
)

// Health thresholds. A cycle sleeps at most maxIdle and its fires run in
// parallel, so 20 minutes without one means the loop is stuck. Several failed
// calls in an hour and not a single booking means vapi is failing us (rate
// limit, outage, a changed API).
const (
	engineStale     = 20 * time.Minute
	errorSpikeCount = 5
	alertEvery      = 6 * time.Hour
)

// Watchdog checks the engine's health every minute and calls alert (throttled)
// when it stops cycling or its booking calls keep failing. It never returns.
func Watchdog(e *Engine, alert func(title, body string)) {
	var last time.Time
	for range time.Tick(time.Minute) {
		problem := healthProblem(e, time.Now())
		if problem == "" || time.Since(last) < alertEvery {
			continue
		}
		last = time.Now()
		log.Printf("automation: HEALTH ALERT — %s", problem)
		alert("VirginBot: el motor no va bien", problem+" Mira los logs y la vista Admin. No vuelvo a avisar en 6 h.")
	}
}

// healthProblem describes what's wrong with the engine, or "" if it's fine.
func healthProblem(e *Engine, now time.Time) string {
	if lc := e.LastCycle(); !lc.IsZero() && now.Sub(lc) > engineStale {
		return fmt.Sprintf("El motor no completa un ciclo desde hace %s.", now.Sub(lc).Round(time.Minute))
	}
	if e.store == nil {
		return ""
	}
	o := e.store.OutcomesSince(now.Add(-time.Hour), "engine") // a failing watch isn't the engine
	if o[OutcomeError] >= errorSpikeCount && o[OutcomeBooked] == 0 {
		return fmt.Sprintf("%d intentos de reserva con error en la última hora y ninguna reserva.", o[OutcomeError])
	}
	return ""
}
