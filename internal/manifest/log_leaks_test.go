package manifest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
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
