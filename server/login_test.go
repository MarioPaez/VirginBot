package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

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
