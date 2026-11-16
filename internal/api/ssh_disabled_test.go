package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/api"
	pkgservices "blacksmith/pkg/services"
)

const trustedPath = "/b/inst-1/certificates/trusted"

// sshOffConfig is a configuration with ssh.enabled false. Turning SSH off
// turns the UI terminal off with it.
type sshOffConfig struct{ mockConfig }

func (sshOffConfig) IsSSHEnabled() bool           { return false }
func (sshOffConfig) IsSSHUITerminalEnabled() bool { return false }

// terminalOffConfig turns off only the UI terminal.
type terminalOffConfig struct{ mockConfig }

func (terminalOffConfig) IsSSHEnabled() bool           { return true }
func (terminalOffConfig) IsSSHUITerminalEnabled() bool { return false }

func newSSHGateAPI(t *testing.T, cfg api.Dependencies) *api.InternalAPI {
	t.Helper()

	cfg.Logger = &mockLogger{}
	cfg.Vault = &memoryVault{saved: map[string][]byte{}}
	cfg.Broker = &mockBroker{}
	cfg.Director = &startupDirector{}
	cfg.CFManager = &mockCFManager{}
	cfg.ServicesManager = &pkgservices.Manager{}
	cfg.SecurityMiddleware = &pkgservices.SecurityMiddleware{}

	apiHandler := api.NewInternalAPI(cfg)
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	return apiHandler
}

// Every route that opens an SSH session to a VM answers 403 when ssh.enabled
// is false, and the answer says SSH is disabled.
func TestSSHBackedRoutesRefuseWhenSSHIsOff(t *testing.T) {
	t.Parallel()

	router := newSSHGateAPI(t, api.Dependencies{Config: &sshOffConfig{}})

	routes := []struct{ method, path string }{
		{http.MethodGet, "/b/blacksmith/ssh/stream"},
		{http.MethodGet, "/b/inst-1/ssh/stream"},
		{http.MethodGet, "/b/ssh/status"},
		{http.MethodPost, "/b/inst-1/ssh/execute"},
		{http.MethodPost, "/b/inst-1/rabbitmq/ssh/list_queues"},
		{http.MethodGet, "/b/inst-1/rabbitmq/ssh/cluster_status"},
		{http.MethodGet, "/b/inst-1/rabbitmq/rabbitmqctl/stream"},
		{http.MethodPost, "/b/inst-1/rabbitmq/rabbitmqctl/execute"},
		{http.MethodGet, "/b/inst-1/rabbitmq/plugins/stream"},
		{http.MethodPost, "/b/inst-1/rabbitmq/plugins/execute"},
		{http.MethodGet, trustedPath},
		{http.MethodPost, trustedPath},
		{http.MethodDelete, trustedPath},
		{http.MethodPost, "/b/inst-1/certificates/trusted/file"},
		{http.MethodPost, "/b/upgrade/commands/preview"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, strings.NewReader("{}"))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.String())
			}

			if !strings.Contains(rec.Body.String(), "SSH is disabled") || !strings.Contains(rec.Body.String(), "ssh_disabled") {
				t.Errorf("the refusal does not say SSH is disabled: %s", rec.Body.String())
			}
		})
	}
}

// A command task is an SSH-backed request too, so it is refused before the
// body or the credential is looked at.
func TestCommandTaskRefusedWhenSSHIsOff(t *testing.T) {
	t.Parallel()

	router := newSSHGateAPI(t, api.Dependencies{Config: &sshOffConfig{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", strings.NewReader(`{"kind":"command"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "ssh_disabled") {
		t.Fatalf("got %d %s, want 403 ssh_disabled", rec.Code, rec.Body.String())
	}
}

// The UI terminal status reports off when SSH is off, and when only the
// terminal is off, and the routes that are not SSH-backed stay open.
func TestTerminalStatusAndOpenRoutes(t *testing.T) {
	t.Parallel()

	for name, cfg := range map[string]api.Dependencies{
		"ssh off":      {Config: &sshOffConfig{}},
		"terminal off": {Config: &terminalOffConfig{}},
	} {
		router := newSSHGateAPI(t, cfg)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/config/ssh/ui-terminal-status", nil)
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
			t.Errorf("%s: terminal status = %d %s, want enabled false", name, rec.Code, rec.Body.String())
		}
	}

	router := newSSHGateAPI(t, api.Dependencies{Config: &sshOffConfig{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/inst-1/vms", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "ssh_disabled") {
		t.Errorf("a route that opens no SSH session was refused as SSH: %s", rec.Body.String())
	}
}

// With ui_terminal_enabled false and ssh.enabled true, the other SSH-backed
// routes are not refused by the SSH switch.
func TestTerminalOffLeavesOtherSSHRoutesOpen(t *testing.T) {
	t.Parallel()

	router := newSSHGateAPI(t, api.Dependencies{Config: &terminalOffConfig{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/inst-1/ssh/execute", strings.NewReader("{}"))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "ssh_disabled") {
		t.Errorf("ssh.enabled is true, yet the route was refused as SSH disabled: %s", rec.Body.String())
	}
}

// With ssh.enabled false, the availability read says commands are off and
// gives ssh_disabled as the reason, and it still answers 200.
func TestCommandAvailabilityReportsSSHOff(t *testing.T) {
	t.Parallel()

	router := newSSHGateAPI(t, api.Dependencies{Config: &sshOffConfig{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/upgrade/commands/availability", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Enabled   bool   `json:"enabled"`
		AllowRoot bool   `json:"allow_root"`
		Reason    string `json:"reason"`
	}

	err := json.Unmarshal(rec.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	if body.Enabled || body.AllowRoot || body.Reason != "ssh_disabled" {
		t.Errorf("got %+v, want commands off with reason ssh_disabled", body)
	}
}
