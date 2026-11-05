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
	"blacksmith/pkg/utils"

	graftlog "github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// TestStemcellOverlayMergesDoNotRace runs overlay merges alongside
// GenManifest's renders, so the race detector sees the two in concurrent
// use. The overlay sets the stemcell directly and shares no mutable state
// with the render, so it takes no lock.
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

// TestStemcellOverlayLeavesRequestOperatorsLiteral renders a plan whose
// nested request parameter holds operator text, as a tenant may send it,
// and runs the overlay over the render, as a stemcell upgrade does with
// the manifest it reads back from the director. The overlay must change
// the stemcell and leave the operator text exactly as it was, so none of
// grab, file, or vault runs with the broker's environment or credentials.
//
//nolint:paralleltest // t.Setenv cannot run in a parallel test
func TestStemcellOverlayLeavesRequestOperatorsLiteral(t *testing.T) {
	const probe = "stemcell-overlay-env-probe-do-not-report"

	t.Setenv("BLACKSMITH_STEMCELL_OVERLAY_PROBE", probe)
	// GenManifest sets CREDENTIALS and leaves it set.
	t.Setenv("CREDENTIALS", "")

	operators := map[string]interface{}{
		"env":   "(( grab $BLACKSMITH_STEMCELL_OVERLAY_PROBE ))",
		"file":  "(( file /etc/hostname ))",
		"vault": `(( vault "secret/x:y" ))`,
	}

	plan := services.Plan{
		ID: "overlay-literal",
		Manifest: map[string]interface{}{
			"name": "overlay-literal",
			"stemcells": []interface{}{
				map[string]interface{}{"alias": "default", "os": targetStemcellOS, "version": "1.1"},
			},
		},
	}

	params := map[string]interface{}{"nested": operators}

	rendered, err := manifest.GenManifest(plan, "overlay-literal", utils.Wrap("meta.params", params))
	if err != nil {
		t.Fatalf("generating the manifest: %v", err)
	}

	merged, err := MergeStemcellOverlay(rendered, targetStemcellOS, targetStemcellVersion)
	if err != nil {
		t.Fatalf("merging the stemcell overlay: %v", err)
	}

	if strings.Contains(merged, probe) {
		t.Error("the overlay output holds the probe value from the environment")
	}

	stemcellOS, version, err := GetCurrentStemcell(merged)
	if err != nil {
		t.Fatalf("reading the stemcell back: %v", err)
	}

	if stemcellOS != targetStemcellOS || version != targetStemcellVersion {
		t.Errorf("stemcell is %s/%s, want %s/%s", stemcellOS, version, targetStemcellOS, targetStemcellVersion)
	}

	var decoded map[string]interface{}

	err = yaml.Unmarshal([]byte(merged), &decoded)
	if err != nil {
		t.Fatalf("decoding the overlay output: %v", err)
	}

	meta, _ := decoded["meta"].(map[string]interface{})
	requestParams, _ := meta["params"].(map[string]interface{})
	nested, _ := requestParams["nested"].(map[string]interface{})

	for key, want := range operators {
		if got := nested[key]; got != want {
			t.Errorf("meta.params.nested.%s is not the operator text the request sent", key)
		}
	}
}

// TestMergeStemcellOverlayErrorsNameTheStep checks that a failed overlay
// says that reading the manifest failed, names the overlay
// once, and never quotes a manifest value, in its error or in any line
// the broker or graft logs. It swaps the process-wide log hooks, so it
// cannot run in parallel.
//
//nolint:paralleltest // the test swaps the broker's logger and graft's stderr hook
func TestMergeStemcellOverlayErrorsNameTheStep(t *testing.T) {
	const sentinel = "sentinel-manifest-value-do-not-report"

	cases := map[string]struct {
		manifest string
		prefix   string
	}{
		"parse": {
			manifest: "name: " + sentinel + "\nlist: [unterminated\n",
			prefix:   "failed to parse the manifest for the stemcell overlay: ",
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

			if strings.Contains(message, sentinel) {
				t.Error("error quotes a manifest value")
			}

			if strings.Contains(message, "\x1b") {
				t.Error("error holds an ANSI escape")
			}

			assertOverlayLogsClean(t, lines(), sentinel)
		})
	}
}

// TestMergeStemcellOverlayKeepsOperatorText checks that operator text in
// the manifest survives the overlay as it was written. A grab through a
// scalar and a calc over a probe operand would both fail if the overlay
// evaluated them, and graft's error text once quoted the probe in each,
// so the test also reads every line the broker and graft log. It swaps
// the process-wide log hooks, so it cannot run in parallel.
//
//nolint:paralleltest // the test swaps the broker's logger and graft's stderr hook
func TestMergeStemcellOverlayKeepsOperatorText(t *testing.T) {
	const sentinel = "sentinel-manifest-value-do-not-report"

	// A calc expression reads a hyphen as subtraction, so the calc probe
	// is one word.
	const calcProbe = "sentinelcalcoperanddonotreport"

	cases := map[string]struct {
		manifest string
		probe    string
		key      string
		operator string
	}{
		"a grab of a missing path": {
			manifest: "name: " + sentinel + "\ncopy: (( grab meta.missing ))\n",
			probe:    sentinel,
			key:      "copy",
			operator: "(( grab meta.missing ))",
		},
		"a grab through a scalar": {
			manifest: "name: x\nmeta:\n  scalar: " + sentinel + "\ncopy: (( grab meta.scalar.deeper ))\n",
			probe:    sentinel,
			key:      "copy",
			operator: "(( grab meta.scalar.deeper ))",
		},
		"a calc over a probe operand": {
			manifest: "name: x\nmeta:\n  expression: \"" + calcProbe + " * 2\"\ntotal: (( calc meta.expression ))\n",
			probe:    calcProbe,
			key:      "total",
			operator: "(( calc meta.expression ))",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lines := captureOverlayLogs(t)

			merged, err := MergeStemcellOverlay(tc.manifest, targetStemcellOS, targetStemcellVersion)
			if err != nil {
				t.Fatalf("merging the stemcell overlay: %v", err)
			}

			var decoded map[string]interface{}

			err = yaml.Unmarshal([]byte(merged), &decoded)
			if err != nil {
				t.Fatalf("decoding the overlay output: %v", err)
			}

			if got := decoded[tc.key]; got != tc.operator {
				t.Errorf("%s is not the operator text the manifest held", tc.key)
			}

			assertOverlayLogsClean(t, lines(), tc.probe)
		})
	}
}

// assertOverlayLogsClean fails the test when a logged line quotes probe or
// holds an ANSI escape.
func assertOverlayLogsClean(t *testing.T, lines []string, probe string) {
	t.Helper()

	for _, line := range lines {
		if strings.Contains(line, probe) {
			t.Error("a log line quotes a manifest value")
		}

		if strings.Contains(line, "\x1b") {
			t.Error("a log line holds an ANSI escape")
		}
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
