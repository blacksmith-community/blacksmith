package bosh_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

const configLogSentinel = "sentinel-config-secret-do-not-log"

type recordingBoshLogger struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordingBoshLogger) record(format string, args []interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *recordingBoshLogger) Infof(format string, args ...interface{})  { r.record(format, args) }
func (r *recordingBoshLogger) Debugf(format string, args ...interface{}) { r.record(format, args) }
func (r *recordingBoshLogger) Errorf(format string, args ...interface{}) { r.record(format, args) }

func (r *recordingBoshLogger) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return strings.Join(r.lines, "\n")
}

// GetConfig is generic, so what it logs must never include a config body.
func TestGetConfigNeverLogsTheConfigBody(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs" {
			return false
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"7","type":"resurrection","name":"blacksmith","content":"password: ` + configLogSentinel + `\nrules: []\n","created_at":"2026-10-05 00:00:00 UTC"}]`))

		return true
	})

	logs := &recordingBoshLogger{}
	director := newReadAdapter(t, server, func(config *bosh.Config) { config.Logger = logs })

	got, err := director.GetConfig(t.Context(), "resurrection", "blacksmith")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	data, isMap := got.(map[string]interface{})
	if !isMap || data["password"] != configLogSentinel {
		t.Fatalf("expected the parsed config to carry its content, got %v", got)
	}

	out := logs.output()
	if !strings.Contains(out, "resurrection/blacksmith") {
		t.Errorf("expected the config type and name in the log, got:\n%s", out)
	}

	if strings.Contains(out, configLogSentinel) {
		t.Errorf("log output leaked config content:\n%s", out)
	}
}
