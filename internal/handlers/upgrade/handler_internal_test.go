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

	return &upgrade.UpgradeTask{ID: "task-1", Kind: upgrade.TaskKindCertificates, Name: req.Name, ClientRequestID: req.ClientRequestID}, existing, nil
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

	tasks := &certTasks{}
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks}

	body := strings.Replace(certificateCreateBody, `"reason"`, `"created_from":"spoofed","reason"`, 1)

	status, answer := postCertificateTask(t, handler, body)
	if status != http.StatusCreated {
		t.Fatalf("status %d, body %v", status, answer)
	}

	if _, set := answer["created_from"]; set {
		t.Fatalf("the task must not carry the client's created_from: %v", answer)
	}

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
