package config_test

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"

	"blacksmith/internal/config"
)

func TestCommandsConfigSwitches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		enabled bool
		root    bool
		key     string
	}{
		{"nothing set", ``, true, false, ""},
		{"enabled false alone", "commands:\n  enabled: false\n", false, false, ""},
		{"allow_root alone", "commands:\n  allow_root: true\n", true, true, ""},
		{"disabled with allow_root", "commands:\n  enabled: false\n  allow_root: true\n", false, false, ""},
		{"enabled with allow_root", "commands:\n  enabled: true\n  allow_root: true\n", true, true, ""},
		{"credential key", "commands:\n  credential:\n    key: hunter2\n", true, false, "hunter2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var cfg config.Config

			err := yaml.Unmarshal([]byte(test.yaml), &cfg)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			cmds := cfg.Commands
			if got := cmds.CommandsEnabled(); got != test.enabled {
				t.Errorf("CommandsEnabled() = %v, want %v", got, test.enabled)
			}

			if got := cmds.RootAllowed(); got != test.root {
				t.Errorf("RootAllowed() = %v, want %v", got, test.root)
			}

			if got := cmds.CredentialKey(); got != test.key {
				t.Errorf("CredentialKey() does not match the configured key (length %d, want %d)", len(got), len(test.key))
			}
		})
	}
}

func TestCommandsConfigYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	raw := "commands:\n  enabled: false\n  allow_root: true\n  credential:\n    key: hunter2\n"

	var cfg config.Config

	err := yaml.Unmarshal([]byte(raw), &cfg)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Commands.Operation.On == nil || *cfg.Commands.Operation.On {
		t.Error("enabled did not load as false")
	}

	if cfg.Commands.Root == nil || !*cfg.Commands.Root {
		t.Error("allow_root did not load as true")
	}

	if cfg.Commands.Credential.Key != "hunter2" {
		t.Error("credential.key did not load")
	}
}

func TestBOSHDirectorWorkerCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		workers int
		want    int
	}{
		{"unset", 0, 4},
		{"two", 2, 2},
		{"eight", 8, 8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			bosh := config.BOSHConfig{DirectorWorkers: test.workers}
			if got := bosh.DirectorWorkerCount(); got != test.want {
				t.Errorf("DirectorWorkerCount() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBOSHDirectorWorkersValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setting string
		fails   bool
	}{
		{"negative", "  director_workers: -1\n", true},
		{"one", "  director_workers: 1\n", true},
		{"zero", "  director_workers: 0\n", false},
		{"unset", "", false},
		{"two", "  director_workers: 2\n", false},
		{"eight", "  director_workers: 8\n", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.ReadConfig(writeBOSHConfig(t, test.setting))
			if !test.fails {
				if err != nil {
					t.Fatalf("ReadConfig: %v", err)
				}

				return
			}

			if !errors.Is(err, config.ErrBOSHDirectorWorkersTooFew) {
				t.Fatalf("expected %v, got %v", config.ErrBOSHDirectorWorkersTooFew, err)
			}

			if !strings.Contains(err.Error(), "director_workers") {
				t.Errorf("expected the error to name director_workers, got %q", err.Error())
			}
		})
	}
}
