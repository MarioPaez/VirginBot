package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/db"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dbh, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbh.Close() })
	loc, _ := time.LoadLocation("Europe/Rome")
	acc, err := account.NewStore(dbh, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{db: dbh, loc: loc, accounts: acc}
}

// Waitlist places are stored apart from bookings: they don't count as booked,
// they carry the position, and moving from the waitlist to an active booking is
// reported as a promotion (exactly once).
func TestReconcileWaitlist(t *testing.T) {
	s := testServer(t)
	day := time.Now().In(s.loc).AddDate(0, 0, 2).Format("2006-01-02")
	calis := calendar.Class{Date: day, Start: "18:30", End: "19:30", Name: "Calisthenics", Club: "Milano Cavour",
		ClubID: 224, ClassID: 303495, SessionID: 79403, BookingID: 2458420}
	waiting := calis
	waiting.Waitlisted, waiting.WaitlistPos = true, 2

	if p := s.reconcileBookings(1, day, day, []calendar.Class{waiting}); len(p) != 0 {
		t.Fatalf("joining a waitlist isn't a promotion: %v", p)
	}
	if s.isBooked(1, calis.Name, calis.Club, day, calis.Start) {
		t.Fatal("a waitlist place must not count as booked (the engine still has to try)")
	}
	list := s.listBookings(1)
	if len(list) != 1 || !list[0].Waitlisted || list[0].Booked || list[0].WaitlistPos != 2 || list[0].Status != "waitlisted" {
		t.Fatalf("listBookings = %+v", list)
	}
	if next := s.nextWaitlistedClass(time.Now().In(s.loc)); next[1].IsZero() {
		t.Fatal("the user must be in the waitlist sync")
	}

	// Moved up the line: still waitlisted, new position, no promotion.
	waiting.WaitlistPos = 1
	if p := s.reconcileBookings(1, day, day, []calendar.Class{waiting}); len(p) != 0 {
		t.Fatalf("moving up isn't a promotion: %v", p)
	}

	// Virgin now reports it as an active booking: promoted, once.
	booked := calis
	booked.Booked = true
	if p := s.reconcileBookings(1, day, day, []calendar.Class{booked}); len(p) != 1 {
		t.Fatalf("expected one promotion, got %v", p)
	}
	if !s.isBooked(1, calis.Name, calis.Club, day, calis.Start) {
		t.Fatal("the promoted class must be booked")
	}
	if p := s.reconcileBookings(1, day, day, []calendar.Class{booked}); len(p) != 0 {
		t.Fatalf("a promotion must be reported only once, got %v", p)
	}
	if next := s.nextWaitlistedClass(time.Now().In(s.loc)); len(next) != 0 {
		t.Fatalf("no waitlist places left, no sync expected: %v", next)
	}

	// The calendar overlay shows the waitlist position on the matching class.
	s.reconcileBookings(1, day, day, []calendar.Class{waiting})
	out := s.overlayBooked(1, []calendar.Class{{Date: day, Start: "18:30", Name: "Calisthenics", Club: "Milano Cavour", Status: "waitlist"}})
	if !out[0].Waitlisted || out[0].WaitlistPos != 1 || out[0].BookingID != 2458420 || out[0].Booked {
		t.Fatalf("overlay = %+v", out[0])
	}
}
