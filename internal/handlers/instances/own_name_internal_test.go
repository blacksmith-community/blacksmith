package instances

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"blacksmith/pkg/logger"
)

const prodEnvironment = "prod"

type environmentName string

func (e environmentName) GetEnvironment() string { return string(e) }

// The deployment the UI shows and asks for is the broker's own name, as
// the ownership gate resolves it, so the two always agree. With no name
// the answer holds an empty deployment and never a guessed one.
func TestInstanceDetailsNameTheSharedOwnDeployment(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		file        string
		environment string
		want        string
	}{
		"the instance file":       {file: "lab-bs\n", environment: prodEnvironment, want: "lab-bs"},
		"a guid in the file":      {file: "5f6a0d1c-2b3e-4c8d-9e7f-a1b2c3d4e5f6\n", environment: prodEnvironment, want: "5f6a0d1c-2b3e-4c8d-9e7f-a1b2c3d4e5f6"},
		"the environment":         {environment: prodEnvironment, want: "prod-blacksmith"},
		"neither file nor env":    {want: ""},
		"a blank environment too": {environment: "  ", want: ""},
	}

	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "deployment")

			if scenario.file != "" {
				err := os.WriteFile(path, []byte(scenario.file), 0o600)
				if err != nil {
					t.Fatalf("writing the instance file: %v", err)
				}
			}

			handler := NewHandler(logger.NewNoOpLogger(), environmentName(scenario.environment))
			handler.ownFile = path

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/instance", nil)
			recorder := httptest.NewRecorder()

			handler.GetInstanceDetails(recorder, req)

			var body map[string]interface{}

			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decoding %q: %v", recorder.Body.String(), err)
			}

			data, _ := body["data"].(map[string]interface{})
			if data == nil {
				data = body
			}

			if got, _ := data["deployment"].(string); got != scenario.want {
				t.Errorf("deployment = %q, want %q (body %s)", got, scenario.want, recorder.Body.String())
			}
		})
	}
}
