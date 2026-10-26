package server

import (
	"reflect"
	"testing"
	"time"

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

// A new booking is told both ways: to whoever usually goes, and to the booker
// about who's booked already. Dropping it tells whoever heard, once; the
// dropper's own marks go too.
func TestFriendNotices(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	for id, email := range map[int64]string{1: "mario@x", 2: "nikko@x", 3: "ana@x", 4: "off@x"} {
		s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (?, ?, x'00', '')`, id, email)
	}
	s.db.Exec(`UPDATE users SET friend_notify = 0 WHERE id = 4`)
	day := time.Now().In(s.loc).AddDate(0, 0, 2).Format("2006-01-02")
	c := calendar.Class{Name: "Calisthenics", Club: "Milano Cavour", Date: day, Start: "18:30"}
	for _, uid := range []int64{1, 4} { // Mario and the opted-out user are booked
		b := c
		b.Booked = true
		s.reconcileBookings(uid, day, day, []calendar.Class{b})
	}

	// Nikko books: Mario hears and Nikko hears about Mario and user 4 (who opted
	// out of hearing, not of being seen, as in "Who's going"); Ana isn't on it.
	want := []friendNotice{{to: 1, about: 2}, {to: 2, about: 1}, {to: 2, about: 4}}
	if got := s.friendsBooked(2, c); !reflect.DeepEqual(got, want) {
		t.Fatalf("booked = %v, want %v", got, want)
	}
	if got := s.friendsBooked(2, c); len(got) != 0 {
		t.Fatalf("told twice: %v", got)
	}

	// Mario drops it: Nikko hears, once.
	if got := s.friendsLeft(1, c); !reflect.DeepEqual(got, []friendNotice{{to: 2, about: 1}}) {
		t.Fatalf("left = %v", got)
	}
	if got := s.friendsLeft(1, c); len(got) != 0 {
		t.Fatalf("left told twice: %v", got)
	}
	// Mario's "Nikko goes" mark went with him: Nikko dropping it tells nobody.
	if got := s.friendsLeft(2, c); len(got) != 0 {
		t.Fatalf("Mario told after leaving: %v", got)
	}
}
