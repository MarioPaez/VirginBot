package server

import (
	"log"
	"strings"
	"time"
	"unicode"

	"github.com/MarioPaez/VirginBot/calendar"
	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
)

// usualVisits: how many times someone must have been to a weekly slot (same
// class, club, weekday and time) for it to count as one of their classes.
const usualVisits = 2

// notifyFriends tells the other users that friendID just booked c, if c is one
// of their usual classes — same class and club, same weekday and time: they
// have been there usualVisits times, automate it, or are booked on it too.
// Once per user, friend and occurrence; off for users who opted out.
func (s *Server) notifyFriends(friendID int64, c calendar.Class) {
	if s.push == nil {
		return
	}
	day, err := time.ParseInLocation("2006-01-02", c.Date, s.loc)
	if err != nil {
		return
	}
	wd := int(day.Weekday())
	friend := displayName(s.accounts.Email(friendID))
	for _, uid := range s.accounts.ListUserIDs() {
		if uid == friendID || !s.accounts.FriendNotify(uid) || !s.isUsualClass(uid, c, wd) {
			continue
		}
		res, err := s.db.Exec(`INSERT OR IGNORE INTO friend_notified (user_id, friend_id, name, club, date, start) VALUES (?, ?, ?, ?, ?, ?)`,
			uid, friendID, c.Name, c.Club, c.Date, c.Start)
		if err != nil {
			log.Printf("friend notice u%d: %v", uid, err)
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // already told
		}
		lang := s.langOf(uid)
		when, _ := s.startOf(c.Date, c.Start)
		s.push.Notify(uid, push.Message{
			Title: i18n.T(lang, "push.friend.title", friend),
			Body:  i18n.T(lang, "push.class", c.Name, c.Club, i18n.FormatDateTime(lang, when)),
			URL:   "/?view=shared",
		})
	}
}

// isUsualClass reports whether c's weekly slot is one of uid's classes.
func (s *Server) isUsualClass(uid int64, c calendar.Class, weekday int) bool {
	if s.isBooked(uid, c.Name, c.Club, c.Date, c.Start) {
		return true
	}
	if r, ok := s.store.Find(uid, c.Name, c.Club, weekday, c.Start); ok && r.Enabled && r.Covers(c.Date) {
		return true
	}
	// SQLite's strftime('%w') is the weekday, 0 = Sunday like time.Weekday.
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM history WHERE user_id = ? AND name = ? AND club = ? AND start = ?
		AND CAST(strftime('%w', date) AS INTEGER) = ? AND (attended IS NULL OR attended = 1)`,
		uid, c.Name, c.Club, c.Start, weekday).Scan(&n)
	return n >= usualVisits
}

// displayName: "mario.paez@x" → "Mario.paez", like the FE's "Who's going".
func displayName(email string) string {
	name, _, _ := strings.Cut(email, "@")
	if name == "" {
		return email
	}
	r := []rune(name)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
