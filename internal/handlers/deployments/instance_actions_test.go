package deployments_test

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
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	ownedInstance   = "7a1b"
	ownedDeployment = "redis-small-7a1b"
	instanceTaskID  = 4321
	actionRestart   = "restart"
	actionRecreate  = "recreate"

	// directorCredential is the basic-auth user and password of the test
	// director.
	directorCredential = "admin"
)

var errInstanceDirectorRefused = errors.New("director refused the job-state change")

// instanceDirector records each restart and recreate it receives, with the
// options and whether the guard held both the instance and its deployment at
// that moment. With fail set it refuses each one.
type instanceDirector struct {
	*testutil.MockBOSHDirector

	guard   *mutation.Guard
	fail    error
	running []bosh.Task
	listErr error

	mu       sync.Mutex
	lookups  []string
	calls    []string
	held     []bool
	restarts []bosh.RestartOpts
	recreate []bosh.RecreateOpts
}

func newInstanceDirector(guard *mutation.Guard) *instanceDirector {
	return &instanceDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), guard: guard}
}

// FindRunningTasksForDeployment answers with the running tasks set on the
// fake, or with listErr, and records which deployment it was asked about.
func (d *instanceDirector) FindRunningTasksForDeployment(_ context.Context, deployment string) ([]bosh.Task, error) {
	d.mu.Lock()
	d.lookups = append(d.lookups, deployment)
	d.mu.Unlock()

	if d.listErr != nil {
		return nil, d.listErr
	}

	return d.running, nil
}

func (d *instanceDirector) RestartDeployment(_ context.Context, name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	d.mu.Lock()
	d.restarts = append(d.restarts, opts)
	d.mu.Unlock()

	return d.task("RestartDeployment", name)
}

func (d *instanceDirector) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	d.mu.Lock()
	d.recreate = append(d.recreate, opts)
	d.mu.Unlock()

	return d.task("RecreateDeployment", name)
}

func (d *instanceDirector) task(method, deployment string) (*bosh.Task, error) {
	heldBoth := false

	if d.guard != nil {
		_, byInstance := d.guard.Holder(ownedInstance, "")
		_, byDeployment := d.guard.Holder("", deployment)
		heldBoth = byInstance && byDeployment
	}

	d.mu.Lock()
	d.calls = append(d.calls, method+" "+deployment)
	d.held = append(d.held, heldBoth)
	d.mu.Unlock()

	if d.fail != nil {
		return nil, d.fail
	}

	return &bosh.Task{ID: instanceTaskID, State: queuedTaskState}, nil
}

func (d *instanceDirector) recorded() ([]string, []bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.calls...), append([]bool(nil), d.held...)
}

// failingInstanceVault fails every index read, as an unreachable Vault does.
type failingInstanceVault struct {
	testutil.InstanceVault
}

var errVaultDown = errors.New("vault is sealed")

func (v *failingInstanceVault) FindInstance(context.Context, string) (*vaultPkg.Instance, bool, error) {
	return nil, false, errVaultDown
}

// auditLogger records every Info line a handler logs.
type auditLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *auditLogger) Named(string) logger.Logger                      { return l }
func (l *auditLogger) WithField(string, interface{}) logger.Logger     { return l }
func (l *auditLogger) WithFields(map[string]interface{}) logger.Logger { return l }

func (l *auditLogger) Info(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}

func (l *auditLogger) Infof(msg string, args ...interface{}) { l.Info(msg, args...) }

func (l *auditLogger) audits() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	var out []string

	for _, line := range l.lines {
		if strings.HasPrefix(line, "audit=instance-action ") {
			out = append(out, line)
		}
	}

	return out
}

func ownedVault() *testutil.InstanceVault {
	return &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		ownedInstance: {ID: ownedInstance, ServiceID: "redis", PlanID: "redis-small", DeploymentName: ownedDeployment},
	}}
}

type instanceActionSetup struct {
	guard    *mutation.Guard
	director *instanceDirector
	log      *auditLogger
	handler  *deployments.Handler
}

