package bosh_test

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// callBackstop is how long a test waits for a call that must end before it
// gives up on a hang. It is far past the time the call takes on an idle
// host, and no test checks the clock against it.
const callBackstop = 20 * time.Second

// datedContext is an expiring context that also reports a deadline, so the
// error of a call under it names the deadline the call had.
type datedContext struct {
	*expiringContext

	deadline time.Time
}

func (c datedContext) Deadline() (time.Time, bool) { return c.deadline, true }

// A director that accepts a request and never answers must not hold the
// caller past the caller's deadline, and the request itself must be
// cancelled rather than left open on the wire.
func TestDirectorCallEndsWithTheCallersContext(t *testing.T) {
	arrived := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	release := make(chan struct{})

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case arrived <- struct{}{}:
		default:
		}

		select {
		case <-request.Context().Done():
			select {
			case canceled <- struct{}{}:
			default:
			}
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	director, err := bosh.NewDirectorAdapter(bosh.Config{Address: server.URL, Username: basicAuthCredential, Password: basicAuthCredential, CACert: string(caPEM)})
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	// The deadline passes once the director has the request, never before,
	// so a slow host cannot spend it ahead of the request, which would leave
	// the director nothing to hold.
	expiring, expire := newExpiringContext(t.Context())
	defer expire()

	ctx := datedContext{expiringContext: expiring, deadline: time.Now().Add(callBackstop)}

	done := make(chan error, 1)

	go func() {
		_, infoErr := director.GetInfo(ctx)
		done <- infoErr
	}()

	select {
	case <-arrived:
	case infoErr := <-done:
		t.Fatalf("GetInfo ended before the director had the request: %v", infoErr)
	case <-time.After(callBackstop):
		t.Fatalf("the director had no request %s after GetInfo started", callBackstop)
	}

	expire()

	var infoErr error

	select {
	case infoErr = <-done:
	case <-time.After(callBackstop):
		t.Fatalf("GetInfo against a hung director was still waiting %s after its deadline passed, so the call is not bound to its context", callBackstop)
	}

	if !errors.Is(infoErr, context.DeadlineExceeded) {
		t.Fatalf("GetInfo returned %v, want an error that wraps context.DeadlineExceeded", infoErr)
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(infoErr, &callErr) {
		t.Fatalf("GetInfo returned %T (%v), want a *bosh.DirectorCallError", infoErr, infoErr)
	}

	if callErr.Operation != "GetInfo" || callErr.Director != server.URL || callErr.Deadline.IsZero() {
		t.Fatalf("DirectorCallError names operation %q, director %q, and deadline %v, want GetInfo, %s, and the call's deadline", callErr.Operation, callErr.Director, callErr.Deadline, server.URL)
	}

	select {
	case <-canceled:
	case <-time.After(callBackstop):
		t.Fatal("the director never saw its request cancelled, so the HTTP request outlived the call")
	}
}

// The advice for a call that got no answer names the director's own endpoint
// and Blacksmith's task view, and sends the operator to no command line tool.
func TestDirectorCallErrorAdviceNamesNoCLI(t *testing.T) {
	t.Parallel()

	const (
		director      = "https://10.0.0.4:25555"
		tasksEndpoint = "/b/tasks"
	)

	deadline := time.Date(2026, time.October, 5, 14, 3, 11, 0, time.UTC)

	message := (&bosh.DirectorCallError{
		Operation: "GetInfo",
		Director:  director,
		Deadline:  deadline,
		Cause:     context.DeadlineExceeded,
		Err:       context.DeadlineExceeded,
	}).Error()

	for _, want := range []string{
		"GetInfo",
		"2026-10-05T14:03:11Z",
		"GET " + director + "/info",
		tasksEndpoint,
		"monit summary",
		"network path from the broker to " + director,
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the advice to contain %q, got: %s", want, message)
		}
	}

	// Each forbidden command is built from parts so that a search for bosh
	// commands in the source does not match this guard.
	for _, forbidden := range []string{"bosh" + " env", "bosh" + " tasks", "bosh" + " cli"} {
		if strings.Contains(strings.ToLower(message), forbidden) {
			t.Errorf("the advice must not send the operator to %q, got: %s", forbidden, message)
		}
	}
}
