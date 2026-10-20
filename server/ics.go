package server

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Private calendar feed: /cal/<secret>.ics lists the user's bookings as an
// iCalendar subscription the phone refreshes on its own. The secret is the
// only credential (calendar apps can't sign in), so it's long, random and
// rotatable.

// handleCalFeed: GET the feed's path (created on first use), POST to rotate it.
func (s *Server) handleCalFeed(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	var tok string
	var err error
	switch r.Method {
	case http.MethodGet:
		tok, err = s.accounts.CalToken(userID)
	case http.MethodPost:
		tok, err = s.accounts.RotateCalToken(userID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"path": "/cal/" + tok + ".ics"})
}

// handleICS serves the feed (no session: the secret in the path authenticates).
func (s *Server) handleICS(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/cal/"), ".ics")
	userID, found := s.accounts.UserByCalToken(tok)
	if !ok || len(tok) != 48 || !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write([]byte(s.buildICS(userID, time.Now()))); err != nil {
		log.Printf("ics u%d: %v", userID, err)
	}
}

// icsEvent is one booked class in the feed.
type icsEvent struct {
	name, club, date, start, end, instructor string
}

// buildICS renders the user's booked classes: the upcoming ones and the last
// 30 days of history (so attended classes stay in the calendar).
func (s *Server) buildICS(userID int64, now time.Time) string {
	from := now.In(s.loc).AddDate(0, 0, -30).Format("2006-01-02")
	rows, err := s.db.Query(`SELECT name, club, date, start, end_time, instructor FROM bookings
		WHERE user_id = ? AND status = 'booked' AND date >= ?
		UNION SELECT name, club, date, start, end_time, instructor FROM history
		WHERE user_id = ? AND date >= ?
		ORDER BY date, start`, userID, from, userID, from)
	var events []icsEvent
	if err != nil {
		log.Printf("ics u%d: %v", userID, err)
	} else {
		for rows.Next() {
			var e icsEvent
			if rows.Scan(&e.name, &e.club, &e.date, &e.start, &e.end, &e.instructor) == nil {
				events = append(events, e)
			}
		}
		rows.Close()
	}

	var b strings.Builder
	line := func(l string) { b.WriteString(foldICS(l)); b.WriteString("\r\n") }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//VirginBot//Bookings//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:VirginBot")
	line("X-WR-TIMEZONE:Europe/Rome")
	line("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	line("X-PUBLISHED-TTL:PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, e := range events {
		start, ok := s.startOf(e.date, e.start)
		if !ok {
			continue
		}
		end, ok := s.startOf(e.date, e.end)
		if !ok {
			end = start.Add(time.Hour)
		} else if !end.After(start) { // ends after midnight
			end = end.AddDate(0, 0, 1)
		}
		uid := sha1.Sum(fmt.Appendf(nil, "%d|%s|%s|%s|%s", userID, e.name, e.club, e.date, e.start))
		line("BEGIN:VEVENT")
		line("UID:" + hex.EncodeToString(uid[:]) + "@virginbot")
		line("DTSTAMP:" + stamp)
		line("DTSTART:" + start.UTC().Format("20060102T150405Z"))
		line("DTEND:" + end.UTC().Format("20060102T150405Z"))
		line("SUMMARY:" + escICS(e.name))
		line("LOCATION:" + escICS("Virgin Active "+e.club))
		if e.instructor != "" {
			line("DESCRIPTION:" + escICS(e.instructor))
		}
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

// escICS escapes a TEXT value (RFC 5545 §3.3.11).
func escICS(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(s)
}

// foldICS folds a content line at 75 octets without splitting a UTF-8
// character (RFC 5545 §3.1).
func foldICS(l string) string {
	if len(l) <= 75 {
		return l
	}
	var b strings.Builder
	n := 0
	for _, r := range l {
		size := utf8.RuneLen(r)
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1 // the leading space counts
		}
		b.WriteRune(r)
		n += size
	}
	return b.String()
}
