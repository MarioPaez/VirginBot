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

// Auth mantiene un cliente HTTP autenticado en www POR USUARIO, reutilizándolo
// hasta que caduca y re-logueando con las credenciales guardadas.
type Auth struct {
	store *account.Store

	mu      sync.Mutex
	clients map[int64]*authEntry
}

type authEntry struct {
	client *http.Client
	expiry time.Time
}

func NewAuth(store *account.Store) *Auth {
	return &Auth{store: store, clients: map[int64]*authEntry{}}
}

// Login valida las credenciales contra Virgin (login real), las guarda cifradas
// (creando/actualizando el usuario) y cachea la sesión recién creada. Devuelve
// el id del usuario.
func (a *Auth) Login(email, pass string) (int64, error) {
	client, err := session.LoginWWW(email, pass)
	if err != nil {
		return 0, err
	}
	userID, err := a.store.Upsert(email, pass)
	if err != nil {
		return 0, err
	}
	a.mu.Lock()
	a.clients[userID] = &authEntry{client: client, expiry: time.Now().Add(authTTL)}
	a.mu.Unlock()
	return userID, nil
}

// ClientFor devuelve un cliente autenticado para el usuario, logueando con sus
// credenciales guardadas si hace falta.
func (a *Auth) ClientFor(userID int64) (*http.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.clients[userID]; e != nil && time.Now().Before(e.expiry) {
		return e.client, nil
	}
	email, pass, ok := a.store.Get(userID)
	if !ok {
		return nil, errNoCreds
	}
	c, err := session.LoginWWW(email, pass)
	if err != nil {
		return nil, err
	}
	a.clients[userID] = &authEntry{client: c, expiry: time.Now().Add(authTTL)}
	return c, nil
}

// InvalidateUser fuerza un nuevo login del usuario en la próxima llamada.
func (a *Auth) InvalidateUser(userID int64) {
	a.mu.Lock()
	if e := a.clients[userID]; e != nil {
		e.expiry = time.Time{}
	}
	a.mu.Unlock()
}

func (a *Auth) Email(userID int64) string { return a.store.Email(userID) }
