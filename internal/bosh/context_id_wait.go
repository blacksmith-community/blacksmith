package bosh

import (
	"context"
	"fmt"
	"time"
)

// LostAnswerSupersededError is what the waiters of a lost deploy answer get
// when a later deploy of the same deployment took over the lost deploy's
// slot. The director takes a lock for each deployment, so once it accepted
// the later deploy, the lost one is not running. It wraps
// ErrLostAnswerSuperseded.
type LostAnswerSupersededError struct {
	// Deployment is the deployment both deploys were for.
	Deployment string
	// At is when the later deploy took over the slot, which is just before
	// that deploy was sent.
	At time.Time
}

// Error names the later deploy and when it was sent.
func (e *LostAnswerSupersededError) Error() string {
	return fmt.Sprintf("a later deploy of %s, sent at %s, replaced the deploy whose answer was lost",
		e.Deployment, e.At.UTC().Format(time.RFC3339))
}

// Unwrap returns ErrLostAnswerSuperseded, so errors.Is matches it.
func (e *LostAnswerSupersededError) Unwrap() error {
	return ErrLostAnswerSuperseded
}

// AwaitTaskByContextID asks the director for the task with contextID
// every 30 seconds until it finds one, until a lookup that succeeded at
// or after since plus UnknownOutcomeGrace finds none, or until ctx
// ends. It returns the task, an error that wraps
// ErrNoTaskWithContextID, or the context's error. A failed lookup never
// counts toward the grace.
func AwaitTaskByContextID(ctx context.Context, director Director, contextID string, since time.Time) (*Task, error) {
	return awaitTaskByContextID(ctx, director, contextID, since, systemFollowClock{})
}

// awaitTaskByContextID is AwaitTaskByContextID on the clock given.
func awaitTaskByContextID(ctx context.Context, director Director, contextID string, since time.Time, clock followClock) (*Task, error) {
	graceEnds := since.Add(UnknownOutcomeGrace)

	for {
		now := clock.Now()
		task, err := director.FindTaskByContextID(ctx, contextID)

		if ctx.Err() != nil {
			return nil, fmt.Errorf("stopped looking for the BOSH task with context ID %s: %w", contextID, ctx.Err())
		}

		if err == nil && task != nil && task.ID > 0 {
			return task, nil
		}

		if err == nil && !now.Before(graceEnds) {
			return nil, fmt.Errorf("%w %s, %v after the answer was lost", ErrNoTaskWithContextID, contextID, UnknownOutcomeGrace)
		}

		if clock.Sleep(ctx, followLostAnswerInterval) != nil {
			return nil, fmt.Errorf("stopped looking for the BOSH task with context ID %s: %w", contextID, ctx.Err())
		}
	}
}
