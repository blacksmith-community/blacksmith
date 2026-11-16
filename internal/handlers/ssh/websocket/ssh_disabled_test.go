package websocket_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/handlers/ssh/websocket"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// offConfig turns SSH, or only the terminal, off.
type offConfig struct {
	interfaces.Config

	ssh, terminal bool
}

func (c offConfig) IsSSHEnabled() bool           { return c.ssh }
func (c offConfig) IsSSHUITerminalEnabled() bool { return c.ssh && c.terminal }
func (c offConfig) GetEnvironment() string       { return "lab" }

// An explicit false reaches the 403 paths, opens no session, and says why.
func TestTerminalRefusedWhenTheSwitchesAreOff(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		cfg  offConfig
		want string
	}{
		"ssh off":      {offConfig{ssh: false, terminal: true}, "SSH is disabled"},
		"terminal off": {offConfig{ssh: true, terminal: false}, "SSH Terminal UI is disabled"},
	}

	for name, testCase := range cases {
		for pathName, path := range terminalPaths() {
			t.Run(name+" "+pathName, func(t *testing.T) {
				t.Parallel()

				recorder := &sessionRecorder{}
				handler := websocket.NewHandler(websocket.Dependencies{
					Logger:           logger.Get().Named("test"),
					Config:           testCase.cfg,
					WebSocketHandler: recorder,
					Director:         blacksmithManifestDirector{},
					Vault: &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
						"inst-1": {ServiceID: "redis", PlanID: "standalone"},
					}},
				})

				req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"?instance=node", nil)
				rec := httptest.NewRecorder()

				handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body.String())
				}

				if !strings.Contains(rec.Body.String(), testCase.want) {
					t.Errorf("body %q does not say %q", rec.Body.String(), testCase.want)
				}

				if recorder.opened {
					t.Error("a session was opened although the switch is off")
				}
			})
		}
	}
}

// The status route opens no session, yet it belongs to the SSH feature, so
// it is refused when ssh.enabled is false.
func TestSSHStatusRefusedWhenSSHIsOff(t *testing.T) {
	t.Parallel()

	handler := websocket.NewHandler(websocket.Dependencies{
		Logger:           logger.Get().Named("test"),
		Config:           offConfig{ssh: false, terminal: true},
		WebSocketHandler: &sessionRecorder{},
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/ssh/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SSH is disabled") {
		t.Fatalf("got %d %s, want 403 SSH is disabled", rec.Code, rec.Body.String())
	}
}
