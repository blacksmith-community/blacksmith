package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// The deployment and director task the details tests list.
const (
	detailsDeployment = "valkey-1"
	detailsTaskID     = 42

	// detailsTaskPath is the route a details task is read on.
	detailsTaskPath = "/tasks/42"

	// detailsCancelPath is the route a details task is cancelled on.
	detailsCancelPath = "/task/42"

	// detailsTaskPage is the page the errors point to for the task.
	detailsTaskPage = "/b/tasks/42"

	// detailsVMListing is the operation a VM listing's errors name.
	detailsVMListing = "GetDeploymentVMs"

	// detailsInstanceUUID is the instance UUID of the first fixture row.
	detailsInstanceUUID = "uuid-0"

	// cancelRequestedText begins the message of a requested cancel.
	cancelRequestedText = "Blacksmith asked the director to cancel BOSH task 42"
)

// The task states the details tests script.
const (
	taskQueued     = "queued"
	taskCancelling = "cancelling"
	taskCancelled  = "cancelled"
	taskTimedOut   = "timeout"
)

// errListingPanicked is the error a listing that panicked fails a test
// with.
var errListingPanicked = errors.New("the listing panicked")

// vmRow is one VM as the director's vm_state.rb writes it in the result
// output of a format=full listing. It carries job_state and no
// process_state, because vm_state.rb writes no process_state.
const vmRow = `{"agent_id":"agent-0","job_name":"node","index":0,"id":"uuid-0","job_state":"running","state":"started",` +
	`"bootstrap":true,"ips":["10.0.0.5"],"dns":["0.node.default.valkey-1.bosh"],"az":"z1","vm_type":"default",` +
	`"vm_cid":"vm-0","disk_cid":"disk-0","disk_cids":["disk-0"],"ignore":false,"vm_created_at":"2026-10-01T12:00:00Z",` +
	`"active":true,"stemcell":{"name":"bosh-warden-boshlite-ubuntu-noble","version":"1.100","api_version":3},` +
	`"cloud_properties":{},"processes":[{"name":"valkey","state":"running","uptime":{"secs":120},` +
	`"mem":{"kb":2048,"percent":0.5},"cpu":{"total":1.5}}],"vitals":{"cpu":{"sys":"1.0","user":"2.0","wait":"0.1"},` +
	`"mem":{"kb":"4096","percent":"10"},"swap":{"kb":"0","percent":"0"},"load":["0.1","0.2","0.3"],` +
	`"disk":{"system":{"inode_percent":"5","percent":"40"}},"uptime":{"secs":3600}}}`

// detailsDirector is a test director that lists a deployment's VMs or
// instances as a director task. The listing redirects to task
// detailsTaskID, each read of the task answers what state returns, and
// the task's result output is output. A cancel answers what cancel writes,
// or 204 when cancel is nil.
type detailsDirector struct {
	*boshtest.Director

	polls   atomic.Int32
	cancels atomic.Int32

	firstPoll     chan struct{}
	firstPollOnce sync.Once
}

// detailsConfig says how a detailsDirector answers.
type detailsConfig struct {
	// kind is "vms" or "instances".
	kind string
	// list answers the listing. Nil redirects to the task.
	list func(http.ResponseWriter, *http.Request)
	// state returns the task's state and result for the nth read, counting
	// from 1. Nil answers done with an empty result.
	state func(poll int32, cancels int32) (string, string)
	// read answers a read of the task in place of state, when it returns
	// true.
	read func(http.ResponseWriter, int32) bool
	// cancel answers a cancel. Nil answers 204.
	cancel func(http.ResponseWriter)
	// output is the task's result output.
	output string
}

