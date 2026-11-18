package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// backfillStopWithin is how soon the boot backfill must give up on a
// stalled director once the broker is told to stop.
const backfillStopWithin = 5 * time.Second

// stalledDirector accepts a deployment list and never answers it, as a
// wedged director does, until the caller's context ends or the test
// releases it.
type stalledDirector struct {
	*testutil.ScriptedBOSHDirector

	asked    chan struct{}
	askOnce  sync.Once
	released chan struct{}
}

func (d *stalledDirector) GetDeployments(ctx context.Context) ([]bosh.Deployment, error) {
	d.askOnce.Do(func() { close(d.asked) })

	select {
	case <-ctx.Done():
		return nil, ctx.Err() //nolint:wrapcheck // a director's request ends with its context's error, as an HTTP request does
	case <-d.released:
		return nil, errDirectorListFailed
	}
}

// The service context does not end on a signal, so the boot backfill
// listens for SIGINT and SIGTERM itself. A stop sent while it waits on a
// stalled director ends the wait at once, rather than leaving the stop
// unanswered until the supervisor kills the broker.
//
//nolint:paralleltest // the test signals its own process, so nothing else may run alongside it
func TestBackfillDeploymentNamesStopsOnSIGTERM(t *testing.T) {
	// The test's own registration keeps the signal from ending the test
	// process when the backfill does not listen for it.
	held := make(chan os.Signal, 1)
	signal.Notify(held, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(held) })

	director := &stalledDirector{
		ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector(),
		asked:                make(chan struct{}),
		released:             make(chan struct{}),
	}

	t.Cleanup(func() { close(director.released) })

	pool := bosh.NewPooledDirector(director, 1, time.Second, loggerPkg.CreateSilentLogger())
	logger := newRecordingLogger()
	done := make(chan struct{})

	go func() {
		defer close(done)

		backfillDeploymentNames(context.Background(), &broker.Broker{BOSH: director}, pool, logger)
	}()

	select {
	case <-director.asked:
	case <-time.After(backfillStopWithin):
		t.Fatal("the backfill never asked the director for its deployments")
	}

	err := syscall.Kill(os.Getpid(), syscall.SIGTERM)
	if err != nil {
		t.Fatalf("could not signal the test process: %v", err)
	}

	select {
	case <-done:
	case <-time.After(backfillStopWithin):
		t.Fatalf("the backfill still waits on the stalled director %v after SIGTERM", backfillStopWithin)
	}

	warnings := strings.Join(logger.lines("warn"), "\n")
	if !strings.Contains(warnings, "were not recorded") {
		t.Errorf("expected a warning that the names were not recorded, got %q", warnings)
	}
}
