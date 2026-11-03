package manifest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"

	graftlog "github.com/fivetwenty-io/graft/log"
)

const (
	leakSentinelPassword   = "sentinel-password-do-not-log"    //nolint:gosec // fake credential the tests prove stays out of logs
	leakSentinelVault      = "sentinel-vault-token-do-not-log" //nolint:gosec // fake credential the tests prove stays out of logs
	leakSentinelBoshSecret = "sentinel-bosh-secret-do-not-log" //nolint:gosec // fake credential the tests prove stays out of logs
)

// captureLogger records every message at every level, formatted the way the
// broker's printf-style logger formats them.
type captureLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
}

func (c *captureLogger) record(msg string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(msg, args...))
}

func (c *captureLogger) Debug(msg string, args ...interface{}) { c.record(msg, args) }
func (c *captureLogger) Info(msg string, args ...interface{})  { c.record(msg, args) }
func (c *captureLogger) Warn(msg string, args ...interface{})  { c.record(msg, args) }
func (c *captureLogger) Error(msg string, args ...interface{}) { c.record(msg, args) }

func (c *captureLogger) Named(string) logger.Logger { return c }

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

func TestBuildInitCommandNeverLogsSecretEnvironment(t *testing.T) {
	t.Setenv("VAULT_TOKEN", leakSentinelVault)
	t.Setenv("BOSH_CLIENT_SECRET", leakSentinelBoshSecret)
	t.Setenv("VAULT_ADDR", "http://vault.example:8200")

	capture := newCaptureLogger()
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	cmd := buildInitCommand(context.Background(), services.Plan{ID: "plan-1", InitScriptPath: "/tmp/init"}, "inst-1")

	if !strings.Contains(strings.Join(cmd.Env, "\n"), leakSentinelVault) {
		t.Fatal("the command environment should still carry VAULT_TOKEN for the init script")
	}

	out := capture.output()
	if out == "" {
		t.Fatal("expected the builder to log something")
	}

	for _, secret := range []string{leakSentinelVault, leakSentinelBoshSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked %q:\n%s", secret, out)
		}
	}
}

func TestExtractCredentialsNeverLogsAStringValue(t *testing.T) {
	capture := newCaptureLogger()

	_, err := extractCredentialsFromManifest("credentials: "+leakSentinelPassword+"\n", capture)
	if err == nil {
		t.Fatal("expected an error for a credentials string")
	}

	out := capture.output()
	if !strings.Contains(out, "got string instead of map") {
		t.Errorf("expected the failure to be logged, got:\n%s", out)
	}

	if strings.Contains(out, leakSentinelPassword) || strings.Contains(err.Error(), leakSentinelPassword) {
		t.Errorf("credentials value leaked:\n%s\n%s", out, err)
	}
}

func TestFailedInitScriptOutputMasksSecretEnvironment(t *testing.T) {
	capture := newCaptureLogger()
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	script := filepath.Join(t.TempDir(), "init")

	body := "#!/bin/sh\necho \"token=$VAULT_TOKEN secret=$BOSH_CLIENT_SECRET\"\nexit 1\n"

	err := os.WriteFile(script, []byte(body), 0o700) //nolint:gosec // test script must be executable
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(context.Background(), script)
	cmd.Env = []string{"VAULT_TOKEN=" + leakSentinelVault, "BOSH_CLIENT_SECRET=" + leakSentinelBoshSecret}

	err = executeInitScript(cmd, script)
	if err == nil {
		t.Fatal("expected the init script to fail")
	}

	out := capture.output()
	if !strings.Contains(out, "token=<redacted> secret=<redacted>") {
		t.Errorf("expected the failure output with masked values, got:\n%s", out)
	}

	for _, secret := range []string{leakSentinelVault, leakSentinelBoshSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked %q:\n%s", secret, out)
		}
	}
}

// TestGenManifestErrorsQuoteNoValue drives a render into an evaluation
// failure where a key beside the failing operator holds a probe value. One
// case grabs through the probe as if it were a map, and the other has calc
// evaluate an expression that holds the probe. Neither the returned error
// nor any line the broker or graft logs may quote the probe or hold an
// ANSI escape.
//
//nolint:paralleltest // the test swaps the broker's logger and graft's stderr hook, and GenManifest sets CREDENTIALS
func TestGenManifestErrorsQuoteNoValue(t *testing.T) {
	// A calc expression reads a hyphen as subtraction, so the probe is one
	// word.
	const probe = "sentinelmanifestvaluedonotreport"

	cases := map[string]map[string]interface{}{
		"grab through a scalar": {
			"meta": map[string]interface{}{"scalar": probe},
			"copy": "(( grab meta.scalar.deeper ))",
		},
		"calc over a probe operand": {
			"meta":  map[string]interface{}{"expression": probe + " * 2"},
			"total": "(( calc meta.expression ))",
		},
	}

	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CREDENTIALS", "")

			capture := newCaptureLogger()
			previousLogger := logger.Get()
			previousStderr := graftlog.PrintStdErrf

			logger.Set(capture)

			var (
				graftMu    sync.Mutex
				graftLines []string
			)

			graftlog.PrintStdErrf = func(format string, args ...interface{}) {
				graftMu.Lock()
				defer graftMu.Unlock()

				graftLines = append(graftLines, fmt.Sprintf(format, args...))
			}

			t.Cleanup(func() {
				logger.Set(previousLogger)

				graftlog.PrintStdErrf = previousStderr
			})

			_, err := GenManifest(services.Plan{ID: "value-free-errors", Manifest: manifest}, "value-free-errors")
			if err == nil {
				t.Fatal("the render succeeded")
			}

			if strings.Contains(err.Error(), probe) {
				t.Error("the error quotes a manifest value")
			}

			if strings.Contains(err.Error(), "\x1b") {
				t.Error("the error holds an ANSI escape")
			}

			graftMu.Lock()
			lines := append([]string{capture.output()}, graftLines...)
			graftMu.Unlock()

			for _, line := range lines {
				if strings.Contains(line, probe) {
					t.Error("a log line quotes a manifest value")
				}

				if strings.Contains(line, "\x1b") {
					t.Error("a log line holds an ANSI escape")
				}
			}
		})
	}
}
