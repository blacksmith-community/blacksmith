package bosh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// cancelTimeout bounds the cancel that waitThenCancel sends once a task's
// wait has stopped, for a read-only task and for an SSH setup task alike,
// unless the caller gives a bound of its own.
const cancelTimeout = 10 * time.Second

// deadlineCancelTimeout bounds the cancel of a read-only task whose wait
// runs under a request's deadline, as a log fetch and a VM listing for the
// logs endpoint do. That deadline falls 10 seconds before the server's
// write timeout, so the cancel must leave most of those seconds for writing
// the response.
const deadlineCancelTimeout = 3 * time.Second

// ErrTaskFailed is the sentinel a TaskFailedError wraps.
var ErrTaskFailed = errors.New("the BOSH task did not finish successfully")

// TaskFailedError is the error a call returns when the director task it
// waited for ended in a state other than done.
type TaskFailedError struct {
	// Operation is the Director method that was running.
	Operation string
	// Director is the director address the call went to.
	Director string
	// Deployment is the deployment the task was for.
	Deployment string
	// TaskID is the director task that ended.
	TaskID int
	// State is the state the task ended in.
	State string
	// Result is the director's result text for the task.
	Result string
	// SSHUser is the throwaway user an SSH setup task may have created,
	// and SSHInstance the instance it is on. Both are empty for every
	// other task.
	SSHUser     string
	SSHInstance string
}

func (e *TaskFailedError) Error() string {
	result := e.Result
	if result == "" {
		result = "the director gave no result text"
	}

	message := fmt.Sprintf("%s for deployment %s on BOSH director %s failed, because BOSH task %d ended in state %s: %s. "+
		"Check the task at /b/tasks/%d",
		e.Operation, e.Deployment, e.Director, e.TaskID, e.State, result, e.TaskID)

	return withSSHUser(message, e.SSHUser, e.SSHInstance)
}

// Unwrap returns ErrTaskFailed.
func (e *TaskFailedError) Unwrap() error {
	return ErrTaskFailed
}

// waitOrCancel waits for the read-only task with WaitForTask, and returns the
// task in whatever terminal state it reached. When the wait stops because
// the caller's context ended, a read-only task is no longer worth running,
// so waitOrCancel asks the director to cancel it, on a detached context
// bounded by bound, and records what became of the cancel in the
// DirectorCallError the wait returned. A caller with no deadline of its own
// passes cancelTimeout. A shutdown is the exception.
// When the context's cause is ErrBrokerShuttingDown, no cancel is sent,
// because the task harms nothing and ends on its own, and the shutdown has
// no time to spare. A wait that stopped on a failed read of the task while
// the context was live sends no cancel either. In every case the error
// names the given operation.
func (d *DirectorAdapter) waitOrCancel(ctx context.Context, operation string, taskID int, bound time.Duration) (*Task, error) {
	return d.waitThenCancel(ctx, operation, taskID, true, bound)
}

// waitThenCancel is waitOrCancel for any task. With spareAtShutdown false,
// the cancel is sent even when the context's cause is ErrBrokerShuttingDown,
// for a task whose work must not run on unwatched, such as an SSH setup,
// which creates a user.
func (d *DirectorAdapter) waitThenCancel(ctx context.Context, operation string, taskID int, spareAtShutdown bool, bound time.Duration) (*Task, error) {
	task, err := d.WaitForTask(ctx, taskID)
	if err == nil {
		return task, nil
	}

	var callErr *DirectorCallError
	if !errors.As(err, &callErr) {
		return nil, err
	}

	callErr.Operation = operation

	if ctx.Err() == nil {
		return nil, callErr
	}

	if spareAtShutdown && errors.Is(context.Cause(ctx), ErrBrokerShuttingDown) {
		d.log.Infof("The broker is shutting down, so Blacksmith leaves read-only BOSH task %d to end on its own", taskID)

		return nil, callErr
	}

	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bound)
	defer cancel()

	cancelErr := d.CancelTask(cancelCtx, taskID)

	// Every refusal CancelTask returns carries the task's state as a
	// *boshapi.TaskStateError, so the state is always known here.
	var stateErr *boshapi.TaskStateError

	switch {
	case cancelErr == nil:
		callErr.Task = TaskCancelRequested
	case errors.As(cancelErr, &stateErr) && (stateErr.State == taskStateCancelling || stateErr.State == taskStateCancelled):
		// A cancel already landed, so the task is stopping or has stopped.
		callErr.Task = TaskCancelRequested
		callErr.TaskState = stateErr.State
	case errors.As(cancelErr, &stateErr):
		callErr.Task = TaskCancelRefused
		callErr.TaskState = stateErr.State
	default:
		d.log.Errorf("Failed to cancel BOSH task %d after its wait stopped: %v", taskID, cancelErr)

		callErr.Task = TaskCancelFailed
		callErr.CancelErr = cancelErr
	}

	return nil, callErr
}

