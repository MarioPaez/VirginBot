package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/session"
)

// authTTL: la sesión de www (JWT/.AspNet.Cookies) dura ~2h; refrescamos antes.
const authTTL = 90 * time.Minute

// Auth mantiene un cliente HTTP autenticado en www, renovándolo al caducar.
type Auth struct {
	user, pass string

	mu     sync.Mutex
	client *http.Client
	expiry time.Time
}

func NewAuth(user, pass string) *Auth {
	return &Auth{user: user, pass: pass}
}

// Client devuelve un cliente autenticado, haciendo login si hace falta.
func (a *Auth) Client() (*http.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client != nil && time.Now().Before(a.expiry) {
		return a.client, nil
	}
	c, err := session.LoginWWW(a.user, a.pass)
	if err != nil {
		return nil, err
	}
	a.client = c
	a.expiry = time.Now().Add(authTTL)
	return c, nil
}

// Invalidate fuerza un nuevo login en la próxima llamada a Client.
func (a *Auth) Invalidate() {
	a.mu.Lock()
	a.expiry = time.Time{}
	a.mu.Unlock()
}
