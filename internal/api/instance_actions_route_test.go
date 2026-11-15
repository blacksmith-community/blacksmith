package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/mutation"
	vaultPkg "blacksmith/pkg/vault"
)

// ownedIndexVault answers FindInstance for one instance and leaves every
// other call to the wrapped Vault.
type ownedIndexVault struct {
	interfaces.Vault

	instance vaultPkg.Instance
}

func (v ownedIndexVault) FindInstance(_ context.Context, instanceID string) (*vaultPkg.Instance, bool, error) {
	if instanceID != v.instance.ID {
		return nil, false, nil
	}

	instance := v.instance

	return &instance, true, nil
}

// restartOnlyDirector lists no running tasks, records the deployments it
// restarts or recreates, and panics, through the nil embedded Director, on
// any other call.
type restartOnlyDirector struct {
	bosh.Director

	calls []string
}

func (d *restartOnlyDirector) FindRunningTasksForDeployment(context.Context, string) ([]bosh.Task, error) {
	return nil, nil
}

func (d *restartOnlyDirector) RestartDeployment(_ context.Context, name string, _ bosh.RestartOpts) (*bosh.Task, error) {
	d.calls = append(d.calls, "restart "+name)

	return &bosh.Task{ID: 4321, State: "queued"}, nil
}

func (d *restartOnlyDirector) RecreateDeployment(_ context.Context, name string, _ bosh.RecreateOpts) (*bosh.Task, error) {
	d.calls = append(d.calls, "recreate "+name)

	return &bosh.Task{ID: 4322, State: "queued"}, nil
}

// POST /b/{id}/restart and /b/{id}/recreate reach the deployments handler's
// instance actions, which check ownership in the broker's index and reserve
// the instance in the dependencies' guard.
func TestInstanceActionRoutes(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)

	release, err := guard.Acquire("7a1b", "redis-small-7a1b", mutation.Holder{
		Kind:        mutation.KindCertificates,
		Description: "certificate rotation of 7a1b",
	})
	if err != nil {
		t.Fatalf("holding the instance: %v", err)
	}
	defer release()

	deps := createTestDependencies(t)
	deps.Guard = guard
	director := &restartOnlyDirector{}
	deps.Director = director
	deps.Vault = ownedIndexVault{Vault: deps.Vault, instance: vaultPkg.Instance{ID: "7a1b", PlanID: "redis-small", DeploymentName: "redis-small-7a1b"}}

	apiHandler := api.NewInternalAPI(deps)
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	select {
	case <-apiHandler.UpgradeHandler().GetManager().Loaded():
	case <-time.After(5 * time.Second):
		t.Fatal("the upgrade manager did not load its stored tasks")
	}

	serve := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		recorder := httptest.NewRecorder()
		apiHandler.ServeHTTP(recorder, req)

		return recorder
	}

	for _, action := range []string{"restart", "recreate"} {
		recorder := serve(http.MethodPost, "/b/7a1b/"+action)
		if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), apierr.CodeInstanceBusy) {
			t.Errorf("%s of the held instance answered %d %s, want 409 %s", action, recorder.Code, recorder.Body.String(), apierr.CodeInstanceBusy)
		}

		recorder = serve(http.MethodPost, "/b/not-ours/"+action)
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), apierr.CodeInstanceNotFound) {
			t.Errorf("%s of an unowned instance answered %d %s, want 404 %s", action, recorder.Code, recorder.Body.String(), apierr.CodeInstanceNotFound)
		}

		recorder = serve(http.MethodGet, "/b/7a1b/"+action)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s answered %d, want 405", action, recorder.Code)
		}
	}

	if len(director.calls) != 0 {
		t.Fatalf("the director was called while the instance was held: %v", director.calls)
	}

	release()

	for _, action := range []string{"restart", "recreate"} {
		recorder := serve(http.MethodPost, "/b/7a1b/"+action)
		if recorder.Code != http.StatusAccepted {
			t.Errorf("%s of the owned instance answered %d %s, want 202", action, recorder.Code, recorder.Body.String())
		}
	}

	if want := []string{"restart redis-small-7a1b", "recreate redis-small-7a1b"}; strings.Join(director.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("director calls = %v, want %v", director.calls, want)
	}
}
