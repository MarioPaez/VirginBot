package server

import (
	"net/http"
	"strconv"

	"github.com/MarioPaez/VirginBot/automation"
	"github.com/MarioPaez/VirginBot/calendar"
)

// adminUser is a row of the admin user picker.
type adminUser struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out := []adminUser{}
	for _, id := range s.accounts.ListUserIDs() {
		out = append(out, adminUser{ID: id, Email: s.accounts.Email(id)})
	}
	writeJSON(w, out)
}

// adminAutomationView is a rule tagged with its owner, for the admin view (the
// UserID field shadows automation.Rule's, which is deliberately hidden from the
// per-user JSON).
type adminAutomationView struct {
	automation.Rule
	UserID     int64  `json:"userId"`
	OwnerEmail string `json:"ownerEmail"`
}

// handleAdminAutomations lists (GET) every user's automations, or pauses/enables
// (PATCH) and removes (DELETE) any user's rule by id.
func (s *Server) handleAdminAutomations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rules := s.store.ListAll()
		out := make([]adminAutomationView, 0, len(rules))
		for _, rule := range rules {
			out = append(out, adminAutomationView{
				Rule: rule, UserID: rule.UserID, OwnerEmail: s.accounts.Email(rule.UserID),
			})
		}
		writeJSON(w, out)
	case http.MethodPatch:
		userID, id, ok := adminRuleTarget(r)
		if !ok {
			http.Error(w, "userId and id are required", http.StatusBadRequest)
			return
		}
		if _, found := s.store.Get(userID, id); !found {
			http.Error(w, "automation not found", http.StatusNotFound)
			return
		}
		enabled := r.URL.Query().Get("enabled") == "1"
		if err := s.store.SetEnabled(userID, id, enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		userID, id, ok := adminRuleTarget(r)
		if !ok {
			http.Error(w, "userId and id are required", http.StatusBadRequest)
			return
		}
		if err := s.store.Remove(userID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func adminRuleTarget(r *http.Request) (userID int64, id string, ok bool) {
	uid, err := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	id = r.URL.Query().Get("id")
	return uid, id, err == nil && id != ""
}

// adminBooking is a booking tagged with its owner, read-only in the admin view.
type adminBooking struct {
	calendar.Class
	OwnerEmail string `json:"ownerEmail"`
}

// handleAdminBookings lists every user's upcoming bookings (view-only: cancelling
// on behalf of another user isn't exposed here).
func (s *Server) handleAdminBookings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.listAllBookings())
}

// listAllBookings reads every user's upcoming bookings. The DB connection pool
// has exactly one connection (see db.Open), so we fully drain and close this
// query BEFORE resolving owner emails with separate queries — interleaving them
// would deadlock (the email lookup would wait forever for a connection held by
// these still-open rows).
func (s *Server) listAllBookings() []adminBooking {
	rows, err := s.db.Query(
		`SELECT user_id, name, club, date, start, end_time, instructor, club_id, class_id, session_id, booking_id
		 FROM bookings WHERE date >= ? ORDER BY user_id, date, start`, s.today())
	if err != nil {
		return []adminBooking{}
	}
	type row struct {
		userID int64
		class  calendar.Class
	}
	var raw []row
	for rows.Next() {
		var rw row
		if err := rows.Scan(&rw.userID, &rw.class.Name, &rw.class.Club, &rw.class.Date, &rw.class.Start,
			&rw.class.End, &rw.class.Instructor, &rw.class.ClubID, &rw.class.ClassID, &rw.class.SessionID, &rw.class.BookingID); err != nil {
			continue
		}
		raw = append(raw, rw)
	}
	rows.Close()

	out := make([]adminBooking, 0, len(raw))
	for _, rw := range raw {
		rw.class.Booked = true
		rw.class.Status = "booked"
		out = append(out, adminBooking{Class: rw.class, OwnerEmail: s.accounts.Email(rw.userID)})
	}
	return out
}