func newDetailsDirector(t *testing.T, cfg detailsConfig) *detailsDirector {
	t.Helper()

	if cfg.kind == "" {
		cfg.kind = "vms"
	}

	director := &detailsDirector{firstPoll: make(chan struct{})}

	listPath := "/deployments/" + detailsDeployment + "/" + cfg.kind

	director.Director = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == listPath:
			if cfg.list != nil {
				cfg.list(writer, request)

				return true
			}

			writer.Header().Set("Location", detailsTaskPath)
			writer.WriteHeader(http.StatusFound)
		case request.Method == http.MethodGet && request.URL.Path == detailsTaskPath:
			poll := director.polls.Add(1)
			director.firstPollOnce.Do(func() { close(director.firstPoll) })

			if cfg.read != nil && cfg.read(writer, poll) {
				return true
			}

			state, result := batchTaskDone, ""
			if cfg.state != nil {
				state, result = cfg.state(poll, director.cancels.Load())
			}

			task := directorTask(detailsTaskID, state)
			task["description"] = "retrieve vm-stats"
			task["result"] = result
			task["deployment"] = detailsDeployment

			boshtest.WriteJSON(writer, http.StatusOK, task)
		case request.Method == http.MethodGet && request.URL.Path == detailsTaskPath+"/output":
			boshtest.WriteText(writer, http.StatusOK, cfg.output)
		case request.Method == http.MethodDelete && request.URL.Path == detailsCancelPath:
			director.cancels.Add(1)

			if cfg.cancel != nil {
				cfg.cancel(writer)

				return true
			}

			writer.WriteHeader(http.StatusNoContent)
		default:
			return false
		}

		return true
	})

	return director
}

// queuedUntilCancelled keeps the task queued until a cancel arrives, and
// cancelled after it.
func queuedUntilCancelled(_ int32, cancels int32) (string, string) {
	if cancels > 0 {
		return taskCancelled, "Task 42 cancelled"
	}

	return taskQueued, ""
}

// listingCall is a details listing the tests run against both listings.
type listingCall struct {
	operation string
	kind      string
	call      func(ctx context.Context, director bosh.Director) error
}

func vmListing() listingCall {
	return listingCall{operation: detailsVMListing, kind: "vms", call: func(ctx context.Context, director bosh.Director) error {
		_, err := director.GetDeploymentVMs(ctx, detailsDeployment)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	}}
}

// callWithin runs call and fails the test when it has not returned within
// limit, so a call that ignores its context fails rather than hangs.
func callWithin(t *testing.T, limit time.Duration, call func() error) error {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- call() }()

	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("the call had not returned %s after it started, so it does not end with its context", limit)

		return nil
	}
}

// checkCallError checks that err is a DirectorCallError for operation and
// task detailsTaskID with the outcome want, and returns it.
func checkCallError(t *testing.T, err error, operation string, want bosh.TaskOutcome) *bosh.DirectorCallError {
	t.Helper()

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
	}

	if callErr.Operation != operation || callErr.TaskID != detailsTaskID || callErr.Task != want {
		t.Fatalf("DirectorCallError names operation %q, task %d, and outcome %v, want %s, %d, and %v: %v",
			callErr.Operation, callErr.TaskID, callErr.Task, operation, detailsTaskID, want, err)
	}

	return callErr
}

// A details task that ends in any state but done fails the listing with a
// TaskFailedError that names the task and carries the director's result.
func TestGetDeploymentVMsFailsWhenTheTaskEndsInError(t *testing.T) {
	for _, state := range []string{batchTaskError, taskCancelled, taskTimedOut} {
		t.Run(state, func(t *testing.T) {
			server := newDetailsDirector(t, detailsConfig{state: func(int32, int32) (string, string) {
				return state, "the vm-stats task ended " + state
			}})
			director := newTaskAdapter(t, server.Director)

			err := callWithin(t, 10*time.Second, func() error { return vmListing().call(t.Context(), director) })

			var failed *bosh.TaskFailedError
			if !errors.As(err, &failed) {
				t.Fatalf("expected a *bosh.TaskFailedError, got %T: %v", err, err)
			}

			if !errors.Is(err, bosh.ErrTaskFailed) {
				t.Errorf("expected the error to wrap ErrTaskFailed: %v", err)
			}

			if failed.TaskID != detailsTaskID || failed.State != state || failed.Result != "the vm-stats task ended "+state ||
				failed.Deployment != detailsDeployment || failed.Operation != detailsVMListing {
				t.Errorf("unexpected TaskFailedError fields: %+v", failed)
			}

			for _, want := range []string{"the vm-stats task ended " + state, detailsTaskPage, state} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected the error to mention %q, got: %v", want, err)
				}
			}

			if outputs := requestsTo(server.Director, http.MethodGet, detailsTaskPath+"/output"); len(outputs) != 0 {
				t.Errorf("a task that failed has no result to read, but its output was read %d times", len(outputs))
			}
		})
	}
}

