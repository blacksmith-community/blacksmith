package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/mutation"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

// fakeTasks is a taskManager whose task calls all fail with err.
type fakeTasks struct {
	taskManager

	err error
}

func (f *fakeTasks) GetTask(string) (*upgrade.UpgradeTask, error) { return nil, f.err }
func (f *fakeTasks) PauseTask(context.Context, string) error      { return f.err }
func (f *fakeTasks) ResumeTask(context.Context, string) error     { return f.err }
func (f *fakeTasks) CancelTask(context.Context, string) error     { return f.err }

func (f *fakeTasks) CreateTask(context.Context, upgrade.CreateTaskRequest) (*upgrade.UpgradeTask, error) {
	return nil, f.err
}

func serve(t *testing.T, method, path string, err error) (int, map[string]interface{}) {
	t.Helper()

	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: &fakeTasks{err: err}}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))

	var body map[string]interface{}

	jsonErr := json.Unmarshal(rec.Body.Bytes(), &body)
	if jsonErr != nil {
		t.Fatalf("body is not valid JSON: %v", jsonErr)
	}

	return rec.Code, body
}

func TestTaskActionErrorsMapSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not found", upgrade.ErrTaskNotFound, http.StatusNotFound, "instance_not_found"},
		{"wrapped not found", fmt.Errorf("x: %w", upgrade.ErrTaskNotFound), http.StatusNotFound, "instance_not_found"},
		{"not running", upgrade.ErrTaskNotRunning, http.StatusConflict, "invalid_task_state"},
		{"not paused", upgrade.ErrTaskNotPaused, http.StatusConflict, "invalid_task_state"},
		{"other", errors.New(`task "x" broke`), http.StatusInternalServerError, "internal_error"},
	}

	for _, action := range []string{"pause", "resume", "cancel"} {
		for _, test := range tests {
			t.Run(action+"/"+test.name, func(t *testing.T) {
				t.Parallel()

				status, body := serve(t, http.MethodPost, "/b/upgrade/tasks/x/"+action, test.err)
				if status != test.status {
					t.Fatalf("status = %d, want %d", status, test.status)
				}

				if body["code"] != test.code || body["success"] != false || body["error"] != test.err.Error() {
					t.Fatalf("unexpected body: %v", body)
				}
			})
		}
	}
}

func TestGetTaskNotFoundIsCoded(t *testing.T) {
	t.Parallel()

	status, body := serve(t, http.MethodGet, "/b/upgrade/tasks/x", upgrade.ErrTaskNotFound)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}

	if body["code"] != "instance_not_found" || body["error"] != "task not found" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestCreateBusyIsCodedWithTheHolder(t *testing.T) {
	t.Parallel()

	busy := &mutation.BusyError{Holder: mutation.Holder{Kind: mutation.KindCertificates, Description: "certificate rotation of redis-7a1b"}}

	for name, err := range map[string]error{
		"busy error":         busy,
		"wrapped busy error": fmt.Errorf("%w, so instance 7a1b can't be upgraded", busy),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{logger: logger.NewNoOpLogger(), tasks: &fakeTasks{err: err}}
			rec := httptest.NewRecorder()
			body := strings.NewReader(`{"instance_ids":["7a1b"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`)
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", body))

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}

			var answer map[string]interface{}

			jsonErr := json.Unmarshal(rec.Body.Bytes(), &answer)
			if jsonErr != nil {
				t.Fatalf("body is not valid JSON: %v", jsonErr)
			}

			message, _ := answer["error"].(string)
			if answer["code"] != "instance_busy" || answer["success"] != false || !strings.Contains(message, "certificate rotation of redis-7a1b") {
				t.Fatalf("unexpected body: %v", answer)
			}

			if len(answer) != 3 {
				t.Fatalf("body = %v, want only success, error, and code", answer)
			}
		})
	}
}

// certTasks is a taskManager that records each certificate create request
// and answers with one task, reporting it as existing once the request ID
// has been seen.
type certTasks struct {
	taskManager

	// createdFrom is where the manager says each task was created.
	createdFrom string

	mu       sync.Mutex
	requests []upgrade.CreateCertificateTaskRequest
}

func (c *certTasks) CreateCertificateTask(_ context.Context, req upgrade.CreateCertificateTaskRequest) (*upgrade.UpgradeTask, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing := false

	for _, seen := range c.requests {
		if seen.ClientRequestID == req.ClientRequestID {
			existing = true
		}
	}

	c.requests = append(c.requests, req)

	return &upgrade.UpgradeTask{ID: "task-1", Kind: upgrade.TaskKindCertificates, Name: req.Name, ClientRequestID: req.ClientRequestID, CreatedFrom: c.createdFrom}, existing, nil
}

