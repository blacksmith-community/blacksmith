package deployments_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// acceptedTaskID is the task the director names when it accepts a request.
const acceptedTaskID = 77

// acceptingDirector answers each mutation the way the director accepts
// it, with a task that has not started yet.
type acceptingDirector struct {
	*testutil.MockBOSHDirector

	mu       sync.Mutex
	restarts []bosh.RestartOpts
	deletes  []string
}

func newAcceptingDirector() *acceptingDirector {
	return &acceptingDirector{MockBOSHDirector: testutil.NewMockBOSHDirector()}
}

func (d *acceptingDirector) RestartDeployment(_ context.Context, _ string, opts bosh.RestartOpts) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.restarts = append(d.restarts, opts)

	return &bosh.Task{ID: acceptedTaskID, State: "queued"}, nil
}

func (d *acceptingDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.deletes = append(d.deletes, name)

	return &bosh.Task{ID: acceptedTaskID, State: "queued"}, nil
}

// warningLogger records the warnings a handler logs.
type warningLogger struct {
	*logger.NoOpLogger

	mu       sync.Mutex
	warnings []string
}

func (l *warningLogger) Named(string) logger.Logger                      { return l }
func (l *warningLogger) WithField(string, interface{}) logger.Logger     { return l }
func (l *warningLogger) WithFields(map[string]interface{}) logger.Logger { return l }

func (l *warningLogger) Warning(msg string, args ...interface{}) { l.record(msg, args...) }
func (l *warningLogger) Warn(msg string, args ...interface{})    { l.record(msg, args...) }
func (l *warningLogger) Warningf(msg string, args ...interface{}) {
	l.record(msg, args...)
}

func (l *warningLogger) Warnf(msg string, args ...interface{}) { l.record(msg, args...) }

func (l *warningLogger) record(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.warnings = append(l.warnings, fmt.Sprintf(msg, args...))
}

func (l *warningLogger) recorded() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.warnings...)
}

// A job-state request that still carries force is sent without it, because
// the director ignores force on job-state changes, and the handler warns
// that it dropped it.
func TestJobStateRouteWithForceLogsAWarningAndCarriesOn(t *testing.T) {
	t.Parallel()

	director := newAcceptingDirector()
	log := &warningLogger{NoOpLogger: &logger.NoOpLogger{}}
	handler := deployments.NewHandler(deployments.Dependencies{Logger: log, Director: director})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/dep-1/restart?force=true", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(director.restarts) != 1 {
		t.Fatalf("expected one restart, got %d", len(director.restarts))
	}

	warnings := log.recorded()

	found := false

	for _, warning := range warnings {
		if strings.Contains(warning, "/b/deployments/dep-1/restart") && strings.Contains(warning, "ignores force") {
			found = true
		}
	}

	if !found {
		t.Errorf("expected a warning that names the route and says the director ignores force, got %q", warnings)
	}
}

// The delete answers as soon as the director accepts it, so it says the
// delete was accepted and names its task, and never says it is deleted.
func TestDeleteHandlerAnswersAcceptedWithTheTaskID(t *testing.T) {
	t.Parallel()

	director := newAcceptingDirector()
	handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/b/deployments/dep-1", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
	}

	if body["accepted"] != true || body["task_id"] != float64(acceptedTaskID) || body["deployment"] != "dep-1" {
		t.Errorf("expected accepted, the task ID, and the deployment, got %v", body)
	}

	for _, gone := range []string{"deleted", "state"} {
		if _, ok := body[gone]; ok {
			t.Errorf("expected no %q in the answer, got %v", gone, body)
		}
	}

	if len(director.deletes) != 1 || director.deletes[0] != "dep-1" {
		t.Errorf("expected one delete of dep-1, got %v", director.deletes)
	}
}

// The update stores the manifest as soon as the director accepts the
// deploy, before the deploy task has run.
func TestUpdateHandlerStoresTheManifestOnceTheDirectorAccepts(t *testing.T) {
	t.Parallel()

	vaultAdapter := setupVaultForManifestTest(t)
	director, instanceID, deploymentName := setupDirectorForManifestTest()
	director.updateDeploymentTask = &bosh.Task{ID: acceptedTaskID, State: "queued"}

	seedVaultWithTestData(t, vaultAdapter.server, instanceID, deploymentName)
	handler := createHandlerForManifestTest(vaultAdapter, director)

	manifest := map[string]interface{}{"name": deploymentName}
	_, recorder := executeManifestUpdate(t, handler, deploymentName, manifest)

	verifyManifestUpdateResponse(t, recorder, director, manifest)
	verifyManifestCachedInVault(t, vaultAdapter, instanceID, director.updateDeploymentManifest)

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
	}

	if body["task_id"] != float64(acceptedTaskID) || body["state"] != "queued" {
		t.Errorf("expected the accepted task, got %v", body)
	}
}
