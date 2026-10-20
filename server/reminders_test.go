package server

import (
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

func TestDueReminders(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	if _, err := s.db.Exec(`INSERT INTO users (id, email, pass, created, reminder_hours) VALUES (1, 'a@x', x'00', '', 12), (2, 'b@x', x'00', '', 0)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(s.loc).Truncate(time.Minute)
	at := now.Add(12 * time.Hour)
	class := calendar.Class{Date: at.Format("2006-01-02"), Start: at.Format("15:04"), Name: "Calisthenics", Club: "Milano Cavour", Booked: true}
	s.reconcileBookings(1, class.Date, class.Date, []calendar.Class{class})
	s.reconcileBookings(2, class.Date, class.Date, []calendar.Class{class}) // reminders off

	if due := s.dueReminders(now.Add(-time.Minute)); len(due) != 0 {
		t.Fatalf("not due before its time: %+v", due)
	}
	due := s.dueReminders(now.Add(time.Minute))
	if len(due) != 1 || due[0].userID != 1 {
		t.Fatalf("expected user 1's reminder, got %+v", due)
	}
	if due := s.dueReminders(now.Add(reminderGrace + time.Minute)); len(due) != 0 {
		t.Fatalf("a missed reminder isn't sent late: %+v", due)
	}
	s.reminderCycle(now.Add(time.Minute))
	if due := s.dueReminders(now.Add(2 * time.Minute)); len(due) != 0 {
		t.Fatalf("sent once only: %+v", due)
	}
}