// readOnlyTaskResult waits for the read-only task, as waitOrCancel does
// with the cancel bounded by bound, and returns its result output. A
// task that ended in any state but done returns a TaskFailedError with the
// director's result text.
func (d *DirectorAdapter) readOnlyTaskResult(ctx context.Context, operation, deployment string, taskID int, bound time.Duration) (string, error) {
	task, err := d.waitOrCancel(ctx, operation, taskID, bound)
	if err != nil {
		return "", err
	}

	return d.finishedTaskResult(ctx, "Read-only", operation, deployment, task)
}

// sshSetupResult waits for an SSH setup task and returns its result
// output, within sshSetupTimeout of ctx. The bound covers this wait only,
// never the session that follows. A task that ended in any state but done
// returns a TaskFailedError with the director's result text. A wait the
// context or the bound ends cancels the task even at shutdown, because a
// setup creates a user.
func (d *DirectorAdapter) sshSetupResult(ctx context.Context, operation, deployment string, taskID int) (string, error) {
	bound := d.setupTimeout
	if bound <= 0 {
		bound = sshSetupTimeout
	}

	waitCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	task, err := d.waitThenCancel(waitCtx, operation, taskID, false, cancelTimeout)
	if err != nil {
		if ctx.Err() == nil && errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			d.log.Errorf("SSH setup task %d for deployment %s had not finished %s after Blacksmith began waiting, so Blacksmith stopped waiting and asked the director to cancel it",
				taskID, deployment, bound)
		}

		return "", err
	}

	return d.finishedTaskResult(waitCtx, "SSH setup", operation, deployment, task)
}

// finishedTaskResult reads the result output of a task that has reached a
// terminal state. A task that ended in any state but done returns a
// TaskFailedError with the director's result text. kind names the task in
// the log.
func (d *DirectorAdapter) finishedTaskResult(ctx context.Context, kind, operation, deployment string, task *Task) (string, error) {
	if task.State != taskStateDone {
		return "", d.taskFailed(kind, operation, deployment, task)
	}

	output, err := d.api.Tasks().GetOutput(ctx, task.ID, &boshapi.TaskOutputOptions{Type: taskOutputResult})
	if err != nil {
		d.log.Errorf("Failed to read the result of BOSH task %d: %v", task.ID, err)

		return "", d.requestFailure(fmt.Sprintf("read the result of BOSH task %d", task.ID), err)
	}

	return output, nil
}

// taskFailed logs a read-only or SSH setup task that ended in any state but
// done, and returns the TaskFailedError that carries the director's result
// text. kind names the task in the log.
func (d *DirectorAdapter) taskFailed(kind, operation, deployment string, task *Task) error {
	d.log.Errorf("%s BOSH task %d for deployment %s ended in state %s: %s", kind, task.ID, deployment, task.State, task.Result)

	return &TaskFailedError{
		Operation:  operation,
		Director:   d.address,
		Deployment: deployment,
		TaskID:     task.ID,
		State:      task.State,
		Result:     task.Result,
	}
}

// detailsListing names a listing that starts a read-only director task, as
// GET /deployments/<d>/vms?format=full and
// GET /deployments/<d>/instances?format=full do.
type detailsListing struct {
	// operation is the Director method that lists.
	operation string
	// resource is "vms" or "instances".
	resource string
	// list sends the listing request and returns the task it started.
	list func(ctx context.Context, deployment string) (*boshapi.Task, error)
	// cancelBound bounds the cancel of the listing's task once its context
	// ends. Zero means cancelTimeout.
	cancelBound time.Duration
}

