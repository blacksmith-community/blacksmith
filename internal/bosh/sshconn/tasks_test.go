package sshconn_test

import (
	"context"
	"testing"

	"blacksmith/internal/bosh/sshconn"
)

// The observer a context carries hears each task, and a context with none
// gives an observer that hears nothing and does not panic.
func TestTaskObserverTravelsOnTheContext(t *testing.T) {
	t.Parallel()

	var setup, cleanup int

	ctx := sshconn.WithTaskObserver(context.Background(), sshconn.TaskObserver{
		Setup:   func(id int) { setup = id },
		Cleanup: func(id int) { cleanup = id },
	})

	observer := sshconn.TaskObserverFrom(ctx)
	observer.SetupStarted(41)
	observer.CleanupStarted(42)

	if setup != 41 || cleanup != 42 {
		t.Errorf("the observer heard setup %d and cleanup %d, want 41 and 42", setup, cleanup)
	}

	observer.SetupStarted(0)
	observer.CleanupStarted(-1)

	if setup != 41 || cleanup != 42 {
		t.Errorf("a task ID that is not positive reached the observer: %d and %d", setup, cleanup)
	}

	none := sshconn.TaskObserverFrom(context.Background())
	none.SetupStarted(1)
	none.CleanupStarted(1)
}
