package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/apierr"
	"blacksmith/internal/mutation"
)

func TestUpgradeManagerUsesDependenciesGuard(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)

	deps := createTestDependencies(t)
	deps.Guard = guard

	apiHandler := api.NewInternalAPI(deps)
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	if got := apiHandler.UpgradeHandler().GetManager().Guard(); got != guard {
		t.Fatalf("the upgrade manager uses guard %p, want the dependencies' guard %p", got, guard)
	}
}

// The deployments handler reserves deployments in the dependencies' guard,
// so a deployment action is refused while another holder has the instance.
func TestDeploymentActionsUseDependenciesGuard(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)

	release, err := guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{
		Kind:        mutation.KindCertificates,
		Description: "certificate rotation of redis-7a1b",
	})
	if err != nil {
		t.Fatalf("holding the instance: %v", err)
	}
	defer release()

	deps := createTestDependencies(t)
	deps.Guard = guard

	apiHandler := api.NewInternalAPI(deps)
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	// Deployment actions answer tasks_loading until the stored tasks are
	// loaded, so the restart waits for the load.
	select {
	case <-apiHandler.UpgradeHandler().GetManager().Loaded():
	case <-time.After(5 * time.Second):
		t.Fatal("the upgrade manager did not load its stored tasks")
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/redis-7a1b/restart", nil)
	recorder := httptest.NewRecorder()

	apiHandler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), apierr.CodeInstanceBusy) {
		t.Fatalf("restart answered %d %s, want 409 with %s", recorder.Code, recorder.Body.String(), apierr.CodeInstanceBusy)
	}
}
