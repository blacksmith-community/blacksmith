package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	managedDeployment = "valkey-1"
	otherDeployment   = "cf-prod"
	ownDeployment     = "lab-blacksmith"
	stateProcessing   = "processing"
	valkeyService     = "valkey"
	outputTypeTask    = "task"
	managedTaskRoute  = "/b/tasks/1"
	managedOutputPath = "/b/tasks/1/output"
	managedCancelPath = "/b/tasks/1/cancel"
)

var errIndexDown = errors.New("vault is down")

// brokenIndexVault fails every read.
type brokenIndexVault struct{ *testutil.InstanceVault }

func (brokenIndexVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errIndexDown
}

type taskScenario struct {
	handler  *tasks.Handler
	director *fakeDirector
}

func newTaskScenario(t *testing.T, vault interfaces.Vault) taskScenario {
	t.Helper()

	director := newFakeDirector()
	director.taskDetails[1] = bosh.Task{ID: 1, State: stateProcessing, Deployment: managedDeployment}
	director.taskDetails[2] = bosh.Task{ID: 2, State: stateProcessing, Deployment: otherDeployment}
	director.taskDetails[3] = bosh.Task{ID: 3, State: stateProcessing}
	director.taskDetails[4] = bosh.Task{ID: 4, State: stateProcessing, Deployment: ownDeployment}

	for id := 1; id <= 4; id++ {
		director.taskOutputs[outputKey{id: id, typ: outputTypeTask}] = "output"
		director.taskOutputs[outputKey{id: id, typ: "result"}] = ""
	}

	ownFile := filepath.Join(t.TempDir(), "deployment")

	err := os.WriteFile(ownFile, []byte(ownDeployment+"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return taskScenario{
		handler: tasks.NewHandler(tasks.Dependencies{
			Logger: logger.NewNoOpLogger(), Director: director, Vault: vault, OwnNameFile: ownFile,
		}),
		director: director,
	}
}

func managingTaskVault() *testutil.InstanceVault {
	return &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"abc": {ID: "abc", ServiceID: valkeyService, PlanID: valkeyService, DeploymentName: managedDeployment},
		"old": {ID: "old", ServiceID: valkeyService, PlanID: valkeyService, DeploymentName: otherDeployment, Status: vaultPkg.StatusDeleted},
	}}
}

func (s taskScenario) call(t *testing.T, method, route string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), method, route, strings.NewReader("{}")))

	return recorder
}

func codeOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	return body.Code
}

// The task routes answer only for tasks of the broker's own service
// instances. A task of another deployment, or of one whose instance is gone,
// is refused on every route with deployment_not_managed.
func TestTaskRoutesRefuseATaskOfAnUnmanagedDeployment(t *testing.T) {
	t.Parallel()

	scenario := newTaskScenario(t, managingTaskVault())

	routes := []struct{ method, path string }{
		{http.MethodGet, "/b/tasks/2"},
		{http.MethodGet, "/b/tasks/2/output"},
		{http.MethodPost, "/b/tasks/2/cancel"},
	}

	for _, route := range routes {
		recorder := scenario.call(t, route.method, route.path)

		if recorder.Code != http.StatusForbidden || codeOf(t, recorder) != apierr.CodeDeploymentNotManaged {
			t.Errorf("%s %s = %d %q, want 403 %s", route.method, route.path, recorder.Code, codeOf(t, recorder), apierr.CodeDeploymentNotManaged)
		}
	}

	if len(scenario.director.cancelledTasks) != 0 {
		t.Errorf("cancelled %v, want no cancel of an unmanaged task", scenario.director.cancelledTasks)
	}
}

// A task of a managed deployment reads and cancels as before.
func TestTaskRoutesServeATaskOfAManagedDeployment(t *testing.T) {
	t.Parallel()

	scenario := newTaskScenario(t, managingTaskVault())

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, managedTaskRoute},
		{http.MethodGet, managedOutputPath},
		{http.MethodPost, managedCancelPath},
	} {
		recorder := scenario.call(t, route.method, route.path)

		if recorder.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200: %s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}

	if len(scenario.director.cancelledTasks) != 1 || scenario.director.cancelledTasks[0] != 1 {
		t.Errorf("cancelled %v, want task 1", scenario.director.cancelledTasks)
	}
}

