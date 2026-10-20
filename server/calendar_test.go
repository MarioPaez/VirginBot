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

func TestMarkOpening(t *testing.T) {
	s := &Server{}
	s.loc, _ = time.LoadLocation("Europe/Rome")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, s.loc)
	classes := []calendar.Class{
		{Date: "2026-10-29", Start: "18:15", Name: "Calisthenics", Status: "unavailable"}, // opens 21/10 18:15
		{Date: "2026-10-27", Start: "18:15", Name: "Calisthenics", Status: "unavailable"}, // opened yesterday
		{Date: "2026-10-23", Start: "09:00", Name: "Solarium", Status: "unavailable"},     // 2-day window: 21/10 09:00
		{Date: "2026-10-29", Start: "18:15", Name: "Calisthenics", Status: "bookable"},
	}
	s.markOpening(classes, now)
	want := []string{
		time.Date(2026, 10, 21, 18, 15, 0, 0, s.loc).Format(time.RFC3339),
		"",
		time.Date(2026, 10, 21, 9, 0, 0, 0, s.loc).Format(time.RFC3339),
		"",
	}
	for i, w := range want {
		if classes[i].OpensAt != w {
			t.Errorf("class %d: OpensAt = %q, want %q", i, classes[i].OpensAt, w)
		}
	}
}