func newInstanceActionSetup(deps deployments.Dependencies) instanceActionSetup {
	setup := instanceActionSetup{
		guard: mutation.NewGuard(time.Now),
		log:   &auditLogger{NoOpLogger: &logger.NoOpLogger{}},
	}
	setup.director = newInstanceDirector(setup.guard)

	if deps.Vault == nil {
		deps.Vault = ownedVault()
	}

	deps.Logger = setup.log
	deps.Director = setup.director
	deps.Guard = setup.guard
	setup.handler = deployments.NewHandler(deps)

	return setup
}

// serveInstanceAction sends the request as the broker's basic-auth wrapper
// hands it on, with the authenticated actor in the context.
func serveInstanceAction(t *testing.T, handler *deployments.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	req.RemoteAddr = "192.0.2.10:51234"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, "operator")))

	if !handler.CanHandleInstanceAction(req.URL.Path) {
		t.Fatalf("CanHandleInstanceAction(%q) = false", req.URL.Path)
	}

	recorder := httptest.NewRecorder()
	handler.ServeInstanceAction(recorder, req)

	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
	}

	return body
}

func TestInstanceActionPaths(t *testing.T) {
	t.Parallel()

	handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger()})

	for path, want := range map[string]bool{
		"/b/7a1b/restart":          true,
		"/b/7a1b/recreate":         true,
		"/b/7a1b/stop":             false,
		"/b/7a1b/restart/now":      false,
		"/b/deployments/x/restart": false,
		"/b//restart":              false,
	} {
		if got := handler.CanHandleInstanceAction(path); got != want {
			t.Errorf("CanHandleInstanceAction(%q) = %v, want %v", path, got, want)
		}
	}
}

// Restart and Recreate of an instance the broker owns run the director's
// job-state change on the deployment the index records, while the guard holds
// both the instance and its deployment, and answer 202 with the BOSH task.
func TestInstanceActionsRunOnTheOwnedDeployment(t *testing.T) {
	t.Parallel()

	for _, action := range []struct{ name, call string }{
		{name: actionRestart, call: "RestartDeployment"},
		{name: actionRecreate, call: "RecreateDeployment"},
	} {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			setup := newInstanceActionSetup(deployments.Dependencies{})

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/"+action.name)

			if recorder.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202: %s", recorder.Code, recorder.Body.String())
			}

			body := decodeBody(t, recorder)
			if body["instance_id"] != ownedInstance || body["deployment"] != ownedDeployment ||
				body["operation"] != action.name || body["task_id"] != float64(instanceTaskID) || body["state"] != queuedTaskState {
				t.Errorf("body = %v", body)
			}

			calls, held := setup.director.recorded()
			if len(calls) != 1 || calls[0] != action.call+" "+ownedDeployment {
				t.Fatalf("director calls = %v, want one %s of %s", calls, action.call, ownedDeployment)
			}

			if !held[0] {
				t.Errorf("the guard did not hold both %s and %s during the call", ownedInstance, ownedDeployment)
			}

			if snapshot := setup.guard.Snapshot(); len(snapshot) != 0 {
				t.Errorf("guard still holds %v after the handler returned", snapshot)
			}
		})
	}
}

// The request carries no options. A recreate never fixes, never dry-runs,
// and never skips drain, and a restart never skips drain.
func TestInstanceActionsSendDefaultOptions(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{})

	serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart?skip_drain=true")
	serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/recreate?fix=true&dry_run=true")

	setup.director.mu.Lock()
	defer setup.director.mu.Unlock()

	if len(setup.director.restarts) != 1 || setup.director.restarts[0] != (bosh.RestartOpts{}) {
		t.Errorf("restart options = %+v, want the defaults", setup.director.restarts)
	}

	if len(setup.director.recreate) != 1 || setup.director.recreate[0] != (bosh.RecreateOpts{}) {
		t.Errorf("recreate options = %+v, want the defaults", setup.director.recreate)
	}
}

// An entry indexed before deployment_name existed runs on plan-instance.
func TestInstanceActionsBuildTheLegacyDeploymentName(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		ownedInstance: {ID: ownedInstance, PlanID: "redis-small"},
	}}
	setup := newInstanceActionSetup(deployments.Dependencies{Vault: vault})

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}

	if calls, _ := setup.director.recorded(); len(calls) != 1 || calls[0] != "RestartDeployment redis-small-"+ownedInstance {
		t.Errorf("director calls = %v", calls)
	}
}

