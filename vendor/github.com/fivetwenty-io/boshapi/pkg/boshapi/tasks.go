package boshapi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Task represents a BOSH Director task.
type Task struct {
	ID             int    `json:"id"`
	State          string `json:"state"`
	Description    string `json:"description"`
	Timestamp      *int64 `json:"timestamp"`
	StartedAt      *int64 `json:"started_at"`
	Result         string `json:"result"`
	User           string `json:"user"`
	DeploymentName string `json:"deployment"`
	ContextID      string `json:"context_id"`
}

// TaskListOptions and its ToValues method are defined in query.go.

// TaskOutputOptions represents options for getting task output.
type TaskOutputOptions struct {
	Type string // "debug", "event", "result", "cpi", "none"
}

// ToValues converts TaskOutputOptions to url.Values.
func (o *TaskOutputOptions) ToValues() url.Values {
	if o == nil {
		return nil
	}

	values := url.Values{}

	if o.Type != "" {
		values.Set("type", o.Type)
	}

	return values
}

// PollOptions represents options for polling a task until completion.
type PollOptions struct {
	// Interval is the wait between checks of the task. The first check runs
	// right away. Zero means 2 seconds.
	Interval time.Duration
	// Timeout ends the wait after this long, with a *TaskWaitError that
	// wraps context.DeadlineExceeded. Zero means only the context bounds the
	// wait.
	Timeout time.Duration
}

// TaskError reports a task that reached a terminal state other than done:
// error, cancelled, or timeout. PollUntilComplete returns it together with
// the final task, and Task holds that same task, so its State says which of
// the three it was.
type TaskError struct {
	Task *Task
}

// Error implements the error interface.
func (e *TaskError) Error() string {
	if e.Task == nil {
		return "BOSH task ended in a state other than done, and no task record came with the error. " +
			"Check the director's recent tasks with GET /tasks."
	}

	var msg strings.Builder

	fmt.Fprintf(&msg, "BOSH task %d", e.Task.ID)

	if e.Task.Description != "" {
		fmt.Fprintf(&msg, " (%s)", e.Task.Description)
	}

	if e.Task.DeploymentName != "" {
		fmt.Fprintf(&msg, " on deployment %s", e.Task.DeploymentName)
	}

	fmt.Fprintf(&msg, " ended in state %q", e.Task.State)

	if e.Task.Result != "" {
		fmt.Fprintf(&msg, " with result %q", e.Task.Result)
	} else {
		msg.WriteString(" with no result text")
	}

	msg.WriteString(". ")

	switch e.Task.State {
	case "cancelled":
		fmt.Fprintf(&msg, "The task was cancelled before it finished. Check the director's events for who "+
			"cancelled it, and the task's event output with GET /tasks/%d/output?type=event.", e.Task.ID)
	case "timeout":
		fmt.Fprintf(&msg, "The director gave up on the task when it ran too long. Check the task's debug output "+
			"with GET /tasks/%d/output?type=debug, and whether the director's workers or the CPI are slow or stuck.",
			e.Task.ID)
	default:
		fmt.Fprintf(&msg, "Check the task's debug output with GET /tasks/%d/output?type=debug for the cause.",
			e.Task.ID)
	}

	return msg.String()
}

// TaskWaitError reports that PollUntilComplete stopped waiting for a task
// before the task reached a terminal state, because the context ended, the
// poll Timeout passed, or a check of the task failed. Polling never cancels
// the director task, so the task may still be running. TaskWaitError unwraps
// to Err, so errors.Is(err, context.DeadlineExceeded) and errors.As to a
// *TransportError or *APIError see the cause.
type TaskWaitError struct {
	// TaskID is the ID of the task that was being waited on.
	TaskID int
	// LastState is the state the last successful check read, or empty when
	// no check succeeded.
	LastState string
	// Err is the context error when the caller's context ended, an error
	// that wraps context.DeadlineExceeded when the poll Timeout passed, or
	// the failed check's error.
	Err error
}

// Error implements the error interface.
func (e *TaskWaitError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "Stopped waiting for BOSH task %d", e.TaskID)

	if e.LastState != "" {
		fmt.Fprintf(&msg, " while it was %q", e.LastState)
	} else {
		msg.WriteString(" before its state was read")
	}

	switch {
	case errors.Is(e.Err, context.DeadlineExceeded):
		fmt.Fprintf(&msg, ", because the wait's deadline passed (%v). ", e.Err)
	case errors.Is(e.Err, context.Canceled):
		msg.WriteString(", because the caller's context was cancelled. ")
	default:
		fmt.Fprintf(&msg, ", because checking the task failed: %s. ", strings.TrimSuffix(fmt.Sprint(e.Err), "."))
	}

	fmt.Fprintf(&msg, "The task was not cancelled and may still be running on the director. "+
		"Check its state with GET /tasks/%d, and cancel it with DELETE /task/%d if it should stop.", e.TaskID, e.TaskID)

	if errors.Is(e.Err, context.DeadlineExceeded) {
		msg.WriteString(" If the deadline is too short for this task, wait again with a longer one.")
	}

	return msg.String()
}

// Unwrap returns Err.
func (e *TaskWaitError) Unwrap() error {
	return e.Err
}

// TaskStateError reports that the director refused to cancel a task because
// the task is no longer queued or processing. The director answers such a
// cancel with HTTP 400 and the body "Cannot cancel task N: invalid state (X)".
// A caller treats it as "the task already finished, or a cancel already
// landed", not as a failed request.
type TaskStateError struct {
	// TaskID is the ID of the task the cancel named.
	TaskID int
	// State is the task's state as the director reported it, such as done,
	// error, cancelled, timeout, or cancelling.
	State string
}

// Error implements the error interface.
func (e *TaskStateError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "The BOSH director did not cancel task %d because the task is in state %q "+
		"(it answered DELETE /task/%d with HTTP 400: \"Cannot cancel task %d: invalid state (%s)\"). ",
		e.TaskID, e.State, e.TaskID, e.TaskID, e.State)

	if e.State == "cancelling" {
		msg.WriteString("The task is already being cancelled, so an earlier cancel landed and there is nothing more to send. ")
	} else {
		msg.WriteString("Only a queued or processing task can be cancelled, and this one has already finished. ")
	}

	fmt.Fprintf(&msg, "Check the task's state and result with GET /tasks/%d.", e.TaskID)

	return msg.String()
}
