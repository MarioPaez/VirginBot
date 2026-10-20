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
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { books++; return nil }
	e := NewEngine(nil, fetch, book, nil)
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

// A successful booking also pushes a short notice (title + class line + the
// bookings page) to the user's devices when push is set.
func TestPushOnBooked(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { return nil }
	e := NewEngine(nil, fetch, book, func(int64) string { return "es" })
	e.loc, e.gap, e.gap1, e.lead = loc, 0, 0, 0
	var got []string
	e.SetPush(func(userID int64, title, body, url string) { got = append(got, title, body, url) })

	e.maybeFire(o, time.Date(2026, 6, 23, 18, 15, 1, 0, loc))
	if len(got) != 3 {
		t.Fatalf("expected one push, got %v", got)
	}
	if got[0] != "Reservada ✓" || !strings.HasPrefix(got[1], "Calisthenics · Milano Corso Como · ") || got[2] != "/?view=bookings" {
		t.Fatalf("unexpected push %q", got)
	}
}

// If the spot goes between the fetch and the POST, Virgin waitlists us: the
// engine stops for that occurrence (no more POSTs, no "couldn't book" later)
// and tells the user their position.
func TestWaitlistedStopsAndNotifies(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	var posts int
	book := func(int64, int, int, int, string) error { posts++; return ErrWaitlisted{Position: 2} }
	e := NewEngine(nil, fetch, book, func(int64) string { return "es" })
	e.loc, e.gap, e.gap1, e.lead = loc, 0, 0, 0
	var titles []string
	e.SetPush(func(_ int64, title, _, _ string) { titles = append(titles, title) })

	e.maybeFire(o, time.Date(2026, 6, 27, 18, 15, 1, 0, loc)) // T-2
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc)) // T-1, the last fire
	if posts != 1 {
		t.Fatalf("must stop after being waitlisted; POSTs=%d", posts)
	}
	if len(titles) != 1 || titles[0] != "En lista de espera · posición 2" {
		t.Fatalf("expected one waitlist notice with the position, got %q", titles)
	}
}

func TestFailureNoticeAfterLastTrigger(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)

	// The class is never bookable (full) → must notify after the last fire (24h before).
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "waitlist", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { return nil }
	var failNotices int
	e := NewEngine(nil, fetch, book, nil)
	e.SetPush(func(_ int64, _, _, url string) {
		if url == "/?view=automations" { // the "couldn't book" notice
			failNotices++
		}
	})
	e.loc = loc
	e.gap = 0
	e.gap1 = 0
	e.lead = 0

	// Intermediate fire (T-2d 18:15): no failure notice yet.
	e.maybeFire(o, time.Date(2026, 6, 27, 18, 15, 1, 0, loc))
	if failNotices != 0 {
		t.Fatalf("must not notify failure on intermediate fires; failNotices=%d", failNotices)
	}
	// Last fire (T-1d 18:15): notifies failure once.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failNotices != 1 {
		t.Fatalf("must notify failure after the last fire; failNotices=%d", failNotices)
	}
}

// Inside the last 24h (no fires left) the fallback only fires for a rule just
// created, not for an old one after a restart (occState lives in memory).
func TestFallbackOnlyForNewRules(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 6, 29, 8, 0, 0, 0, loc) // class day, 18:15 still ahead

	var books int
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: "bookable", ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
	book := func(int64, int, int, int, string) error { books++; return nil }
	newEngine := func() *Engine {
		e := NewEngine(nil, fetch, book, nil)
		e.loc, e.gap, e.gap1, e.lead = loc, 0, 0, 0
		return e
	}

	old := sampleOcc(loc)
	old.rule.Created = now.AddDate(0, 0, -10)
	newEngine().maybeFire(old, now)
	if books != 0 {
		t.Fatalf("an old rule must not fire on restart inside the last 24h; books=%d", books)
	}

	fresh := sampleOcc(loc)
	fresh.rule.Created = now.Add(-time.Minute)
	newEngine().maybeFire(fresh, now)
	if books != 1 {
		t.Fatalf("a rule just created must try once; books=%d", books)
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
	rule, err := store.Add(7, "Calisthenics", "Milano Corso Como", wd, "23:30", "")
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}

	e := NewEngine(store, nil, nil, nil)
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

