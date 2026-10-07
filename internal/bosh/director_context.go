package bosh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// DirectorCallError is the error a director call returns when its context
// ended before the director answered, and the error WaitForTask returns
// whenever it stops before its task finished. It names the operation, the
// director, and the deadline, and for a call that followed a director task,
// the task and what became of it. It unwraps to both its cause and the error
// the call itself saw, so errors.Is(err, context.DeadlineExceeded) and
// errors.Is(err, context.Canceled) work on it.
type DirectorCallError struct {
	// Operation is the Director method that was running.
	Operation string
	// Director is the director address the call went to.
	Director string
	// Deadline is the context's deadline, zero when it had none.
	Deadline time.Time
	// Cause is the context's error, context.DeadlineExceeded or
	// context.Canceled. For a request that got no answer within the request
	// timeout, it names that timeout instead, and for a wait that stopped on
	// a failed read of its task, it names that read.
	Cause error
	// Err is the error the call returned when the context ended.
	Err error
	// TaskID is the director task the call was following, or 0.
	TaskID int
	// Task is what became of that task when the call stopped.
	Task TaskOutcome
}

// TaskOutcome says what became of a director task when a call stopped
// following it.
type TaskOutcome int

const (
	// TaskNone means the call followed no task.
	TaskNone TaskOutcome = iota
	// TaskLeftRunning means Blacksmith stopped waiting and left the task
	// running on the director.
	TaskLeftRunning
	// TaskCancelRequested means Blacksmith asked the director to cancel the
	// task.
	TaskCancelRequested
	// TaskCancelFailed means Blacksmith tried to cancel the task and the
	// cancel failed, so the task may still be running.
	TaskCancelFailed
)

// String names the outcome for logs.
func (o TaskOutcome) String() string {
	switch o {
	case TaskNone:
		return "none"
	case TaskLeftRunning:
		return "left running"
	case TaskCancelRequested:
		return "cancel requested"
	case TaskCancelFailed:
		return "cancel failed"
	default:
		return fmt.Sprintf("TaskOutcome(%d)", int(o))
	}
}

// sentence says what became of task taskID, and where to follow it.
func (o TaskOutcome) sentence(taskID int) string {
	switch o {
	case TaskLeftRunning:
		return fmt.Sprintf("BOSH task %d is still running; Blacksmith stopped waiting and did not cancel it. Follow it at /b/tasks/%d. ",
			taskID, taskID)
	case TaskCancelRequested:
		return fmt.Sprintf("Blacksmith asked the director to cancel BOSH task %d. Check that it ended at /b/tasks/%d. ",
			taskID, taskID)
	case TaskCancelFailed:
		return fmt.Sprintf("Blacksmith tried to cancel BOSH task %d, and the cancel failed, so the task may still be running. "+
			"Check it at /b/tasks/%d, and cancel it there if it should stop. ", taskID, taskID)
	default:
		return ""
	}
}

func (e *DirectorCallError) Error() string {
	// A failed read is the director's own answer, which carries its own
	// advice, so the advice for a call that got no answer does not apply.
	var readFailed *taskReadError
	if errors.As(e.Cause, &readFailed) {
		return fmt.Sprintf("BOSH director call %s to %s stopped because %s. %sThe failed read's error was: %v",
			e.Operation, e.Director, readFailed.Error(), e.Task.sentence(e.TaskID), e.Err)
	}

	var why string

	var timeout *requestTimeoutError

	// A call that follows a task waits for the task to finish, and the
	// director answers each read of it along the way.
	before := "before the director answered"
	if e.TaskID > 0 {
		before = fmt.Sprintf("before BOSH task %d finished", e.TaskID)
	}

	switch {
	case errors.As(e.Cause, &timeout):
		why = e.Cause.Error()
	case errors.Is(e.Cause, context.DeadlineExceeded) && !e.Deadline.IsZero():
		why = "its deadline of " + e.Deadline.UTC().Format(time.RFC3339) + " passed " + before
	case errors.Is(e.Cause, context.DeadlineExceeded):
		why = "its deadline passed " + before
	default:
		why = "its caller cancelled it " + before
	}

	return fmt.Sprintf("BOSH director call %s to %s stopped because %s. %s"+
		"The likely causes are a hung or overloaded director, or a network partition between the broker and the director. "+
		"Check that the director answers bosh env, look at its running tasks with bosh tasks and at monit summary on the director, "+
		"and check the network path from the broker to %s. The call's own error was: %v",
		e.Operation, e.Director, why, e.Task.sentence(e.TaskID), e.Director, e.Err)
}

// taskReadError is the cause of a wait that stopped because a read of its
// task failed while the caller's context was still live.
type taskReadError struct {
	taskID int
	// lastState is the state the last good read saw, or empty when no read
	// succeeded.
	lastState string
}

func (e *taskReadError) Error() string {
	if e.lastState == "" {
		return fmt.Sprintf("a read of BOSH task %d failed before its state was read", e.taskID)
	}

	return fmt.Sprintf("a read of BOSH task %d failed while the task was %q", e.taskID, e.lastState)
}

func (e *DirectorCallError) Unwrap() []error {
	return []error{e.Cause, e.Err}
}

// explainContext replaces a failed call's error with a DirectorCallError
// when the call's context has ended, so the caller learns that the deadline
// or a cancellation stopped the call rather than reading whatever the HTTP
// layer said when its request was torn down. It does the same when the
// caller's context is still live but the director gave no answer within the
// bosh.request_timeout, so that the error names the setting that bounded it.
func (d *DirectorAdapter) explainContext(ctx context.Context, operation string, err *error) {
	if *err == nil {
		return
	}

	var already *DirectorCallError
	if errors.As(*err, &already) {
		return
	}

	if ctx.Err() != nil {
		deadline, _ := ctx.Deadline()

		*err = &DirectorCallError{
			Operation: operation,
			Director:  d.address,
			Deadline:  deadline,
			Cause:     ctx.Err(),
			Err:       *err,
		}

		return
	}

	var transport *boshapi.TransportError
	if errors.As(*err, &transport) && transport.Timeout {
		*err = &DirectorCallError{
			Operation: operation,
			Director:  d.address,
			Cause:     &requestTimeoutError{timeout: d.requestTimeout, attempts: transport.Attempts},
			Err:       *err,
		}
	}
}
