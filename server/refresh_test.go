package server

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/vapi"
)

// Sessions are renewed with the stored refresh token, without the password; a
// dead refresh token falls back to a login once, and a 401 mid-session drops it.
func TestAuthPrefersRefresh(t *testing.T) {
	refresh := "e30." + base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(14*24*time.Hour).Unix()))) + ".sig"
	var logins, refreshes atomic.Int32
	revoked := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			logins.Add(1)
		case http.MethodPatch:
			refreshes.Add(1)
			if revoked.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{ "statusCode": 401, "message": "API key or access token is missing or invalid" }`)
				return
			}
		}
		fmt.Fprintf(w, `{"token":"access","refreshToken":%q}`, refresh)
	}))
	defer srv.Close()
	defer vapi.SetBaseURL(srv.URL)()

	s := testServer(t)
	a := NewAuth(s.accounts, "key")
	uid, err := a.Login("a@x", "pw")
	if err != nil || logins.Load() != 1 || s.accounts.RefreshToken(uid) != refresh {
		t.Fatalf("login: %v, logins %d", err, logins.Load())
	}

	a.mu.Lock()
	delete(a.clients, uid) // the access token expired (or the app restarted)
	a.mu.Unlock()
	if _, err := a.ClientFor(uid); err != nil || refreshes.Load() != 1 || logins.Load() != 1 {
		t.Fatalf("renewal: %v, refreshes %d, logins %d", err, refreshes.Load(), logins.Load())
	}

	revoked.Store(true)
	a.mu.Lock()
	delete(a.clients, uid)
	a.mu.Unlock()
	if _, err := a.ClientFor(uid); err != nil || refreshes.Load() != 2 || logins.Load() != 2 {
		t.Fatalf("fallback: %v, refreshes %d, logins %d", err, refreshes.Load(), logins.Load())
	}

	a.Invalidate(uid) // a 401 mid-session: the next session comes from a login
	if s.accounts.RefreshToken(uid) != "" {
		t.Fatal("Invalidate kept the refresh token")
	}
}
