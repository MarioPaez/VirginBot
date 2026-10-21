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

// authTTL: the vapi token lasts ~2h; we renew it before then (with the refresh
// token, or a login once that's gone).
const authTTL = 100 * time.Minute

// clientAlertEvery throttles the "app credentials rejected" alert: the engine hits
// it every round for every user, one notification per window is enough.
const clientAlertEvery = 6 * time.Hour

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

	alertMu   sync.Mutex
	alert     func(subject, body string)
	lastAlert time.Time

	credsRejected func(userID int64) // the user's stored password stopped working; nil = nobody told
}

type authEntry struct {
	client *vapi.Client
	expiry time.Time
}

func NewAuth(store *account.Store, apiKey string) *Auth {
	return &Auth{store: store, apiKey: apiKey, clients: map[int64]*authEntry{}, rejected: map[int64]bool{}}
}

// OnClientRejected sets who gets told when Virgin rejects the app credentials
// (x-api-key / app version): nothing works for anyone until they're updated.
// The fix: capture the official app's traffic (Proxyman with SSL Proxying) and
// compare x-api-key, x-app-id, x-app-version and the host; a new key goes to
// VA_API_KEY (.env and `fly secrets set`), the rest to the constants in
// vapi/vapi.go, then deploy.
func (a *Auth) OnClientRejected(fn func(title, body string)) {
	a.alertMu.Lock()
	a.alert = fn
	a.alertMu.Unlock()
}

// OnCredsRejected sets who's told when Virgin rejects a user's STORED password
// (changed on Virgin's side): their automations stop until they sign in again.
func (a *Auth) OnCredsRejected(fn func(userID int64)) { a.credsRejected = fn }

// observe fires the (throttled, async) alert on vapi.ErrClientRejected and
// returns err unchanged, so it can wrap any return.
func (a *Auth) observe(err error) error {
	if !errors.Is(err, vapi.ErrClientRejected) {
		return err
	}
	a.alertMu.Lock()
	defer a.alertMu.Unlock()
	log.Printf("auth: ALERT — %v", err)
	if a.alert == nil || time.Since(a.lastAlert) < clientAlertEvery {
		return err
	}
	a.lastAlert = time.Now()
	alert := a.alert
	go alert("Virgin rechaza la app",
		"Login, calendario y reservas automáticas caídos para todos. "+
			"Actualiza VA_API_KEY o la versión de la app (ver logs). No vuelvo a avisar en 6 h.")
	return err
}

// Login validates the credentials against vapi, stores them encrypted (creating/
// updating the user) and caches the session. Returns the user id.
func (a *Auth) Login(email, pass string) (int64, error) {
	client, err := vapi.Login(a.apiKey, email, pass)
	if err != nil {
		return 0, a.observe(err)
	}
	userID, err := a.store.Upsert(email, pass)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errSaveAccount, err)
	}
	a.mu.Lock()
	a.keep(userID, client)
	delete(a.rejected, userID) // fresh, valid password
	a.mu.Unlock()
	a.store.ClearCredsAlert(userID)
	return userID, nil
}

// keep caches the user's client and stores its refresh token. Called with a.mu held.
func (a *Auth) keep(userID int64, c *vapi.Client) {
	a.clients[userID] = &authEntry{client: c, expiry: time.Now().Add(authTTL)}
	if err := a.store.SetRefreshToken(userID, c.RefreshToken()); err != nil {
		log.Printf("auth: u%d: storing the refresh token: %v", userID, err)
	}
}

// resume renews the user's session from the stored refresh token, without the
// password. Called with a.mu held.
func (a *Auth) resume(userID int64) (*vapi.Client, bool) {
	rt := a.store.RefreshToken(userID)
	if rt == "" {
		return nil, false
	}
	c, err := vapi.Resume(a.apiKey, rt)
	if err != nil {
		log.Printf("auth: u%d: %v; logging in with the password", userID, err)
		a.store.SetRefreshToken(userID, "") // dead: don't try it again
		return nil, false
	}
	log.Printf("auth: u%d: session renewed with the refresh token", userID)
	a.keep(userID, c)
	return c, true
}

// ClientFor returns an authenticated vapi client for the user, renewing the
// session if needed: with the refresh token first (no password, so no login
// that counts toward Virgin's rate limit), else re-logging in with the stored
// credentials.
func (a *Auth) ClientFor(userID int64) (*vapi.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.clients[userID]; e != nil && time.Now().Before(e.expiry) {
		return e.client, nil
	}
	if a.rejected[userID] {
		return nil, errCredsRejected
	}
	if c, ok := a.resume(userID); ok {
		return c, nil
	}
	email, pass, ok := a.store.Get(userID)
	if !ok {
		return nil, errNoCreds
	}
	c, err := vapi.Login(a.apiKey, email, pass)
	if errors.Is(err, vapi.ErrBadCredentials) {
		a.rejected[userID] = true
		log.Printf("auth: u%d — Virgin rejected the stored password; pausing re-logins until the user signs in again", userID)
		if a.credsRejected != nil {
			go a.credsRejected(userID)
		}
		return nil, errCredsRejected
	}
	if err != nil {
		return nil, a.observe(err)
	}
	a.keep(userID, c)
	return c, nil
}

func (a *Auth) Email(userID int64) string { return a.store.Email(userID) }

// Invalidate drops the user's cached session, forcing a login on the next call.
// Used when vapi returns 401 (token invalidated out of band) before authTTL
// expires.
func (a *Auth) Invalidate(userID int64) {
	a.mu.Lock()
	delete(a.clients, userID)
	a.mu.Unlock()
	// A session that just got a 401 may have been revoked with its refresh
	// token: the retry logs in with the password, which always recovers.
	if err := a.store.SetRefreshToken(userID, ""); err != nil {
		log.Printf("auth: u%d: clearing the refresh token: %v", userID, err)
	}
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
		return a.observe(err)
	}
	a.Invalidate(userID)
	c, err = a.ClientFor(userID)
	if err != nil {
		return err
	}
	return a.observe(fn(c))
}
