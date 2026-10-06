package bosh_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

// testPollInterval keeps task waits in these tests short.
const testPollInterval = 10 * time.Millisecond

// directorTask is a task in the director's JSON shape.
func directorTask(id int, state string) map[string]interface{} {
	return map[string]interface{}{
		"id":          id,
		"state":       state,
		"description": "create deployment",
		"timestamp":   1759766400,
		"started_at":  1759766000,
		"result":      "",
		"user":        "admin",
		"deployment":  "valkey-1",
		"context_id":  "",
	}
}

// newTaskAdapter builds an adapter against a test director that polls tasks
// every testPollInterval.
func newTaskAdapter(t *testing.T, server *boshtest.Director) bosh.Director {
	t.Helper()

	director := newReadAdapter(t, server, nil)
	bosh.SetTaskPollInterval(director, testPollInterval)

	return director
}

// requestsTo returns the requests with the given method and path.
func requestsTo(server *boshtest.Director, method, path string) []boshtest.Request {
	var found []boshtest.Request

	for _, request := range server.APIRequests() {
		if request.Method == method && request.Path == path {
			found = append(found, request)
		}
	}

	return found
}

// When the caller stops waiting, the director task keeps running. Blacksmith
// never cancels a task just because it stopped waiting for it, and the error
// says so and names the task.
func TestWaitForTaskLeavesTheTaskRunningWhenTheContextEnds(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks/42" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, directorTask(42, "processing"))

		return true
	})

	director := newTaskAdapter(t, server)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	task, err := director.WaitForTask(ctx, 42)
	if task != nil {
		t.Fatalf("expected no task when the wait ends first, got %+v", task)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an error that wraps context.DeadlineExceeded, got: %v", err)
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
	}

	if callErr.Operation != "WaitForTask" || callErr.Director != server.URL || callErr.TaskID != 42 || callErr.Task != bosh.TaskLeftRunning {
		t.Fatalf("DirectorCallError names operation %q, director %q, task %d, and outcome %v, want WaitForTask, %s, 42, and TaskLeftRunning",
			callErr.Operation, callErr.Director, callErr.TaskID, callErr.Task, server.URL)
	}

	message := err.Error()
	for _, want := range []string{
		"WaitForTask", server.URL, "before BOSH task 42 finished",
		"BOSH task 42 is still running; Blacksmith stopped waiting and did not cancel it", "/b/tasks/42",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the error to mention %q, got: %s", want, message)
		}
	}

	if polls := requestsTo(server, http.MethodGet, "/tasks/42"); len(polls) < 2 {
		t.Errorf("expected the wait to poll the task more than once, got %d polls", len(polls))
	}

	for _, request := range server.APIRequests() {
		if request.Method == http.MethodDelete {
			t.Errorf("the wait sent %s %s, but it must never cancel the task", request.Method, request.Path)
		}
	}
}

// Every terminal state ends the wait with the final task and no error,
// because callers judge the state themselves.
func TestWaitForTaskReturnsFailedTasksWithoutError(t *testing.T) {
	for _, state := range []string{"done", "error", "cancelled", "timeout"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			var polls atomic.Int32

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodGet || request.URL.Path != "/tasks/7" {
					return false
				}

				task := directorTask(7, "processing")
				if polls.Add(1) > 1 {
					task["state"] = state
					task["result"] = "the task ended " + state
				}

				boshtest.WriteJSON(writer, http.StatusOK, task)

				return true
			})

			director := newTaskAdapter(t, server)

			task, err := director.WaitForTask(t.Context(), 7)
			if err != nil {
				t.Fatalf("expected no error for a task that ended %s, got: %v", state, err)
			}

			if task == nil || task.ID != 7 || task.State != state || task.Result != "the task ended "+state {
				t.Fatalf("expected task 7 in state %s with its result, got %+v", state, task)
			}

			if task.EndedAt == nil || !task.EndedAt.Equal(time.Unix(1759766400, 0)) {
				t.Errorf("expected the finished task to end at the director's timestamp, got %v", task.EndedAt)
			}

			if polls.Load() < 2 {
				t.Errorf("expected the wait to poll until the task ended, got %d polls", polls.Load())
			}
		})
	}
}

