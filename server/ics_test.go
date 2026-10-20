package server

import (
	"strings"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

func TestBuildICS(t *testing.T) {
	s := testServer(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, s.loc)
	s.reconcileBookings(1, "2026-10-21", "2026-10-21", []calendar.Class{
		{Date: "2026-10-21", Start: "18:30", End: "19:30", Name: "Calisthenics, Performance", Club: "Milano Cavour", Instructor: "Ana; Bo", Booked: true},
		{Date: "2026-10-21", Start: "20:00", End: "21:00", Name: "Cycle", Club: "Milano Cavour", Waitlisted: true, WaitlistPos: 2},
	})
	s.reconcileBookings(2, "2026-10-21", "2026-10-21", []calendar.Class{
		{Date: "2026-10-21", Start: "07:00", End: "08:00", Name: "Lift Club", Club: "Milano Cavour", Booked: true},
	})
	ics := s.buildICS(1, now)
	for _, want := range []string{
		"BEGIN:VCALENDAR\r\n", "END:VCALENDAR\r\n",
		"DTSTART:20261021T163000Z\r\n", "DTEND:20261021T173000Z\r\n", // 18:30 CEST = 16:30 UTC
		`SUMMARY:Calisthenics\, Performance`, `DESCRIPTION:Ana\; Bo`, "LOCATION:Virgin Active Milano Cavour",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("missing %q in:\n%s", want, ics)
		}
	}
	if n := strings.Count(ics, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("only the user's active bookings (no waitlist, no one else's): %d events", n)
	}
}

func TestFoldICS(t *testing.T) {
	l := "DESCRIPTION:" + strings.Repeat("è", 60)
	for i, part := range strings.Split(foldICS(l), "\r\n") {
		if len(part) > 75 {
			t.Fatalf("line %d is %d octets", i, len(part))
		}
		if i > 0 && !strings.HasPrefix(part, " ") {
			t.Fatalf("continuation line %d must start with a space", i)
		}
	}
	if strings.ReplaceAll(foldICS(l), "\r\n ", "") != l {
		t.Fatal("unfolding must give back the line")
	}
}
