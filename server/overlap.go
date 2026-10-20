package server

import (
	"log"

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
		if c.Name == name && c.Club == club && c.Start == start {
			continue // the class itself
		}
		if automation.Overlaps(start, end, c.Start, c.End) {
			c.Date = date
			return c, true
		}
	}
	return calendar.Class{}, false
}

// EngineBusy is the engine's overlap check: another booking at the rule's time.
func (s *Server) EngineBusy(userID int64, r automation.Rule, date string) (string, bool) {
	c, clash := s.clashingBooking(userID, r.Name, r.Club, date, r.Start, r.End)
	if !clash {
		return "", false
	}
	return c.Name + " · " + c.Club + " " + c.Start, true
}
