package websocket_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/ssh/websocket"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const vmUUID = "5f6a0d1c-2b3e-4c8d-9e7f-a1b2c3d4e5f6"

type terminalConfig struct {
	interfaces.Config
}

func (terminalConfig) IsSSHUITerminalEnabled() bool { return true }

func (terminalConfig) GetEnvironment() string { return "lab" }

type blacksmithManifestDirector struct {
	interfaces.Director
}

func (blacksmithManifestDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Name: name, Manifest: "name: lab-blacksmith\ninstance_groups:\n- name: blacksmith\n"}, nil
}

// sessionRecorder stands in for the SSH service and records each session
// the handler opens.
type sessionRecorder struct {
	opened bool
	group  string
	index  int
}

func (r *sessionRecorder) HandleWebSocket(_ context.Context, w http.ResponseWriter, _ *http.Request, _, instanceName string, instanceIndex int) {
	r.opened = true
	r.group = instanceName
	r.index = instanceIndex

	w.WriteHeader(http.StatusSwitchingProtocols)
}

func (r *sessionRecorder) GetActiveSessions() int { return 0 }

func newTerminalHandler(recorder *sessionRecorder) *websocket.Handler {
	return websocket.NewHandler(websocket.Dependencies{
		Logger:           logger.Get().Named("test"),
		Config:           terminalConfig{},
		WebSocketHandler: recorder,
		Director:         blacksmithManifestDirector{},
		Vault: &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
			"inst-1": {ServiceID: "redis", PlanID: "standalone"},
		}},
	})
}

func terminalPaths() map[string]string {
	return map[string]string{
		"blacksmith":       "/b/blacksmith/ssh/stream",
		"service instance": "/b/inst-1/ssh/stream",
	}
}

func openTerminal(t *testing.T, path, index string) (*sessionRecorder, *httptest.ResponseRecorder) {
	t.Helper()

	query := url.Values{"instance": {"node"}}
	if index != "" {
		query.Set("index", index)
	}

	recorder := &sessionRecorder{}
	writer := httptest.NewRecorder()
	newTerminalHandler(recorder).ServeHTTP(writer, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"?"+query.Encode(), nil))

	return recorder, writer
}

// A numeric index, or none at all, opens a session on that VM.
func TestSSHStreamOpensTheVMAtANumericIndex(t *testing.T) {
	t.Parallel()

	for name, path := range terminalPaths() {
		for index, want := range map[string]int{"": 0, "0": 0, "2": 2, "12": 12} {
			recorder, writer := openTerminal(t, path, index)
			if !recorder.opened {
				t.Fatalf("%s index %q: no session opened, answered %d %s", name, index, writer.Code, writer.Body.String())
			}

			if recorder.group != "node" || recorder.index != want {
				t.Errorf("%s index %q: opened node/%d as %s/%d", name, index, want, recorder.group, recorder.index)
			}
		}
	}
}

// An index the handler cannot read must never fall back to 0, because
// that opens a shell on a VM nobody asked for.
func TestSSHStreamRefusesAnIndexItCannotRead(t *testing.T) {
	t.Parallel()

	for name, path := range terminalPaths() {
		refusals := map[string]string{vmUUID: "is a VM ID, not an index"}
		for _, index := range []string{"abc", "-1", "+2", "1.5", "2abc", "0x1", "node/2", "-", "99999999999999999999"} {
			refusals[index] = "is not an instance index"
		}

		for index, want := range refusals {
			recorder, writer := openTerminal(t, path, index)
			if recorder.opened {
				t.Errorf("%s index %q: opened a session on node/%d", name, index, recorder.index)

				continue
			}

			if writer.Code != http.StatusBadRequest {
				t.Errorf("%s index %q: answered %d, want 400", name, index, writer.Code)
			}

			var body struct {
				Error string `json:"error"`
			}

			err := json.Unmarshal(writer.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("%s index %q: body %q: %v", name, index, writer.Body.String(), err)
			}

			if !strings.Contains(body.Error, want) || !strings.Contains(body.Error, index) {
				t.Errorf("%s index %q: error %q does not name the index and say %q", name, index, body.Error, want)
			}
		}
	}
}