// The director's deployment-not-found answer, code 70000, gives
// ErrDeploymentNotFound and keeps the director's own answer. The listing
// sends no other request first.
func TestGetDeploymentVMsKeepsTheDirectorsNotFoundAnswer(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{list: func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'valkey-1' doesn't exist")
	}})
	director := newTaskAdapter(t, server.Director)

	_, err := director.GetDeploymentVMs(t.Context(), detailsDeployment)
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got: %v", err)
	}

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound {
		t.Fatalf("expected the error to keep the director's answer with code 70000, got %T: %v", err, err)
	}

	if !strings.Contains(err.Error(), detailsDeployment) {
		t.Errorf("expected the error to name the deployment, got: %v", err)
	}

	requests := server.APIRequests()
	if len(requests) != 1 || requests[0].Path != "/deployments/"+detailsDeployment+"/vms" {
		t.Errorf("expected the listing to be the only request, got %+v", requests)
	}
}

// A plain 404 is a route the director does not serve, never a missing
// deployment.
func TestGetDeploymentVMsDoesNotTreatARoute404AsAMissingDeployment(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{list: func(writer http.ResponseWriter, request *http.Request) {
		boshtest.WriteRouteNotFound(writer, request)
	}})
	director := newTaskAdapter(t, server.Director)

	_, err := director.GetDeploymentVMs(t.Context(), detailsDeployment)
	if err == nil {
		t.Fatal("expected an error for a route 404")
	}

	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("a route 404 must not read as a missing deployment: %v", err)
	}

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("expected the director's 404 answer to stay reachable, got %T: %v", err, err)
	}
}

// The director writes a null index and null vitals for a VM it cannot read,
// and the listing still converts it, with index -1 and empty vitals.
func TestGetDeploymentVMsAcceptsAVMWithoutAnIndex(t *testing.T) {
	row := `{"agent_id":"agent-0","job_name":"node","index":null,"id":"uuid-0","job_state":"unresponsive agent",` +
		`"state":"started","ips":["10.0.0.5"],"vm_cid":"vm-0","vm_created_at":null,"vitals":null,"processes":[],` +
		`"stemcell":{"name":"","version":"","api_version":0}}`

	server := newDetailsDirector(t, detailsConfig{output: row + "\n"})
	director := newTaskAdapter(t, server.Director)

	var vms []bosh.VM

	err := callWithin(t, 10*time.Second, func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%w: %v", errListingPanicked, recovered)
			}
		}()

		vms, err = director.GetDeploymentVMs(t.Context(), detailsDeployment)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("expected the listing to succeed, got: %v", err)
	}

	if len(vms) != 1 {
		t.Fatalf("expected one VM, got %d", len(vms))
	}

	listed := vms[0]
	if listed.Index != -1 || listed.ID != detailsInstanceUUID || listed.JobState != "unresponsive agent" {
		t.Errorf("expected index -1, ID uuid-0, and job state %q, got %d, %q, and %q", "unresponsive agent", listed.Index, listed.ID, listed.JobState)
	}

	if listed.Vitals.Disk == nil || len(listed.Vitals.Disk) != 0 {
		t.Errorf("expected an empty disk map for null vitals, got %#v", listed.Vitals.Disk)
	}

	if listed.Vitals.Uptime.Seconds != nil || listed.Vitals.Memory.KB != nil {
		t.Errorf("expected no vitals figures for null vitals, got %+v", listed.Vitals)
	}

	if !listed.VMCreatedAt.IsZero() {
		t.Errorf("expected the zero creation time for a null vm_created_at, got %v", listed.VMCreatedAt)
	}
}

// A plain cancel of the caller's context ends the wait, and Blacksmith asks
// the director to cancel the read-only task, so a queued task never runs.
func TestVMDetailsCancelsTheDirectorTaskWhenTheContextEnds(t *testing.T) {
	runCancelsTheTaskWhenTheContextEnds(t, vmListing())
}

