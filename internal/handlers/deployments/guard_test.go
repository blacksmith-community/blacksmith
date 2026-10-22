package deployments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

const (
	guardedDeployment = "redis-7a1b"
	queuedTaskState   = "queued"
)

var errGuardDirectorRefused = errors.New("director refused the request")

// recordingDirector records each mutation the handler sends it, notes
// whether the guard held the deployment at that moment, and answers with
// a queued task or, when fail is set, an error.
type recordingDirector struct {
	*testutil.MockBOSHDirector

	guard *mutation.Guard
	fail  bool

	mu          sync.Mutex
	calls       []string
	heldOnCalls []bool
}

func newRecordingDirector(guard *mutation.Guard, fail bool) *recordingDirector {
	return &recordingDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), guard: guard, fail: fail}
}

func (d *recordingDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	return d.task("DeleteDeployment", name)
}

func (d *recordingDirector) UpdateDeployment(_ context.Context, name, _ string) (*bosh.Task, error) {
	return d.task("UpdateDeployment", name)
}

func (d *recordingDirector) RecreateDeployment(_ context.Context, name string, _ bosh.RecreateOpts) (*bosh.Task, error) {
	return d.task("RecreateDeployment", name)
}

func (d *recordingDirector) RestartDeployment(_ context.Context, name string, _ bosh.RestartOpts) (*bosh.Task, error) {
	return d.task("RestartDeployment", name)
}

func (d *recordingDirector) StopDeployment(_ context.Context, name string, _ bosh.StopOpts) (*bosh.Task, error) {
	return d.task("StopDeployment", name)
}

func (d *recordingDirector) StartDeployment(_ context.Context, name string, _ bosh.StartOpts) (*bosh.Task, error) {
	return d.task("StartDeployment", name)
}

func (d *recordingDirector) RunErrand(_ context.Context, deployment, _ string, _ bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	err := d.record("RunErrand", deployment)
	if err != nil {
		return nil, err
	}

	return &bosh.ErrandResult{}, nil
}

func (d *recordingDirector) record(method, deployment string) error {
	_, held := d.guard.Holder("", deployment)

	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, method)
	d.heldOnCalls = append(d.heldOnCalls, held)

	if d.fail {
		return errGuardDirectorRefused
	}

	return nil
}

func (d *recordingDirector) recorded() ([]string, []bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.calls...), append([]bool(nil), d.heldOnCalls...)
}

func (d *recordingDirector) task(method, deployment string) (*bosh.Task, error) {
	err := d.record(method, deployment)
	if err != nil {
		return nil, err
	}

	return &bosh.Task{ID: acceptedTaskID, State: queuedTaskState}, nil
}

// deploymentAction is one mutating route of the deployments handler and
// the director method it ends in.
type deploymentAction struct {
	name   string
	method string
	path   string
	body   string
	call   string
	// accepted is the status the route answers when the director
	// accepts the request.
	accepted int
}

func deploymentActions() []deploymentAction {
	base := "/b/deployments/" + guardedDeployment

	return []deploymentAction{
		{name: "restart", method: http.MethodPost, path: base + "/restart", call: "RestartDeployment", accepted: http.StatusOK},
		{name: "recreate", method: http.MethodPost, path: base + "/recreate", call: "RecreateDeployment", accepted: http.StatusOK},
		{name: "stop", method: http.MethodPost, path: base + "/stop", call: "StopDeployment", accepted: http.StatusOK},
		{name: "start", method: http.MethodPost, path: base + "/start", call: "StartDeployment", accepted: http.StatusOK},
		{name: "errand", method: http.MethodPost, path: base + "/errands/smoke-tests", call: "RunErrand", accepted: http.StatusOK},
		{name: "delete", method: http.MethodDelete, path: base, call: "DeleteDeployment", accepted: http.StatusOK},
		{
			name: "manifest update", method: http.MethodPost, path: base + "/manifest",
			body: `{"name":"` + guardedDeployment + `"}`, call: "UpdateDeployment",
			// No vault is configured, so storing the accepted manifest
			// fails after the director call returns.
			accepted: http.StatusInternalServerError,
		},
	}
}

func serveAction(t *testing.T, handler *deployments.Handler, action deploymentAction) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), action.method, action.path, strings.NewReader(action.body))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	return recorder
}

