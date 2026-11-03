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

	"github.com/fivetwenty-io/graft/pkg/yaml"
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
		marshaledConfig(t, "kit-"+name, &cfg, cloudConfig)
	}

	recorded.WriteString(parity.Section("hand-built", getConfigBody(t, createTestConfig())))
	marshaledConfig(t, "hand-built", createTestConfig(), cloudConfig)

	parity.Golden(t, filepath.Join("testdata", "parity", "get-config.json"), recorded.String())
}

// marshaledConfig records the YAML text the handler marshals before it reads
// the config back, which is the call at handler.go:397. The text goes in
// testdata/parity-graft, beside the spruce-era recording it replaced, and
// must read as the same tree.
func marshaledConfig(t *testing.T, name string, cfg *config.Config, cloudConfig string) {
	t.Helper()

	text, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	got := strings.ReplaceAll(string(text), cloudConfig, "<cloud-config>")
	file := "get-config-marshaled-" + name + ".yml"

	parity.GoldenYAML(t, filepath.Join("testdata", "parity-graft", file), got)

	want, err := os.ReadFile(filepath.Join("testdata", "parity", file))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	parity.CompareYAML(t, want, []byte(got))
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
