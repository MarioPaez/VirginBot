package server

import (
	"encoding/json"
	"net/http"

	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/push"
)

// SetPush enables Web Push (the engine's booking outcomes reach the user's
// devices). nil keeps it disabled: the endpoints report enabled=false.
func (s *Server) SetPush(n *push.Notifier) { s.push = n }

// handlePush: GET the status for this user (enabled on the server, VAPID key,
// subscribed devices).
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"enabled":   s.push != nil,
		"publicKey": s.push.PublicKey(),
		"devices":   s.push.Count(userIDFrom(r)),
	})
}

// handlePushSubscribe: POST the PushSubscription of this device.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var sub push.Subscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&sub); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.push.Subscribe(userIDFrom(r), sub); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePushUnsubscribe: POST {endpoint} to forget this device.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.push.Unsubscribe(userIDFrom(r), req.Endpoint); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handlePushTest: POST sends a test notification to every device of the user.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID := userIDFrom(r)
	lang := s.langOf(userID)
	sent := s.push.Notify(userID, push.Message{
		Title: i18n.T(lang, "push.test.title"),
		Body:  i18n.T(lang, "push.test.body"),
		URL:   "/",
	})
	writeJSON(w, map[string]any{"ok": sent > 0, "sent": sent})
}
