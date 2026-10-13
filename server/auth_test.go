package server

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MarioPaez/VirginBot/vapi"
)

func TestObserveAlertsOncePerWindow(t *testing.T) {
	a := NewAuth(nil, "")
	var sent atomic.Int32
	done := make(chan struct{}, 4)
	a.OnClientRejected(func(_, _ string) { sent.Add(1); done <- struct{}{} })

	rejected := fmt.Errorf("%w (POST /sessions: HTTP 401)", vapi.ErrClientRejected)
	for i := 0; i < 3; i++ {
		if err := a.observe(rejected); !errors.Is(err, vapi.ErrClientRejected) {
			t.Fatalf("observe must return the error unchanged, got %v", err)
		}
	}
	a.observe(vapi.ErrBadCredentials) // user-level: never alerts
	<-done
	time.Sleep(20 * time.Millisecond)
	if n := sent.Load(); n != 1 {
		t.Fatalf("want 1 alert inside the window, got %d", n)
	}

	a.lastAlert = time.Now().Add(-clientAlertEvery) // window elapsed
	a.observe(rejected)
	<-done
	if n := sent.Load(); n != 2 {
		t.Fatalf("want a new alert after the window, got %d", n)
	}
}
