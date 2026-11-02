package blacksmith_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/config"
	"blacksmith/internal/parity"
)

// TestGetConfigParity records the JSON the config endpoint returns for the
// configs the kit renders and for a hand-built one. The config goes through
// a YAML round trip on its way, which is what the golden pins.
//
//nolint:paralleltest // ReadConfig writes environment variables
func TestGetConfigParity(t *testing.T) {
	kitDir := filepath.Join("..", "..", "config", "testdata", "kit")
	cloudConfig, err := filepath.Abs(filepath.Join("..", "..", "config", "testdata", "cloud-config.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var recorded strings.Builder

	for _, name := range []string{"minimal", "full"} {
		for _, key := range []string{"BOSH_NETWORK", "VAULT_ADDR", "VAULT_TOKEN"} {
			t.Setenv(key, "")
		}

		body, err := os.ReadFile(filepath.Join(kitDir, name+".conf")) //nolint:gosec // a fixture
		if err != nil {
			t.Fatal(err)
		}

		path := filepath.Join(t.TempDir(), "blacksmith.conf")
		text := strings.ReplaceAll(string(body), "/var/vcap/jobs/blacksmith/config/cloud-config.yml", cloudConfig)

		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := config.ReadConfig(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		recorded.WriteString(parity.Section("kit "+name, strings.ReplaceAll(getConfigBody(t, &cfg), cloudConfig, "<cloud-config>")))
	}

	recorded.WriteString(parity.Section("hand-built", getConfigBody(t, createTestConfig())))

	parity.Golden(t, filepath.Join("testdata", "parity", "get-config.json"), recorded.String())
}

func getConfigBody(t *testing.T, cfg *config.Config) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/b/blacksmith/config", nil)
	recorder := httptest.NewRecorder()

	createTestHandler(cfg).GetConfig(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	return recorder.Body.String()
}

// TestFormatManifestResponseParity records the text and parsed form the UI
// gets for each manifest.
func TestFormatManifestResponseParity(t *testing.T) {
	t.Parallel()

	handler := createTestHandler(createTestConfig())

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		recorded.WriteString(parity.Section(doc.Name, parity.Dump(handler.FormatManifestResponse(doc.Body))))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "manifest-response.dump"), recorded.String())
}