// A director-level task, with no deployment, and a task of the broker's own
// deployment can be read, and cancelling either is refused.
func TestTaskRoutesReadButNeverCancelADirectorOrOwnTask(t *testing.T) {
	t.Parallel()

	scenario := newTaskScenario(t, managingTaskVault())

	for _, taskNumber := range []string{"3", "4"} {
		for _, suffix := range []string{"", "/output"} {
			recorder := scenario.call(t, http.MethodGet, "/b/tasks/"+taskNumber+suffix)
			if recorder.Code != http.StatusOK {
				t.Errorf("GET /b/tasks/%s%s = %d, want 200", taskNumber, suffix, recorder.Code)
			}
		}

		recorder := scenario.call(t, http.MethodPost, "/b/tasks/"+taskNumber+"/cancel")
		if recorder.Code != http.StatusForbidden || codeOf(t, recorder) != apierr.CodeDeploymentNotManaged {
			t.Errorf("cancel of task %s = %d %q, want 403 %s", taskNumber, recorder.Code, codeOf(t, recorder), apierr.CodeDeploymentNotManaged)
		}
	}

	if len(scenario.director.cancelledTasks) != 0 {
		t.Errorf("cancelled %v, want no cancel", scenario.director.cancelledTasks)
	}
}

// When the index can't be read, or no Vault is wired, the routes answer 503
// with vault_unavailable, and nothing is cancelled, because nobody knows
// whether the broker owns the deployment.
func TestTaskRoutesAnswer503WhenTheIndexCannotBeRead(t *testing.T) {
	t.Parallel()

	for name, vault := range map[string]interfaces.Vault{
		"an unreadable index": brokenIndexVault{managingTaskVault()},
		"no vault":            nil,
	} {
		scenario := newTaskScenario(t, vault)

		for _, route := range []struct{ method, path string }{
			{http.MethodGet, managedTaskRoute},
			{http.MethodGet, managedOutputPath},
			{http.MethodPost, managedCancelPath},
		} {
			recorder := scenario.call(t, route.method, route.path)

			if recorder.Code != http.StatusServiceUnavailable || codeOf(t, recorder) != apierr.CodeVaultUnavailable {
				t.Errorf("%s: %s %s = %d %q, want 503 %s", name, route.method, route.path, recorder.Code, codeOf(t, recorder), apierr.CodeVaultUnavailable)
			}
		}

		if len(scenario.director.cancelledTasks) != 0 {
			t.Errorf("%s: cancelled %v", name, scenario.director.cancelledTasks)
		}
	}
}

// A team parameter from the caller changes nothing. The routes never read
// it, so a caller can't widen what a route answers.
func TestTaskRoutesIgnoreTheCallersTeam(t *testing.T) {
	t.Parallel()

	scenario := newTaskScenario(t, managingTaskVault())

	recorder := scenario.call(t, http.MethodGet, "/b/tasks/2?team=everyone")
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 whatever the team", recorder.Code)
	}

	recorder = scenario.call(t, http.MethodGet, "/b/tasks/1/output?team=everyone")
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

// A task the director doesn't know is a 404, not a gate answer.
func TestTaskRoutesAnswer404ForAnUnknownTask(t *testing.T) {
	t.Parallel()

	scenario := newTaskScenario(t, managingTaskVault())

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/b/tasks/99"},
		{http.MethodGet, "/b/tasks/99/output"},
		{http.MethodPost, "/b/tasks/99/cancel"},
	} {
		if recorder := scenario.call(t, route.method, route.path); recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", route.method, route.path, recorder.Code)
		}
	}
}

// Only the director's own answer that a task does not exist gives a 404.
// An outage, a timeout, or any other failure of the read answers 502, so an
// operator never reads a director that could not answer as a task that is
// gone. A failed read of the output answers the same way.
func TestTaskRoutesAnswer502WhenTheDirectorFails(t *testing.T) {
	t.Parallel()

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, managedTaskRoute},
		{http.MethodGet, managedOutputPath},
		{http.MethodPost, managedCancelPath},
	} {
		scenario := newTaskScenario(t, managingTaskVault())
		scenario.director.taskDetailErr[1] = errDirectorDown

		recorder := scenario.call(t, route.method, route.path)
		if recorder.Code != http.StatusBadGateway || codeOf(t, recorder) != apierr.CodeInternal {
			t.Errorf("%s %s = %d %q, want 502 %s", route.method, route.path, recorder.Code, codeOf(t, recorder), apierr.CodeInternal)
		}

		if len(scenario.director.cancelledTasks) != 0 {
			t.Errorf("%s %s cancelled %v", route.method, route.path, scenario.director.cancelledTasks)
		}
	}

	scenario := newTaskScenario(t, managingTaskVault())
	scenario.director.taskOutputErr[outputKey{id: 1, typ: outputTypeTask}] = errDirectorDown

	if recorder := scenario.call(t, http.MethodGet, managedOutputPath); recorder.Code != http.StatusBadGateway {
		t.Errorf("a failed output read = %d, want 502", recorder.Code)
	}

	scenario.director.taskOutputErr[outputKey{id: 1, typ: outputTypeTask}] = errFakeNotFound

	if recorder := scenario.call(t, http.MethodGet, managedOutputPath); recorder.Code != http.StatusNotFound {
		t.Errorf("an output the director does not have = %d, want 404", recorder.Code)
	}
}
