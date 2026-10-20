package server

import (
	"log"
	"strings"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

// clashingBooking returns the user's booking on date that overlaps start–end,
// other than the class itself (name+club+start).
func (s *Server) clashingBooking(userID int64, name, club, date, start, end string) (calendar.Class, bool) {
	rows, err := s.db.Query(`SELECT name, club, start, end_time FROM bookings
		WHERE user_id = ? AND date = ? AND status = 'booked'`, userID, date)
	if err != nil {
		log.Printf("clashing booking u%d: %v", userID, err)
		return calendar.Class{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var c calendar.Class
		if rows.Scan(&c.Name, &c.Club, &c.Start, &c.End) != nil {
			continue
		}
		if strings.EqualFold(c.Name, name) && strings.EqualFold(c.Club, club) && c.Start == start {
			continue // the class itself
		}
		if automation.Overlaps(start, end, c.Start, c.End) {
			c.Date = date
			return c, true
		}
	}
	return calendar.Class{}, false
}

// backfillRuleEnds fills in the class end of the user's rules created before it
// was stored, from the calendar just downloaded (the overlap checks need it).
func (s *Server) backfillRuleEnds(userID int64, classes []calendar.Class) {
	for _, r := range s.store.List(userID) {
		if r.End != "" {
			continue
		}
		for _, c := range classes {
			if wd, ok := weekdayOf(c.Date); ok && wd == r.Weekday && c.Start == r.Start && c.End != "" &&
				strings.EqualFold(c.Name, r.Name) && strings.EqualFold(c.Club, r.Club) {
				s.store.SetEnd(userID, r.ID, c.End)
				break
			}
		}
	}
}

// cancelBy is when a booking stops being cancellable (RFC 3339, Italy time), so
// the FE doesn't depend on the device's time zone.
func (s *Server) cancelBy(c calendar.Class) string {
	at, ok := s.startOf(c.Date, c.Start)
	if !ok {
		return ""
	}
	return cancelDeadline(at).Format(time.RFC3339)
}

// EngineBusy is the engine's overlap check: another booking at the rule's time.
// A clash found in the bookings table is confirmed against Virgin first: the
// table may be stale (cancelled in the official app since), and refusing would
// lose the class right as its window opens.
func (s *Server) EngineBusy(userID int64, r automation.Rule, date string) (string, bool) {
	if _, clash := s.clashingBooking(userID, r.Name, r.Club, date, r.Start, r.End); !clash {
		return "", false
	}
	s.fetchBookings(userID)
	c, clash := s.clashingBooking(userID, r.Name, r.Club, date, r.Start, r.End)
	if !clash {
		return "", false
	}
	return c.Name + " · " + c.Club + " " + c.Start, true
}