// While a rotation holds the instance, every mutating deployment action
// answers 409 with instance_busy and the holder's description, and sends
// nothing to the director.
func TestDeploymentActionsBlockedWhileRotating(t *testing.T) {
	t.Parallel()

	for _, action := range deploymentActions() {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			guard := mutation.NewGuard(time.Now)

			release, err := guard.Acquire("7a1b", guardedDeployment, mutation.Holder{
				Kind:        mutation.KindCertificates,
				TaskID:      "rotation-1",
				Description: "certificate rotation of redis-7a1b",
			})
			if err != nil {
				t.Fatalf("holding the instance: %v", err)
			}
			defer release()

			director := newRecordingDirector(guard, false)
			handler := deployments.NewHandler(deployments.Dependencies{
				Logger:   logger.NewNoOpLogger(),
				Director: director,
				Guard:    guard,
			})

			recorder := serveAction(t, handler, action)

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
			}

			var body map[string]interface{}

			err = json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
			}

			if body["code"] != apierr.CodeInstanceBusy || body["success"] != false {
				t.Errorf("body = %v, want success false and code %s", body, apierr.CodeInstanceBusy)
			}

			message, _ := body["error"].(string)
			if !strings.Contains(message, "certificate rotation of redis-7a1b") {
				t.Errorf("error = %q, want the holder's description", message)
			}

			calls, _ := director.recorded()
			if len(calls) != 0 {
				t.Errorf("director calls = %v, want none while the instance is held", calls)
			}

			holder, held := guard.Holder("7a1b", guardedDeployment)
			if !held || holder.Kind != mutation.KindCertificates {
				t.Errorf("Holder = %+v, %v, want the rotation still holding", holder, held)
			}
		})
	}
}

// Each mutating action holds the deployment while it submits its task and
// frees it when the handler returns, whether the director accepted the
// request or refused it.
func TestDeploymentActionsReleaseGuard(t *testing.T) {
	t.Parallel()

	for _, fail := range []bool{false, true} {
		outcome := "accepted"
		if fail {
			outcome = "refused"
		}

		for _, action := range deploymentActions() {
			t.Run(action.name+" "+outcome, func(t *testing.T) {
				t.Parallel()

				guard := mutation.NewGuard(time.Now)
				director := newRecordingDirector(guard, fail)
				handler := deployments.NewHandler(deployments.Dependencies{
					Logger:   logger.NewNoOpLogger(),
					Director: director,
					Guard:    guard,
				})

				recorder := serveAction(t, handler, action)

				if !fail && recorder.Code != action.accepted {
					t.Fatalf("status = %d, want %d: %s", recorder.Code, action.accepted, recorder.Body.String())
				}

				if fail && recorder.Code < http.StatusBadRequest {
					t.Fatalf("status = %d, want an error status: %s", recorder.Code, recorder.Body.String())
				}

				calls, heldOnCalls := director.recorded()
				if len(calls) != 1 || calls[0] != action.call {
					t.Fatalf("director calls = %v, want one %s", calls, action.call)
				}

				if !heldOnCalls[0] {
					t.Errorf("the guard did not hold %s while %s ran", guardedDeployment, action.call)
				}

				if holder, held := guard.Holder("7a1b", guardedDeployment); held {
					t.Errorf("guard still held by %+v after the handler returned", holder)
				}
			})
		}
	}
}

// Until the stored upgrade and certificate tasks are loaded, a stored task
// may still own the deployment without holding it in the guard, so every
// mutating deployment action answers 503 with tasks_loading, sends nothing
// to the director, and holds nothing. Once they are loaded, the action
// goes ahead.
func TestDeploymentActionsWaitForLoadedTasks(t *testing.T) {
	t.Parallel()

	for _, action := range deploymentActions() {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			guard := mutation.NewGuard(time.Now)
			loaded := make(chan struct{})
			director := newRecordingDirector(guard, false)
			handler := deployments.NewHandler(deployments.Dependencies{
				Logger:      logger.NewNoOpLogger(),
				Director:    director,
				Guard:       guard,
				TasksLoaded: loaded,
			})

			recorder := serveAction(t, handler, action)

			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
			}

			var body map[string]interface{}

			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
			}

			if body["code"] != apierr.CodeTasksLoading || body["success"] != false || len(body) != 3 {
				t.Errorf("body = %v, want success false and code %s", body, apierr.CodeTasksLoading)
			}

			if calls, _ := director.recorded(); len(calls) != 0 {
				t.Errorf("director calls = %v, want none while the tasks load", calls)
			}

			if held := guard.Snapshot(); len(held) != 0 {
				t.Errorf("guard holds %v, want nothing while the tasks load", held)
			}

			close(loaded)

			recorder = serveAction(t, handler, action)
			if recorder.Code != action.accepted {
				t.Fatalf("status once loaded = %d, want %d: %s", recorder.Code, action.accepted, recorder.Body.String())
			}
		})
	}
}
