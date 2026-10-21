package server

import (
	"log"
	"strings"
	"time"

	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
)

// weeklyHour: the summary goes out on Sunday from this hour (Italy), when
// people plan their week.
const weeklyHour = 19

// weeklyListed: classes named in the summary; the rest are counted.
const weeklyListed = 3

// RunWeekly sends each user, on Sunday evening, what next week holds: booked
// classes, waitlist places and automations still to get a spot. Never returns.
func (s *Server) RunWeekly() {
	for range time.Tick(time.Minute) {
		s.weeklyCycle(time.Now().In(s.loc))
	}
}

func (s *Server) weeklyCycle(now time.Time) {
	if s.push == nil || now.Weekday() != time.Sunday || now.Hour() < weeklyHour {
		return
	}
	sunday := now.Format("2006-01-02")
	for _, uid := range s.accounts.ListUserIDs() {
		if !s.accounts.WeeklySummary(uid) || !s.accounts.ClaimWeekly(uid, sunday) {
			continue
		}
		w := s.weekAhead(uid, now)
		if len(w.booked)+w.waitlisted+w.pending == 0 {
			continue // nothing planned: no push
		}
		lang := s.langOf(uid)
		s.push.Notify(uid, push.Message{Title: w.title(lang), Body: w.body(lang), URL: "/?view=bookings"})
	}
}

// week is what the user has from next Monday to Sunday.
type week struct {
	booked     []weekClass
	waitlisted int
	pending    int // automated occurrences not booked yet
}

type weekClass struct {
	at   time.Time
	name string
}

// weekAhead gathers the week starting on the Monday after now.
func (s *Server) weekAhead(userID int64, now time.Time) week {
	days := (8 - int(now.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	mon := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, days)
	first, last := mon.Format("2006-01-02"), mon.AddDate(0, 0, 6).Format("2006-01-02")

	var w week
	rows, err := s.db.Query(`SELECT name, date, start, status FROM bookings
		WHERE user_id = ? AND date >= ? AND date <= ? ORDER BY date, start`, userID, first, last)
	if err != nil {
		log.Printf("weekly u%d: %v", userID, err)
		return w
	}
	for rows.Next() {
		var name, date, start, status string
		if rows.Scan(&name, &date, &start, &status) != nil {
			continue
		}
		if status == "waitlisted" {
			w.waitlisted++
		} else if at, ok := s.startOf(date, start); ok {
			w.booked = append(w.booked, weekClass{at: at, name: name})
		}
	}
	rows.Close() // drained before the next queries: the pool has a single connection

	for _, r := range s.store.List(userID) {
		if !r.Enabled {
			continue
		}
		for d := 0; d < 7; d++ {
			day := mon.AddDate(0, 0, d)
			date := day.Format("2006-01-02")
			if int(day.Weekday()) != r.Weekday || !r.Covers(date) {
				continue
			}
			if _, held := s.bookingRow(userID, r.Name, r.Club, date, r.Start); !held && !s.store.Skipped(userID, r.ID, date) &&
				!s.accounts.IsAway(userID, date) {
				w.pending++
			}
		}
	}
	return w
}

func (w week) title(lang i18n.Lang) string {
	switch len(w.booked) {
	case 0:
		return i18n.T(lang, "push.week.title.none")
	case 1:
		return i18n.T(lang, "push.week.title.one")
	}
	return i18n.T(lang, "push.week.title", len(w.booked))
}

// body: "Mon 18:15 Calisthenics, Wed 19:30 Reformer and 1 more. 2 automations
// still to book. On the waitlist: 1."
func (w week) body(lang i18n.Lang) string {
	var parts []string
	if n := len(w.booked); n > 0 {
		names := make([]string, 0, weeklyListed)
		for _, c := range w.booked[:min(n, weeklyListed)] {
			names = append(names, i18n.ShortWeekday(lang, c.at)+" "+c.at.Format("15:04")+" "+c.name)
		}
		list := strings.Join(names, ", ")
		if n > weeklyListed {
			list += " " + i18n.T(lang, "push.week.more", n-weeklyListed)
		}
		parts = append(parts, list)
	}
	switch {
	case w.pending == 1:
		parts = append(parts, i18n.T(lang, "push.week.pending.one"))
	case w.pending > 1:
		parts = append(parts, i18n.T(lang, "push.week.pending", w.pending))
	}
	if w.waitlisted > 0 {
		parts = append(parts, i18n.T(lang, "push.week.waitlisted", w.waitlisted))
	}
	return strings.Join(parts, ". ") + "."
}
