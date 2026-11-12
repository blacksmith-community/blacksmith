package sshconn

import "context"

// TaskObserver hears the IDs of the BOSH tasks that set up and remove the
// user of an SSH session. A caller that opens a session puts one on the
// context it opens the session with, and the director calls Setup as soon
// as it accepts the setup, before anything waits on that task, and Cleanup
// as soon as it accepts the user's cleanup, however the session ends. An
// observer that is not set hears nothing. Each function may be called from
// another goroutine, and after the call that opened the session returned.
type TaskObserver struct {
	Setup   func(taskID int)
	Cleanup func(taskID int)
}

// taskObserverKey is the context key of the TaskObserver.
type taskObserverKey struct{}

// WithTaskObserver returns a context that carries the observer.
func WithTaskObserver(ctx context.Context, observer TaskObserver) context.Context {
	return context.WithValue(ctx, taskObserverKey{}, observer)
}

// TaskObserverFrom returns the observer the context carries, or one that
// hears nothing.
func TaskObserverFrom(ctx context.Context) TaskObserver {
	observer, _ := ctx.Value(taskObserverKey{}).(TaskObserver)

	return observer
}

// SetupStarted tells the observer the director accepted the setup as
// task taskID. A task ID of zero or less tells it nothing.
func (o TaskObserver) SetupStarted(taskID int) {
	if o.Setup != nil && taskID > 0 {
		o.Setup(taskID)
	}
}

// CleanupStarted tells the observer the director accepted the cleanup as
// task taskID. A task ID of zero or less tells it nothing.
func (o TaskObserver) CleanupStarted(taskID int) {
	if o.Cleanup != nil && taskID > 0 {
		o.Cleanup(taskID)
	}
}
