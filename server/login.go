package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

const sessionCookie = "va_session"

// sessions guarda los tokens de sesión del FE en memoria (al reiniciar, el
// usuario vuelve a iniciar sesión; las credenciales siguen guardadas).
type sessions struct {
	mu     sync.Mutex
	tokens map[string]bool
}

func newSessions() *sessions { return &sessions{tokens: map[string]bool{}} }

func (s *sessions) create() string {
	b := make([]byte, 24)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[tok] = true
	s.mu.Unlock()
	return tok
}

func (s *sessions) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tok != "" && s.tokens[tok]
}

func (s *sessions) destroy(tok string) {
	s.mu.Lock()
	delete(s.tokens, tok)
	s.mu.Unlock()
}

// handleLogin valida las credenciales contra Virgin, las guarda y abre sesión.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Pass  string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "petición inválida", http.StatusBadRequest)
		return
	}
	if err := s.auth.Login(req.Email, req.Pass); err != nil {
		// Distingue credenciales mal de problemas de red/bloqueo del sitio.
		msg, code := "credenciales inválidas", http.StatusUnauthorized
		if !strings.Contains(err.Error(), "rechazado") {
			msg, code = "no se pudo conectar con Virgin Active, reinténtalo en un momento", http.StatusBadGateway
		}
		w.WriteHeader(code)
		writeJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	tok := s.sess.create()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	s.Invalidate() // recargar el calendario con la sesión autenticada
	writeJSON(w, map[string]any{"ok": true, "email": req.Email})
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

// handleMe informa del estado de sesión y configuración.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"loggedIn":   s.loggedIn(r),
		"configured": s.auth.Configured(),
		"email":      s.auth.Email(),
	})
}

func (s *Server) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.sess.valid(c.Value)
}

// requireSession protege un handler: 401 si no hay sesión válida.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"error": "no autenticado"})
			return
		}
		next(w, r)
	}
}