// slowWaitDirector is a base director whose WaitForTask lasts until its
// context ends, and whose CreateDeployment answers at once.
type slowWaitDirector struct {
	bosh.Director

	entered     chan struct{}
	enteredOnce sync.Once
}

func (d *slowWaitDirector) WaitForTask(ctx context.Context, id int) (*bosh.Task, error) {
	d.enteredOnce.Do(func() { close(d.entered) })

	<-ctx.Done()

	return nil, ctx.Err()
}

func (d *slowWaitDirector) CreateDeployment(_ context.Context, _ string) (*bosh.Task, error) {
	return &bosh.Task{ID: 2, State: "queued"}, nil
}

// A wait can last as long as a deploy does, so the pooled director must not
// hold a slot for it, or a few long waits would stop every mutation.
func TestPooledWaitDoesNotHoldASlot(t *testing.T) {
	t.Parallel()

	base := &slowWaitDirector{entered: make(chan struct{})}
	pool := bosh.NewPooledDirector(base, 1, 200*time.Millisecond, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	waited := make(chan error, 1)

	go func() {
		_, err := pool.WaitForTask(ctx, 1)
		waited <- err
	}()

	select {
	case <-base.entered:
	case err := <-waited:
		t.Fatalf("the pooled director never reached the base director's WaitForTask, and returned: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the pooled director never reached the base director's WaitForTask")
	}

	stats, err := pool.GetPoolStats()
	if err != nil {
		t.Fatalf("GetPoolStats failed: %v", err)
	}

	if stats.ActiveConnections != 0 {
		t.Errorf("a wait in progress holds %d pool slots, want 0", stats.ActiveConnections)
	}

	task, err := pool.CreateDeployment(t.Context(), "name: valkey-2")
	if err != nil {
		t.Fatalf("a deploy could not get the only pool slot while a wait was in progress: %v", err)
	}

	if task == nil || task.ID != 2 {
		t.Fatalf("expected the deploy's task 2, got %+v", task)
	}

	cancel()

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the pooled wait did not end with its context")
	}
}

// The director filters a deployment's tasks by state itself, so Blacksmith
// asks for the running states rather than scanning recent tasks.
func TestFindRunningTaskForDeploymentFiltersByState(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		task := directorTask(31, "cancelling")
		task["context_id"] = "ctx-31"
		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{task})

		return true
	})

	director := newReadAdapter(t, server, nil)

	task, err := director.FindRunningTaskForDeployment(t.Context(), "valkey-1")
	if err != nil {
		t.Fatalf("FindRunningTaskForDeployment failed: %v", err)
	}

	if task == nil || task.ID != 31 || task.State != "cancelling" || task.Deployment != "valkey-1" || task.ContextID != "ctx-31" {
		t.Fatalf("expected running task 31 with its fields, got %+v", task)
	}

	lists := requestsTo(server, http.MethodGet, "/tasks")
	if len(lists) != 1 {
		t.Fatalf("expected one task list request, got %d: %+v", len(lists), server.APIRequests())
	}

	query, err := url.ParseQuery(lists[0].RawQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", lists[0].RawQuery, err)
	}

	want := url.Values{
		"deployment": {"valkey-1"},
		"state":      {"processing,cancelling,queued"},
		"verbose":    {"1"},
	}
	if query.Encode() != want.Encode() {
		t.Errorf("task list query = %q, want %q", query.Encode(), want.Encode())
	}
}

// When no task is running, the answer is nil with no error.
func TestFindRunningTaskForDeploymentReturnsNilWhenNoneRuns(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

		return true
	})

	director := newReadAdapter(t, server, nil)

	task, err := director.FindRunningTaskForDeployment(t.Context(), "valkey-1")
	if err != nil || task != nil {
		t.Fatalf("expected no task and no error, got %+v and %v", task, err)
	}
}

