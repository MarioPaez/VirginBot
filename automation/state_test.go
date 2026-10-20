package automation

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	return NewStore(dbh)
}

func fullClass(status string) func(int64, string, string) ([]calendar.Class, error) {
	return func(int64, string, string) ([]calendar.Class, error) {
		return []calendar.Class{{
			Date: "2026-06-29", Name: "Calisthenics", Club: "Milano Corso Como", Start: "18:15",
			Status: status, ClubID: 209, ClassID: 387071, SessionID: 79403,
		}}, nil
	}
}

// What a fire did survives a restart: a new engine over the same store doesn't
// book again, doesn't re-send the failure notice, and the attempt is logged.
func TestStatePersistsAcrossRestarts(t *testing.T) {
	loc := mustLoc(t)
	store := testStore(t)
	o := sampleOcc(loc)
	var books int
	book := func(int64, int, int, int, string) error { books++; return nil }
	e := NewEngine(store, fullClass("bookable"), book, nil)
	e.loc, e.gap, e.gap1, e.lead = loc, 0, 0, 0
	e.maybeFire(o, time.Date(2026, 6, 23, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("books=%d", books)
	}

	restored := store.loadStates("2026-06-01")
	st := restored[o.stateKey()]
	if st == nil || !st.booked || st.attempts != 1 || !st.lastFired.Equal(time.Date(2026, 6, 23, 18, 15, 0, 0, loc)) {
		t.Fatalf("restored state = %+v", st)
	}
	e2 := NewEngine(store, fullClass("bookable"), book, nil)
	e2.loc, e2.gap, e2.gap1, e2.lead = loc, 0, 0, 0
	e2.state = restored
	e2.maybeFire(o, time.Date(2026, 6, 24, 18, 15, 1, 0, loc))
	if books != 1 {
		t.Fatalf("a restored booked occurrence must not be booked again; books=%d", books)
	}
	if a := store.RecentAttempts(10); len(a) != 1 || a[0].Outcome != OutcomeBooked || a[0].Source != "engine" {
		t.Fatalf("attempt log = %+v", a)
	}
	store.PurgeEngine("2026-06-30", time.Hour)
	if len(store.loadStates("2026-06-01")) != 0 {
		t.Fatal("past occurrences' state must be purged")
	}
}

// Fires due at the same time run concurrently, so one user's slow fire (no
// spot, many rounds) doesn't delay another's opening fire.
func TestDueFiresRunInParallel(t *testing.T) {
	loc := mustLoc(t)
	var inFlight, peak atomic.Int32
	fetch := func(int64, string, string) ([]calendar.Class, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		inFlight.Add(-1)
		return fullClass("bookable")(0, "", "")
	}
	e := NewEngine(nil, fetch, func(int64, int, int, int, string) error { return nil }, nil)
	e.loc, e.gap, e.gap1, e.lead = loc, 0, 0, 0
	a, b := sampleOcc(loc), sampleOcc(loc)
	b.rule.UserID = 2
	e.fireDue([]occ{a, b}, time.Date(2026, 6, 23, 18, 15, 1, 0, loc))
	if peak.Load() != 2 {
		t.Fatalf("expected both fires in flight together, peak=%d", peak.Load())
	}
}

// Still full at the last fire: the user's fallback (waitlist / watch) takes over
// and the generic "couldn't book" notice is not sent.
func TestMissedFallbackReplacesFailureNotice(t *testing.T) {
	loc := mustLoc(t)
	o := sampleOcc(loc)
	e := NewEngine(nil, fullClass("waitlist"), func(int64, int, int, int, string) error { return nil }, nil)
	e.loc, e.gap, e.gap1, e.lead, e.rounds = loc, 0, 0, 0, 1
	var notices int
	e.SetPush(func(int64, string, string, string) { notices++ })
	var missed []string
	e.SetOnMissed(func(_ int64, r Rule, date, status string) bool {
		missed = append(missed, date+" "+status)
		return true
	})
	e.maybeFire(o, time.Date(2026, 6, 27, 18, 15, 1, 0, loc)) // T-2: not the last one
	if len(missed) != 0 {
		t.Fatalf("the fallback only runs after the last fire: %v", missed)
	}
	e.maybeFire(o, time.Date(2026, 6, 28, 18, 15, 1, 0, loc)) // T-1
	if len(missed) != 1 || missed[0] != "2026-06-29 waitlist" || notices != 0 {
		t.Fatalf("missed=%v notices=%d", missed, notices)
	}
}

func TestHealthProblem(t *testing.T) {
	store := testStore(t)
	e := NewEngine(store, nil, nil, nil)
	now := time.Now()
	if p := healthProblem(e, now); p != "" {
		t.Fatalf("an engine that hasn't cycled yet isn't stale: %q", p)
	}
	e.lastCycle.Store(now.Add(-30 * time.Minute).Unix())
	if p := healthProblem(e, now); p == "" {
		t.Fatal("30 min without a cycle must be reported")
	}
	e.lastCycle.Store(now.Unix())
	for range errorSpikeCount {
		store.RecordAttempt(Attempt{UserID: 1, Name: "x", Source: "engine", Outcome: OutcomeError})
	}
	if p := healthProblem(e, now); p == "" {
		t.Fatal("an error spike without bookings must be reported")
	}
	store.RecordAttempt(Attempt{UserID: 1, Name: "x", Source: "engine", Outcome: OutcomeBooked})
	if p := healthProblem(e, now); p != "" {
		t.Fatalf("a booking in the same hour means vapi works: %q", p)
	}
}

// A member without priority booking (2-day window) gets no fires before their
// window opens; unknown windows keep the class's.
func TestMemberWindowShortensTriggers(t *testing.T) {
	loc := mustLoc(t)
	store := testStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, loc)
	class := now.AddDate(0, 0, 5)
	if _, err := store.Add(7, "Calisthenics", "Milano Corso Como", int(class.Weekday()), "18:15", ""); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(store, nil, nil, nil)
	e.loc = loc
	for _, tc := range []struct{ windowMin, fires int }{{2 * 24 * 60, 2}, {8 * 24 * 60, 8}, {0, 8}, {90, 8}} {
		e.SetMemberWindow(func(int64) int { return tc.windowMin })
		occs := e.occurrences(now)
		if len(occs) != 1 || len(occs[0].triggers()) != tc.fires {
			t.Fatalf("window %d min: expected %d fires, got %+v", tc.windowMin, tc.fires, occs)
		}
	}
}
