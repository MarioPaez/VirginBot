package server

import (
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
)

func watchServer(t *testing.T) *Server {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	return s
}

func TestAddWatchLimits(t *testing.T) {
	s := watchServer(t)
	day := time.Now().In(s.loc).AddDate(0, 0, 2).Format("2006-01-02")
	for i, start := range []string{"07:00", "08:00", "09:00"} {
		if err := s.addWatch(1, "Calisthenics", "Milano Cavour", day, start); err != nil {
			t.Fatalf("watch %d: %v", i, err)
		}
	}
	if err := s.addWatch(1, "Calisthenics", "Milano Cavour", day, "09:00"); err != nil {
		t.Fatalf("re-adding a watched class is idempotent, not over the cap: %v", err)
	}
	if err := s.addWatch(1, "Calisthenics", "Milano Cavour", day, "10:00"); err != errWatchLimit {
		t.Fatalf("a 4th watch must hit the cap, got %v", err)
	}
	if err := s.addWatch(2, "Calisthenics", "Milano Cavour", day, "10:00"); err != nil {
		t.Fatalf("the cap is per user: %v", err)
	}
	soon := time.Now().In(s.loc).Add(watchCutoff() - time.Minute)
	if err := s.addWatch(3, "Calisthenics", "Milano Cavour", soon.Format("2006-01-02"), soon.Format("15:04")); err != errWatchLate {
		t.Fatalf("inside the cutoff a watch must be refused, got %v", err)
	}
}

// One fetch per club-day, the most urgent first and within the per-tick budget;
// a polled group waits its period; watches past the cutoff expire.
func TestDueWatchGroups(t *testing.T) {
	s := watchServer(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, s.loc)
	add := func(uid int64, club, date, start string) {
		if _, err := s.db.Exec(`INSERT INTO watches (user_id, name, club, date, start, created) VALUES (?, 'Calisthenics', ?, ?, ?, ?)`,
			uid, club, date, start, now.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	add(1, "Milano Cavour", "2026-10-20", "18:00")     // today: due every 5 min
	add(2, "Milano Cavour", "2026-10-20", "19:00")     // same club-day: same group
	add(1, "Milano Corso Como", "2026-10-23", "18:00") // far: every 15 min
	add(3, "Milano Corso Como", "2026-10-21", "18:00") // tomorrow
	add(2, "Milano Corso Como", "2026-10-20", "13:00") // inside the cutoff: expires

	last := map[string]time.Time{}
	due := s.dueWatchGroups(now, last)
	if len(due) != watchPollsPerTick || due[0].club != "Milano Cavour" || len(due[0].watches) != 2 || due[0].every != watchMid {
		t.Fatalf("first tick: %+v", due)
	}
	if due[1].date != "2026-10-21" {
		t.Fatalf("second most urgent must be tomorrow's, got %+v", due[1])
	}
	if n := len(s.listWatches(0)); n != 4 {
		t.Fatalf("the watch inside the cutoff must have expired; %d left", n)
	}
	due = s.dueWatchGroups(now.Add(time.Minute), last)
	if len(due) != 1 || due[0].date != "2026-10-23" {
		t.Fatalf("next tick: only the group not polled yet, got %+v", due)
	}
	if due := s.dueWatchGroups(now.Add(2*time.Minute), last); len(due) != 0 {
		t.Fatalf("nothing due before its period: %+v", due)
	}
	if due := s.dueWatchGroups(now.Add(5*time.Minute), last); len(due) != 1 || due[0].club != "Milano Cavour" {
		t.Fatalf("today's group is due again after 5 min: %+v", due)
	}
}