// An instance the broker's index doesn't hold is refused with 404, and
// nothing reaches the director. That covers the broker's own deployment and
// any BOSH deployment the broker didn't provision.
func TestInstanceActionsRefuseInstancesTheBrokerDoesNotOwn(t *testing.T) {
	t.Parallel()

	for _, instanceID := range []string{"blacksmith", "cf", "9f9f-not-ours", ownedDeployment} {
		t.Run(instanceID, func(t *testing.T) {
			t.Parallel()

			setup := newInstanceActionSetup(deployments.Dependencies{})

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+instanceID+"/recreate")

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
			}

			if body := decodeBody(t, recorder); body["code"] != apierr.CodeInstanceNotFound || body["success"] != false {
				t.Errorf("body = %v, want code %s", body, apierr.CodeInstanceNotFound)
			}

			if calls, _ := setup.director.recorded(); len(calls) != 0 {
				t.Errorf("director calls = %v, want none", calls)
			}
		})
	}
}

// When the index can't be read, ownership is unknown, so the action is
// refused with 503 and nothing reaches the director.
func TestInstanceActionsRefuseWhenVaultIsUnreachable(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{Vault: &failingInstanceVault{}})

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}

	if body := decodeBody(t, recorder); body["code"] != apierr.CodeVaultUnavailable {
		t.Errorf("body = %v, want code %s", body, apierr.CodeVaultUnavailable)
	}

	if calls, _ := setup.director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// Only POST changes anything.
func TestInstanceActionsRefuseOtherMethods(t *testing.T) {
	t.Parallel()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		setup := newInstanceActionSetup(deployments.Dependencies{})

		recorder := serveInstanceAction(t, setup.handler, method, "/b/"+ownedInstance+"/restart")

		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, recorder.Code)
		}

		if recorder.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s Allow = %q, want POST", method, recorder.Header().Get("Allow"))
		}

		if calls, _ := setup.director.recorded(); len(calls) != 0 {
			t.Errorf("%s director calls = %v, want none", method, calls)
		}
	}
}

// While another operation holds the instance, the action answers 409 with
// the holder, and nothing reaches the director.
func TestInstanceActionsRefuseABusyInstance(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{})

	release, err := setup.guard.Acquire(ownedInstance, "", mutation.Holder{
		Kind:        mutation.KindUpgrade,
		Description: "upgrade of 7a1b",
	})
	if err != nil {
		t.Fatalf("holding the instance: %v", err)
	}
	defer release()

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	body := decodeBody(t, recorder)
	if body["code"] != apierr.CodeInstanceBusy || !strings.Contains(fmt.Sprint(body["error"]), "upgrade of 7a1b") {
		t.Errorf("body = %v, want %s naming the holder", body, apierr.CodeInstanceBusy)
	}

	if calls, _ := setup.director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// A stored task that waits for the instance owns it, so the action is
// refused even though the guard holds nothing.
func TestInstanceActionsRefuseAnInstanceAStoredTaskWaitsFor(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{
		TaskWaiters: instanceWaiter{instance: ownedInstance},
	})

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/recreate")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	if calls, _ := setup.director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

type instanceWaiter struct{ instance string }

func (w instanceWaiter) WaitingFor(instanceID, _ string) (mutation.Holder, bool) {
	if instanceID == w.instance {
		return mutation.Holder{Kind: mutation.KindUpgrade, Description: "upgrade waiting for 7a1b"}, true
	}

	return mutation.Holder{}, false
}

// Until the stored tasks are loaded the action answers 503 tasks_loading.
func TestInstanceActionsWaitForLoadedTasks(t *testing.T) {
	t.Parallel()

	loaded := make(chan struct{})
	setup := newInstanceActionSetup(deployments.Dependencies{TasksLoaded: loaded})

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}

	if body := decodeBody(t, recorder); body["code"] != apierr.CodeTasksLoading {
		t.Errorf("body = %v, want %s", body, apierr.CodeTasksLoading)
	}

	close(loaded)

	recorder = serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("after the load status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}
}

// A director that refuses the change answers 502, and the guard is freed.
func TestInstanceActionsReportADirectorRefusal(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{})
	setup.director.fail = errInstanceDirectorRefused

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", recorder.Code, recorder.Body.String())
	}

	if body := decodeBody(t, recorder); body["success"] != false || !strings.Contains(fmt.Sprint(body["error"]), "refused") {
		t.Errorf("body = %v", body)
	}

	if snapshot := setup.guard.Snapshot(); len(snapshot) != 0 {
		t.Errorf("guard still holds %v", snapshot)
	}
}

