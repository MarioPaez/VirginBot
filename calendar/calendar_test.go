package calendar

import "testing"

// Clubs vapi lists are added, but the names of the clubs VirginBot started with
// never change: they key the stored bookings and rules.
func TestAddClubs(t *testing.T) {
	AddClubs([]Club{{ID: ClubCavour, Name: "Cavour (renamed)"}, {ID: 301, Name: "Roma Eur"}, {ID: 0, Name: "x"}, {ID: 302}})
	if ClubName(ClubCavour) != "Milano Cavour" {
		t.Fatalf("known club renamed to %q", ClubName(ClubCavour))
	}
	if id, ok := ClubIDByName("roma eur"); !ok || id != 301 || ClubName(301) != "Roma Eur" {
		t.Fatalf("new club: %d %v", id, ok)
	}
	if ClubName(302) != "" {
		t.Fatal("a club without a name was registered")
	}
	all := Clubs()
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Fatalf("not sorted by name: %v", all)
		}
	}
}
