package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// logEntry is one line the broker logged, with its level.
type logEntry struct {
	level   string
	message string
}

// recordingLogger keeps every Info, Warn, and Error line it is given, and
// drops the rest, so a test can say exactly what the broker told the operator.
type recordingLogger struct {
	loggerPkg.Logger

	mu      sync.Mutex
	entries []logEntry
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: loggerPkg.CreateSilentLogger()}
}

func (r *recordingLogger) record(level, msg string, args []interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}

	r.entries = append(r.entries, logEntry{level: level, message: msg})
}

func (r *recordingLogger) Info(msg string, args ...interface{})  { r.record("info", msg, args) }
func (r *recordingLogger) Warn(msg string, args ...interface{})  { r.record("warn", msg, args) }
func (r *recordingLogger) Error(msg string, args ...interface{}) { r.record("error", msg, args) }

func (r *recordingLogger) Warning(msg string, args ...interface{}) { r.record("warn", msg, args) }

// lines returns the messages logged at one level, in order.
func (r *recordingLogger) lines(level string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []string

	for _, entry := range r.entries {
		if entry.level == level {
			out = append(out, entry.message)
		}
	}

	return out
}

// scriptedPool wraps a scripted director the way main.go wraps the real one,
// and fails the test if the director is asked anything, because setup must
// not reach the director when it has no CredHub connection to build.
func scriptedPool(t *testing.T) *bosh.PooledDirector {
	t.Helper()

	director := testutil.NewScriptedBOSHDirector()
	director.GetInfoFn = func() (*bosh.Info, error) {
		t.Error("setup asked the director for /info with no CredHub connection to build")

		return nil, fmt.Errorf("unexpected call")
	}

	return bosh.NewPooledDirector(director, 1, time.Second, loggerPkg.CreateSilentLogger())
}

func enabledCleanupConfig() *config.Config {
	cfg := &config.Config{}
	cfg.CredHub.Cleanup.Enabled = true

	return cfg
}

func TestSetupCredHubCleanupWarnsOnceWhenNoConnectionIsConfigured(t *testing.T) {
	t.Parallel()

	logger := newRecordingLogger()
	brokerInstance := &broker.Broker{}

	client := setupCredHubCleanup(enabledCleanupConfig(), brokerInstance, scriptedPool(t), logger)

	if client != nil {
		t.Error("expected no CredHub client when no connection is configured")
	}

	if brokerInstance.CredentialCleaner != nil {
		t.Error("expected CredentialCleaner to stay nil when no connection is configured")
	}

	if brokerInstance.CredentialFinder != nil {
		t.Error("expected CredentialFinder to stay nil when no connection is configured")
	}

	if errs := logger.lines("error"); len(errs) != 0 {
		t.Errorf("an unconfigured block is not a problem list, but the broker logged errors: %q", errs)
	}

	warnings := logger.lines("warn")
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %d: %q", len(warnings), warnings)
	}

	for _, want := range []string{
		"credhub.url",
		"credhub.ca_cert",
		"credhub.client_secret",
		"credhub.director_name",
		"on by default",
		"deprovisioned instances keep their director CredHub variables",
		"blacksmith-integration",
	} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning does not mention %q: %s", want, warnings[0])
		}
	}
}

func TestSetupCredHubCleanupKeepsProblemListForPartialConnection(t *testing.T) {
	t.Parallel()

	cfg := enabledCleanupConfig()
	cfg.CredHub.URL = "https://10.0.0.6:8844"

	logger := newRecordingLogger()
	brokerInstance := &broker.Broker{}

	client := setupCredHubCleanup(cfg, brokerInstance, scriptedPool(t), logger)

	if client != nil || brokerInstance.CredentialCleaner != nil {
		t.Error("expected cleanup to stay off for a partial connection")
	}

	if warnings := logger.lines("warn"); len(warnings) != 0 {
		t.Errorf("a partial connection must not get the unconfigured warning: %q", warnings)
	}

	errs := logger.lines("error")
	if len(errs) != 1 {
		t.Fatalf("expected one problem-list line, got %d: %q", len(errs), errs)
	}

	for _, want := range []string{
		"credhub.ca_cert is required",
		"credhub.client_secret is required",
		"credhub.director_name is required",
	} {
		if !strings.Contains(errs[0], want) {
			t.Errorf("problem list does not mention %q: %s", want, errs[0])
		}
	}

	if strings.Contains(errs[0], "credhub.url is required") {
		t.Errorf("problem list names the url, which is set: %s", errs[0])
	}
}

func TestSetupCredHubCleanupDisabledStaysQuiet(t *testing.T) {
	t.Parallel()

	logger := newRecordingLogger()
	brokerInstance := &broker.Broker{}

	if client := setupCredHubCleanup(&config.Config{}, brokerInstance, scriptedPool(t), logger); client != nil {
		t.Error("expected no CredHub client when cleanup is disabled")
	}

	if warnings := logger.lines("warn"); len(warnings) != 0 {
		t.Errorf("a deliberately disabled cleanup must not warn: %q", warnings)
	}
}
