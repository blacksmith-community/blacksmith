package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	return apiHandler.UpgradeHandler().GetManager()
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
