package server

import (
	"log"
	"net/http"
	"sort"
	"time"
)

// stats summarizes the user's class history (archived past bookings).
type stats struct {
	Month  int         `json:"month"`  // classes this calendar month
	Total  int         `json:"total"`  // since the history started
	ByBot  int         `json:"byBot"`  // of those, booked by VirginBot (engine, watch, Auto)
	Streak int         `json:"streak"` // consecutive weeks with at least one class, up to this or last week
	Top    []classStat `json:"top"`    // most frequent classes
	Since  string      `json:"since"`  // first day in the history (YYYY-MM-DD), "" if empty
}

type classStat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.userStats(userIDFrom(r), time.Now().In(s.loc)))
}

func (s *Server) userStats(userID int64, now time.Time) stats {
	out := stats{Top: []classStat{}}
	rows, err := s.db.Query(`SELECT name, date, by_bot FROM history WHERE user_id = ? ORDER BY date`, userID)
	if err != nil {
		log.Printf("stats u%d: %v", userID, err)
		return out
	}
	defer rows.Close()
	month := now.Format("2006-01")
	byName := map[string]int{}
	weeks := map[time.Time]bool{} // Monday of each week with a class
	for rows.Next() {
		var name, date string
		var byBot bool
		if rows.Scan(&name, &date, &byBot) != nil {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", date, s.loc)
		if err != nil {
			continue
		}
		if out.Since == "" {
			out.Since = date
		}
		out.Total++
		if byBot {
			out.ByBot++
		}
		if date[:7] == month {
			out.Month++
		}
		byName[name]++
		weeks[mondayOf(d)] = true
	}
	// The streak counts back from this week; a week in progress without a class
	// yet doesn't break it, so it may start from last week.
	wk := mondayOf(now)
	if !weeks[wk] {
		wk = wk.AddDate(0, 0, -7)
	}
	for weeks[wk] {
		out.Streak++
		wk = wk.AddDate(0, 0, -7)
	}
	for n, c := range byName {
		out.Top = append(out.Top, classStat{Name: n, Count: c})
	}
	sort.Slice(out.Top, func(i, j int) bool {
		if out.Top[i].Count != out.Top[j].Count {
			return out.Top[i].Count > out.Top[j].Count
		}
		return out.Top[i].Name < out.Top[j].Name
	})
	out.Top = out.Top[:min(len(out.Top), 3)]
	return out
}

// mondayOf returns the Monday 00:00 of t's week.
func mondayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}
