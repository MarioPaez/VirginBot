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

// friendNotice tells user to that about is going to a class (or no longer is).
type friendNotice struct{ to, about int64 }

// notifyFriends pushes friendID's new booking c both ways: the users for whom
// c is a usual class learn friendID goes, and friendID learns which of them is
// booked on it already.
func (s *Server) notifyFriends(friendID int64, c calendar.Class) {
	if s.push == nil {
		return
	}
	s.pushFriends(s.friendsBooked(friendID, c), "push.friend.title", c)
}

// notifyFriendLeft tells whoever heard userID was going to c that userID no
// longer is (cancelled here or from the official app).
func (s *Server) notifyFriendLeft(userID int64, c calendar.Class) {
	if s.push == nil {
		return
	}
	s.pushFriends(s.friendsLeft(userID, c), "push.friend.left.title", c)
}

// friendsBooked returns who to tell about friendID's new booking c. Each pair
// once per occurrence (friend_notified); users who opted out get nothing.
func (s *Server) friendsBooked(friendID int64, c calendar.Class) []friendNotice {
	day, err := time.ParseInLocation("2006-01-02", c.Date, s.loc)
	if err != nil {
		return nil
	}
	wd := int(day.Weekday())
	tellFriend := s.accounts.FriendNotify(friendID)
	var out []friendNotice
	for _, uid := range s.accounts.ListUserIDs() {
		if uid == friendID {
			continue
		}
		if s.accounts.FriendNotify(uid) && s.isUsualClass(uid, c, wd) && s.markFriendNotified(uid, friendID, c) {
			out = append(out, friendNotice{to: uid, about: friendID})
		}
		if tellFriend && s.isBooked(uid, c.Name, c.Club, c.Date, c.Start) && s.markFriendNotified(friendID, uid, c) {
			out = append(out, friendNotice{to: friendID, about: uid})
		}
	}
	return out
}

// markFriendNotified records that uid was told friendID goes to c; false if
// they already were.
func (s *Server) markFriendNotified(uid, friendID int64, c calendar.Class) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO friend_notified (user_id, friend_id, name, club, date, start) VALUES (?, ?, ?, ?, ?, ?)`,
		uid, friendID, c.Name, c.Club, c.Date, c.Start)
	if err != nil {
		log.Printf("friend notice u%d: %v", uid, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// friendsLeft returns who to tell that userID dropped c: everyone told userID
// was going. The marks both ways go, so booking again tells them again, and a
// second drop (this handler and a later sync) tells nobody.
func (s *Server) friendsLeft(userID int64, c calendar.Class) []friendNotice {
	rows, err := s.db.Query(`DELETE FROM friend_notified WHERE friend_id = ? AND name = ? AND club = ? AND date = ? AND start = ?
		RETURNING user_id`, userID, c.Name, c.Club, c.Date, c.Start)
	if err != nil {
		log.Printf("friend left u%d: %v", userID, err)
		return nil
	}
	var told []int64
	for rows.Next() {
		var uid int64
		if rows.Scan(&uid) == nil {
			told = append(told, uid)
		}
	}
	rows.Close()
	s.db.Exec(`DELETE FROM friend_notified WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		userID, c.Name, c.Club, c.Date, c.Start)
	var out []friendNotice
	for _, uid := range told {
		if s.accounts.FriendNotify(uid) {
			out = append(out, friendNotice{to: uid, about: userID})
		}
	}
	return out
}

func (s *Server) pushFriends(notices []friendNotice, titleKey string, c calendar.Class) {
	when, _ := s.startOf(c.Date, c.Start)
	for _, n := range notices {
		lang := s.langOf(n.to)
		s.push.Notify(n.to, push.Message{
			Title: i18n.T(lang, titleKey, displayName(s.accounts.Email(n.about))),
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