// listDetails runs a details listing for deployment, waits for its task,
// and parses the task's result. Only the director's deployment-not-found
// answer, code 70000, maps to ErrDeploymentNotFound, and it keeps the
// director's answer. A lost answer gets an answerLostError cause and is
// never resent.
func (d *DirectorAdapter) listDetails(ctx context.Context, listing detailsListing, deployment string) ([]boshapi.VMDetails, error) {
	d.log.Debugf("Calling GET /deployments/%s/%s?format=full", deployment, listing.resource)

	task, err := listing.list(ctx, deployment)
	if err == nil && task == nil {
		err = ErrEmptyDirectorAnswer
	}

	if err != nil {
		return nil, d.startFailure(ctx, taskStart{
			operation:  listing.operation,
			deployment: deployment,
			method:     http.MethodGet,
			path:       "/deployments/" + deployment + "/" + listing.resource + "?format=full",
			what:       fmt.Sprintf("list the %s of deployment %s", listing.resource, deployment),
		}, err)
	}

	d.log.Debugf("The director lists the %s of deployment %s in BOSH task %d", listing.resource, deployment, task.ID)

	bound := listing.cancelBound
	if bound <= 0 {
		bound = cancelTimeout
	}

	output, err := d.readOnlyTaskResult(ctx, listing.operation, deployment, task.ID, bound)
	if err != nil {
		return nil, err
	}

	details, err := boshapi.ParseVMDetails(output)
	if err != nil {
		d.log.Errorf("Failed to parse the %s of deployment %s from BOSH task %d: %v", listing.resource, deployment, task.ID, err)

		return nil, &taskResultError{resource: listing.resource, deployment: deployment, director: d.address, taskID: task.ID, err: err}
	}

	return details, nil
}

// taskResultError is a listing whose task finished but whose result does
// not parse.
type taskResultError struct {
	resource   string
	deployment string
	director   string
	taskID     int
	err        error
}

func (e *taskResultError) Error() string {
	return fmt.Sprintf("failed to read the %s of deployment %s from BOSH task %d on BOSH director %s: %v",
		e.resource, e.deployment, e.taskID, e.director, e.err)
}

func (e *taskResultError) Unwrap() error {
	return e.err
}

// taskStart names a request that starts a read-only director task.
type taskStart struct {
	// operation is the Director method that sends the request.
	operation string
	// deployment is the deployment the request is for.
	deployment string
	// method and path are the request's HTTP method, and its path and
	// query, as a lost answer's error names them.
	method string
	path   string
	// what finishes "failed to", as in "list the vms of deployment x".
	what string
}

// startFailure explains a request that was meant to start a read-only task
// and did not, because the director did not accept it or its answer was
// lost. Only the director's deployment-not-found answer, code 70000, maps to
// ErrDeploymentNotFound, and it keeps the director's answer. A lost answer
// gets an answerLostError cause for the request and is never resent.
func (d *DirectorAdapter) startFailure(ctx context.Context, start taskStart, err error) error {
	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound {
		d.log.Infof("Deployment %s not found in BOSH director", start.deployment)

		return fmt.Errorf("%w: %s: %w", ErrDeploymentNotFound, start.deployment, err)
	}

	if boshapi.IsOutcomeUnknown(err) && ctx.Err() == nil {
		d.log.Errorf("The answer to %s %s was lost, so the director may have started a read-only task Blacksmith cannot follow: %v",
			start.method, start.path, err)

		lost := &answerLostError{Method: start.method, Path: start.path}

		var transport *boshapi.TransportError
		if errors.As(err, &transport) && transport.Timeout {
			lost.Timeout = &requestTimeoutError{timeout: d.requestTimeout, attempts: transport.Attempts}
		}

		return &DirectorCallError{
			Operation: start.operation,
			Director:  d.address,
			Cause:     lost,
			Err:       err,
		}
	}

	d.log.Errorf("Failed to %s: %v", start.what, err)

	return d.requestFailure(start.what, err)
}