func runCancelsTheTaskWhenTheContextEnds(t *testing.T, listing listingCall) {
	t.Helper()

	server := newDetailsDirector(t, detailsConfig{kind: listing.kind, state: queuedUntilCancelled})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		<-server.firstPoll
		cancel()
	}()

	err := callWithin(t, 10*time.Second, func() error { return listing.call(ctx, director) })

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected an error that wraps context.Canceled, got: %v", err)
	}

	_ = checkCallError(t, err, listing.operation, bosh.TaskCancelRequested)

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 1 {
		t.Fatalf("expected exactly one DELETE %s on a live context, got %d", detailsCancelPath, len(deletes))
	}

	for _, want := range []string{cancelRequestedText, detailsTaskPage} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

// A shutdown cancels the service context with ErrBrokerShuttingDown, and a
// wait that ends with that cause sends no cancel, because a read-only task
// harms nothing and ends on its own.
func TestWaitOrCancelSendsNoCancelWhenTheBrokerIsShuttingDown(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{state: queuedUntilCancelled})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)

	go func() {
		<-server.firstPoll
		cancel(bosh.ErrBrokerShuttingDown)
	}()

	err := callWithin(t, 10*time.Second, func() error { return vmListing().call(ctx, director) })

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected an error that wraps context.Canceled, got: %v", err)
	}

	_ = checkCallError(t, err, detailsVMListing, bosh.TaskLeftRunning)

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 0 {
		t.Fatalf("expected no cancel at shutdown, got %d", len(deletes))
	}

	if !strings.Contains(err.Error(), "BOSH task 42") {
		t.Errorf("expected the error to name task 42, got: %v", err)
	}
}

// A check's deadline is not a shutdown, so the wait still cancels the
// queued task when the deadline passes.
func TestWaitOrCancelStillCancelsWhenACheckDeadlinePasses(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{state: queuedUntilCancelled})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	err := callWithin(t, 10*time.Second, func() error { return vmListing().call(ctx, director) })

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an error that wraps context.DeadlineExceeded, got: %v", err)
	}

	_ = checkCallError(t, err, detailsVMListing, bosh.TaskCancelRequested)

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 1 {
		t.Fatalf("expected exactly one DELETE %s on a live context, got %d", detailsCancelPath, len(deletes))
	}
}

// A cancel that finds the task already done tells the operator the task
// ended and how. One that finds it cancelling or cancelled means a cancel
// landed, and the message names the state the director gave.
func TestVMDetailsReportsATaskThatEndedBeforeTheCancel(t *testing.T) {
	tests := []struct {
		state     string
		want      bosh.TaskOutcome
		wantState string
		wantText  string
	}{
		{state: batchTaskDone, want: bosh.TaskCancelRefused, wantState: batchTaskDone,
			wantText: "BOSH task 42 had already ended in state done when Blacksmith asked the director to cancel it, so there was nothing to cancel."},
		{state: taskCancelling, want: bosh.TaskCancelRequested, wantState: taskCancelling,
			wantText: "BOSH task 42 was already cancelling when Blacksmith asked the director to cancel it."},
		{state: taskCancelled, want: bosh.TaskCancelRequested, wantState: taskCancelled,
			wantText: "BOSH task 42 had already been cancelled when Blacksmith asked the director to cancel it, so it has stopped."},
	}

	for _, testCase := range tests {
		t.Run(testCase.state, func(t *testing.T) {
			server := newDetailsDirector(t, detailsConfig{
				state: func(int32, int32) (string, string) { return taskQueued, "" },
				cancel: func(writer http.ResponseWriter) {
					boshtest.WriteText(writer, http.StatusBadRequest, "Cannot cancel task 42: invalid state ("+testCase.state+")")
				},
			})
			director := newTaskAdapter(t, server.Director)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			go func() {
				<-server.firstPoll
				cancel()
			}()

			err := callWithin(t, 10*time.Second, func() error { return vmListing().call(ctx, director) })

			callErr := checkCallError(t, err, detailsVMListing, testCase.want)
			if callErr.TaskState != testCase.wantState {
				t.Errorf("expected TaskState %q, got %q", testCase.wantState, callErr.TaskState)
			}

			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("expected the error to mention %q, got: %v", testCase.wantText, err)
			}

			if !errors.Is(err, context.Canceled) {
				t.Errorf("expected the error to keep the context's cause, got: %v", err)
			}
		})
	}
}