// A director that has no such deployment answers 404 with not_found.
// newAdapterSetup serves the instance actions through the broker's real
// director adapter, against a test director that answers with answer. A
// request answer declines gets the test director's defaults.
func newAdapterSetup(t *testing.T, answer boshtest.Handler) instanceActionSetup {
	t.Helper()

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if answer != nil && answer(writer, request) {
			return true
		}

		if request.Method == http.MethodGet && request.URL.Path == "/tasks" {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

			return true
		}

		return false
	})

	director, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  server.URL,
		Username: directorCredential,
		Password: directorCredential,
		CACert:   server.CACert,
	})
	if err != nil {
		t.Fatalf("creating the director adapter: %v", err)
	}

	setup := instanceActionSetup{
		guard: mutation.NewGuard(time.Now),
		log:   &auditLogger{NoOpLogger: &logger.NoOpLogger{}},
	}
	setup.handler = deployments.NewHandler(deployments.Dependencies{
		Logger:   setup.log,
		Director: director,
		Guard:    setup.guard,
		Vault:    ownedVault(),
	})

	return setup
}

// A director that has no deployment for an instance the index still lists
// answers code 70000, which the adapter maps to ErrDeploymentNotFound. The
// action then answers 404 and says the deployment is gone, not that the
// director failed.
func TestInstanceActionsReportAMissingDeployment(t *testing.T) {
	t.Parallel()

	for _, action := range []string{actionRestart, actionRecreate} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			setup := newAdapterSetup(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodPut || request.URL.Path != "/deployments/"+ownedDeployment+"/jobs/*" {
					return false
				}

				boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment '"+ownedDeployment+"' doesn't exist")

				return true
			})

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/"+action)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", recorder.Code, recorder.Body.String())
			}

			if body := decodeBody(t, recorder); body["code"] != apierr.CodeNotFound {
				t.Errorf("body = %v, want %s", body, apierr.CodeNotFound)
			}
		})
	}
}

// When the director's answer to the job-state change is lost after the
// request went out, the director may have started the task. The action
// answers 504 outcome_unknown, never a failure, so nobody resends it as if
// nothing ran, and the audit line says the outcome is unknown.
func TestInstanceActionsReportALostAnswerAsUnknown(t *testing.T) {
	t.Parallel()

	for _, action := range []string{actionRestart, actionRecreate} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			setup := newAdapterSetup(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodPut || request.URL.Path != "/deployments/"+ownedDeployment+"/jobs/*" {
					return false
				}

				boshtest.DropAnswer(writer)

				return true
			})

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/"+action)

			if recorder.Code != http.StatusGatewayTimeout {
				t.Fatalf("status = %d, want 504: %s", recorder.Code, recorder.Body.String())
			}

			body := decodeBody(t, recorder)
			if body["code"] != apierr.CodeOutcomeUnknown || body["success"] != false ||
				body["deployment"] != ownedDeployment || body["operation"] != action || body["instance_id"] != ownedInstance {
				t.Errorf("body = %v, want code %s naming the deployment and operation", body, apierr.CodeOutcomeUnknown)
			}

			if message, _ := body["error"].(string); !strings.Contains(message, "may have started") {
				t.Errorf("error = %q, want it to say the %s may have started", message, action)
			}

			lines := setup.log.audits()
			if len(lines) != 1 || !strings.Contains(lines[0], `outcome="unknown"`) || !strings.Contains(lines[0], `actor="operator"`) {
				t.Errorf("audit lines = %q, want one with outcome unknown and the actor", lines)
			}
		})
	}
}

