package server

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/MarioPaez/VirginBot/account"
	"github.com/MarioPaez/VirginBot/vapi"
)

// authTTL: the vapi token lasts ~2h; we refresh it (re-login) before then.
const authTTL = 100 * time.Minute

var errNoCreds = errors.New("no credentials: please sign in")

// errCredsRejected: Virgin rejected the STORED password (changed on Virgin's side,
// or account locked). Wraps vapi.ErrBadCredentials so errors.Is works for both.
var errCredsRejected = fmt.Errorf("%w; sign in again in VirginBot with your current password", vapi.ErrBadCredentials)

// errSaveAccount: Virgin accepted the login but we failed to persist it (our
// side: DB/encryption), so it must not be reported as a Virgin problem.
var errSaveAccount = errors.New("couldn't save the account")

// Auth keeps a PER-USER authenticated vapi client, re-logging in with the stored
// credentials when the token expires.
type Auth struct {
	store  *account.Store
	apiKey string

	mu      sync.Mutex
	clients map[int64]*authEntry
	// rejected marks users whose stored password Virgin rejected. We stop
	// re-logging in with it until they sign in again: every failed attempt counts
	// toward Virgin's lockout, and the engine would otherwise retry every round.
	rejected map[int64]bool
}

type authEntry struct {
	client *vapi.Client
	expiry time.Time
}

func NewAuth(store *account.Store, apiKey string) *Auth {
	return &Auth{store: store, apiKey: apiKey, clients: map[int64]*authEntry{}, rejected: map[int64]bool{}}
}

// Login validates the credentials against vapi, stores them encrypted (creating/
// updating the user) and caches the session. Returns the user id.
func (a *Auth) Login(email, pass string) (int64, error) {
	client, err := vapi.Login(a.apiKey, email, pass)
	if err != nil {
		return 0, err
	}
	userID, err := a.store.Upsert(email, pass)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errSaveAccount, err)
	}
	a.mu.Lock()
	a.clients[userID] = &authEntry{client: client, expiry: time.Now().Add(authTTL)}
	delete(a.rejected, userID) // fresh, valid password
	a.mu.Unlock()
	return userID, nil
}

// ClientFor returns an authenticated vapi client for the user, re-logging in with
// the stored credentials if needed.
func (a *Auth) ClientFor(userID int64) (*vapi.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.clients[userID]; e != nil && time.Now().Before(e.expiry) {
		return e.client, nil
	}
	if a.rejected[userID] {
		return nil, errCredsRejected
	}
	email, pass, ok := a.store.Get(userID)
	if !ok {
		return nil, errNoCreds
	}
	c, err := vapi.Login(a.apiKey, email, pass)
	if errors.Is(err, vapi.ErrBadCredentials) {
		a.rejected[userID] = true
		log.Printf("auth: u%d — Virgin rejected the stored password; pausing re-logins until the user signs in again", userID)
		return nil, errCredsRejected
	}
	if err != nil {
		return nil, err
	}
	a.clients[userID] = &authEntry{client: c, expiry: time.Now().Add(authTTL)}
	return c, nil
}

func (a *Auth) Email(userID int64) string { return a.store.Email(userID) }

// Invalidate drops the user's cached client, forcing a re-login on the next call.
// Used when vapi returns 401 (token invalidated out of band, e.g. after signing
// in on the phone) before authTTL expires.
func (a *Auth) Invalidate(userID int64) {
	a.mu.Lock()
	delete(a.clients, userID)
	a.mu.Unlock()
}

// Do runs fn with the user's authenticated client and, if vapi returns 401, drops
// the client, re-logs in and retries ONCE. Centralizes recovery from expired
// tokens that TTL caching doesn't catch.
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