// The director cancels a task on DELETE /task/N, its TaskController's
// singular route. It has no DELETE /tasks/N, and a cancel needs no read of
// the task first.
func TestCancelTaskUsesTheDirectorsRoute(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodDelete || request.URL.Path != "/task/42" {
			return false
		}

		writer.WriteHeader(http.StatusNoContent)

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.CancelTask(t.Context(), 42)
	if err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}

	requests := server.APIRequests()
	if len(requests) != 1 || requests[0].Method != http.MethodDelete || requests[0].Path != "/task/42" {
		t.Fatalf("expected exactly one DELETE /task/42, got %+v", requests)
	}
}

// The director refuses to cancel a task that is no longer running, with a
// 400. The tasks handler answers 409 for it, so the error must match
// ErrTaskCannotBeCancelled.
func TestCancelOfAFinishedTaskMapsToCannotBeCancelled(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodDelete || request.URL.Path != "/task/42" {
			return false
		}

		boshtest.WriteText(writer, http.StatusBadRequest, "Cannot cancel task 42: invalid state (done)")

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.CancelTask(t.Context(), 42)
	if !errors.Is(err, bosh.ErrTaskCannotBeCancelled) {
		t.Fatalf("expected ErrTaskCannotBeCancelled, got: %v", err)
	}

	message := err.Error()
	for _, want := range []string{"42", `"done"`} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the error to mention %q, got: %s", want, message)
		}
	}

	if deletes := requestsTo(server, http.MethodDelete, "/task/42"); len(deletes) != 1 {
		t.Errorf("expected one cancel, got %d", len(deletes))
	}
}

// When a cancel's answer is lost, the director may have taken it. Blacksmith
// reads the task, and a task already cancelling needs no second cancel.
func TestLostCancelAnswerIsCheckedByReadingTheTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodDelete && request.URL.Path == "/task/42":
			boshtest.DropAnswer(writer)
		case request.Method == http.MethodGet && request.URL.Path == "/tasks/42":
			boshtest.WriteJSON(writer, http.StatusOK, directorTask(42, "cancelling"))
		default:
			return false
		}

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.CancelTask(t.Context(), 42)
	if err != nil {
		t.Fatalf("expected a cancel that landed to succeed, got: %v", err)
	}

	if deletes := requestsTo(server, http.MethodDelete, "/task/42"); len(deletes) != 1 {
		t.Errorf("expected exactly one cancel, got %d", len(deletes))
	}

	if reads := requestsTo(server, http.MethodGet, "/tasks/42"); len(reads) != 1 {
		t.Errorf("expected one read of the task after the lost answer, got %d", len(reads))
	}
}

// When a cancel's answer is lost and the task still runs, the cancel never
// landed, so Blacksmith sends it once more.
func TestLostCancelAnswerIsResentWhileTheTaskRuns(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	var cancels atomic.Int32

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodDelete && request.URL.Path == "/task/42":
			if cancels.Add(1) == 1 {
				boshtest.DropAnswer(writer)
			} else {
				writer.WriteHeader(http.StatusNoContent)
			}
		case request.Method == http.MethodGet && request.URL.Path == "/tasks/42":
			boshtest.WriteJSON(writer, http.StatusOK, directorTask(42, "processing"))
		default:
			return false
		}

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.CancelTask(t.Context(), 42)
	if err != nil {
		t.Fatalf("expected the second cancel to succeed, got: %v", err)
	}

	if got := cancels.Load(); got != 2 {
		t.Errorf("expected two cancels, got %d", got)
	}
}