// A task the director is already running on the deployment, such as an
// earlier recreate, a manual deploy, or a resurrector scan-and-fix, holds
// its lock. The action asks the director first, while the guard holds the
// instance, and answers 409 deployment_locked with that task, so it never
// queues a task the director would fail.
func TestInstanceActionsRefuseWhileADirectorTaskRuns(t *testing.T) {
	t.Parallel()

	for _, action := range []string{actionRestart, actionRecreate} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			setup := newInstanceActionSetup(deployments.Dependencies{})
			setup.director.running = []bosh.Task{{ID: 812, State: "processing", Deployment: ownedDeployment}}

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/"+action)

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
			}

			body := decodeBody(t, recorder)
			if body["code"] != apierr.CodeDeploymentLocked || body["task_id"] != float64(812) ||
				body["operation"] != action || body["deployment"] != ownedDeployment || body["success"] != false {
				t.Errorf("body = %v, want %s with task 812", body, apierr.CodeDeploymentLocked)
			}

			if message, _ := body["error"].(string); !strings.Contains(message, "task 812") {
				t.Errorf("error = %q, want it to name task 812", message)
			}

			setup.director.mu.Lock()
			lookups := append([]string(nil), setup.director.lookups...)
			setup.director.mu.Unlock()

			if len(lookups) != 1 || lookups[0] != ownedDeployment {
				t.Errorf("running-task lookups = %v, want one for %s", lookups, ownedDeployment)
			}

			if calls, _ := setup.director.recorded(); len(calls) != 0 {
				t.Errorf("director calls = %v, want none while task 812 runs", calls)
			}

			if snapshot := setup.guard.Snapshot(); len(snapshot) != 0 {
				t.Errorf("guard still holds %v after the refusal", snapshot)
			}
		})
	}
}

// When the director can't list the deployment's tasks, nobody knows whether
// one holds the lock, so the action refuses rather than guess.
func TestInstanceActionsRefuseWhenTheTaskListFails(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{})
	setup.director.listErr = errInstanceDirectorRefused

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", recorder.Code, recorder.Body.String())
	}

	if calls, _ := setup.director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// The real adapter asks the director for the deployment's queued,
// processing, and cancelling tasks, and a listed one refuses the action.
func TestInstanceActionsAskTheDirectorForRunningTasks(t *testing.T) {
	t.Parallel()

	var (
		lock  sync.Mutex
		query []string
		puts  int
	)

	setup := newAdapterSetup(t, func(writer http.ResponseWriter, request *http.Request) bool {
		lock.Lock()
		defer lock.Unlock()

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/tasks":
			query = append(query, request.URL.RawQuery)

			boshtest.WriteJSON(writer, http.StatusOK, []map[string]interface{}{
				{"id": 812, "state": "processing", "description": "create deployment", "deployment": ownedDeployment, "user": "admin", "timestamp": 1700000000},
			})

			return true
		case request.Method == http.MethodPut:
			puts++
		}

		return false
	})

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/recreate")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}

	if body := decodeBody(t, recorder); body["code"] != apierr.CodeDeploymentLocked || body["task_id"] != float64(812) {
		t.Errorf("body = %v, want %s with task 812", body, apierr.CodeDeploymentLocked)
	}

	lock.Lock()
	defer lock.Unlock()

	if len(query) != 1 || !strings.Contains(query[0], "deployment="+ownedDeployment) {
		t.Errorf("task list queries = %q, want one for %s", query, ownedDeployment)
	}

	if puts != 0 {
		t.Errorf("the director got %d job-state changes, want none", puts)
	}
}

// Each action that reaches the director logs one audit line with the actor
// the broker's basic-auth wrapper put in the context, and where the request
// came from, together with the outcome and the BOSH task.
func TestInstanceActionsRecordTheActor(t *testing.T) {
	t.Parallel()

	setup := newInstanceActionSetup(deployments.Dependencies{})

	serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/recreate")

	lines := setup.log.audits()
	if len(lines) != 1 {
		t.Fatalf("audit lines = %q, want one", lines)
	}

	for _, want := range []string{
		"instance=" + ownedInstance,
		"deployment=" + ownedDeployment,
		"operation=recreate",
		`actor="operator"`,
		`from="192.0.2.10:51234 via 198.51.100.7"`,
		`outcome="accepted"`,
		fmt.Sprintf("task=%d", instanceTaskID),
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("audit line %q lacks %q", lines[0], want)
		}
	}

	setup.director.fail = errInstanceDirectorRefused

	serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")

	lines = setup.log.audits()
	if len(lines) != 2 || !strings.Contains(lines[1], `outcome="failed"`) || !strings.Contains(lines[1], `actor="operator"`) {
		t.Errorf("audit lines after a refusal = %q", lines)
	}
}
