package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "va_session"
	sessionTTL    = 30 * 24 * time.Hour // los tokens caducan a los 30 días

	loginWindow      = 5 * time.Minute // ventana del rate-limit de login
	loginMaxAttempts = 8               // intentos permitidos por IP y ventana
)

// ctxKey es el tipo de las claves de contexto de este paquete.
type ctxKey int

const ctxUserID ctxKey = iota

// sessions persiste los tokens de sesión del FE en SQLite (atados a un usuario),
// así el login sobrevive a los reinicios de la aplicación. Incluye un rate-limit
// en memoria de los intentos de login por IP.
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

// allow aplica el rate-limit: como máximo loginMaxAttempts por IP cada loginWindow.
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
		log.Printf("crear sesión: %v", err)
	}
	return tok
}

// valid devuelve el usuario asociado al token, o ok=false si no existe o caducó.
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
		s.destroy(tok) // caducada: la limpiamos
		return 0, false
	}
	return userID, true
}

func (s *sessions) destroy(tok string) {
	s.db.Exec(`DELETE FROM sessions WHERE token = ?`, tok)
}

// handleLogin valida las credenciales contra Virgin, materializa el usuario y
// abre sesión.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sess.allow(clientIP(r)) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"ok": false, "error": "demasiados intentos, espera unos minutos"})
		return
	}
	var req struct {
		Email string `json:"email"`
		Pass  string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "petición inválida", http.StatusBadRequest)
		return
	}
	if !emailAllowed(req.Email) {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"ok": false, "error": "esta cuenta no está autorizada en este servidor"})
		return
	}
	userID, err := s.auth.Login(req.Email, req.Pass)
	if err != nil {
		// Distingue credenciales mal de problemas de red/bloqueo del sitio.
		msg, code := "credenciales inválidas", http.StatusUnauthorized
		if !strings.Contains(err.Error(), "rechazado") {
			msg, code = "no se pudo conectar con Virgin Active, reinténtalo en un momento", http.StatusBadGateway
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
	s.InvalidateUser(userID) // recargar el calendario con la sesión autenticada
	writeJSON(w, map[string]any{"ok": true, "email": req.Email})
}

// emailAllowed comprueba la allowlist opcional VA_ALLOWED_EMAILS (lista separada
// por comas). Si la variable está vacía, el registro es abierto (cualquier cuenta
// Virgin válida). Útil para restringir un despliegue público a ti y a tus amigos.
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

// clientIP obtiene la IP del cliente, respetando X-Forwarded-For tras un proxy
// que termina TLS (hosting). Toma el primer salto de la cadena.
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

// isHTTPS detecta si la petición llega por HTTPS (directo o tras un proxy que
// termina TLS, como en el hosting). Permite marcar la cookie como Secure en
// producción sin romper el desarrollo local en http://localhost.
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

// handleMe informa del estado de sesión y del email del usuario logueado.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.userOf(r)
	email := ""
	if ok {
		email = s.auth.Email(userID)
	}
	writeJSON(w, map[string]any{
		"loggedIn":   ok,
		"configured": ok,
		"email":      email,
	})
}

// userOf devuelve el usuario de la cookie de sesión.
func (s *Server) userOf(r *http.Request) (int64, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return 0, false
	}
	return s.sess.valid(c.Value)
}

// userIDFrom recupera el usuario inyectado por requireSession en el contexto.
func userIDFrom(r *http.Request) int64 {
	if v, ok := r.Context().Value(ctxUserID).(int64); ok {
		return v
	}
	return 0
}

// requireSession protege un handler: 401 si no hay sesión válida; si la hay,
// inyecta el userID en el contexto de la petición.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.userOf(r)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "no autenticado"})
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserID, userID)))
	}
}