// A cancel that fails is reported with its own error, and errors.Is still
// answers for the wait's cause, not the cancel's.
func TestVMDetailsReportsAFailedCancel(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{
		state: func(int32, int32) (string, string) { return taskQueued, "" },
		cancel: func(writer http.ResponseWriter) {
			boshtest.WriteError(writer, http.StatusInternalServerError, 100, "The worker database is locked.")
		},
	})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		<-server.firstPoll
		cancel()
	}()

	err := callWithin(t, 10*time.Second, func() error { return vmListing().call(ctx, director) })

	callErr := checkCallError(t, err, detailsVMListing, bosh.TaskCancelFailed)
	if callErr.CancelErr == nil {
		t.Fatal("expected the cancel's error in CancelErr")
	}

	if !strings.Contains(err.Error(), "The worker database is locked.") {
		t.Errorf("expected the error to print the cancel's error, got: %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is to find the context's cause, got: %v", err)
	}

	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) && apiErr.Description == "The worker database is locked." {
		t.Errorf("the cancel's error must not be reachable through Unwrap: %v", err)
	}
}

// A read of the task that fails while the caller's context is live leaves
// the task running, and sends no cancel.
func TestVMDetailsLeavesTheTaskRunningWhenAReadFails(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{
		state: func(int32, int32) (string, string) { return "processing", "" },
		read: func(writer http.ResponseWriter, poll int32) bool {
			if poll < 2 {
				return false
			}

			boshtest.WriteError(writer, http.StatusInternalServerError, 100, "The database is locked.")

			return true
		},
	})
	director := newTaskAdapter(t, server.Director)

	err := callWithin(t, 30*time.Second, func() error { return vmListing().call(t.Context(), director) })

	_ = checkCallError(t, err, detailsVMListing, bosh.TaskLeftRunning)

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 0 {
		t.Fatalf("expected no cancel when a read fails on a live context, got %d", len(deletes))
	}

	if !strings.Contains(err.Error(), "BOSH task 42 is still running") {
		t.Errorf("expected the error to say the task is still running, got: %v", err)
	}
}

// A lost answer to a details listing is not an unknown outcome, to
// Blacksmith or to boshapi. The director may have started a read-only task,
// which ends on its own, and the caller can ask again. The request is never
// resent.
func TestVMDetailsLostAnswerIsNotOutcomeUnknown(t *testing.T) {
	runLostAnswerIsNotOutcomeUnknown(t, vmListing())
}

func runLostAnswerIsNotOutcomeUnknown(t *testing.T, listing listingCall) {
	t.Helper()

	server := newDetailsDirector(t, detailsConfig{kind: listing.kind, list: func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.DropAnswer(writer)
	}})
	director := newTaskAdapter(t, server.Director)

	err := callWithin(t, 30*time.Second, func() error { return listing.call(t.Context(), director) })
	if err == nil {
		t.Fatal("expected an error for a lost answer")
	}

	listPath := "/deployments/" + detailsDeployment + "/" + listing.kind
	if gets := requestsTo(server.Director, http.MethodGet, listPath); len(gets) != 1 {
		t.Fatalf("expected exactly one GET %s, got %d", listPath, len(gets))
	}

	method, path, lost := bosh.AnswerLostRequest(err)
	if !lost {
		t.Fatalf("expected a DirectorCallError whose cause is a lost answer, got %T: %v", err, err)
	}

	if method != http.MethodGet || path != listPath+"?format=full" {
		t.Errorf("expected the cause to name GET %s?format=full, got %s %s", listPath, method, path)
	}

	if errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("a lost answer to a read must not read as an unknown outcome: %v", err)
	}

	if boshapi.IsOutcomeUnknown(err) {
		t.Errorf("a lost answer to a read must not read as an unknown outcome to boshapi: %v", err)
	}

	for _, want := range []string{"the caller can ask again", "read-only task", "did not resend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

// A listing that gets no answer within bosh.request_timeout is a lost
// answer, and its error names the setting that bounded it. The caller's own
// context is still live, so the error does not read as its deadline.
func TestVMDetailsLostAnswerNamesTheRequestTimeout(t *testing.T) {
	release := make(chan struct{})

	server := newDetailsDirector(t, detailsConfig{list: func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}})
	t.Cleanup(func() { close(release) })

	director := newReadAdapter(t, server.Director, func(config *bosh.Config) {
		config.RequestTimeout = 150 * time.Millisecond
	})

	err := callWithin(t, 30*time.Second, func() error { return vmListing().call(t.Context(), director) })

	if _, _, lost := bosh.AnswerLostRequest(err); !lost {
		t.Fatalf("expected a DirectorCallError whose cause is a lost answer, got %T: %v", err, err)
	}

	if !strings.Contains(err.Error(), "no answer within the 150ms bosh.request_timeout") {
		t.Errorf("expected the error to name bosh.request_timeout and its value, got: %v", err)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the caller's context is live, so the error must not read as its deadline: %v", err)
	}

	listPath := "/deployments/" + detailsDeployment + "/vms"
	if gets := requestsTo(server.Director, http.MethodGet, listPath); len(gets) != 1 {
		t.Fatalf("expected exactly one GET %s, got %d", listPath, len(gets))
	}
}

