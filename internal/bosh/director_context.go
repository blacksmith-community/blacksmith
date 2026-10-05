package bosh

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DirectorCallError is the error a director call returns when its context
// ended before the director answered. It names the operation, the director,
// and the deadline, and it unwraps to both the context's error and the
// error the call itself saw, so errors.Is(err, context.DeadlineExceeded)
// and errors.Is(err, context.Canceled) work on it.
type DirectorCallError struct {
	// Operation is the Director method that was running.
	Operation string
	// Director is the director address the call went to.
	Director string
	// Deadline is the context's deadline, zero when it had none.
	Deadline time.Time
	// Cause is the context's error, context.DeadlineExceeded or
	// context.Canceled.
	Cause error
	// Err is the error the call returned when the context ended.
	Err error
}

func (e *DirectorCallError) Error() string {
	var why string

	switch {
	case errors.Is(e.Cause, context.DeadlineExceeded) && !e.Deadline.IsZero():
		why = "its deadline of " + e.Deadline.UTC().Format(time.RFC3339) + " passed before the director answered"
	case errors.Is(e.Cause, context.DeadlineExceeded):
		why = "its deadline passed before the director answered"
	default:
		why = "its caller cancelled it before the director answered"
	}

	return fmt.Sprintf("BOSH director call %s to %s stopped because %s. "+
		"The likely causes are a hung or overloaded director, or a network partition between the broker and the director. "+
		"Check that the director answers bosh env, look at its running tasks with bosh tasks and at monit summary on the director, "+
		"and check the network path from the broker to %s. The call's own error was: %v",
		e.Operation, e.Director, why, e.Director, e.Err)
}

func (e *DirectorCallError) Unwrap() []error {
	return []error{e.Cause, e.Err}
}

// explainContext replaces a failed call's error with a DirectorCallError
// when the call's context has ended, so the caller learns that the deadline
// or a cancellation stopped the call rather than reading whatever the HTTP
// layer said when its request was torn down.
func (d *DirectorAdapter) explainContext(ctx context.Context, operation string, err *error) {
	if *err == nil || ctx.Err() == nil {
		return
	}

	var already *DirectorCallError
	if errors.As(*err, &already) {
		return
	}

	deadline, _ := ctx.Deadline()

	*err = &DirectorCallError{
		Operation: operation,
		Director:  d.address,
		Deadline:  deadline,
		Cause:     ctx.Err(),
		Err:       *err,
	}
}
