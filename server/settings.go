package server

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/MarioPaez/VirginBot/account"
)

// Reminder lead times the user can pick (hours before the class; 0 = off).
// Every option is past the cancel line (2 h), so the reminder still lets the
// user cancel in time.
var reminderChoices = []int{0, 4, 6, 12, 24}

// settings is the account sheet's form. The switches are pointers: nil (an
// older app that doesn't know them) keeps the stored choice.
type settings struct {
	ReminderHours int    `json:"reminderHours"`
	FullAction    string `json:"fullAction"`
	FriendNotify  *bool  `json:"friendNotify"`
	WeeklySummary *bool  `json:"weeklySummary"`
	AllClasses    *bool  `json:"allClasses"`
}

// handleSettings: GET / PUT the user's notification preferences.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	switch r.Method {
	case http.MethodGet:
		p := s.accounts.Prefs(userID)
		writeJSON(w, map[string]any{
			"reminderHours":  p.ReminderHours,
			"fullAction":     p.FullAction,
			"friendNotify":   p.FriendNotify,
			"weeklySummary":  p.WeeklySummary,
			"allClasses":     p.AllClasses,
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
		p := s.accounts.Prefs(userID)
		p.ReminderHours, p.FullAction = req.ReminderHours, req.FullAction
		for _, sw := range []struct {
			in  *bool
			dst *bool
		}{{req.FriendNotify, &p.FriendNotify}, {req.WeeklySummary, &p.WeeklySummary}, {req.AllClasses, &p.AllClasses}} {
			if sw.in != nil {
				*sw.dst = *sw.in
			}
		}
		if err := s.accounts.SetPrefs(userID, p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, settingsOf(p))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func settingsOf(p account.Prefs) settings {
	return settings{ReminderHours: p.ReminderHours, FullAction: p.FullAction,
		FriendNotify: &p.FriendNotify, WeeklySummary: &p.WeeklySummary, AllClasses: &p.AllClasses}
}
