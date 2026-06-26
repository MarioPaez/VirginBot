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

// Invalidate descarta el cliente cacheado del usuario, forzando un re-login en la
// próxima llamada. Se usa cuando vapi responde 401 (token invalidado fuera de
// banda, p. ej. al iniciar sesión en el móvil) antes de que expire authTTL.
func (a *Auth) Invalidate(userID int64) {
	a.mu.Lock()
	delete(a.clients, userID)
	a.mu.Unlock()
}

// Do ejecuta fn con el cliente autenticado del usuario y, si vapi devuelve 401,
// descarta el cliente, re-loguea y reintenta UNA vez. Centraliza la recuperación
// de tokens caducados que el cacheo por TTL no detecta.
func (a *Auth) Do(userID int64, fn func(*vapi.Client) error) error {
	c, err := a.ClientFor(userID)
	if err != nil {
		return err
	}
	err = fn(c)
	if !errors.Is(err, vapi.ErrUnauthorized) {
		return err
	}
	a.Invalidate(userID)
	c, err = a.ClientFor(userID)
	if err != nil {
		return err
	}
	return fn(c)
}
