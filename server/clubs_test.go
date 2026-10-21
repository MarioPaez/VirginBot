package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/calendar"
)

// The clubs a user sees: their choice, else their home club, else the defaults.
func TestClubsFor(t *testing.T) {
	s := testServer(t)
	s.clubs = []int{calendar.ClubCorsoComo, calendar.ClubCavour}
	s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`)
	if got := s.clubsFor(1); len(got) != 2 {
		t.Fatalf("no home club: %v", got)
	}
	s.accounts.SetMember(1, calendar.ClubPalermo, 0)
	if got := s.clubsFor(1); len(got) != 1 || got[0] != calendar.ClubPalermo {
		t.Fatalf("home club: %v", got)
	}
	s.accounts.SetClubs(1, []int{calendar.ClubCavour, calendar.ClubCorsoComo})
	if got := s.clubsFor(1); len(got) != 2 || got[0] != calendar.ClubCavour {
		t.Fatalf("chosen: %v", got)
	}
}

func TestHandleClubsValidates(t *testing.T) {
	s := testServer(t)
	s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`)
	put := func(body string) int {
		req := httptest.NewRequest(http.MethodPut, "/api/clubs", strings.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleClubs(rec, req.WithContext(context.WithValue(req.Context(), ctxUserID, int64(1))))
		return rec.Code
	}
	for body, want := range map[string]int{
		`{"clubs":[209,224,224]}`:             200, // duplicates collapse
		`{"clubs":[]}`:                        400,
		`{"clubs":[99999]}`:                   400, // unknown club
		`{"clubs":[209,224,231,209,224,231]}`: 200, // 3 once deduplicated
	} {
		if got := put(body); got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	calendar.AddClubs([]calendar.Club{{ID: 401, Name: "A"}, {ID: 402, Name: "B"}, {ID: 403, Name: "C"}})
	if got := put(`{"clubs":[209,224,231,401,402,403]}`); got != 400 {
		t.Errorf("6 clubs: %d, want 400", got)
	}
}

// With "every class" on, nothing is curated away (the engine needs that too).
func TestCurateAllClasses(t *testing.T) {
	s := testServer(t)
	s.keep = func(c calendar.Class) bool { return c.Name == "Calisthenics" }
	s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`)
	classes := []calendar.Class{{Name: "Calisthenics"}, {Name: "Yoga"}}
	if got := s.curate(1, classes); len(got) != 1 {
		t.Fatalf("popular only: %v", got)
	}
	s.accounts.SetPrefs(1, account.Prefs{FullAction: "", AllClasses: true})
	if got := s.curate(1, classes); len(got) != 2 {
		t.Fatalf("every class: %v", got)
	}
}
