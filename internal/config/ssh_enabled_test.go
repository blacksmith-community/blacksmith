package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/config"
)

func readSSHConfig(t *testing.T, sshBlock string) config.Config {
	t.Helper()

	raw := "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n" + sshBlock
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	return cfg
}

// Both SSH switches default to on, and an explicit false stays false.
func TestSSHSwitchesHonorAnExplicitFalse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		yaml         string
		wantSSH      bool
		wantTerminal bool
	}{
		{"both absent", "", true, true},
		{"ssh block without the switches", "ssh:\n  timeout: 30\n", true, true},
		{"both true", "ssh:\n  enabled: true\n  ui_terminal_enabled: true\n", true, true},
		{"ssh false", "ssh:\n  enabled: false\n", false, false},
		{"terminal false", "ssh:\n  ui_terminal_enabled: false\n", true, false},
		{"both false", "ssh:\n  enabled: false\n  ui_terminal_enabled: false\n", false, false},
		{"ssh true and terminal false", "ssh:\n  enabled: true\n  ui_terminal_enabled: false\n", true, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := readSSHConfig(t, testCase.yaml)

			if got := cfg.IsSSHEnabled(); got != testCase.wantSSH {
				t.Errorf("IsSSHEnabled() = %t, want %t", got, testCase.wantSSH)
			}

			if got := cfg.IsSSHUITerminalEnabled(); got != testCase.wantTerminal {
				t.Errorf("IsSSHUITerminalEnabled() = %t, want %t", got, testCase.wantTerminal)
			}
		})
	}
}

// A config built by hand with no SSH block, such as a test's, is also on.
func TestSSHSwitchesDefaultOnForAnEmptyConfig(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}

	if !cfg.IsSSHEnabled() || !cfg.IsSSHUITerminalEnabled() {
		t.Error("an empty config turned SSH off")
	}
}
