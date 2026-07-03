package server

import (
	"log"
	"net/http"
	"time"
)

// sharedClass is one upcoming class occurrence with everyone who booked it.
// Served to any signed-in user (the "who's going" view), so it exposes only
// what that list shows: no booking/session ids.
type sharedClass struct {
	Name       string   `json:"name"`
	Club       string   `json:"club"`
	Date       string   `json:"date"`
	Start      string   `json:"start"`
	End        string   `json:"end"`
	Instructor string   `json:"instructor"`
	Users      []string `json:"users"`
}

// handleSharedBookings lists everyone's upcoming bookings grouped by class
// occurrence, so users can see who they'll share a class with. Read-only.
func (s *Server) handleSharedBookings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Freshen everyone's bookings in the background (guarded, one reconciliation
	// per user); the response serves the current table, like /api/bookings.
	for _, uid := range s.accounts.ListUserIDs() {
		s.triggerBookings(uid)
	}
	writeJSON(w, s.listSharedBookings())
}

// listSharedBookings groups every user's future bookings by occurrence
// (name+club+date+start). Like listAllBookings, it fully drains the query
// before resolving emails: the pool has a single connection.
func (s *Server) listSharedBookings() []sharedClass {
	rows, err := s.db.Query(
		`SELECT user_id, name, club, date, start, end_time, instructor
		 FROM bookings WHERE date >= ? ORDER BY date, start, name, club, user_id`, s.today())
	if err != nil {
		log.Printf("list shared bookings: %v", err)
		return []sharedClass{}
	}
	type row struct {
		userID int64
		class  sharedClass
	}
	var raw []row
	for rows.Next() {
		var rw row
		if err := rows.Scan(&rw.userID, &rw.class.Name, &rw.class.Club, &rw.class.Date,
			&rw.class.Start, &rw.class.End, &rw.class.Instructor); err != nil {
			continue
		}
		raw = append(raw, rw)
	}
	rows.Close()

	now := time.Now().In(s.loc)
	out := []sharedClass{}
	idx := map[string]int{} // occurrence key -> index in out
	for _, rw := range raw {
		if t, err := time.ParseInLocation("2006-01-02 15:04", rw.class.Date+" "+rw.class.Start, s.loc); err == nil && t.Before(now) {
			continue // today's classes that already started
		}
		email := s.accounts.Email(rw.userID)
		k := rw.class.Date + "|" + rw.class.Club + "|" + rw.class.Name + "|" + rw.class.Start
		if i, ok := idx[k]; ok {
			out[i].Users = append(out[i].Users, email)
			continue
		}
		rw.class.Users = []string{email}
		idx[k] = len(out)
		out = append(out, rw.class)
	}
	return out
}