// postCertificateTask sends the body to POST /b/upgrade/tasks and returns
// the status and the task the answer carries.
func postCertificateTask(t *testing.T, handler *Handler, body string) (int, map[string]interface{}) {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", strings.NewReader(body)))

	var answer map[string]interface{}

	err := json.Unmarshal(rec.Body.Bytes(), &answer)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	return rec.Code, answer
}

const certificateCreateBody = `{"kind":"certificates","preview_id":"pv-5d0c","client_request_id":"3b8f","name":"CHG-1234 Redis leaves","reason":"CHG-1234"}`

func TestCreateHandlerAnswers200OnRetry(t *testing.T) {
	t.Parallel()

	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: &certTasks{}}

	status, first := postCertificateTask(t, handler, certificateCreateBody)
	if status != http.StatusCreated || first["id"] != "task-1" {
		t.Fatalf("first create: status %d, body %v", status, first)
	}

	status, retried := postCertificateTask(t, handler, certificateCreateBody)
	if status != http.StatusOK || retried["id"] != "task-1" {
		t.Fatalf("retried create: status %d, body %v", status, retried)
	}
}

func TestCreateHandlerIgnoresClientCreatedFrom(t *testing.T) {
	t.Parallel()

	tasks := &certTasks{createdFrom: "api"}
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks}

	body := strings.Replace(certificateCreateBody, `"reason"`, `"created_from":"spoofed","reason"`, 1)

	status, answer := postCertificateTask(t, handler, body)
	if status != http.StatusCreated {
		t.Fatalf("status %d, body %v", status, answer)
	}

	// The answer carries where the manager says the task was created,
	// never the value the client sent.
	if answer["created_from"] != "api" {
		t.Fatalf("expected the manager's created_from %q, got %v", "api", answer)
	}

	// The request the manager gets has no created_from. If the request type
	// ever gained that field, the client's value would show up here and
	// the comparison would fail.
	want := upgrade.CreateCertificateTaskRequest{PreviewID: "pv-5d0c", ClientRequestID: "3b8f", Name: "CHG-1234 Redis leaves", Reason: "CHG-1234"}
	if len(tasks.requests) != 1 || tasks.requests[0] != want {
		t.Fatalf("expected the manager to get %+v, got %+v", want, tasks.requests)
	}
}

func TestCreateHandlerMapsCertificateErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err    error
		status int
		code   string
	}{
		"expired preview": {apierr.New(http.StatusGone, apierr.CodePreviewExpired, "The preview has expired."), http.StatusGone, apierr.CodePreviewExpired},
		"conflict":        {apierr.New(http.StatusConflict, apierr.CodeRequestIDConflict, "used before"), http.StatusConflict, apierr.CodeRequestIDConflict},
		"loading":         {upgrade.ErrTasksLoading, http.StatusServiceUnavailable, apierr.CodeTasksLoading},
		"other":           {errors.New("broke"), http.StatusInternalServerError, apierr.CodeInternal},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{logger: logger.NewNoOpLogger(), tasks: &failingCertTasks{err: test.err}}

			status, answer := postCertificateTask(t, handler, certificateCreateBody)
			if status != test.status || answer["code"] != test.code || answer["success"] != false || answer["error"] != test.err.Error() {
				t.Fatalf("status %d, body %v", status, answer)
			}
		})
	}

	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: &certTasks{}}

	status, answer := postCertificateTask(t, handler, `{"kind":"certificates","name":5}`)
	if status != http.StatusBadRequest || answer["code"] != apierr.CodeNoSelections {
		t.Fatalf("a body that does not decode: status %d, body %v", status, answer)
	}
}

// failingCertTasks is a taskManager whose certificate creates fail with err.
type failingCertTasks struct {
	taskManager

	err error
}

func (f *failingCertTasks) CreateCertificateTask(context.Context, upgrade.CreateCertificateTaskRequest) (*upgrade.UpgradeTask, bool, error) {
	return nil, false, f.err
}

// decisionTasks is a taskManager that records each decision call and
// answers it with err, and answers GetTask with the task.
type decisionTasks struct {
	taskManager

	err error

	mu    sync.Mutex
	calls []string
	got   []any
}

func (d *decisionTasks) record(call string, req any) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, call)
	d.got = append(d.got, req)

	return d.err
}

func (d *decisionTasks) GetTask(taskID string) (*upgrade.UpgradeTask, error) {
	return &upgrade.UpgradeTask{ID: taskID, Kind: upgrade.TaskKindCertificates}, nil
}

