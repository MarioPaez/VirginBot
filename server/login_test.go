package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// The web app logs in with a native form POST (so browsers offer to save the
// password): failures redirect back with the reason, and a form posted from
// another site is refused.
func TestFormLogin(t *testing.T) {
	s := testServer(t)
	s.sess = newSessions(s.db)
	s.auth = NewAuth(s.accounts, "test-key")
	t.Setenv("VA_ALLOWED_EMAILS", "friend@x")
	post := func(origin string) *httptest.ResponseRecorder {
		body := url.Values{"username": {"stranger@x"}, "password": {"pw"}}.Encode()
		req := httptest.NewRequest(http.MethodPost, "http://virginbot.test/api/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.handleLogin(rec, req)
		return rec
	}
	rec := post("http://virginbot.test")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/?loginError=") {
		t.Fatalf("a failed form login must redirect back with the reason: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := post("https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("a cross-site form login must be refused, got %d", rec.Code)
	}
}
