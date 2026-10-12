package bosh_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

// slowCancelAnswer is how long the test directors take to answer a cancel.
// It is longer than the 3 second bound a log fetch puts on its cancel and
// shorter than the 10 second bound everything else keeps.
const slowCancelAnswer = 4 * time.Second

// An SSH setup task creates a user, so the cancel sent when its wait
// passes the deadline must not be cut short the way a log fetch's is. A
// director that takes about 4 seconds to answer it still gets the cancel,
// and the error says the cancel landed.
func TestSSHSetupCancelKeepsTenSecondsForASlowDirector(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskQueued}
	})
	server.CancelDelay = slowCancelAnswer

	bosh.SetSSHSetupTimeout(director, testSetupTimeout)

	err := failSetup(t, context.WithoutCancel(t.Context()), director)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("SSHSession = %v, want the setup bound's deadline", err)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("SSHSession returned %T, want a *bosh.DirectorCallError: %v", err, err)
	}

	if callErr.TaskID != setup.TaskID || callErr.Task != bosh.TaskCancelRequested {
		t.Errorf("DirectorCallError names task %d and outcome %v, want task %d and %v: %v",
			callErr.TaskID, callErr.Task, setup.TaskID, bosh.TaskCancelRequested, err)
	}

	cancels := server.Cancels()
	if len(cancels) != 1 || cancels[0].TaskID != setup.TaskID || cancels[0].State != boshtest.TaskQueued {
		t.Fatalf("cancels %+v, want one for queued setup task %d", cancels, setup.TaskID)
	}

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())
}

// The instance listing is read-only and runs under no request deadline of
// its own, so the cancel of its task keeps the 10 second bound, and a
// director that takes about 4 seconds to answer it still gets the cancel.
func TestGetInstancesCancelKeepsTenSecondsForASlowDirector(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newDetailsDirector(t, detailsConfig{
		kind:  instancesKind,
		state: queuedUntilCancelled,
		cancel: func(writer http.ResponseWriter) {
			time.Sleep(slowCancelAnswer)
			writer.WriteHeader(http.StatusNoContent)
		},
	})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	err := callWithin(t, 30*time.Second, func() error { return instanceListing().call(ctx, director) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an error that wraps context.DeadlineExceeded, got: %v", err)
	}

	_ = checkCallError(t, err, instanceListing().operation, bosh.TaskCancelRequested)
}
