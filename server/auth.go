package server

import (
	"errors"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/vapi"
)

// authTTL: el token de vapi dura ~2h; lo refrescamos (re-login) antes.
const authTTL = 100 * time.Minute

var errNoCreds = errors.New("no hay credenciales: inicia sesión")

// Auth mantiene un cliente vapi autenticado POR USUARIO, re-logueando con las
// credenciales guardadas cuando el token caduca.
type Auth struct {
	store  *account.Store
	apiKey string

	mu      sync.Mutex
	clients map[int64]*authEntry
}

type authEntry struct {
	client *vapi.Client
	expiry time.Time
}

func NewAuth(store *account.Store, apiKey string) *Auth {
	return &Auth{store: store, apiKey: apiKey, clients: map[int64]*authEntry{}}
}

// Login valida las credenciales contra vapi, las guarda cifradas (creando/
// actualizando el usuario) y cachea la sesión. Devuelve el id del usuario.
func (a *Auth) Login(email, pass string) (int64, error) {
	client, err := vapi.Login(a.apiKey, email, pass)
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

// ClientFor devuelve un cliente vapi autenticado para el usuario, re-logueando
// con sus credenciales guardadas si hace falta.
func (a *Auth) ClientFor(userID int64) (*vapi.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.clients[userID]; e != nil && time.Now().Before(e.expiry) {
		return e.client, nil
	}
	email, pass, ok := a.store.Get(userID)
	if !ok {
		return nil, errNoCreds
	}
	c, err := vapi.Login(a.apiKey, email, pass)
	if err != nil {
		return nil, err
	}
	a.clients[userID] = &authEntry{client: c, expiry: time.Now().Add(authTTL)}
	return c, nil
}

func (a *Auth) Email(userID int64) string { return a.store.Email(userID) }
