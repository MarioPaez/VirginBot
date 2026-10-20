package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MarioPaez/VirginBot/automation"
)

// SetEngineHealth lets the admin view show when the engine last completed a
// cycle (nil = the engine isn't running here).
func (s *Server) SetEngineHealth(lastCycle func() time.Time) { s.engineCycle = lastCycle }

type adminAttempt struct {
	automation.Attempt
	OwnerEmail string `json:"ownerEmail"`
}

// handleAdminAttempts returns the engine's health and its latest attempts (who,
// what, when, outcome), to answer "why didn't it book?" without the logs.
func (s *Server) handleAdminAttempts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 500 {
		limit = 100
	}
	attempts := s.store.RecentAttempts(limit)
	out := make([]adminAttempt, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, adminAttempt{Attempt: a, OwnerEmail: s.accounts.Email(a.UserID)})
	}
	lastCycle := ""
	if s.engineCycle != nil {
		if t := s.engineCycle(); !t.IsZero() {
			lastCycle = t.Format(time.RFC3339)
		}
	}
	writeJSON(w, map[string]any{
		"engine":    s.engineCycle != nil,
		"lastCycle": lastCycle,
		"lastDay":   s.store.OutcomesSince(time.Now().Add(-24 * time.Hour)),
		"attempts":  out,
	})
}
