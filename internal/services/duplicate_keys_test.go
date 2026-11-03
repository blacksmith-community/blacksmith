package services_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

// warnCapture records the broker's warnings, formatted the way its
// printf-style logger formats them, and drops every other level.
type warnCapture struct {
	*logger.NoOpLogger

	mu       sync.Mutex
	warnings []string
}

func (c *warnCapture) Warn(msg string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.warnings = append(c.warnings, fmt.Sprintf(msg, args...))
}

func (c *warnCapture) Named(string) logger.Logger { return c }

// TestReadPlanWarnsOfRepeatedTemplateKeys loads a plan whose plan.yml,
// manifest.yml, and params.yml each repeat a key. The plan loads with the
// last value of each key, and one warning per repeat names the file and
// the key without the value.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestReadPlanWarnsOfRepeatedTemplateKeys(t *testing.T) {
	capture := &warnCapture{NoOpLogger: &logger.NoOpLogger{}}
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	dir := t.TempDir()
	files := map[string]string{
		"plan.yml":     "id: repeats\nname: repeats\ndescription: plan-first-value\ndescription: plan-last-value\n",
		"manifest.yml": "meta:\n  size: manifest-first-value\n  size: manifest-last-value\n",
		"params.yml":   "params:\n  port: params-first-value\n  port: params-last-value\n",
	}

	for name, body := range files {
		err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	plan, err := services.ReadPlan(dir)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}

	if plan.Description != "plan-last-value" {
		t.Errorf("description = %q, want the last value", plan.Description)
	}

	meta, _ := plan.Manifest["meta"].(map[string]interface{})
	if meta["size"] != "manifest-last-value" {
		t.Errorf("meta.size = %v, want the last value", meta["size"])
	}

	params, _ := plan.Manifest["params"].(map[string]interface{})
	if params["port"] != "params-last-value" {
		t.Errorf("params.port = %v, want the last value", params["port"])
	}

	if len(capture.warnings) != len(files) {
		t.Fatalf("got %d warnings, want %d:\n%s", len(capture.warnings), len(files), strings.Join(capture.warnings, "\n"))
	}

	for file, key := range map[string]string{"plan.yml": "description", "manifest.yml": "size", "params.yml": "port"} {
		if !hasWarning(capture.warnings, filepath.Join(dir, file), key) {
			t.Errorf("no warning names %s and the key %s:\n%s", file, key, strings.Join(capture.warnings, "\n"))
		}
	}

	for _, warning := range capture.warnings {
		if strings.Contains(warning, "-value") {
			t.Errorf("a warning holds a value: %s", warning)
		}
	}
}

func hasWarning(warnings []string, file, key string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, file) && strings.Contains(warning, key) {
			return true
		}
	}

	return false
}
