package server

import (
	"testing"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

// A class is "usual" for someone who went to that weekly slot twice, automates
// it or is booked on it; the same class at another time or club isn't.
func TestIsUsualClass(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	c := calendar.Class{Name: "Calisthenics", Club: "Milano Cavour", Date: "2026-10-26", Start: "18:30"} // Monday
	for _, d := range []string{"2026-10-19", "2026-10-12"} {
		s.db.Exec(`INSERT INTO history (user_id, name, club, date, start) VALUES (2, 'Calisthenics', 'Milano Cavour', ?, '18:30')`, d)
	}
	s.db.Exec(`INSERT INTO history (user_id, name, club, date, start) VALUES (3, 'Calisthenics', 'Milano Cavour', '2026-10-19', '18:30')`)
	s.store.Add(4, "Calisthenics", "Milano Cavour", 1, "18:30", "19:30", "")
	s.db.Exec(`INSERT INTO history (user_id, name, club, date, start) VALUES (5, 'Calisthenics', 'Milano Corso Como', '2026-10-19', '18:30'), (5, 'Calisthenics', 'Milano Corso Como', '2026-10-12', '18:30')`)

	for uid, want := range map[int64]bool{2: true, 3: false, 4: true, 5: false} {
		if got := s.isUsualClass(uid, c, 1); got != want {
			t.Errorf("user %d: usual = %v, want %v", uid, got, want)
		}
	}
	if displayName("ana.rossi@x.it") != "Ana.rossi" {
		t.Fatal(displayName("ana.rossi@x.it"))
	}
}
