package server

import (
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

// The Solarium's beds collapse into one row that keeps the most bookable bed's
// ids but adds up the free places of all of them.
func TestDedupeSumsSpots(t *testing.T) {
	bed := func(classID, spots int, status string) calendar.Class {
		return calendar.Class{Date: "2026-10-21", Start: "10:00", Name: "Solarium", Club: "Milano Corso Como",
			ClassID: classID, Status: status, Spots: spots, Capacity: 1}
	}
	out := dedupe([]calendar.Class{bed(1, 0, "full"), bed(2, 1, "bookable"), bed(3, 1, "bookable"), bed(4, 0, "full")})
	if len(out) != 1 {
		t.Fatalf("expected one row, got %d", len(out))
	}
	if c := out[0]; c.ClassID != 2 || c.Status != "bookable" || c.Spots != 2 || c.Capacity != 4 {
		t.Fatalf("row = %+v", c)
	}
}

// vapi reports classes as "Available" before their window opens: OpensAt marks
// them, with the member's own window when it's shorter than the class's.
func TestMarkOpening(t *testing.T) {
	s := testServer(t)
	acc := s.accounts
	priority, _ := acc.Upsert("p@x", "pw")
	regular, _ := acc.Upsert("r@x", "pw")
	acc.SetMember(priority, 209, 8*24*60)
	acc.SetMember(regular, 209, 2*24*60)

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, s.loc)
	at := func(d, hm string) string {
		t, _ := time.ParseInLocation("2006-01-02 15:04", d+" "+hm, s.loc)
		return t.Format(time.RFC3339)
	}
	classes := func() []calendar.Class {
		return []calendar.Class{
			{Date: "2026-10-29", Start: "18:15", Name: "Calisthenics", Status: "bookable"}, // 8 days: opens 21/10
			{Date: "2026-10-27", Start: "18:15", Name: "Calisthenics", Status: "bookable"}, // 8 days: open since 19/10
			{Date: "2026-10-23", Start: "09:00", Name: "Solarium", Status: "bookable"},     // 2 days: opens 21/10
			{Date: "2026-10-29", Start: "18:15", Name: "Calisthenics", Status: "booked", Booked: true},
		}
	}
	for _, tc := range []struct {
		user int64
		want []string
	}{
		{priority, []string{at("2026-10-21", "18:15"), "", at("2026-10-21", "09:00"), ""}},
		{regular, []string{at("2026-10-27", "18:15"), at("2026-10-25", "18:15"), at("2026-10-21", "09:00"), ""}},
	} {
		cs := classes()
		s.markOpening(tc.user, cs, now)
		for i, w := range tc.want {
			if cs[i].OpensAt != w {
				t.Errorf("user %d class %d: OpensAt = %q, want %q", tc.user, i, cs[i].OpensAt, w)
			}
		}
	}
}
