package server

import (
	"encoding/json"
	"net/http"
	"slices"
)

// Reminder lead times the user can pick (hours before the class; 0 = off).
// Every option is past the cancel line (2 h), so the reminder still lets the
// user cancel in time.
var reminderChoices = []int{0, 4, 6, 12, 24}

type settings struct {
	ReminderHours int    `json:"reminderHours"`
	FullAction    string `json:"fullAction"`
	FriendNotify  *bool  `json:"friendNotify"` // nil (an older app) keeps the stored choice
}

// handleSettings: GET / PUT the user's notification preferences.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"reminderHours":  s.accounts.ReminderHours(userID),
			"fullAction":     s.accounts.FullAction(userID),
			"friendNotify":   s.accounts.FriendNotify(userID),
			"watchCutoffMin": int(watchCutoff().Minutes()), // for the "until N h before" text
		})
	case http.MethodPut:
		var req settings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil ||
			!slices.Contains(reminderChoices, req.ReminderHours) ||
			!slices.Contains([]string{fullNotify, fullWaitlist, fullWatch}, req.FullAction) {
			http.Error(w, "invalid settings", http.StatusBadRequest)
			return
		}
		friends := s.accounts.FriendNotify(userID)
		if req.FriendNotify != nil {
			friends = *req.FriendNotify
		}
		req.FriendNotify = &friends
		if err := s.accounts.SetPrefs(userID, req.ReminderHours, req.FullAction, friends); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, req)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
