package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/vapi"
)

func TestLoginFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"bad credentials", vapi.ErrBadCredentials, http.StatusUnauthorized},
		{"stored creds rejected", errCredsRejected, http.StatusUnauthorized},
		{"app credentials outdated", fmt.Errorf("%w (x)", vapi.ErrClientRejected), http.StatusBadGateway},
		{"our DB failed", fmt.Errorf("%w: disk full", errSaveAccount), http.StatusInternalServerError},
		{"lockout", &vapi.StatusError{Status: http.StatusTooManyRequests}, http.StatusBadGateway},
		{"outage", &vapi.StatusError{Status: http.StatusServiceUnavailable}, http.StatusBadGateway},
		{"timeout", &url.Error{Op: "Post", URL: "x", Err: context.DeadlineExceeded}, http.StatusBadGateway},
		{"unknown", errors.New("boom"), http.StatusBadGateway},
	}
	for _, c := range cases {
		code, msg := loginFailure(c.err)
		if code != c.code || msg == "" {
			t.Errorf("%s: got %d %q, want %d", c.name, code, msg, c.code)
		}
	}
}

// The session cookie must persist (an expiry-less cookie is dropped by iOS when
// the installed app closes) and an active session keeps renewing.
func TestSessionPersistsAndRenews(t *testing.T) {
	s := testServer(t)
	s.sess = newSessions(s.db)
	s.auth = NewAuth(s.accounts, "test-key")
	uid, _ := s.accounts.Upsert("a@x", "pw")
	tok := s.sess.create(uid)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	s.handleMe(rec, req)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != tok || cookies[0].MaxAge != int(sessionTTL/time.Second) || !cookies[0].HttpOnly {
		t.Fatalf("/api/me must re-issue a persistent session cookie, got %+v", cookies)
	}

	if s.sess.renew(tok) {
		t.Fatal("a fresh session isn't rewritten (at most once a day)")
	}
	old := time.Now().Add(-sessionTTL + time.Hour).Format(time.RFC3339) // expires in 1 h
	s.db.Exec(`UPDATE sessions SET created = ? WHERE token = ?`, old, tok)
	if !s.sess.renew(tok) {
		t.Fatal("an old session must be renewed")
	}
	if _, ok := s.sess.valid(tok); !ok {
		t.Fatal("the renewed session must still be valid")
	}
	s.db.Exec(`UPDATE sessions SET created = ? WHERE token = ?`, time.Now().Add(-sessionTTL-time.Hour).Format(time.RFC3339), tok)
	if _, ok := s.sess.valid(tok); ok {
		t.Fatal("30 days without opening the app still expires the session")
	}
}
