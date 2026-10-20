package server

import (
	"encoding/json"
	"net/http"
	"slices"
)

// Reminder lead times the user can pick (hours before the class; 0 = off).
var reminderChoices = []int{0, 2, 6, 12, 24}

type settings struct {
	ReminderHours int    `json:"reminderHours"`
	FullAction    string `json:"fullAction"`
}

// handleSettings: GET / PUT the user's notification preferences.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, settings{ReminderHours: s.accounts.ReminderHours(userID), FullAction: s.accounts.FullAction(userID)})
	case http.MethodPut:
		var req settings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil ||
			!slices.Contains(reminderChoices, req.ReminderHours) ||
			!slices.Contains([]string{fullNotify, fullWaitlist, fullWatch}, req.FullAction) {
			http.Error(w, "invalid settings", http.StatusBadRequest)
			return
		}
		if err := s.accounts.SetPrefs(userID, req.ReminderHours, req.FullAction); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, req)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
