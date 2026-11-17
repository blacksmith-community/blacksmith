package blacksmith_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

// A deprovisioned instance leaves a tombstone in the index, and resurrection
// is not toggled on the deployment of an instance that is gone.
func TestToggleResurrectionRefusesATombstonedInstance(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"gone": {ServiceID: "redis", PlanID: "standalone", Status: vaultPkg.StatusDeleted},
	}}
	director := &resurrectionDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
	handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"), Config: createTestConfig(), Vault: vault, Director: director,
	})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/gone/resurrection", strings.NewReader(`{"enabled": true}`))
	recorder := httptest.NewRecorder()

	handler.ToggleResurrection(recorder, request)

	if recorder.Code != http.StatusNotFound || len(director.toggled) != 0 {
		t.Errorf("status %d with toggles %v, want 404 and none", recorder.Code, director.toggled)
	}
}

// The broker names its own deployment through one resolver. The instance
// file wins, the environment plus -blacksmith is the fallback, and a broker
// that knows neither toggles nothing.
func TestToggleResurrectionOnTheBrokerUsesTheOwnNameResolver(t *testing.T) {
	t.Parallel()

	named := t.TempDir() + "/deployment"

	err := os.WriteFile(named, []byte("lab-blacksmith-2\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	missing := t.TempDir() + "/none"

	for _, scenario := range []struct {
		file, env, want string
	}{
		{named, testEnv, "lab-blacksmith-2"},
		{missing, testEnv, "testing-blacksmith"},
		{missing, "", ""},
	} {
		director := &resurrectionDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		cfg := createTestConfig()
		cfg.Env = scenario.env
		handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
			Logger: logger.Get().Named("test"), Config: cfg, Vault: &testutil.InstanceVault{}, Director: director, OwnNameFile: scenario.file,
		})

		request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/blacksmith/resurrection", strings.NewReader(`{"enabled": true}`))
		recorder := httptest.NewRecorder()

		handler.ToggleResurrection(recorder, request)

		if scenario.want == "" {
			if recorder.Code != http.StatusNotFound || len(director.toggled) != 0 {
				t.Errorf("env %q: status %d with toggles %v, want 404 and none", scenario.env, recorder.Code, director.toggled)
			}

			continue
		}

		if recorder.Code != http.StatusOK || len(director.toggled) != 1 || director.toggled[0] != scenario.want {
			t.Errorf("env %q: status %d toggled %v, want [%s]", scenario.env, recorder.Code, director.toggled, scenario.want)
		}
	}
}

const testEnv = "testing"
