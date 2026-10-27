package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/api"
	"blacksmith/internal/config"
	"blacksmith/internal/upgrade"
	pkgservices "blacksmith/pkg/services"
)

// fakeCommandKey is a stand-in that is not a real credential.
const fakeCommandKey = "fake-key-for-wiring-test"

func newCommandWiringAPI(t *testing.T, commands func() config.CommandsConfig, bosh func() config.BOSHConfig) *upgrade.Manager {
	t.Helper()

	return newCommandRouterAPI(t, commands, bosh).UpgradeHandler().GetManager()
}

// newCommandRouterAPI builds the production API, whose router the tests can
// send requests through.
func newCommandRouterAPI(t *testing.T, commands func() config.CommandsConfig, bosh func() config.BOSHConfig) *api.InternalAPI {
	t.Helper()

	apiHandler := api.NewInternalAPI(api.Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              &memoryVault{saved: map[string][]byte{}},
		Broker:             &mockBroker{},
		Director:           &startupDirector{},
		CFManager:          &mockCFManager{},
		ServicesManager:    &pkgservices.Manager{},
		SecurityMiddleware: &pkgservices.SecurityMiddleware{},
		Commands:           commands,
		BOSH:               bosh,
	})
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	return apiHandler
}

func commandPreview(t *testing.T, manager *upgrade.Manager) *upgrade.CommandPreview {
	t.Helper()

	preview, err := manager.PreviewCommand(context.Background(), upgrade.CommandPreviewRequest{
		InstanceIDs: []string{"instance-1"},
		Command:     "uptime",
		RunAs:       "vcap",
	})
	if err != nil {
		t.Fatalf("previewing a command failed: %v", err)
	}

	return preview
}

// The commands and BOSH blocks reach the manager through the production
// constructor, so the authorizer enforces the credential key and a preview
// reports the configured worker count.
func TestAPIWiresConfiguredCommandSettings(t *testing.T) {
	t.Parallel()

	manager := newCommandWiringAPI(t,
		func() config.CommandsConfig {
			return config.CommandsConfig{Credential: config.CommandCredential{Key: fakeCommandKey}}
		},
		func() config.BOSHConfig { return config.BOSHConfig{DirectorWorkers: 5} },
	)

	authorizer := manager.Authorizer()

	without := httptest.NewRequest(http.MethodPost, "/", nil)
	if err := authorizer.AuthorizeRequest(context.Background(), without, "vcap"); err == nil {
		t.Fatal("a request without the command key header was authorized")
	}

	with := httptest.NewRequest(http.MethodPost, "/", nil)
	with.Header.Set(upgrade.CommandKeyHeader, fakeCommandKey)

	if err := authorizer.AuthorizeRequest(context.Background(), with, "vcap"); err != nil {
		t.Fatalf("a request with the command key header was refused: %v", err)
	}

	if got := commandPreview(t, manager).WorkerCount; got != 5 {
		t.Fatalf("a preview reports %d workers, want 5", got)
	}
}

// With both blocks absent the documented defaults apply.
func TestAPIWiresCommandDefaults(t *testing.T) {
	t.Parallel()

	var (
		commands config.CommandsConfig
		bosh     config.BOSHConfig
	)

	manager := newCommandWiringAPI(t,
		func() config.CommandsConfig { return commands },
		func() config.BOSHConfig { return bosh },
	)

	deps := manager.Dependencies()
	if deps.Commands == nil {
		t.Fatal("the manager has no commands function")
	}

	if !deps.Commands().CommandsEnabled() {
		t.Error("commands read as off with no commands block, want on")
	}

	if deps.Commands().RootAllowed() {
		t.Error("root reads as allowed with no commands block, want off")
	}

	preview := commandPreview(t, manager)
	if preview.WorkerCount != 4 {
		t.Errorf("a preview reports %d workers, want 4", preview.WorkerCount)
	}

	if preview.MaxInFlightLimit != 3 {
		t.Errorf("a preview reports a max in flight limit of %d, want 3", preview.MaxInFlightLimit)
	}
}

