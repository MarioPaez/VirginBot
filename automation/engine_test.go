package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
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
			Status: "bookable", BookingID: 123, Center: 209,
		}}, nil
	}
	book := func(int64, int, int) error { books++; return nil }
	e := NewEngine(nil, fetch, book, nil)
	e.loc = loc
	e.gap = 0

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
			Status: "waitlist", BookingID: 123, Center: 209,
		}}, nil
	}
	book := func(int64, int, int) error { return nil }
	var failEmails int
	notify := func(_ int64, subject, _ string) {
		if strings.HasPrefix(subject, "VirginBot: ✗") {
			failEmails++
		}
	}
	e := NewEngine(nil, fetch, book, notify)
	e.loc = loc
	e.gap = 0

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
