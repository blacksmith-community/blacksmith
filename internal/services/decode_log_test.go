package services_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

// decodeProbe stands in for a credential value. No error or log line may
// hold it.
const decodeProbe = "probe-7f3c1e-value"

// logCapture records every line the broker logs at debug, info, warn, and
// error, formatted the way its printf-style logger formats them.
type logCapture struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (c *logCapture) record(level, msg string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, level+" "+fmt.Sprintf(msg, args...))
}

func (c *logCapture) Debug(msg string, args ...interface{}) { c.record("DEBUG", msg, args...) }
func (c *logCapture) Info(msg string, args ...interface{})  { c.record("INFO", msg, args...) }
func (c *logCapture) Warn(msg string, args ...interface{})  { c.record("WARN", msg, args...) }
func (c *logCapture) Error(msg string, args ...interface{}) { c.record("ERROR", msg, args...) }

func (c *logCapture) Named(string) logger.Logger { return c }

// TestCatalogDecodeFailuresLeaveFileContentOut loads plan files that hold
// a credential value and fail to decode. Neither the error nor any log
// line holds the value, and a debug line names the file and its size.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestCatalogDecodeFailuresLeaveFileContentOut(t *testing.T) {
	cases := map[string]struct {
		files   map[string]string
		file    string
		service bool
		debug   bool
	}{
		"plan.yml with a type error": {
			files: map[string]string{"plan.yml": "id: leaks\nname: leaks\ncredentials:\n  password: " + decodeProbe + "\nlimit: [1]\n"},
			file:  "plan.yml",
			debug: true,
		},
		"plan.yml with a syntax error": {
			files: map[string]string{"plan.yml": "id: leaks\nname: leaks\ncredentials:\n  password: " + decodeProbe + "\n  other: [unclosed\n"},
			file:  "plan.yml",
			debug: true,
		},
		"service.yml with a type error": {
			files: map[string]string{
				"service.yml":        "id: leaks\nname: leaks\ndescription: " + decodeProbe + "\nbindable: [1]\n",
				"small/plan.yml":     "id: small\nname: small\n",
				"small/manifest.yml": "meta: {}\n",
			},
			file:    "service.yml",
			service: true,
			debug:   true,
		},
		"service.yml with a syntax error": {
			files: map[string]string{
				"service.yml":        "id: leaks\nname: leaks\ndescription: " + decodeProbe + "\ntags: [unclosed\n",
				"small/plan.yml":     "id: small\nname: small\n",
				"small/manifest.yml": "meta: {}\n",
			},
			file:    "service.yml",
			service: true,
			debug:   true,
		},
		"credentials.yml with a syntax error": {
			files: map[string]string{
				"plan.yml":        "id: leaks\nname: leaks\n",
				"credentials.yml": "password: " + decodeProbe + "\nother: [unclosed\n",
			},
			file: "credentials.yml",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			capture := &logCapture{NoOpLogger: &logger.NoOpLogger{}}
			previous := logger.Get()

			logger.Set(capture)
			t.Cleanup(func() { logger.Set(previous) })

			dir := writePlanDir(t, tc.files)
			path := filepath.Join(dir, tc.file)

			var err error
			if tc.service {
				_, err = services.ReadService(dir)
			} else {
				_, err = services.ReadPlan(dir)
			}

			if err == nil {
				t.Fatal("the load succeeds, want an error")
			}

			if strings.Contains(err.Error(), decodeProbe) {
				t.Error("the error holds the file's content")
			}

			for _, line := range capture.lines {
				if strings.Contains(line, decodeProbe) {
					t.Errorf("a %s log line holds the file's content", strings.Fields(line)[0])
				}
			}

			if tc.debug {
				size := fmt.Sprintf("%d bytes", len(tc.files[tc.file]))
				if !hasDebugLine(capture.lines, path, size) {
					t.Errorf("no debug line names %s and its size of %s", tc.file, size)
				}
			}
		})
	}
}

func hasDebugLine(lines []string, path, size string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "DEBUG ") && strings.Contains(line, path) && strings.Contains(line, size) {
			return true
		}
	}

	return false
}
