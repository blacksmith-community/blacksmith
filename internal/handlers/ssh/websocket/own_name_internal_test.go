package websocket

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

type environmentConfig struct {
	interfaces.Config

	environment string
}

func (c environmentConfig) GetEnvironment() string { return c.environment }

func ownNameFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "deployment")

	if content != "" {
		err := os.WriteFile(path, []byte(content), 0o600)
		if err != nil {
			t.Fatalf("writing the instance file: %v", err)
		}
	}

	return path
}

// The handler opens SSH to the deployment the shared own name resolves to.
// With no instance file and no environment there is no name, and the
// handler refuses instead of guessing a deployment called blacksmith.
func TestBlacksmithDeploymentComesFromTheSharedOwnName(t *testing.T) {
	t.Parallel()

	log := logger.Get().Named("test")

	cases := map[string]struct {
		file        string
		environment string
		want        string
		wantErr     error
	}{
		"the instance file": {file: "lab-bs\n", environment: "prod", want: "lab-bs", wantErr: errBOSHDirectorNotConfigured},
		"the environment":   {environment: "prod", want: "prod-blacksmith", wantErr: errBOSHDirectorNotConfigured},
		"no name":           {wantErr: errNoOwnDeployment},
	}

	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(Dependencies{
				Logger:      log,
				Config:      environmentConfig{environment: scenario.environment},
				OwnNameFile: ownNameFile(t, scenario.file),
			})

			got, _, err := handler.resolveBlacksmithDeployment(t.Context(), log)
			if !errors.Is(err, scenario.wantErr) {
				t.Fatalf("error = %v, want %v", err, scenario.wantErr)
			}

			if got != scenario.want {
				t.Errorf("deployment = %q, want %q", got, scenario.want)
			}
		})
	}
}
