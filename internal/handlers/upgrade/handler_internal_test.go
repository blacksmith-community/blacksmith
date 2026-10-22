package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