// The production constructor gives the manager a command runner, beside
// its patch runner, so a command task can open the SSH sessions it needs.
func TestAPIWiresTheCommandRunner(t *testing.T) {
	t.Parallel()

	manager := newCommandWiringAPI(t,
		func() config.CommandsConfig { return config.CommandsConfig{} },
		func() config.BOSHConfig { return config.BOSHConfig{} },
	)

	deps := manager.Dependencies()
	if deps.PatchRunner == nil {
		t.Fatal("the manager has no patch runner")
	}

	if deps.CommandRunner == nil {
		t.Fatal("the manager has no command runner")
	}
}

// sendThroughRouter sends one request through the production router and
// returns the status and the error code of the answer.
func sendThroughRouter(t *testing.T, router http.Handler, method, target, body, key string) (int, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if key != "" {
		req.Header.Set(upgrade.CommandKeyHeader, key)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var answer struct {
		Code string `json:"code"`
	}

	_ = json.Unmarshal(rec.Body.Bytes(), &answer)

	return rec.Code, answer.Code
}

func routerCreateBody(previewID string) string {
	return `{"kind":"command","preview_id":"` + previewID + `","client_request_id":"11111111-2222-4333-8444-555555555555","command":"uptime","run_as":"vcap","timeout_seconds":600,"name":"wiring","reason":"CHG-1"}`
}

// A create sent through the production router without the second
// credential is refused, makes no task, and leaves the preview unspent.
func TestCreateThroughRouterNeedsCredential(t *testing.T) {
	t.Parallel()

	router := newCommandRouterAPI(t,
		func() config.CommandsConfig {
			return config.CommandsConfig{Credential: config.CommandCredential{Key: fakeCommandKey}}
		},
		func() config.BOSHConfig { return config.BOSHConfig{} })
	manager := router.UpgradeHandler().GetManager()

	select {
	case <-manager.Loaded():
	case <-t.Context().Done():
	}

	preview := commandPreview(t, manager)

	for name, key := range map[string]string{"no header": "", "wrong header": "wrong-key"} {
		status, code := sendThroughRouter(t, router, http.MethodPost, "/b/upgrade/tasks", routerCreateBody(preview.ID), key)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: create answered %d %q, want 401", name, status, code)
		}
	}

	if n := len(manager.ListTasks()); n != 0 {
		t.Errorf("%d tasks exist after refused creates, want 0", n)
	}

	// The fake vault knows no instance, so a create that gets past the
	// gate finds every instance blocked. Reaching that check means the
	// preview was still readable after the two refusals.
	status, code := sendThroughRouter(t, router, http.MethodPost, "/b/upgrade/tasks", routerCreateBody(preview.ID), fakeCommandKey)
	if status != http.StatusBadRequest || code != "no_selections" {
		t.Errorf("create with the key answered %d %q, want 400 no_selections from past the gate", status, code)
	}
}

// With allow_root unset, a root preview through the production router is
// refused and a vcap preview is accepted.
func TestRootPreviewRefusedWhenAllowRootUnset(t *testing.T) {
	t.Parallel()

	router := newCommandRouterAPI(t,
		func() config.CommandsConfig { return config.CommandsConfig{} },
		func() config.BOSHConfig { return config.BOSHConfig{} })
	manager := router.UpgradeHandler().GetManager()

	select {
	case <-manager.Loaded():
	case <-t.Context().Done():
	}

	body := func(runAs string) string {
		return `{"instance_ids":["instance-1"],"command":"uptime","run_as":"` + runAs + `","timeout_seconds":600}`
	}

	status, code := sendThroughRouter(t, router, http.MethodPost, "/b/upgrade/commands/preview", body("root"), "")
	if status != http.StatusForbidden || code != "root_disabled" {
		t.Errorf("root preview answered %d %q, want 403 root_disabled", status, code)
	}

	status, code = sendThroughRouter(t, router, http.MethodPost, "/b/upgrade/commands/preview", body("vcap"), "")
	if status != http.StatusOK {
		t.Errorf("vcap preview answered %d %q, want 200", status, code)
	}
}
