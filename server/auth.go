package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/session"
)

// authTTL: la sesión de www (JWT/.AspNet.Cookies) dura ~2h; refrescamos antes.
const authTTL = 90 * time.Minute

var errNoCreds = errors.New("no hay credenciales: inicia sesión")

// Auth mantiene un cliente HTTP autenticado en www usando las credenciales
// guardadas en el almacén, renovándolo al caducar.
type Auth struct {
	store *account.Store

	mu     sync.Mutex
	client *http.Client
	expiry time.Time
}

func NewAuth(store *account.Store) *Auth {
	return &Auth{store: store}
}

// Login valida las credenciales contra Virgin (haciendo login real) y, si son
// correctas, las guarda cifradas y reutiliza la sesión recién creada.
func (a *Auth) Login(email, pass string) error {
	client, err := session.LoginWWW(email, pass)
	if err != nil {
		return err
	}
	if err := a.store.Set(email, pass); err != nil {
		return err
	}
	a.mu.Lock()
	a.client = client
	a.expiry = time.Now().Add(authTTL)
	a.mu.Unlock()
	return nil
}

// Configured indica si hay credenciales guardadas.
func (a *Auth) Configured() bool {
	_, _, ok := a.store.Get()
	return ok
}

func (a *Auth) Email() string { return a.store.Email() }

// Client devuelve un cliente autenticado, logueando con las credenciales
// guardadas si hace falta.
func (a *Auth) Client() (*http.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil && time.Now().Before(a.expiry) {
		return a.client, nil
	}
	email, pass, ok := a.store.Get()
	if !ok {
		return nil, errNoCreds
	}
	c, err := session.LoginWWW(email, pass)
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
