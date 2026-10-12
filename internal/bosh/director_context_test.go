package bosh_test

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

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

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, infoErr := director.GetInfo(ctx)
		done <- infoErr
	}()

	<-arrived

	var infoErr error

	select {
	case infoErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GetInfo against a hung director was still waiting 5s after its 200ms deadline, so the call is not bound to its context")
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
	case <-time.After(5 * time.Second):
		t.Fatal("the director never saw its request cancelled, so the HTTP request outlived the call")
	}
}