// Cancelling one occurrence skips only that date: the rule survives and keeps
// scheduling the other weeks; booking it again (Unskip) re-schedules it.
func TestSkippedOccurrenceNotScheduled(t *testing.T) {
	loc := mustLoc(t)
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer dbh.Close()
	store := NewStore(dbh)

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, loc)
	day := now.AddDate(0, 0, 1) // tomorrow; the same weekday recurs at +8, still within the horizon
	wd, date := int(day.Weekday()), day.Format("2006-01-02")
	rule, err := store.Add(7, "Calisthenics", "Milano Corso Como", wd, "18:15", "")
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	e := NewEngine(store, nil, nil, nil)
	e.loc = loc

	has := func(d string) bool {
		for _, o := range e.occurrences(now) {
			if o.rule.ID == rule.ID && o.date == d {
				return true
			}
		}
		return false
	}
	next := day.AddDate(0, 0, 7).Format("2006-01-02")
	if !has(date) || !has(next) {
		t.Fatal("both weekly occurrences must be scheduled before cancelling")
	}

	if ok, err := store.Skip(7, "Calisthenics", "Milano Corso Como", wd, "18:15", date); err != nil || !ok {
		t.Fatalf("skip must mark the date: ok=%v err=%v", ok, err)
	}
	if has(date) {
		t.Fatal("a cancelled occurrence must NOT be scheduled")
	}
	if !has(next) {
		t.Fatal("the following week must still be scheduled")
	}
	if _, ok := store.Get(7, rule.ID); !ok {
		t.Fatal("cancelling must not delete the rule")
	}

	if err := store.Unskip(7, "Calisthenics", "Milano Corso Como", wd, "18:15", date); err != nil {
		t.Fatalf("unskip: %v", err)
	}
	if !has(date) {
		t.Fatal("unskipping must schedule the occurrence again")
	}

	// Skip without a matching rule is a no-op; past skips are purged.
	if ok, err := store.Skip(7, "Yoga", "Milano Corso Como", wd, "18:15", date); err != nil || ok {
		t.Fatalf("skip without a rule must be a no-op: ok=%v err=%v", ok, err)
	}
	store.Skip(7, "Calisthenics", "Milano Corso Como", wd, "18:15", date)
	if err := store.PurgeSkips(next); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if store.Skipped(7, rule.ID, date) {
		t.Fatal("skips before the purge date must be deleted")
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
	fetch := func(int64, string, string) ([]calendar.Class, error) {
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
	var okNotices, failNotices int
	e := NewEngine(nil, fetch, book, nil)
	e.SetPush(func(_ int64, _, _, url string) {
		switch url {
		case "/?view=bookings":
			okNotices++
		case "/?view=automations":
			failNotices++
		}
	})
	e.loc = loc
	e.rounds = 2
	e.gap, e.gap1, e.lead = 0, 0, 0

	// Last fire (T-1d 18:15): book always fails, but the booking went through.
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc))
	if failNotices != 0 {
		t.Fatalf("must not notify failure if the booking went through; failNotices=%d", failNotices)
	}
	if okNotices != 1 {
		t.Fatalf("must confirm the booking in the final reconciliation; okNotices=%d", okNotices)
	}
}

// A one-off rule schedules only its own date, switching it to weekly schedules
// every week again, and it's purged once its date has passed.
func TestOnceRuleOnlyItsDate(t *testing.T) {
	loc := mustLoc(t)
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer dbh.Close()
	store := NewStore(dbh)

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, loc) // Wednesday
	first := now.AddDate(0, 0, 1)                  // Thursday 2/7; Thursday 9/7 is also in the horizon
	once := first.Format("2006-01-02")
	rule, err := store.Add(7, "Calisthenics", "Milano Corso Como", int(first.Weekday()), "18:15", once)
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	e := NewEngine(store, nil, nil, nil)
	e.loc = loc

	occs := e.occurrences(now)
	if countOcc(occs, rule.ID) != 1 || occs[0].date != once {
		t.Fatalf("a one-off rule must schedule only %s: %+v", once, occs)
	}
	if _, err := store.Add(7, "Calisthenics", "Milano Corso Como", int(first.Weekday()), "18:15", ""); err != nil {
		t.Fatal(err)
	}
	if got := countOcc(e.occurrences(now), rule.ID); got != 2 {
		t.Fatalf("switched to weekly: expected both Thursdays, got %d", got)
	}

	store.Add(7, "Calisthenics", "Milano Corso Como", int(first.Weekday()), "18:15", once)
	if err := store.PurgeOnce(once); err != nil { // the day itself: still kept
		t.Fatal(err)
	}
	if _, ok := store.Get(7, rule.ID); !ok {
		t.Fatal("a one-off rule must survive until its date has passed")
	}
	store.PurgeOnce(first.AddDate(0, 0, 1).Format("2006-01-02"))
	if _, ok := store.Get(7, rule.ID); ok {
		t.Fatal("a one-off rule must be purged after its date")
	}
}
