package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// sweepWait bounds each wait in the sweep tests.
const sweepWait = 5 * time.Second

// countingSweeper counts its sweeps and reports each one on swept. A sweep
// fails with err, or panics with panicWith, when either is set.
type countingSweeper struct {
	mu        sync.Mutex
	count     int
	err       error
	panicWith string
	swept     chan int
}

func newCountingSweeper() *countingSweeper {
	return &countingSweeper{swept: make(chan int, 16)}
}

func (s *countingSweeper) SweepSSHCleanups(_ context.Context) error {
	s.mu.Lock()
	s.count++
	count, err, panicWith := s.count, s.err, s.panicWith
	s.mu.Unlock()

	s.swept <- count

	if panicWith != "" {
		panic(panicWith)
	}

	return err
}

// waitForSweep waits for sweep number want.
func (s *countingSweeper) waitForSweep(t *testing.T, want int) {
	t.Helper()

	select {
	case got := <-s.swept:
		if got != want {
			t.Fatalf("sweep %d ran, want sweep %d", got, want)
		}
	case <-time.After(sweepWait):
		t.Fatalf("sweep %d did not run within %s", want, sweepWait)
	}
}

// noSweep fails the test when a sweep runs within a short while.
func (s *countingSweeper) noSweep(t *testing.T, after string) {
	t.Helper()

	select {
	case got := <-s.swept:
		t.Fatalf("sweep %d ran %s, want none", got, after)
	case <-time.After(50 * time.Millisecond):
	}
}

// startSweeps runs the sweep loop against sweeper and a tick channel the
// test drives, and returns the channel, the log, and a stop that ends the
// service context and waits for the loop to return.
func startSweeps(t *testing.T, sweeper *countingSweeper) (chan time.Time, *shutdownLog, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	logger := newShutdownLog()
	done := make(chan struct{})

	go func() {
		defer close(done)

		runSSHCleanupSweeps(ctx, sweeper, ticks, logger)
	}()

	stop := func() {
		cancel()

		select {
		case <-done:
		case <-time.After(sweepWait):
			t.Fatalf("the sweep loop did not return within %s of the service context ending", sweepWait)
		}
	}

	t.Cleanup(stop)

	return ticks, logger, stop
}

// tick sends one tick, and fails the test when the loop does not take it.
func tick(t *testing.T, ticks chan time.Time) {
	t.Helper()

	select {
	case ticks <- time.Now():
	case <-time.After(sweepWait):
		t.Fatalf("the sweep loop did not take a tick within %s", sweepWait)
	}
}

// The broker sweeps once at boot and once on every tick, and stops when
// the service context ends.
func TestSSHSweepRunsAtBootAndOnTheTimer(t *testing.T) {
	t.Parallel()

	sweeper := newCountingSweeper()
	ticks, _, stop := startSweeps(t, sweeper)

	sweeper.waitForSweep(t, 1)
	sweeper.noSweep(t, "before the first tick")

	tick(t, ticks)
	sweeper.waitForSweep(t, 2)

	tick(t, ticks)
	sweeper.waitForSweep(t, 3)

	stop()
	sweeper.noSweep(t, "after the service context ended")
}

// A sweep that fails or panics is logged, and the next tick sweeps again.
func TestSSHSweepFailureIsLoggedAndTheTimerKeepsRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*countingSweeper)
		want  string
	}{
		{"an error", func(s *countingSweeper) { s.err = errors.New("vault is sealed") }, "vault is sealed"}, //nolint:err113 // a test's own failure
		{"a panic", func(s *countingSweeper) { s.panicWith = "sweep blew up" }, "sweep blew up"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			sweeper := newCountingSweeper()
			test.setup(sweeper)

			ticks, logger, _ := startSweeps(t, sweeper)

			sweeper.waitForSweep(t, 1)
			tick(t, ticks)
			sweeper.waitForSweep(t, 2)

			deadline := time.Now().Add(sweepWait)
			for strings.Count(logger.output(), test.want) < 2 {
				if time.Now().After(deadline) {
					t.Fatalf("the two failed sweeps were not both logged with %q: %s", test.want, logger.output())
				}

				time.Sleep(time.Millisecond)
			}
		})
	}
}