func (d *decisionTasks) ReconcileRegeneration(_ context.Context, taskID, instanceID string, req upgrade.ReconcileRequest) error {
	return d.record("reconcile "+taskID+" "+instanceID, req)
}

func (d *decisionTasks) ApproveChangedEffects(_ context.Context, taskID, instanceID string, req upgrade.ApproveRequest) error {
	return d.record("approve "+taskID+" "+instanceID, req)
}

func (d *decisionTasks) ResolveDeployOutcome(_ context.Context, taskID, instanceID string, req upgrade.ResolveRequest) error {
	return d.record("resolve "+taskID+" "+instanceID, req)
}

func (d *decisionTasks) RepreviewForDecision(_ context.Context, taskID, instanceID string) (*upgrade.Preview, error) {
	err := d.record("preview "+taskID+" "+instanceID, nil)
	if err != nil {
		return nil, err
	}

	return &upgrade.Preview{ID: "pv-bound", Instances: []upgrade.PreviewInstance{}, Warnings: []string{}}, nil
}

// postDecision sends the body to the path and returns the status and the
// decoded answer.
func postDecision(t *testing.T, handler *Handler, path, body string) (int, map[string]interface{}) {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body)))

	var answer map[string]interface{}

	err := json.Unmarshal(rec.Body.Bytes(), &answer)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	return rec.Code, answer
}

// Each decision route reaches its manager call with the decoded body and
// answers with the task, and the re-preview answers with the preview.
func TestDecisionRoutes(t *testing.T) {
	t.Parallel()

	tasks := &decisionTasks{}
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks}

	routes := []struct {
		path, body, call string
		want             any
	}{
		{
			"/b/upgrade/tasks/t1/instances/i1/reconcile",
			`{"variable":"/d/dep/leaf","action":"adopt","version_id":"v2","client_request_id":"r1"}`,
			"reconcile t1 i1",
			upgrade.ReconcileRequest{Variable: "/d/dep/leaf", Action: "adopt", VersionID: "v2", ClientRequestID: "r1"},
		},
		{
			"/b/upgrade/tasks/t1/instances/i1/approve",
			`{"preview_id":"pv-bound","client_request_id":"r2"}`,
			"approve t1 i1",
			upgrade.ApproveRequest{PreviewID: "pv-bound", ClientRequestID: "r2"},
		},
		{
			"/b/upgrade/tasks/t1/instances/i1/resolve",
			`{"action":"verify"}`,
			"resolve t1 i1",
			upgrade.ResolveRequest{Action: "verify"},
		},
	}

	for _, route := range routes {
		status, answer := postDecision(t, handler, route.path, route.body)
		if status != http.StatusOK || answer["id"] != "t1" {
			t.Fatalf("%s: status %d, body %v", route.path, status, answer)
		}

		tasks.mu.Lock()
		call, got := tasks.calls[len(tasks.calls)-1], tasks.got[len(tasks.got)-1]
		tasks.mu.Unlock()

		if call != route.call || got != route.want {
			t.Fatalf("%s: called %q with %+v, want %q with %+v", route.path, call, got, route.call, route.want)
		}
	}

	status, answer := postDecision(t, handler, "/b/upgrade/tasks/t1/instances/i1/preview", "")
	if status != http.StatusOK || answer["preview_id"] != "pv-bound" {
		t.Fatalf("preview: status %d, body %v", status, answer)
	}
}

// A decision error answers with its own status and code, and a body that
// does not decode is a bad request that reaches no manager call.
func TestDecisionRoutesAnswerErrors(t *testing.T) {
	t.Parallel()

	tasks := &decisionTasks{err: apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision, "The instance waits for no decision.")}
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks}

	for _, action := range []string{"reconcile", "approve", "resolve", "preview"} {
		status, answer := postDecision(t, handler, "/b/upgrade/tasks/t1/instances/i1/"+action, `{}`)
		if status != http.StatusConflict || answer["code"] != apierr.CodeNotAwaitingDecision || answer["success"] != false ||
			answer["error"] != "The instance waits for no decision." {
			t.Fatalf("%s: status %d, body %v", action, status, answer)
		}
	}

	tasks.mu.Lock()
	before := len(tasks.calls)
	tasks.mu.Unlock()

	status, answer := postDecision(t, handler, "/b/upgrade/tasks/t1/instances/i1/reconcile", `{"action":`)
	if status != http.StatusBadRequest || answer["code"] != apierr.CodeInvalidMode || answer["success"] != false {
		t.Fatalf("undecodable body: status %d, body %v", status, answer)
	}

	tasks.mu.Lock()
	after := len(tasks.calls)
	tasks.mu.Unlock()

	if after != before {
		t.Fatalf("expected no manager call for an undecodable body, got %d", after-before)
	}
}
