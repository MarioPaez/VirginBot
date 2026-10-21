package vapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// jwt builds an unsigned token with the given expiry (only exp is read).
func jwt(exp time.Time) string {
	claims := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "e30." + claims + ".sig"
}

// Resume renews the session with PATCH /sessions and the refresh token as
// x-auth-token; a rejected refresh is a plain error, never "app rejected".
func TestResume(t *testing.T) {
	good := jwt(time.Now().Add(14 * 24 * time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/sessions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-auth-token") != good {
			w.WriteHeader(http.StatusUnauthorized) // the gateway's shape, as observed
			fmt.Fprint(w, `{ "statusCode": 401, "message": "API key or access token is missing or invalid" }`)
			return
		}
		fmt.Fprint(w, `{"token":"new-access","refreshToken":"new-refresh"}`)
	}))
	defer srv.Close()
	defer SetBaseURL(srv.URL)()

	c, err := Resume("key", good)
	if err != nil || c.token != "new-access" || c.RefreshToken() != "new-refresh" {
		t.Fatalf("resume: %+v, %v", c, err)
	}
	_, err = Resume("key", jwt(time.Now().Add(time.Hour))) // valid-looking, but revoked
	if err == nil || errors.Is(err, ErrClientRejected) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a rejected refresh must be a plain error, got %v", err)
	}
	if _, err := Resume("key", jwt(time.Now().Add(-time.Minute))); !errors.Is(err, ErrRefreshExpired) {
		t.Fatalf("an expired refresh token must not reach the network, got %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Unix(1793000000, 0)
	if got, ok := TokenExpiry(jwt(exp)); !ok || !got.Equal(exp) {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, bad := range []string{"", "a.b", "a.!!!.c", "e30.e30.sig"} {
		if _, ok := TokenExpiry(bad); ok {
			t.Errorf("%q: expiry from a malformed token", bad)
		}
	}
}
