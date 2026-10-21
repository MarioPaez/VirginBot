package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

func TestValidAway(t *testing.T) {
	today := "2026-10-21"
	cases := map[[2]string]bool{
		{"2026-10-25", "2026-11-01"}: true,
		{"2026-10-01", "2026-10-21"}: true,  // started already, ends today
		{"2026-10-25", "2026-10-24"}: false, // backwards
		{"2026-10-01", "2026-10-20"}: false, // over
		{"2026-10-25", "2027-10-25"}: false, // too long
		{"25/10/2026", "2026-11-01"}: false,
	}
	for c, ok := range cases {
		if got := validAway(c[0], c[1], today) == ""; got != ok {
			t.Errorf("%v: valid=%v, want %v", c, got, ok)
		}
	}
}

// On holiday, an automation's next attempt skips those days, and the bookings
// already made in them are listed so the user can cancel them.
func TestAway(t *testing.T) {
	s := testServer(t)
	s.store = automation.NewStore(s.db)
	s.db.Exec(`INSERT INTO users (id, email, pass, created) VALUES (1, 'a@x', x'00', '')`)
	now := time.Now().In(s.loc)
	in3, in10 := now.AddDate(0, 0, 3), now.AddDate(0, 0, 10)
	rule, _ := s.store.Add(1, "Calisthenics", "Milano Cavour", int(in3.Weekday()), "18:30", "19:30", "")
	booked := calendar.Class{Date: in3.Format("2006-01-02"), Start: "07:00", End: "08:00", Name: "Cycle", Club: "Milano Cavour", Booked: true}
	s.reconcileBookings(1, booked.Date, booked.Date, []calendar.Class{booked})

	before, _, _ := s.nextAttempt(1, rule)
	req := httptest.NewRequest(http.MethodPut, "/api/away",
		strings.NewReader(`{"from":"`+now.Format("2006-01-02")+`","until":"`+in3.AddDate(0, 0, 1).Format("2006-01-02")+`"}`))
	rec := httptest.NewRecorder()
	s.handleAway(rec, req.WithContext(context.WithValue(req.Context(), ctxUserID, int64(1))))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Cycle"`) {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	after, _, _ := s.nextAttempt(1, rule)
	if before != booked.Date || after != in10.Format("2006-01-02") {
		t.Fatalf("next class before %s, after %s", before, after)
	}
	if !s.accounts.IsAway(1, booked.Date) || s.accounts.IsAway(1, in10.Format("2006-01-02")) {
		t.Fatal("IsAway")
	}
}
