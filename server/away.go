package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/MarioPaez/VirginBot/calendar"
)

// maxAwayDays caps a holiday: a typo in the year shouldn't stop the bot for good.
const maxAwayDays = 90

type awayView struct {
	From  string `json:"from"`  // YYYY-MM-DD, "" = no holidays
	Until string `json:"until"` // included
	// Bookings already made in those days: VirginBot doesn't cancel them, the
	// user is told to (a no-show counts toward Virgin's block).
	Bookings []calendar.Class `json:"bookings"`
}

// handleAway: GET / PUT {from, until} / DELETE the user's holidays, when the
// engine books nothing for them (rules stay as they are and resume after).
func (s *Server) handleAway(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var req awayView
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if msg := validAway(req.From, req.Until, s.today()); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := s.accounts.SetAway(userID, req.From, req.Until); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case http.MethodDelete:
		if err := s.accounts.SetAway(userID, "", ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.awayOf(userID))
}

// validAway explains what's wrong with a holiday range ("" = fine).
func validAway(from, until, today string) string {
	f, err1 := time.Parse("2006-01-02", from)
	u, err2 := time.Parse("2006-01-02", until)
	switch {
	case err1 != nil || err2 != nil:
		return "invalid dates"
	case u.Before(f):
		return "the end is before the start"
	case until < today:
		return "the holidays are over"
	case u.Sub(f) > maxAwayDays*24*time.Hour:
		return "too long: at most 90 days"
	}
	return ""
}

// awayOf returns the user's holidays if they aren't over yet, with the bookings
// that fall in them.
func (s *Server) awayOf(userID int64) awayView {
	v := awayView{Bookings: []calendar.Class{}}
	from, until := s.accounts.Away(userID)
	if from == "" || until < s.today() {
		return v
	}
	v.From, v.Until = from, until
	for _, b := range s.listBookings(userID) {
		if from <= b.Date && b.Date <= until {
			v.Bookings = append(v.Bookings, b)
		}
	}
	return v
}