// Current mode asks the director for the running states. The director has no
// task type filter, so no type parameter is sent.
func TestGetTasksCurrentModeSendsTheRunningStates(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{directorTask(5, "processing"), directorTask(4, "queued")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	tasks, err := director.GetTasks(t.Context(), "current", 10, nil, "")
	if err != nil {
		t.Fatalf("GetTasks failed: %v", err)
	}

	if len(tasks) != 2 || tasks[0].ID != 5 || tasks[1].ID != 4 {
		t.Fatalf("expected tasks 5 and 4 in the director's order, got %+v", tasks)
	}

	lists := requestsTo(server, http.MethodGet, "/tasks")
	if len(lists) != 1 {
		t.Fatalf("expected one task list request, got %+v", server.APIRequests())
	}

	query, err := url.ParseQuery(lists[0].RawQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", lists[0].RawQuery, err)
	}

	if got := query.Get("state"); got != "processing,cancelling,queued" {
		t.Errorf("state = %q, want processing,cancelling,queued", got)
	}

	if query.Has("type") {
		t.Errorf("the query sent a type parameter, which the director does not support: %q", lists[0].RawQuery)
	}

	if query.Has("limit") {
		t.Errorf("current mode sent a limit, but it asks for every running task: %q", lists[0].RawQuery)
	}
}

// Recent mode asks for twice the limit, so the state and team filters that
// run in Blacksmith still have enough tasks to fill the page.
func TestGetTasksRecentModeSendsALimit(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{
			directorTask(9, "done"), directorTask(8, "error"), directorTask(7, "done"),
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	tasks, err := director.GetTasks(t.Context(), "recent", 10, []string{"done"}, "")
	if err != nil {
		t.Fatalf("GetTasks failed: %v", err)
	}

	if len(tasks) != 2 || tasks[0].ID != 9 || tasks[1].ID != 7 {
		t.Fatalf("expected the done tasks 9 and 7, got %+v", tasks)
	}

	lists := requestsTo(server, http.MethodGet, "/tasks")
	if len(lists) != 1 {
		t.Fatalf("expected one task list request, got %+v", server.APIRequests())
	}

	query, err := url.ParseQuery(lists[0].RawQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", lists[0].RawQuery, err)
	}

	want := url.Values{"limit": {"20"}, "verbose": {"1"}}
	if query.Encode() != want.Encode() {
		t.Errorf("task list query = %q, want %q", query.Encode(), want.Encode())
	}
}

// All mode merges the running tasks with the recent ones, newest first, with
// each task once.
func TestGetAllTasksMergesRunningAndRecentTasks(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		if request.URL.Query().Has("state") {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{directorTask(12, "processing"), directorTask(3, "queued")})
		} else {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{directorTask(12, "processing"), directorTask(11, "done"), directorTask(10, "error")})
		}

		return true
	})

	director := newReadAdapter(t, server, nil)

	tasks, err := director.GetAllTasks(t.Context(), 10)
	if err != nil {
		t.Fatalf("GetAllTasks failed: %v", err)
	}

	var ids []int
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}

	if len(ids) != 4 || ids[0] != 12 || ids[1] != 11 || ids[2] != 10 || ids[3] != 3 {
		t.Fatalf("expected tasks 12, 11, 10, and 3, got %v", ids)
	}

	if lists := requestsTo(server, http.MethodGet, "/tasks"); len(lists) != 2 {
		t.Fatalf("expected two task list requests, got %+v", server.APIRequests())
	}
}

// A task read carries the director's own fields and times.
func TestGetTaskMapsTheDirectorsFields(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks/42" {
			return false
		}

		task := directorTask(42, "error")
		task["result"] = "Action Failed get_task"
		task["context_id"] = "ctx-42"
		boshtest.WriteJSON(writer, http.StatusOK, task)

		return true
	})

	director := newReadAdapter(t, server, nil)

	task, err := director.GetTask(t.Context(), 42)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	want := bosh.Task{
		ID: 42, State: "error", Description: "create deployment", Timestamp: 1759766400,
		StartedAt: time.Unix(1759766000, 0), Result: "Action Failed get_task", User: "admin",
		Deployment: "valkey-1", ContextID: "ctx-42",
	}

	if task.EndedAt == nil || !task.EndedAt.Equal(time.Unix(1759766400, 0)) {
		t.Errorf("EndedAt = %v, want the director's timestamp for a finished task", task.EndedAt)
	}

	task.EndedAt = nil
	if !task.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", task.StartedAt, want.StartedAt)
	}

	task.StartedAt = want.StartedAt
	if *task != want {
		t.Errorf("GetTask = %+v, want %+v", *task, want)
	}
}

// A missing task keeps the director's answer, so a caller can tell a task the
// director has no record of from a failed request.
func TestGetTaskOfAMissingTaskKeepsTheDirectorsAnswer(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks/404" {
			return false
		}

		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeTaskNotFound, "Task 404 not found")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetTask(t.Context(), 404)
	if err == nil {
		t.Fatal("expected an error for a missing task")
	}

	for _, want := range []string{server.URL, "404", "Task 404 not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %s", want, err.Error())
		}
	}
}

