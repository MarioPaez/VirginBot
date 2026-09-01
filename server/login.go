package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/i18n"
	"github.com/MarioPaez/VirginBot/vapi"
)

const (
	sessionCookie = "va_session"
	sessionTTL    = 30 * 24 * time.Hour // tokens expire after 30 days

	loginWindow      = 5 * time.Minute // login rate-limit window
	loginMaxAttempts = 8               // attempts allowed per IP and window
)

// ctxKey is the type of this package's context keys.
type ctxKey int

const ctxUserID ctxKey = iota

// sessions persists the FE session tokens in SQLite (tied to a user), so login
// survives application restarts. It includes an in-memory rate limit of login
// attempts per IP.
type sessions struct {
	db *sql.DB

	mu       sync.Mutex
	attempts map[string]*loginAttempts
}

type loginAttempts struct {
	count int
	reset time.Time
}

func newSessions(db *sql.DB) *sessions {
	return &sessions{db: db, attempts: map[string]*loginAttempts{}}
}

// allow applies the rate limit: at most loginMaxAttempts per IP every loginWindow.
func (s *sessions) allow(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	a := s.attempts[ip]
	if a == nil || now.After(a.reset) {
		s.attempts[ip] = &loginAttempts{count: 1, reset: now.Add(loginWindow)}
		return true
	}
	a.count++
	return a.count <= loginMaxAttempts
}

func (s *sessions) create(userID int64) string {
	b := make([]byte, 24)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, created) VALUES (?, ?, ?)`,
		tok, userID, time.Now().Format(time.RFC3339)); err != nil {
		log.Printf("create session: %v", err)
	}
	return tok
}

// valid returns the user associated with the token, or ok=false if it doesn't
// exist or has expired.
func (s *sessions) valid(tok string) (int64, bool) {
	if tok == "" {
		return 0, false
	}
	var userID int64
	var created string
	if err := s.db.QueryRow(`SELECT user_id, created FROM sessions WHERE token = ?`, tok).Scan(&userID, &created); err != nil {
		return 0, false
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil && time.Since(t) > sessionTTL {
		s.destroy(tok) // expired: clean it up
		return 0, false
	}
	return userID, true
}

func (s *sessions) destroy(tok string) {
	s.db.Exec(`DELETE FROM sessions WHERE token = ?`, tok)
}

// handleLogin validates the credentials against Virgin, materializes the user and
// opens a session.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sess.allow(clientIP(r)) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"ok": false, "error": "too many attempts, wait a few minutes"})
		return
	}
	var req struct {
		Email string `json:"email"`
		Pass  string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !emailAllowed(req.Email) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"ok": false, "error": "this account isn't allowed on this server"})
		return
	}
	userID, err := s.auth.Login(req.Email, req.Pass)
	if err != nil {
		// 401 from VA = bad credentials; anything else = network/service problem.
		msg, code := "invalid credentials", http.StatusUnauthorized
		if !errors.Is(err, vapi.ErrUnauthorized) && !strings.Contains(err.Error(), "rejected") {
			msg, code = "couldn't connect to Virgin Active, try again in a moment", http.StatusBadGateway
		}
		w.WriteHeader(code)
		writeJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	tok := s.sess.create(userID)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	// Detect the browser language the first time (doesn't override a prior choice).
	if s.accounts.Lang(userID) == "" {
		s.accounts.SetLang(userID, string(i18n.Normalize(r.Header.Get("Accept-Language"))))
	}
	s.InvalidateUser(userID) // reload the calendar with the authenticated session
	writeJSON(w, map[string]any{"ok": true, "email": req.Email})
}

// emailAllowed checks the optional VA_ALLOWED_EMAILS allowlist (comma-separated
// list). If the variable is empty, registration is open (any valid Virgin
// account). Handy to restrict a public deployment to you and your friends.
func emailAllowed(email string) bool {
	raw := strings.TrimSpace(os.Getenv("VA_ALLOWED_EMAILS"))
	if raw == "" {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, allowed := range strings.Split(raw, ",") {
		if email == strings.ToLower(strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

// isAdmin reports whether email matches VA_ADMIN_EMAIL (the only account allowed
// into the admin views). Empty VA_ADMIN_EMAIL disables the admin views for
// everyone.
func isAdmin(email string) bool {
	admin := strings.ToLower(strings.TrimSpace(os.Getenv("VA_ADMIN_EMAIL")))
	return admin != "" && strings.ToLower(strings.TrimSpace(email)) == admin
}

// clientIP obtains the client IP, honoring X-Forwarded-For behind a TLS-
// terminating proxy (hosting). Takes the first hop of the chain.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// isHTTPS detects whether the request arrives over HTTPS (directly or behind a
// TLS-terminating proxy, as in hosting). Lets us mark the cookie as Secure in
// production without breaking local development on http://localhost.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sess.destroy(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

// handleMe reports the session status and the logged-in user's email.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.userOf(r)
	email, lang := "", ""
	if ok {
		email = s.auth.Email(userID)
		lang = s.accounts.Lang(userID)
	}
	writeJSON(w, map[string]any{
		"loggedIn":   ok,
		"configured": ok,
		"email":      email,
		"lang":       lang,
		"isAdmin":    ok && isAdmin(email),
	})
}

// handleSetLang sets the user's preferred language (it/es/en). Used by the
// frontend selector; affects the UI and the emails.
func (s *Server) handleSetLang(w http.ResponseWriter, r *http.Request) {
	userID := userIDFrom(r)
	lang := string(i18n.Coerce(r.URL.Query().Get("lang")))
	if err := s.accounts.SetLang(userID, lang); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "lang": lang})
}

// userOf returns the user from the session cookie.
func (s *Server) userOf(r *http.Request) (int64, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return 0, false
	}
	return s.sess.valid(c.Value)
}

// userIDFrom retrieves the user injected by requireSession into the context.
func userIDFrom(r *http.Request) int64 {
	if v, ok := r.Context().Value(ctxUserID).(int64); ok {
		return v
	}
	return 0
}

// requireSession guards a handler: 401 if there's no valid session; if there is,
// injects the userID into the request context.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.userOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "not authenticated"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserID, userID)))
	}
}

// requireAdmin guards a handler like requireSession, additionally requiring the
// session's user to match VA_ADMIN_EMAIL: 403 for anyone else.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireSession(func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(s.auth.Email(userIDFrom(r))) {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]any{"error": "admin only"})
			return
		}
		next(w, r)
	})
}