// boshapi tells the reader of a lost answer to a task-starting request to
// look for the task before sending the request again. That advice is for a
// change, and a read-only task needs none, so the error keeps only what the
// director answered, whichever way the answer was lost.
func TestVMDetailsLostAnswerGivesNoAdviceToLookForATask(t *testing.T) {
	tests := []struct {
		name   string
		answer func(http.ResponseWriter, *http.Request)
		want   string
	}{
		{
			name: "a redirect that names no task",
			answer: func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Location", "/elsewhere")
				writer.WriteHeader(http.StatusFound)
			},
			want: `HTTP 302 and a redirect to "/elsewhere"`,
		},
		{
			name: "an answer that is not a redirect",
			answer: func(writer http.ResponseWriter, _ *http.Request) {
				boshtest.WriteText(writer, http.StatusOK, "not a task")
			},
			want: "HTTP 200",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := newDetailsDirector(t, detailsConfig{list: testCase.answer})
			director := newTaskAdapter(t, server.Director)

			err := callWithin(t, 30*time.Second, func() error { return vmListing().call(t.Context(), director) })

			if _, _, lost := bosh.AnswerLostRequest(err); !lost {
				t.Fatalf("expected a DirectorCallError whose cause is a lost answer, got %T: %v", err, err)
			}

			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("expected the error to mention %q, got: %v", testCase.want, err)
			}

			for _, advice := range []string{"before you send it again", "got no answer"} {
				if strings.Contains(err.Error(), advice) {
					t.Errorf("expected the error not to say %q, got: %v", advice, err)
				}
			}
		})
	}
}

// convertRow parses one director row and converts it.
func convertRow(t *testing.T, row string) bosh.VM {
	t.Helper()

	details, err := boshapi.ParseVMDetails(row)
	if err != nil || len(details) != 1 {
		t.Fatalf("parsing the row: %v (%d rows)", err, len(details))
	}

	return bosh.ConvertVMInfo(details[0])
}

// vm_state.rb writes the job's state as job_state and writes no
// process_state, so the job state comes from job_state.
func TestConvertVMInfoTakesJobStateFromTheJobStateKey(t *testing.T) {
	t.Parallel()

	row := strings.Replace(vmRow, `"job_state":"running"`, `"job_state":"failing"`, 1)
	if strings.Contains(row, "process_state") {
		t.Fatal("the fixture must carry no process_state key")
	}

	converted := convertRow(t, row)
	if converted.JobState != "failing" {
		t.Errorf("expected the job state failing, got %q", converted.JobState)
	}

	if converted.Index != 0 || converted.ID != detailsInstanceUUID || converted.CID != "vm-0" || converted.AgentID != "agent-0" {
		t.Errorf("unexpected identity fields: index %d, ID %q, CID %q, agent %q", converted.Index, converted.ID, converted.CID, converted.AgentID)
	}

	if !converted.VMCreatedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("expected the creation time to parse, got %v", converted.VMCreatedAt)
	}

	if len(converted.DNS) != 1 || converted.DNS[0] != "0.node.default.valkey-1.bosh" {
		t.Errorf("expected the dns key to map across, got %v", converted.DNS)
	}

	checkFullRowFigures(t, converted)
}

