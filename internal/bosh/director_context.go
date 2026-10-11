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
// errors.Is(err, context.Canceled) work on it. A lost answer to a request
// that starts a read-only task is the exception. It unwraps to its cause
// only, so neither Blacksmith's ErrOutcomeUnknown nor boshapi's
// IsOutcomeUnknown reads it as a change whose outcome is unknown, and a
// request timeout does not read as the caller's own deadline.
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
	// a failed read of its task, it names that read. For a request that
	// starts a read-only task and whose answer was lost, it names that
	// request, and the request timeout when that is why.
	Cause error
	// Err is the error the call returned when the context ended.
	Err error
	// TaskID is the director task the call was following, or 0.
	TaskID int
	// Task is what became of that task when the call stopped.
	Task TaskOutcome
	// TaskState is the state the director gave for the task when it
	// refused Blacksmith's cancel. With TaskCancelRefused it is the state
	// the task had already ended in, and with TaskCancelRequested it is
	// cancelling or cancelled, because a cancel had already landed. It is
	// empty when the state was not learned.
	TaskState string
	// CancelErr is the error of a cancel that failed. It is set only with
	// TaskCancelFailed. The message prints it, and Unwrap does not return
	// it, so errors.Is answers for the call's own cause.
	CancelErr error
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
	// TaskCancelRefused means the task had already ended when Blacksmith
	// asked the director to cancel it, so there was nothing to cancel.
	TaskCancelRefused
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
	case TaskCancelRefused:
		return "cancel refused"
	default:
		return fmt.Sprintf("TaskOutcome(%d)", int(o))
	}
}

func (e *DirectorCallError) Error() string {
	// A lost answer to a request that starts a read-only task is not a
	// call that stopped waiting, so it gets its own message.
	var lost *answerLostError
	if errors.As(e.Cause, &lost) {
		return fmt.Sprintf("BOSH director call %s to %s did not get a usable answer, because %s. "+
			"The director may have started a read-only task whose ID Blacksmith never learned, so Blacksmith cannot cancel it, "+
			"and such a task ends on its own. Blacksmith did not resend the request, and the caller can ask again. "+
			"The request's own failure was: %s",
			e.Operation, e.Director, lost.Error(), answerLostDetail(e.Err))
	}

	// A failed read is the director's own answer, which carries its own
	// advice, so the advice for a call that got no answer does not apply.
	var readFailed *taskReadError
	if errors.As(e.Cause, &readFailed) {
		return fmt.Sprintf("BOSH director call %s to %s stopped because %s. %sThe failed read's error was: %v",
			e.Operation, e.Director, readFailed.Error(), e.taskSentence(), e.Err)
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
		e.Operation, e.Director, why, e.taskSentence(), e.Director, e.Err)
}

