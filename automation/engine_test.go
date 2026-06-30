package automation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
)

func mustLoc(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatalf("tz: %v", err)
	}
	return loc
}

// calisthenics (ventana 7 días) el lunes 29/06 a las 18:15.
func sampleOcc(loc *time.Location) occ {
	start := time.Date(2026, 6, 29, 18, 15, 0, 0, loc)
	return occ{
		rule:  Rule{UserID: 1, ID: "x", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15", Weekday: 1, OpensDaysBefore: 7, Enabled: true},
		date:  "2026-06-29",
		start: start,
	}
}

func TestTriggers(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)
	trs := o.triggers()
	if len(trs) != 7 {
		t.Fatalf("esperaba 7 disparos (T-7..T-1), got %d", len(trs))
	}
	if !trs[0].Equal(o.start.AddDate(0, 0, -7)) {
		t.Errorf("primer disparo debe ser la apertura T-7d: %v", trs[0])
	}
	if !trs[6].Equal(o.start.AddDate(0, 0, -1)) {
		t.Errorf("último disparo debe ser 24h antes: %v", trs[6])
	}
	for _, tr := range trs {
		if tr.Hour() != 18 || tr.Minute() != 15 {
			t.Errorf("todo disparo debe ser a la hora de la clase (18:15): %v", tr)
		}
	}
}

func TestFiresAtClassHourNotBefore(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	var books int
	fetch := func(int64, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { books++; return nil }
	e := NewEngine(nil, fetch, book, nil, nil)
	e.loc = loc
	e.gap = 0
	e.gap1 = 0
	e.lead = 0

	// A media tarde del día del disparo, ANTES de las 18:15: no debe reservar.
	e.maybeFire(o, time.Date(2026, 6, 23, 14, 0, 0, 0, loc))
	if books != 0 {
		t.Fatalf("no debe reservar antes de la hora de la clase; books=%d", books)
	}

	// Justo a las 18:15 (disparo T-6d): debe reservar una vez.
	e.maybeFire(o, time.Date(2026, 6, 23, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("debe reservar a la hora de la clase; books=%d", books)
	}

	// Disparo del día siguiente: ya reservada, no repite.
	e.maybeFire(o, time.Date(2026, 6, 24, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("no debe reservar dos veces; books=%d", books)
	}
}

func TestFailureEmailAfterLastTrigger(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	// La clase nunca es reservable (llena) → debe avisar tras el último disparo (24h antes).
	fetch := func(int64, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "waitlist", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { return nil }
	var failEmails int
	notify := func(_ int64, subject, _ string) {
		if strings.HasPrefix(subject, "VirginBot: ✗") {
			failEmails++
		}
	}
	e := NewEngine(nil, fetch, book, notify, nil)
	e.loc = loc
	e.gap = 0
	e.gap1 = 0
	e.lead = 0

	// Disparo intermedio (T-2d 18:15): aún no avisa de fallo.
	e.maybeFire(o, time.Date(2026, 6, 27, 18, 15, 1, 0, loc))
	if failEmails != 0 {
		t.Fatalf("no debe avisar de fallo en disparos intermedios; failEmails=%d", failEmails)
	}
	// Último disparo (T-1d 18:15): avisa de fallo una vez.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failEmails != 1 {
		t.Fatalf("debe avisar de fallo tras el último disparo; failEmails=%d", failEmails)
	}
}

// Pausar una regla (enabled=0) debe sacarla de la planificación del motor sin
// borrarla; reactivarla la vuelve a programar. Usa una BD SQLite temporal real.
func TestPausedRuleNotScheduled(t *testing.T) {
	loc := mustLoc(t)
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("abrir BD: %v", err)
	}
	defer dbh.Close()
	store := NewStore(dbh)

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, loc)
	wd := int(now.AddDate(0, 0, 2).Weekday()) // clase 2 días después (dentro del horizonte)
	rule, err := store.Add(7, "Calisthenics", "Milano Corso Como", wd, "23:30")
	if err != nil {
		t.Fatalf("añadir regla: %v", err)
	}

	e := NewEngine(store, nil, nil, nil, nil)
	e.loc = loc

	if got := countOcc(e.occurrences(now), rule.ID); got != 1 {
		t.Fatalf("una regla activa debe programarse; ocurrencias=%d", got)
	}
	if err := store.SetEnabled(7, rule.ID, false); err != nil {
		t.Fatalf("pausar: %v", err)
	}
	if got := countOcc(e.occurrences(now), rule.ID); got != 0 {
		t.Fatalf("una regla pausada NO debe programarse; ocurrencias=%d", got)
	}
	// Sigue existiendo (no se ha borrado), solo deshabilitada.
	if r, ok := store.Get(7, rule.ID); !ok || r.Enabled {
		t.Fatalf("la regla pausada debe seguir existiendo y con enabled=false; ok=%v enabled=%v", ok, r.Enabled)
	}
	if err := store.SetEnabled(7, rule.ID, true); err != nil {
		t.Fatalf("reactivar: %v", err)
	}
	if got := countOcc(e.occurrences(now), rule.ID); got != 1 {
		t.Fatalf("al reactivar debe volver a programarse; ocurrencias=%d", got)
	}
}

func countOcc(occs []occ, id string) int {
	n := 0
	for _, o := range occs {
		if o.rule.ID == id {
			n++
		}
	}
	return n
}

// La API de reserva devuelve error en TODAS las rondas (incluida la última) pero la
// reserva sí cuajó: la reconciliación final debe detectarla, confirmar éxito y NO
// avisar de fallo. Cubre el caso del error-pero-cuajó en la última ronda.
func TestFinalReconcileConfirmsBooking(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	var fetches int
	fetch := func(int64, string) ([]calendar.Class, error) {
		fetches++
		c := calendar.Class{
			Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			ClubID: 209, ClassID: 387071, SessionID: 79403,
		}
		// Durante las rondas se ve reservable; en la reconciliación final (última
		// consulta) ya aparece reservada (la reserva cuajó pese al error de la API).
		if fetches >= 3 {
			c.Booked = true
			c.Status = "booked"
		} else {
			c.Status = "bookable"
		}
		return []calendar.Class{c}, nil
	}
	book := func(int64, int, int, int, string) error { return errors.New("HTTP 500") }
	var okEmails, failEmails int
	notify := func(_ int64, subject, _ string) {
		switch {
		case strings.HasPrefix(subject, "VirginBot: ✓"):
			okEmails++
		case strings.HasPrefix(subject, "VirginBot: ✗"):
			failEmails++
		}
	}
	e := NewEngine(nil, fetch, book, notify, nil)
	e.loc = loc
	e.rounds = 2
	e.gap, e.gap1, e.lead = 0, 0, 0

	// Último disparo (T-1d 18:15): book falla siempre, pero la reserva cuajó.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failEmails != 0 {
		t.Fatalf("no debe avisar de fallo si la reserva cuajó; failEmails=%d", failEmails)
	}
	if okEmails != 1 {
		t.Fatalf("debe confirmar la reserva en la reconciliación final; okEmails=%d", okEmails)
	}
}
