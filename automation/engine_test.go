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

// calisthenics (8-day window) on Monday 29/06 at 18:15.
func sampleOcc(loc *time.Location) occ {
	start := time.Date(2026, 6, 29, 18, 15, 0, 0, loc)
	return occ{
		rule:  Rule{UserID: 1, ID: "x", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15", Weekday: 1, OpensDaysBefore: 8, Enabled: true},
		date:  "2026-06-29",
		start: start,
	}
}

func TestTriggers(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)
	trs := o.triggers()
	if len(trs) != 8 {
		t.Fatalf("expected 8 fires (T-8..T-1), got %d", len(trs))
	}
	if !trs[0].Equal(o.start.AddDate(0, 0, -8)) {
		t.Errorf("first fire must be the opening T-8d: %v", trs[0])
	}
	if !trs[7].Equal(o.start.AddDate(0, 0, -1)) {
		t.Errorf("last fire must be 24h before: %v", trs[7])
	}
	for _, tr := range trs {
		if tr.Hour() != 18 || tr.Minute() != 15 {
			t.Errorf("every fire must be at the class time (18:15): %v", tr)
		}
	}
}

func TestFiresAtClassHourNotBefore(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	var books int
	fetch := func(int64, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { books++; return nil }
	e := NewEngine(nil, fetch, book, nil, nil)
	e.loc = loc
	e.gap = 0
	e.gap1 = 0
	e.lead = 0

	// Mid-afternoon on the fire day, BEFORE 18:15: must not book.
	e.maybeFire(o, time.Date(2026, 6, 23, 14, 0, 0, 0, loc))
	if books != 0 {
		t.Fatalf("must not book before the class time; books=%d", books)
	}

	// Right at 18:15 (fire T-6d): must book once.
	e.maybeFire(o, time.Date(2026, 6, 23, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("must book at the class time; books=%d", books)
	}

	// Next day's fire: already booked, doesn't repeat.
	e.maybeFire(o, time.Date(2026, 6, 24, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("must not book twice; books=%d", books)
	}
}

func TestFailureEmailAfterLastTrigger(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	// The class is never bookable (full) → must notify after the last fire (24h before).
	fetch := func(int64, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
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

	// Intermediate fire (T-2d 18:15): no failure notice yet.
	e.maybeFire(o, time.Date(2026, 6, 27, 18, 15, 1, 0, loc))
	if failEmails != 0 {
		t.Fatalf("must not notify failure on intermediate fires; failEmails=%d", failEmails)
	}
	// Last fire (T-1d 18:15): notifies failure once.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failEmails != 1 {
		t.Fatalf("must notify failure after the last fire; failEmails=%d", failEmails)
	}
}

// Pausing a rule (enabled=0) must remove it from the engine's scheduling without
// deleting it; re-enabling schedules it again. Uses a real temporary SQLite DB.
func TestPausedRuleNotScheduled(t *testing.T) {
	loc := mustLoc(t)
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer dbh.Close()
	store := NewStore(dbh)

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, loc)
	wd := int(now.AddDate(0, 0, 2).Weekday()) // class 2 days later (within the horizon)
	rule, err := store.Add(7, "Calisthenics", "Milano Corso Como", wd, "23:30")
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}

	e := NewEngine(store, nil, nil, nil, nil)
	e.loc = loc

	// The horizon (9 days) can span the same weekday twice: what matters is
	// scheduled (>0) vs not scheduled (0), not the exact count.
	if got := countOcc(e.occurrences(now), rule.ID); got == 0 {
		t.Fatal("an active rule must be scheduled; occurrences=0")
	}
	if err := store.SetEnabled(7, rule.ID, false); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if got := countOcc(e.occurrences(now), rule.ID); got != 0 {
		t.Fatalf("a paused rule must NOT be scheduled; occurrences=%d", got)
	}
	// Still exists (not deleted), just disabled.
	if r, ok := store.Get(7, rule.ID); !ok || r.Enabled {
		t.Fatalf("the paused rule must still exist with enabled=false; ok=%v enabled=%v", ok, r.Enabled)
	}
	if err := store.SetEnabled(7, rule.ID, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if got := countOcc(e.occurrences(now), rule.ID); got == 0 {
		t.Fatal("re-enabling must schedule it again; occurrences=0")
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

// The booking API returns an error on ALL rounds (including the last) but the
// booking did go through: the final reconciliation must detect it, confirm
// success and NOT notify failure. Covers the error-but-went-through case on the
// last round.
func TestFinalReconcileConfirmsBooking(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	var fetches int
	fetch := func(int64, string) ([]calendar.Class, error) {
		fetches++
		c := calendar.Class{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			ClubID: 209, ClassID: 387071, SessionID: 79403,
		}
		// During the rounds it looks bookable; in the final reconciliation (last
		// query) it already shows booked (the booking went through despite the API error).
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

	// Last fire (T-1d 18:15): book always fails, but the booking went through.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failEmails != 0 {
		t.Fatalf("must not notify failure if the booking went through; failEmails=%d", failEmails)
	}
	if okEmails != 1 {
		t.Fatalf("must confirm the booking in the final reconciliation; okEmails=%d", okEmails)
	}
}
