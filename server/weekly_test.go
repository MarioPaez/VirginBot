package server

import (
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
)

// The summary covers next Monday to Sunday: booked classes in order, waitlist
// places, and automated occurrences not held yet (a skipped one doesn't count).
func TestWeekAhead(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	sunday := time.Date(2026, 10, 25, 19, 0, 0, 0, s.loc)
	cls := func(date, start, name string) calendar.Class {
		return calendar.Class{Date: date, Start: start, End: start, Name: name, Club: "Milano Cavour", Booked: true}
	}
	wait := cls("2026-10-29", "07:00", "Cycle")
	wait.Booked, wait.Waitlisted, wait.WaitlistPos = false, true, 2
	s.reconcileBookings(1, "2026-10-25", "2026-11-03", []calendar.Class{
		cls("2026-10-28", "19:30", "Reformer"),
		cls("2026-10-26", "18:30", "Calisthenics"),
		cls("2026-10-25", "20:00", "TRX"),          // this Sunday: not next week
		cls("2026-11-02", "18:30", "Calisthenics"), // the Monday after
		wait,
	})
	s.store.Add(1, "Calisthenics", "Milano Cavour", 1, "18:30", "19:30", "") // Mon: booked
	s.store.Add(1, "Lift Club", "Milano Cavour", 5, "08:00", "09:00", "")    // Fri: pending
	tue, _ := s.store.Add(1, "TRX", "Milano Cavour", 2, "12:00", "13:00", "")
	s.store.Skip(1, tue.Name, tue.Club, tue.Weekday, tue.Start, "2026-10-27") // cancelled: not pending
	s.store.Add(1, "Cycle", "Milano Cavour", 4, "07:00", "08:00", "")         // Thu: on the waitlist

	w := s.weekAhead(1, sunday)
	if len(w.booked) != 2 || w.booked[0].name != "Calisthenics" || w.booked[1].name != "Reformer" ||
		w.waitlisted != 1 || w.pending != 1 {
		t.Fatalf("week: %+v", w)
	}
	if got, want := w.title(i18n.ES), "La semana que viene tienes 2 clases"; got != want {
		t.Fatalf("title %q, want %q", got, want)
	}
	if got, want := w.body(i18n.ES), "Lun 18:30 Calisthenics, Mié 19:30 Reformer. 1 automatización aún por reservar. En lista de espera: 1."; got != want {
		t.Fatalf("body %q, want %q", got, want)
	}
}

func TestWeekBodyMore(t *testing.T) {
	at := time.Date(2026, 10, 26, 18, 30, 0, 0, time.UTC)
	w := week{booked: []weekClass{{at, "A"}, {at, "B"}, {at, "C"}, {at, "D"}, {at, "E"}}, pending: 2}
	if got, want := w.body(i18n.EN), "Mon 18:30 A, Mon 18:30 B, Mon 18:30 C and 2 more. 2 automations still to book."; got != want {
		t.Fatalf("body %q, want %q", got, want)
	}
}

// Only one summary per Sunday, even across restarts.
func TestClaimWeekly(t *testing.T) {
	s := testServer(t)
	if _, err := s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`); err != nil {
		t.Fatal(err)
	}
	if !s.accounts.ClaimWeekly(1, "2026-10-25") || s.accounts.ClaimWeekly(1, "2026-10-25") {
		t.Fatal("the same Sunday was claimed twice (or never)")
	}
	if !s.accounts.ClaimWeekly(1, "2026-11-01") {
		t.Fatal("the next Sunday wasn't claimed")
	}
}
