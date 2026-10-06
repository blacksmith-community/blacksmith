package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/fivetwenty-io/boshapi/internal/constants"
	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type tasksClient struct {
	httpClient *http.Client
}

func NewTasksClient(httpClient *http.Client) boshapi.TasksClient {
	return &tasksClient{httpClient: httpClient}
}

func (c *tasksClient) List(ctx context.Context, opts *boshapi.TaskListOptions) ([]boshapi.Task, error) {
	var query url.Values
	if opts != nil {
		query = opts.ToValues()
	}
	resp, err := c.httpClient.Get(ctx, "/tasks", query)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	var tasks []boshapi.Task
	if err := json.Unmarshal(resp.Body, &tasks); err != nil {
		return nil, fmt.Errorf("parsing tasks: %w", err)
	}

	return tasks, nil
}

func (c *tasksClient) Get(ctx context.Context, id int) (*boshapi.Task, error) {
	task, err := c.get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("getting task %d: %w", id, err)
	}

	return task, nil
}

// get reads task id from the director and returns the HTTP client's error
// as it is, so a caller that names the task itself does not name it twice.
func (c *tasksClient) get(ctx context.Context, id int) (*boshapi.Task, error) {
	path := "/tasks/" + strconv.Itoa(id)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the task
	}

	var task boshapi.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		return nil, fmt.Errorf("parsing the director's answer to GET %s: %w", path, err)
	}

	return &task, nil
}

func (c *tasksClient) GetOutput(ctx context.Context, id int, opts *boshapi.TaskOutputOptions) (string, error) {
	path := "/tasks/" + strconv.Itoa(id) + "/output"
	var query url.Values
	if opts != nil {
		query = opts.ToValues()
	}
	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return "", fmt.Errorf("getting task output: %w", err)
	}

	return string(resp.Body), nil
}

// Cancel asks the director to cancel a queued or processing task. The
// director serves the cancel on DELETE /task/{id}, the singular path of its
// TaskController, and answers 204 when it accepts it, and any 3xx answer is returned as a
// *boshapi.RedirectError, because only a 2xx confirms the cancel. When the task is no
// longer queued or processing, the director answers 400 with the body
// "Cannot cancel task N: invalid state (X)", which Cancel returns as a
// *boshapi.TaskStateError. Any other failure comes back as the HTTP client's
// error.
func (c *tasksClient) Cancel(ctx context.Context, id int) error {
	path := "/task/" + strconv.Itoa(id)

	_, err := c.httpClient.Do(ctx, &http.Request{Method: nethttp.MethodDelete, Path: path, NoTaskRedirect: true})
	if err != nil {
		if stateErr := taskStateError(id, err); stateErr != nil {
			return stateErr
		}

		return fmt.Errorf("cancelling task %d: %w", id, err)
	}

	return nil
}

// invalidStateAnswer matches the whole body of the director's 400 answer to a
// cancel of a task that is no longer queued or processing.
var invalidStateAnswer = regexp.MustCompile(`^Cannot cancel task ([1-9][0-9]*): invalid state \(([a-z_]+)\)$`)

// taskStateError returns a *boshapi.TaskStateError when err is the
// director's 400 answer that task taskID is in a state that cannot be
// cancelled, and nil for any other error.
func taskStateError(taskID int, err error) *boshapi.TaskStateError {
	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != nethttp.StatusBadRequest || apiErr.Code != 0 {
		return nil
	}

	match := invalidStateAnswer.FindStringSubmatch(apiErr.Description)
	if match == nil || match[1] != strconv.Itoa(taskID) {
		return nil
	}

	return &boshapi.TaskStateError{TaskID: taskID, State: match[2]}
}

// PollUntilComplete checks the task right away and then once per interval,
// which defaults to 2 seconds, until the task reaches a terminal state or the
// context ends. A zero Timeout leaves the context as the only bound on the
// wait, and a positive Timeout ends the wait after that long.
//
// A done task comes back with a nil error. A task that ends in error,
// cancelled, or timeout comes back together with a *boshapi.TaskError that
// holds it. Queued, processing, cancelling, and any state the client does not
// know keep the poll going.
//
// When the context ends, or a check fails after the HTTP client's retries,
// PollUntilComplete returns a *boshapi.TaskWaitError with the task ID and the
// last state it read. Polling never cancels the director task, so the task
// may still be running.
func (c *tasksClient) PollUntilComplete(ctx context.Context, taskID int, opts *boshapi.PollOptions) (*boshapi.Task, error) {
	interval := constants.DefaultTaskPollInterval

	var timeout time.Duration

	if opts != nil {
		if opts.Interval > 0 {
			interval = opts.Interval
		}

		if opts.Timeout > 0 {
			timeout = opts.Timeout
		}
	}

	pollCtx := ctx

	if timeout > 0 {
		var cancel context.CancelFunc

		pollCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var (
		lastState string
		timer     *time.Timer
	)

	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		if pollCtx.Err() != nil {
			return nil, waitError(ctx, pollCtx, timeout, taskID, lastState, nil)
		}

		task, err := c.get(pollCtx, taskID)
		if err != nil {
			return nil, waitError(ctx, pollCtx, timeout, taskID, lastState, err)
		}

		lastState = task.State

		switch task.State {
		case constants.TaskStateDone:
			return task, nil
		case constants.TaskStateError, constants.TaskStateCancelled, constants.TaskStateTimeout:
			return task, &boshapi.TaskError{Task: task}
		}

		if timer == nil {
			timer = time.NewTimer(interval)
		} else {
			timer.Reset(interval)
		}

		select {
		case <-pollCtx.Done():
			return nil, waitError(ctx, pollCtx, timeout, taskID, lastState, nil)
		case <-timer.C:
		}
	}
}

// waitError builds the *boshapi.TaskWaitError for a wait that ended before
// the task did. When a context ended, Err is the caller's context error, or,
// when only the poll Timeout passed, an error that names the timeout and
// wraps context.DeadlineExceeded. Otherwise Err is the failed check's error.
func waitError(ctx, pollCtx context.Context, timeout time.Duration, taskID int, lastState string, pollErr error) error {
	err := pollErr

	switch {
	case ctx.Err() != nil:
		err = ctx.Err()
	case pollCtx.Err() != nil:
		err = fmt.Errorf("the poll timeout of %s passed: %w", timeout, context.DeadlineExceeded)
	}

	return &boshapi.TaskWaitError{TaskID: taskID, LastState: lastState, Err: err}
}
