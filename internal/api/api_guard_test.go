package api_test

import (
	"testing"
	"time"

	"blacksmith/internal/api"
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
