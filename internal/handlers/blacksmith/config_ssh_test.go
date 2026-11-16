package blacksmith_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/config"
)

// A config loaded from a file that leaves the SSH keys out serves them as
// true, never as null, so a consumer of the config endpoint never sees null
// for a switch the broker treats as on.
func TestLoadedConfigServesAbsentSSHSwitchesAsTrue(t *testing.T) {
	t.Parallel()

	raw := "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n"
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	var served struct {
		SSH struct {
			Enabled           *bool `json:"enabled"`
			UITerminalEnabled *bool `json:"ui_terminal_enabled"`
		} `json:"ssh"`
	}

	err = json.Unmarshal([]byte(getConfigBody(t, &cfg)), &served)
	if err != nil {
		t.Fatalf("the config answer is not JSON: %v", err)
	}

	for name, got := range map[string]*bool{"ssh.enabled": served.SSH.Enabled, "ssh.ui_terminal_enabled": served.SSH.UITerminalEnabled} {
		if got == nil || !*got {
			t.Errorf("%s is %v, want true", name, got)
		}
	}
}
