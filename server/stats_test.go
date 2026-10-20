package server

import (
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

// Started classes reach the history when the bookings are reconciled or purged,
// flagged when VirginBot booked them, and feed the stats.
func TestHistoryAndStats(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	now := time.Now().In(s.loc)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	past := calendar.Class{Date: yesterday, Start: "18:30", End: "19:30", Name: "Calisthenics", Club: "Milano Cavour", Booked: true}
	future := calendar.Class{Date: tomorrow, Start: "18:30", End: "19:30", Name: "Calisthenics", Club: "Milano Cavour", Booked: true}
	s.reconcileBookings(1, yesterday, tomorrow, []calendar.Class{past, future})
	s.MarkBotBooked(1, past.Name, past.Club, past.Date, past.Start)

	s.purgePastBookings()
	st := s.userStats(1, now)
	if st.Total != 1 || st.ByBot != 1 || len(st.Top) != 1 || st.Top[0].Name != "Calisthenics" || st.Since != yesterday {
		t.Fatalf("after the purge: %+v", st)
	}
	s.purgePastBookings() // idempotent
	if st := s.userStats(1, now); st.Total != 1 {
		t.Fatalf("archiving twice must not duplicate: %+v", st)
	}
	if s.userStats(2, now).Total != 0 {
		t.Fatal("stats are per user")
	}
}

func TestStreak(t *testing.T) {
	s := testServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, s.loc) // Wednesday
	for _, d := range []string{"2026-10-13", "2026-10-06", "2026-10-07", "2026-09-22"} {
		s.db.Exec(`INSERT INTO history (user_id, name, club, date, start) VALUES (1, 'Cycle', 'Milano Cavour', ?, '18:00')`, d)
	}
	// This week has no class yet: the streak counts back from last week (2), and
	// the gap of the week of 29/9 ends it.
	if st := s.userStats(1, now); st.Streak != 2 || st.Total != 4 || st.Month != 3 {
		t.Fatalf("stats = %+v", st)
	}
}
