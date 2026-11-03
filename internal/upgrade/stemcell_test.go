//nolint:testpackage // the test merges the overlay onto the package's test manifest
package upgrade

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"

	graftlog "github.com/fivetwenty-io/graft/log"
)

// TestStemcellOverlayMergesDoNotRace runs overlay merges alongside
// GenManifest's renders, so the race detector sees the two in concurrent
// use. The overlay builds a graft engine of its own on every call and
// shares no mutable state with the render, so the test would most likely
// still pass if the overlay stopped taking the manifest lock. The overlay
// keeps the lock because evaluation reads the process-wide CREDENTIALS
// value that GenManifest sets.
//
//nolint:paralleltest // t.Setenv cannot run in a parallel test
func TestStemcellOverlayMergesDoNotRace(t *testing.T) {
	// GenManifest sets CREDENTIALS and leaves it set.
	t.Setenv("CREDENTIALS", "")

	plan := services.Plan{
		ID: "overlay-race",
		Manifest: map[string]interface{}{
			"name": "overlay-race",
			"stemcells": []interface{}{
				map[string]interface{}{"alias": "default", "os": targetStemcellOS, "version": "1.1"},
			},
		},
	}

	var merges sync.WaitGroup

	for range 8 {
		merges.Add(2)

		go func() {
			defer merges.Done()

			_, err := MergeStemcellOverlay(waitManifest, targetStemcellOS, targetStemcellVersion)
			if err != nil {
				t.Errorf("merging the stemcell overlay: %v", err)
			}
		}()

		go func() {
			defer merges.Done()

			_, err := manifest.GenManifest(plan, "stemcell-instance")
			if err != nil {
				t.Errorf("generating the manifest: %v", err)
			}
		}()
	}

	merges.Wait()
}

// TestMergeStemcellOverlayErrorsNameTheStep checks that a failed overlay
// says whether reading, merging, or evaluating the manifest failed, names
// the overlay once, and never quotes a manifest value. The two grab and
// calc cases put the probe where graft's own error text used to quote it,
// and the test also reads every line the broker and graft log. It swaps
// the process-wide log hooks, so it cannot run in parallel.
//
//nolint:paralleltest // the test swaps the broker's logger and graft's stderr hook
func TestMergeStemcellOverlayErrorsNameTheStep(t *testing.T) {
	const sentinel = "sentinel-manifest-value-do-not-report"

	// A calc expression reads a hyphen as subtraction, so the calc probe
	// is one word.
	const calcProbe = "sentinelcalcoperanddonotreport"

	cases := map[string]struct {
		manifest string
		probe    string
		prefix   string
	}{
		"parse": {
			manifest: "name: " + sentinel + "\nlist: [unterminated\n",
			probe:    sentinel,
			prefix:   "failed to parse the manifest for the stemcell overlay: ",
		},
		"merge": {
			manifest: "name: " + sentinel + "\nmeta: (( delete \"x\" ))\n",
			probe:    sentinel,
			prefix:   "failed to merge the stemcell overlay: ",
		},
		"evaluate": {
			manifest: "name: " + sentinel + "\ncopy: (( grab meta.missing ))\n",
			probe:    sentinel,
			prefix:   "failed to evaluate the manifest after the stemcell overlay merge: ",
		},
		"evaluate a grab through a scalar": {
			manifest: "name: x\nmeta:\n  scalar: " + sentinel + "\ncopy: (( grab meta.scalar.deeper ))\n",
			probe:    sentinel,
			prefix:   "failed to evaluate the manifest after the stemcell overlay merge: ",
		},
		"evaluate a calc over a probe operand": {
			manifest: "name: x\nmeta:\n  expression: \"" + calcProbe + " * 2\"\ntotal: (( calc meta.expression ))\n",
			probe:    calcProbe,
			prefix:   "failed to evaluate the manifest after the stemcell overlay merge: ",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lines := captureOverlayLogs(t)

			_, err := MergeStemcellOverlay(tc.manifest, targetStemcellOS, targetStemcellVersion)
			if err == nil {
				t.Fatal("the overlay succeeded")
			}

			message := err.Error()

			if !strings.HasPrefix(message, tc.prefix) {
				t.Errorf("error does not start with %q", tc.prefix)
			}

			if count := strings.Count(message, "stemcell overlay"); count != 1 {
				t.Errorf("error names the stemcell overlay %d times", count)
			}

			if strings.Contains(message, tc.probe) {
				t.Error("error quotes a manifest value")
			}

			if strings.Contains(message, "\x1b") {
				t.Error("error holds an ANSI escape")
			}

			for _, line := range lines() {
				if strings.Contains(line, tc.probe) {
					t.Error("a log line quotes a manifest value")
				}

				if strings.Contains(line, "\x1b") {
					t.Error("a log line holds an ANSI escape")
				}
			}
		})
	}
}

// captureOverlayLogs records every line the broker's logger and graft's
// stderr hook receive until the test ends, and returns a reader for them.
func captureOverlayLogs(t *testing.T) func() []string {
	t.Helper()

	capture := &captureLogger{redeployLogger: &redeployLogger{NoOpLogger: &logger.NoOpLogger{}}}

	var (
		graftMu    sync.Mutex
		graftLines []string
	)

	previousLogger := logger.Get()
	previousStderr := graftlog.PrintStdErrf

	logger.Set(capture)

	graftlog.PrintStdErrf = func(format string, args ...interface{}) {
		graftMu.Lock()
		defer graftMu.Unlock()

		graftLines = append(graftLines, fmt.Sprintf(format, args...))
	}

	t.Cleanup(func() {
		logger.Set(previousLogger)

		graftlog.PrintStdErrf = previousStderr
	})

	return func() []string {
		graftMu.Lock()
		defer graftMu.Unlock()

		return append(capture.all(), graftLines...)
	}
}
