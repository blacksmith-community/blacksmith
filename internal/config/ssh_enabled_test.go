package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

// defaultSSHTimeout is the timeout an ssh block with none set loads with.
const defaultSSHTimeout = 600

// A legacy bosh.ssh block never overrides a value the top-level ssh block
// sets, and a legacy false counts on its own.
func TestLegacySSHBlockNeverOverridesTheSSHBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		yaml         string
		wantSSH      bool
		wantTerminal bool
		wantTimeout  int
	}{
		{"ssh false, legacy timeout", "ssh:\n  enabled: false\nbosh_ssh_timeout: 30\n", false, false, 30},
		{"terminal false, legacy timeout", "ssh:\n  ui_terminal_enabled: false\nbosh_ssh_timeout: 30\n", true, false, 30},
		{"legacy false alone", "bosh_ssh_enabled: false\n", false, false, defaultSSHTimeout},
		{"legacy false, ssh true", "ssh:\n  enabled: true\nbosh_ssh_enabled: false\n", true, true, defaultSSHTimeout},
		{"legacy true, ssh false", "ssh:\n  enabled: false\nbosh_ssh_enabled: true\n", false, false, defaultSSHTimeout},
		{"legacy true alone", "bosh_ssh_enabled: true\n", true, true, defaultSSHTimeout},
		{"legacy timeout alone", "bosh_ssh_timeout: 30\n", true, true, 30},
		{"both timeouts", "ssh:\n  timeout: 60\nbosh_ssh_timeout: 30\n", true, true, 60},
		{"neither", "", true, true, defaultSSHTimeout},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := readLegacySSHConfig(t, testCase.yaml)

			if got := cfg.IsSSHEnabled(); got != testCase.wantSSH {
				t.Errorf("IsSSHEnabled() = %t, want %t", got, testCase.wantSSH)
			}

			if got := cfg.IsSSHUITerminalEnabled(); got != testCase.wantTerminal {
				t.Errorf("IsSSHUITerminalEnabled() = %t, want %t", got, testCase.wantTerminal)
			}

			if cfg.SSH.Timeout != testCase.wantTimeout {
				t.Errorf("ssh.timeout = %d, want %d", cfg.SSH.Timeout, testCase.wantTimeout)
			}
		})
	}
}

// readLegacySSHConfig loads a config whose bosh.ssh block is built from the
// bosh_ssh_enabled and bosh_ssh_timeout lines of the case. The lines are
// turned into the nested legacy block, and the rest is the top-level block.
func readLegacySSHConfig(t *testing.T, text string) config.Config {
	t.Helper()

	var top, legacy []string

	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "bosh_ssh_enabled: "):
			legacy = append(legacy, "    enabled: "+strings.TrimPrefix(line, "bosh_ssh_enabled: "))
		case strings.HasPrefix(line, "bosh_ssh_timeout: "):
			legacy = append(legacy, "    timeout: "+strings.TrimPrefix(line, "bosh_ssh_timeout: "))
		case line != "":
			top = append(top, line)
		}
	}

	raw := "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n"
	if len(legacy) > 0 {
		raw += "  ssh:\n" + strings.Join(legacy, "\n") + "\n"
	}

	if len(top) > 0 {
		raw += strings.Join(top, "\n") + "\n"
	}

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