// checkFullRowFigures checks that the vitals, the stemcell, and the process
// figures of the vmRow fixture map across.
func checkFullRowFigures(t *testing.T, converted bosh.VM) {
	t.Helper()

	if converted.Vitals.Uptime.Seconds == nil || *converted.Vitals.Uptime.Seconds != 3600 {
		t.Errorf("expected an uptime of 3600 seconds, got %v", converted.Vitals.Uptime.Seconds)
	}

	if converted.Vitals.Memory.KB == nil || *converted.Vitals.Memory.KB != 4096 || converted.Vitals.Disk["system"].Percent != "40" {
		t.Errorf("expected the memory and disk vitals to map across, got %+v", converted.Vitals)
	}

	if converted.Stemcell.Name != "bosh-warden-boshlite-ubuntu-noble" || converted.Stemcell.ApiVersion != 3 {
		t.Errorf("expected the stemcell to map across, got %+v", converted.Stemcell)
	}

	if len(converted.Processes) != 1 || converted.Processes[0].CPU.Total == nil || *converted.Processes[0].CPU.Total != 1.5 ||
		converted.Processes[0].Memory.KB == nil || *converted.Processes[0].Memory.KB != 2048 ||
		converted.Processes[0].Uptime.Seconds == nil || *converted.Processes[0].Uptime.Seconds != 120 {
		t.Errorf("expected the process figures to map across, got %+v", converted.Processes)
	}
}

// A process the agent reports no figures for keeps nil figures, so an
// absent figure stays distinct from a zero one. A negative count is not a
// figure either. A row with no dns key still gives an empty DNS list.
func TestConvertVMInfoLeavesAbsentProcessFiguresNil(t *testing.T) {
	t.Parallel()

	row := `{"agent_id":"agent-0","job_name":"node","index":1,"id":"uuid-1","job_state":"running","state":"started",` +
		`"processes":[{"name":"valkey","state":"running"},` +
		`{"name":"exporter","state":"running","uptime":{"secs":-5},"mem":{"kb":-1,"percent":0},"cpu":{"total":0}}],` +
		`"stemcell":{"name":"s","version":"1","api_version":3},"vitals":{"cpu":{},"mem":{},"swap":{},"load":[],"disk":{}}}`

	converted := convertRow(t, row)

	if converted.DNS == nil || len(converted.DNS) != 0 {
		t.Errorf("expected an empty, non-nil DNS list for a row with no dns key, got %#v", converted.DNS)
	}

	if len(converted.Processes) != 2 {
		t.Fatalf("expected two processes, got %d", len(converted.Processes))
	}

	bare := converted.Processes[0]
	if bare.CPU.Total != nil || bare.Memory.KB != nil || bare.Memory.Percent != nil || bare.Uptime.Seconds != nil {
		t.Errorf("expected nil figures for a process with no cpu, mem, or uptime, got %+v", bare)
	}

	zeroed := converted.Processes[1]
	if zeroed.Uptime.Seconds != nil || zeroed.Memory.KB != nil {
		t.Errorf("expected nil for a negative uptime and memory, got %+v", zeroed)
	}

	if zeroed.CPU.Total == nil || *zeroed.CPU.Total != 0 || zeroed.Memory.Percent == nil || *zeroed.Memory.Percent != 0 {
		t.Errorf("expected a zero CPU total and memory percent to stay zero rather than absent, got %+v", zeroed)
	}

	if converted.Vitals.Uptime.Seconds != nil {
		t.Errorf("expected no uptime for vitals without one, got %v", *converted.Vitals.Uptime.Seconds)
	}
}

// errResultUnparsable is a task result that does not parse.
var errResultUnparsable = errors.New("unexpected end of JSON input")

// A task that ended answered the call, so a context that ends after it
// does not turn the task's failure, or a result that does not parse, into
// a call that stopped before the director answered.
func TestAFinishedTaskIsNotExplainedAsAStoppedCall(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	finished := []error{
		&bosh.TaskFailedError{
			Operation: detailsVMListing, Director: server.URL, Deployment: detailsDeployment,
			TaskID: detailsTaskID, State: batchTaskError, Result: "the agent did not answer",
		},
		bosh.TaskResultUnreadable("vms", detailsDeployment, detailsTaskID, errResultUnparsable),
	}

	for _, err := range finished {
		explained := bosh.ExplainContext(ctx, director, detailsVMListing, err)

		var callErr *bosh.DirectorCallError
		if errors.As(explained, &callErr) {
			t.Errorf("a finished task's error was explained as a stopped call: %v", explained)
		}

		if !errors.Is(explained, err) {
			t.Errorf("expected the finished task's own error, got: %v", explained)
		}
	}
}
