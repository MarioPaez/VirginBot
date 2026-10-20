package server

import (
	"log"
	"time"

	"github.com/MarioPaez/VirginBot/account"
)

// reminderGrace: a reminder is only sent close to its due time. A class booked
// after that moment (e.g. booked 1 h before with a 12 h lead) gets none: being
// asked "still going?" right after booking would be noise.
const reminderGrace = 3 * time.Minute // a couple of ticks of slack

// RunReminders pushes "still going?" the user's chosen hours before each booked
// class, so they cancel in time and free the spot. Never returns.
func (s *Server) RunReminders() {
	for range time.Tick(time.Minute) {
		s.reminderCycle(time.Now().In(s.loc))
	}
}

type reminder struct {
	userID                  int64
	name, club, date, start string
	hours                   int
}

// dueReminders returns the reminders due at now, not sent yet. The rows are
// drained before returning: the pool has a single connection.
func (s *Server) dueReminders(now time.Time) []reminder {
	rows, err := s.db.Query(`SELECT b.user_id, b.name, b.club, b.date, b.start, `+account.ReminderSQL+`
		FROM bookings b JOIN users u ON u.id = b.user_id
		WHERE b.status = 'booked' AND `+account.ReminderSQL+` > 0 AND b.date >= ? AND b.date <= ?
		  AND NOT EXISTS (SELECT 1 FROM reminders_sent r WHERE r.user_id = b.user_id AND r.name = b.name
		                  AND r.club = b.club AND r.date = b.date AND r.start = b.start)`,
		now.Format("2006-01-02"), now.AddDate(0, 0, 2).Format("2006-01-02"))
	if err != nil {
		log.Printf("reminders: %v", err)
		return nil
	}
	var all []reminder
	for rows.Next() {
		var r reminder
		if rows.Scan(&r.userID, &r.name, &r.club, &r.date, &r.start, &r.hours) == nil {
			all = append(all, r)
		}
	}
	rows.Close()
	var due []reminder
	for _, r := range all {
		at, ok := s.startOf(r.date, r.start)
		if !ok || !now.Before(at) {
			continue
		}
		if when := at.Add(-time.Duration(r.hours) * time.Hour); !now.Before(when) && now.Sub(when) <= reminderGrace {
			due = append(due, r)
		}
	}
	return due
}

func (s *Server) reminderCycle(now time.Time) {
	for _, r := range s.dueReminders(now) {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO reminders_sent (user_id, name, club, date, start) VALUES (?, ?, ?, ?, ?)`,
			r.userID, r.name, r.club, r.date, r.start); err != nil {
			log.Printf("reminders u%d: %v", r.userID, err)
			continue
		}
		at, _ := s.startOf(r.date, r.start)
		s.notifyClass(r.userID, r.name, r.club, r.date, r.start, "/?view=bookings", "push.reminder.title",
			cancelDeadline(at).Format("15:04"))
	}
}
