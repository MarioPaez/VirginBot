package server

import (
	"log"
	"net/http"
	"strings"
)

// handleFavInstructors: GET the user's favourite instructors; PUT ?name=X&on=1|0
// stars or unstars one.
func (s *Server) handleFavInstructors(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" || len(name) > 120 {
			http.Error(w, "invalid instructor", http.StatusBadRequest)
			return
		}
		q := `DELETE FROM fav_instructors WHERE user_id = ? AND name = ?`
		if r.URL.Query().Get("on") == "1" {
			q = `INSERT OR IGNORE INTO fav_instructors (user_id, name) VALUES (?, ?)`
		}
		if _, err := s.db.Exec(q, userID, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.favInstructors(userID))
}

func (s *Server) favInstructors(userID int64) []string {
	out := []string{}
	rows, err := s.db.Query(`SELECT name FROM fav_instructors WHERE user_id = ? ORDER BY name`, userID)
	if err != nil {
		log.Printf("fav instructors u%d: %v", userID, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}