// Task output types map onto the director's output types. The UI's "task"
// view is the event log.
func TestGetTaskOutputMapsTheOutputType(t *testing.T) {
	tests := []struct{ asked, sent string }{
		{asked: "task", sent: "event"},
		{asked: "event", sent: "event"},
		{asked: "debug", sent: "debug"},
		{asked: "result", sent: "result"},
		{asked: "cpi", sent: "cpi"},
		{asked: "", sent: "debug"},
		{asked: "anything", sent: "debug"},
	}

	for _, testCase := range tests {
		t.Run("type="+testCase.asked, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodGet || request.URL.Path != "/tasks/42/output" {
					return false
				}

				boshtest.WriteText(writer, http.StatusOK, "output of type "+request.URL.Query().Get("type"))

				return true
			})

			director := newReadAdapter(t, server, nil)

			output, err := director.GetTaskOutput(t.Context(), 42, testCase.asked)
			if err != nil {
				t.Fatalf("GetTaskOutput failed: %v", err)
			}

			if output != "output of type "+testCase.sent {
				t.Errorf("output = %q, want the %s output", output, testCase.sent)
			}
		})
	}
}

// Task events come from the event output, one JSON object a line, and lines
// that are not events are skipped.
func TestGetTaskEventsParsesTheEventOutput(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks/42/output" || request.URL.Query().Get("type") != "event" {
			return false
		}

		boshtest.WriteText(writer, http.StatusOK,
			`{"time":1759766000,"stage":"Preparing deployment","tags":[],"total":1,"task":"Preparing deployment","index":1,"state":"started","progress":0}`+"\n"+
				"not an event\n"+
				`{"time":1759766010,"stage":"Updating instance","tags":["valkey"],"total":1,"task":"valkey/0","index":1,"state":"failed","progress":100,"error":{"code":450001,"message":"Timed out"}}`+"\n")

		return true
	})

	director := newReadAdapter(t, server, nil)

	events, err := director.GetTaskEvents(t.Context(), 42)
	if err != nil {
		t.Fatalf("GetTaskEvents failed: %v", err)
	}

	if len(events) != 2 || events[0].Stage != "Preparing deployment" || events[1].State != "failed" ||
		events[1].Error == nil || events[1].Error.Code != 450001 {
		t.Fatalf("expected two parsed events with the failure's error, got %+v", events)
	}
}

// Each outcome for a director task adds its own sentence to the error, so an
// operator knows whether the task still runs.
func TestDirectorCallErrorNamesTheTaskOutcome(t *testing.T) {
	tests := []struct {
		outcome bosh.TaskOutcome
		want    string
	}{
		{outcome: bosh.TaskLeftRunning, want: "BOSH task 42 is still running; Blacksmith stopped waiting and did not cancel it"},
		{outcome: bosh.TaskCancelRequested, want: "Blacksmith asked the director to cancel BOSH task 42"},
		{outcome: bosh.TaskCancelFailed, want: "Blacksmith tried to cancel BOSH task 42, and the cancel failed"},
	}

	for _, testCase := range tests {
		t.Run(testCase.outcome.String(), func(t *testing.T) {
			err := &bosh.DirectorCallError{
				Operation: "WaitForTask",
				Director:  "https://127.0.0.1:25555",
				Cause:     context.Canceled,
				Err:       context.Canceled,
				TaskID:    42,
				Task:      testCase.outcome,
			}

			message := err.Error()
			for _, want := range []string{testCase.want, "/b/tasks/42"} {
				if !strings.Contains(message, want) {
					t.Errorf("expected the error to mention %q, got: %s", want, message)
				}
			}
		})
	}

	plain := (&bosh.DirectorCallError{Operation: "GetInfo", Director: "https://127.0.0.1:25555", Cause: context.Canceled, Err: context.Canceled}).Error()
	if strings.Contains(plain, "/b/tasks/") {
		t.Errorf("a call with no task names no task path: %s", plain)
	}
}
