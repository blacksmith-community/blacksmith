package blacksmith_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// resurrectionDirector records the deployments whose resurrection is toggled.
type resurrectionDirector struct {
	*testutil.ScriptedBOSHDirector

	mu      sync.Mutex
	toggled []string
}

func (d *resurrectionDirector) EnableResurrection(_ context.Context, deployment string, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.toggled = append(d.toggled, deployment)

	return nil
}

// The reconciler can rewrite an index entry's plan ID after the deployment
// was created, so the handler toggles resurrection on the deployment the
// entry records, and builds the name from the plan ID only for an entry that
// records none.
func TestToggleResurrectionNamesTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"adopted": {ServiceID: "redis", PlanID: "standalone", DeploymentName: "standalone-classic-adopted"},
		"legacy":  {ServiceID: "redis", PlanID: "standalone"},
	}}

	for instanceID, want := range map[string]string{"adopted": "standalone-classic-adopted", "legacy": "standalone-legacy"} {
		director := &resurrectionDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
			Logger:   logger.Get().Named("test"),
			Config:   createTestConfig(),
			Vault:    vault,
			Director: director,
		})

		request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/"+instanceID+"/resurrection", strings.NewReader(`{"enabled": true}`))
		recorder := httptest.NewRecorder()

		handler.ToggleResurrection(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("instance %s: status %d: %s", instanceID, recorder.Code, recorder.Body.String())
		}

		if len(director.toggled) != 1 || director.toggled[0] != want {
			t.Errorf("instance %s toggled resurrection on %v, want [%s]", instanceID, director.toggled, want)
		}
	}
}
