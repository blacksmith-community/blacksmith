package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
)

// warnCapture records the lines logged at the Warn level.
type warnCapture struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (w *warnCapture) Warn(msg string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lines = append(w.lines, fmt.Sprintf(msg, args...))
}

func (w *warnCapture) Named(string) logger.Logger { return w }

// readWithWarnings loads a config with extra appended to a minimal one,
// and returns the warnings the load logged.
func readWithWarnings(t *testing.T, extra string) []string {
	t.Helper()

	capture := &warnCapture{NoOpLogger: &logger.NoOpLogger{}}
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	raw := "vault:\n  address: https://vault.example.com:8200\nbosh:\n  address: https://10.0.0.6:25555\n  username: admin\n  password: bosh-secret\n" + extra
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err = config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()

	return append([]string(nil), capture.lines...)
}

// countMentions returns how many lines name the key.
func countMentions(lines []string, key string) int {
	count := 0

	for _, line := range lines {
		if strings.Contains(line, key) {
			count++
		}
	}

	return count
}

// These tests swap the global logger, so none of them runs in parallel.
func TestReadConfigWarnsOnceOfDeprecatedReuseAfter(t *testing.T) { //nolint:paralleltest // swaps the global logger
	lines := readWithWarnings(t, "broker:\n  tls:\n    reuse_after: 4\n")

	if got := countMentions(lines, "broker.tls.reuse_after"); got != 1 {
		t.Fatalf("got %d warnings naming broker.tls.reuse_after, want 1: %q", got, lines)
	}

	for _, line := range lines {
		if strings.Contains(line, "broker.tls.reuse_after") && (!strings.Contains(line, "no effect") || !strings.Contains(line, "removed")) {
			t.Errorf("the warning %q must say the key has no effect and will be removed", line)
		}
	}

	if got := countMentions(lines, "vm_monitoring.max_retries"); got != 0 {
		t.Errorf("an unset vm_monitoring.max_retries drew %d warnings: %q", got, lines)
	}
}

func TestReadConfigWarnsOnceOfDeprecatedMaxRetries(t *testing.T) { //nolint:paralleltest // swaps the global logger
	lines := readWithWarnings(t, "vm_monitoring:\n  max_retries: 5\n")

	if got := countMentions(lines, "vm_monitoring.max_retries"); got != 1 {
		t.Fatalf("got %d warnings naming vm_monitoring.max_retries, want 1: %q", got, lines)
	}

	for _, line := range lines {
		if strings.Contains(line, "vm_monitoring.max_retries") && (!strings.Contains(line, "no effect") || !strings.Contains(line, "removed")) {
			t.Errorf("the warning %q must say the key has no effect and will be removed", line)
		}
	}

	if got := countMentions(lines, "broker.tls.reuse_after"); got != 0 {
		t.Errorf("an unset broker.tls.reuse_after drew %d warnings: %q", got, lines)
	}
}

func TestReadConfigIsSilentWhenDeprecatedKeysAreUnset(t *testing.T) { //nolint:paralleltest // swaps the global logger
	lines := readWithWarnings(t, "")

	for _, key := range []string{"broker.tls.reuse_after", "vm_monitoring.max_retries"} {
		if got := countMentions(lines, key); got != 0 {
			t.Errorf("an unset %s drew %d warnings: %q", key, got, lines)
		}
	}
}
