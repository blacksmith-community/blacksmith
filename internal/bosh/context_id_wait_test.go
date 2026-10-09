package bosh_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// The task the director lists for firstContextID in these tests.
const awaitTaskID = 700

// awaitInBackground runs AwaitTaskByContextID for firstContextID on the
// fake clock, on its own goroutine.
func awaitInBackground(ctx context.Context, base *fakeBatchBase, since time.Time, clock *fakeBatchClock) <-chan waitResult {
	return waitInBackground(func() (*bosh.Task, error) {
		return bosh.AwaitTaskByContextIDWithClock(ctx, base, firstContextID, since, clock)
	})
}

// expectNoResult fails the test if the wait has returned.
func expectNoResult(t *testing.T, result <-chan waitResult) {
	t.Helper()

	select {
	case got := <-result:
		t.Fatalf("expected the wait to keep looking, but it returned %+v and %v", got.task, got.err)
	case <-time.After(20 * time.Millisecond):
	}
}

// waitLookups waits until the fake has answered the number of lookups
// given.
func waitLookups(t *testing.T, base *fakeBatchBase, want int) {
	t.Helper()

	waitUntil(t, "the lookups reach the count", func() bool { return base.lookups() >= want })
}

func TestAwaitTaskByContextIDReturnsTheTaskOnceItTurnsUp(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	clock := newFakeBatchClock()
	result := awaitInBackground(t.Context(), base, clock.Now(), clock)

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	base.addTask(awaitTaskID, firstDeployment, bosh.DeployTaskDescription)
	base.set(func(f *fakeBatchBase) { f.byContextID[firstContextID] = awaitTaskID })
	clock.advance(bosh.BatchLostAnswerInterval)

	got := receive(t, result)
	if got.err != nil || got.task == nil || got.task.ID != awaitTaskID {
		t.Fatalf("expected BOSH task %d, got %+v and %v", awaitTaskID, got.task, got.err)
	}

	if lookups := base.lookups(); lookups != 2 {
		t.Errorf("expected two lookups, got %d", lookups)
	}
}

func TestAwaitTaskByContextIDKeepsLookingBeforeTheGrace(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	clock := newFakeBatchClock()
	result := awaitInBackground(t.Context(), base, clock.Now(), clock)

	// Every lookup before the grace has passed finds nothing and does not
	// end the wait.
	lookupsBeforeTheGrace := int(bosh.UnknownOutcomeGrace / bosh.BatchLostAnswerInterval)

	for lookup := 1; lookup <= lookupsBeforeTheGrace; lookup++ {
		waitLookups(t, base, lookup)
		clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
		expectNoResult(t, result)
		clock.advance(bosh.BatchLostAnswerInterval)
	}

	// The clock now stands at the grace, so the next empty lookup ends it.
	got := receive(t, result)
	if !errors.Is(got.err, bosh.ErrNoTaskWithContextID) {
		t.Fatalf("expected ErrNoTaskWithContextID once the grace passed, got %+v and %v", got.task, got.err)
	}

	if lookups := base.lookups(); lookups != lookupsBeforeTheGrace+1 {
		t.Errorf("expected %d lookups, got %d", lookupsBeforeTheGrace+1, lookups)
	}
}

func TestAwaitTaskByContextIDReturnsNoTaskAfterAnEmptyLookupAtTheGrace(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	clock := newFakeBatchClock()

	// The answer was lost exactly one grace ago.
	result := awaitInBackground(t.Context(), base, clock.Now().Add(-bosh.UnknownOutcomeGrace), clock)

	got := receive(t, result)
	if !errors.Is(got.err, bosh.ErrNoTaskWithContextID) || got.task != nil {
		t.Fatalf("expected ErrNoTaskWithContextID and no task, got %+v and %v", got.task, got.err)
	}

	if !strings.Contains(got.err.Error(), firstContextID) {
		t.Errorf("expected the error to name context ID %s, got %v", firstContextID, got.err)
	}

	if lookups := base.lookups(); lookups != 1 {
		t.Errorf("expected one lookup, got %d", lookups)
	}
}

func TestAwaitTaskByContextIDNeverExpiresTheGraceWhileLookupsFail(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	base.contextErr = errBatchTestRejected
	clock := newFakeBatchClock()

	// The grace ran out long ago, but no lookup has succeeded.
	result := awaitInBackground(t.Context(), base, clock.Now().Add(-2*time.Hour), clock)

	for lookup := 1; lookup <= 5; lookup++ {
		waitLookups(t, base, lookup)
		clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
		expectNoResult(t, result)
		clock.advance(bosh.BatchLostAnswerInterval)
	}

	waitLookups(t, base, 6)
	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
	expectNoResult(t, result)

	base.set(func(f *fakeBatchBase) { f.contextErr = nil })
	clock.advance(bosh.BatchLostAnswerInterval)

	got := receive(t, result)
	if !errors.Is(got.err, bosh.ErrNoTaskWithContextID) {
		t.Fatalf("expected the first lookup that succeeded to end the wait with ErrNoTaskWithContextID, got %+v and %v", got.task, got.err)
	}
}

func TestAwaitTaskByContextIDEndsWithTheContext(t *testing.T) {
	t.Parallel()

	base := newFakeBatchBase()
	clock := newFakeBatchClock()
	ctx, cancel := context.WithCancel(t.Context())
	result := awaitInBackground(ctx, base, clock.Now(), clock)

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
	cancel()

	got := receive(t, result)
	if !errors.Is(got.err, context.Canceled) || got.task != nil {
		t.Fatalf("expected context.Canceled and no task, got %+v and %v", got.task, got.err)
	}

	if errors.Is(got.err, bosh.ErrNoTaskWithContextID) {
		t.Errorf("expected an ended context never to read as no task, got %v", got.err)
	}
}