// answerLostDetail is the part of a lost answer's error that says what
// failed. boshapi's errors for a request that starts a task also tell the
// reader to look for that task before sending the request again, which
// does not apply to a read-only task. So a transport error keeps only its
// underlying failure, and a redirect or an answer that is not a redirect
// keeps only what the director answered.
func answerLostDetail(err error) string {
	var (
		transport *boshapi.TransportError
		redirect  *boshapi.RedirectError
		answer    *boshapi.TaskAnswerError
	)

	switch {
	case errors.As(err, &transport) && transport.Err != nil:
		return transport.Err.Error()
	case errors.As(err, &redirect):
		if redirect.Location == "" {
			return fmt.Sprintf("the director answered %s %s with HTTP %d and no Location header", redirect.Method, redirect.Path, redirect.Status)
		}

		return fmt.Sprintf("the director answered %s %s with HTTP %d and a redirect to %q, which names no task",
			redirect.Method, redirect.Path, redirect.Status, redirect.Location)
	case errors.As(err, &answer):
		if answer.Body == "" {
			return fmt.Sprintf("the director answered %s %s with HTTP %d and an empty body, not a redirect to a task",
				answer.Method, answer.Path, answer.Status)
		}

		return fmt.Sprintf("the director answered %s %s with HTTP %d and the body %q, not a redirect to a task",
			answer.Method, answer.Path, answer.Status, answer.Body)
	case err == nil:
		return "no error was recorded"
	default:
		return err.Error()
	}
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

// answerLostError is the cause of a call whose request starts a read-only
// director task and whose answer was lost, so the call never learned the
// task's ID. It wraps nothing.
type answerLostError struct {
	// Method is the request's HTTP method.
	Method string
	// Path is the request's path and query.
	Path string
	// Timeout is the request timeout that ended the request, or nil when
	// something else lost the answer.
	Timeout *requestTimeoutError
}

func (e *answerLostError) Error() string {
	if e.Timeout != nil {
		return fmt.Sprintf("%s %s had %s", e.Method, e.Path, e.Timeout.Error())
	}

	return fmt.Sprintf("the answer to %s %s was lost", e.Method, e.Path)
}

// Unwrap returns the cause and the call's own error. For a lost answer it
// returns the cause only, because the call's own error is boshapi's
// outcome-unknown error for a change, and a read-only task is no change.
func (e *DirectorCallError) Unwrap() []error {
	var lost *answerLostError
	if errors.As(e.Cause, &lost) {
		return []error{e.Cause}
	}

	return []error{e.Cause, e.Err}
}

// taskSentence says what became of the task the call followed, and where to
// follow it.
func (e *DirectorCallError) taskSentence() string {
	taskID := e.TaskID

	switch e.Task {
	case TaskNone:
		return ""
	case TaskLeftRunning:
		return fmt.Sprintf("BOSH task %d is still running; Blacksmith stopped waiting and did not cancel it. Follow it at /b/tasks/%d. ",
			taskID, taskID)
	case TaskCancelRequested:
		return e.cancelRequestedSentence()
	case TaskCancelRefused:
		if e.TaskState == "" {
			return fmt.Sprintf("BOSH task %d had already ended when Blacksmith asked the director to cancel it, so there was nothing to cancel. "+
				"Its result is at /b/tasks/%d. ", taskID, taskID)
		}

		return fmt.Sprintf("BOSH task %d had already ended in state %s when Blacksmith asked the director to cancel it, so there was nothing to cancel. "+
			"Its result is at /b/tasks/%d. ", taskID, e.TaskState, taskID)
	case TaskCancelFailed:
		if e.CancelErr != nil {
			return fmt.Sprintf("Blacksmith tried to cancel BOSH task %d, and the cancel failed (%v), so the task may still be running. "+
				"Check it at /b/tasks/%d, and cancel it there if it should stop. ", taskID, e.CancelErr, taskID)
		}

		return fmt.Sprintf("Blacksmith tried to cancel BOSH task %d, and the cancel failed, so the task may still be running. "+
			"Check it at /b/tasks/%d, and cancel it there if it should stop. ", taskID, taskID)
	default:
		return ""
	}
}

// cancelRequestedSentence says that Blacksmith asked the director to cancel
// the task, or that the director refused because a cancel had already
// landed, naming the state it gave.
func (e *DirectorCallError) cancelRequestedSentence() string {
	taskID := e.TaskID

	switch e.TaskState {
	case taskStateCancelled:
		return fmt.Sprintf("BOSH task %d had already been cancelled when Blacksmith asked the director to cancel it, so it has stopped. "+
			"Its result is at /b/tasks/%d. ", taskID, taskID)
	case "":
		// The director cancels a queued task at once, but a task it has
		// started stops only at its next checkpoint, and a read-only task
		// has none.
		return fmt.Sprintf("Blacksmith asked the director to cancel BOSH task %d, which stops the task if it had not started yet; "+
			"a task the director had already started may run to its end. Check whether it ended at /b/tasks/%d. ",
			taskID, taskID)
	default:
		return fmt.Sprintf("BOSH task %d was already %s when Blacksmith asked the director to cancel it. "+
			"A task the director had already started may run to its end, so check whether it ended at /b/tasks/%d. ",
			taskID, e.TaskState, taskID)
	}
}

// explainContext replaces a failed call's error with a DirectorCallError
// when the call's context has ended, so the caller learns that the deadline
// or a cancellation stopped the call rather than reading whatever the HTTP
// layer said when its request was torn down. It does the same when the
// caller's context is still live but the director gave no answer within the
// bosh.request_timeout, so that the error names the setting that bounded it.
// An error about a director task that had already ended, whether the task
// failed or its result did not parse, is the director's answer, so it is
// left as it is.
func (d *DirectorAdapter) explainContext(ctx context.Context, operation string, err *error) {
	if *err == nil {
		return
	}

	var (
		already  *DirectorCallError
		failed   *TaskFailedError
		unparsed *taskResultError
	)

	if errors.As(*err, &already) || errors.As(*err, &failed) || errors.As(*err, &unparsed) {
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
