package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"
)

// Virgin doesn't expose attendance (past bookings vanish from its API), so the
// history is ours: every booked class that started, with the user's answer to
// "did you go?". Unanswered classes count as gone; a "no" doesn't count and
// feeds the no-show warning (3 no-shows in 30 days can block bookings 15 days).
const (
	askAttendanceDays = 14 // how far back unanswered classes are asked about
	suggestMinVisits  = 3  // visits to the same weekly slot before suggesting to automate it
	monthsShown       = 6
)

// stats summarizes the user's class history.
type stats struct {
	Month    int          `json:"month"`    // classes this calendar month
	Total    int          `json:"total"`    // since the history started
	ByBot    int          `json:"byBot"`    // of those, booked by VirginBot (engine, watch, Auto)
	Streak   int          `json:"streak"`   // consecutive weeks with at least one class, up to this or last week
	Missed30 int          `json:"missed30"` // classes the user said they didn't go to, last 30 days
	Since    string       `json:"since"`    // first day in the history (YYYY-MM-DD), "" if empty
	Months   []monthStat  `json:"months"`   // last monthsShown months, oldest first
	Classes  []classStat  `json:"classes"`  // classes by frequency (the FE groups them by category)
	Slot     *slotStat    `json:"slot"`     // favourite weekly slot, nil if none repeats
	Suggest  []slotStat   `json:"suggest"`  // frequent weekly slots, to offer automating them
	Pending  []historyRow `json:"pending"`  // recent classes not answered yet ("did you go?")
}

type monthStat struct {
	Month string `json:"month"` // YYYY-MM
	Count int    `json:"count"`
}

type classStat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// slotStat is a weekly slot (same class, club, weekday and time).
type slotStat struct {
	Name    string `json:"name"`
	Club    string `json:"club"`
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Count   int    `json:"count"`
}

type historyRow struct {
	Name  string `json:"name"`
	Club  string `json:"club"`
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.userStats(userIDFrom(r), time.Now().In(s.loc)))
}

// handleAttended records the answer to "did you go?" for one past class.
func (s *Server) handleAttended(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		historyRow
		Attended bool `json:"attended"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	res, err := s.db.Exec(`UPDATE history SET attended = ? WHERE user_id = ? AND name = ? AND club = ? AND date = ? AND start = ?`,
		boolInt(req.Attended), userIDFrom(r), req.Name, req.Club, req.Date, req.Start)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "class not in your history", http.StatusNotFound)
		return
	}
	writeJSON(w, s.userStats(userIDFrom(r), time.Now().In(s.loc)))
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) userStats(userID int64, now time.Time) stats {
	out := stats{Months: []monthStat{}, Classes: []classStat{}, Suggest: []slotStat{}, Pending: []historyRow{}}
	rows, err := s.db.Query(`SELECT name, club, date, start, end_time, by_bot, attended FROM history
		WHERE user_id = ? ORDER BY date, start`, userID)
	if err != nil {
		log.Printf("stats u%d: %v", userID, err)
		return out
	}
	type rec struct {
		historyRow
		byBot    bool
		attended *bool
		day      time.Time
	}
	var all []rec
	for rows.Next() {
		var x rec
		var att *int
		if rows.Scan(&x.Name, &x.Club, &x.Date, &x.Start, &x.End, &x.byBot, &att) != nil {
			continue
		}
		if att != nil {
			v := *att != 0
			x.attended = &v
		}
		d, err := time.ParseInLocation("2006-01-02", x.Date, s.loc)
		if err != nil {
			continue
		}
		x.day = d
		all = append(all, x)
	}
	rows.Close()

	month := now.Format("2006-01")
	months := map[string]int{}
	byName := map[string]int{}
	slots := map[string]*slotStat{}
	weeks := map[time.Time]bool{} // Monday of each week with a class
	askFrom := now.AddDate(0, 0, -askAttendanceDays).Format("2006-01-02")
	missedFrom := now.AddDate(0, 0, -30).Format("2006-01-02")
	for _, x := range all {
		if out.Since == "" {
			out.Since = x.Date
		}
		if x.attended != nil && !*x.attended { // didn't go
			if x.Date >= missedFrom {
				out.Missed30++
			}
			continue
		}
		if x.attended == nil && x.Date >= askFrom {
			out.Pending = append(out.Pending, x.historyRow)
		}
		out.Total++
		if x.byBot {
			out.ByBot++
		}
		if x.Date[:7] == month {
			out.Month++
		}
		months[x.Date[:7]]++
		byName[x.Name]++
		weeks[mondayOf(x.day)] = true
		k := x.Name + "|" + x.Club + "|" + x.day.Weekday().String() + "|" + x.Start
		if slots[k] == nil {
			slots[k] = &slotStat{Name: x.Name, Club: x.Club, Weekday: int(x.day.Weekday()), Start: x.Start}
		}
		slots[k].Count++
		slots[k].End = x.End
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
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.loc)
	for i := monthsShown - 1; i >= 0; i-- {
		m := first.AddDate(0, -i, 0).Format("2006-01")
		out.Months = append(out.Months, monthStat{Month: m, Count: months[m]})
	}
	for n, c := range byName {
		out.Classes = append(out.Classes, classStat{Name: n, Count: c})
	}
	sort.Slice(out.Classes, func(i, j int) bool {
		if out.Classes[i].Count != out.Classes[j].Count {
			return out.Classes[i].Count > out.Classes[j].Count
		}
		return out.Classes[i].Name < out.Classes[j].Name
	})
	for _, sl := range slots {
		if out.Slot == nil || sl.Count > out.Slot.Count || (sl.Count == out.Slot.Count && sl.Start < out.Slot.Start) {
			out.Slot = sl
		}
		if sl.Count >= suggestMinVisits {
			out.Suggest = append(out.Suggest, *sl)
		}
	}
	if out.Slot != nil && out.Slot.Count < 2 {
		out.Slot = nil // a single visit isn't a favourite
	}
	sort.Slice(out.Suggest, func(i, j int) bool { return out.Suggest[i].Count > out.Suggest[j].Count })
	return out
}

// mondayOf returns the Monday 00:00 of t's week.
func mondayOf(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}
