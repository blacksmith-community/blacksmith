//nolint:testpackage // the test merges the overlay onto the package's test manifest
package upgrade

import (
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
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
// the overlay once, and never quotes a manifest value.
func TestMergeStemcellOverlayErrorsNameTheStep(t *testing.T) {
	t.Parallel()

	const sentinel = "sentinel-manifest-value-do-not-report"

	cases := map[string]struct {
		manifest string
		prefix   string
	}{
		"parse": {
			manifest: "name: " + sentinel + "\nlist: [unterminated\n",
			prefix:   "failed to parse the manifest for the stemcell overlay: ",
		},
		"merge": {
			manifest: "name: " + sentinel + "\nmeta: (( delete \"x\" ))\n",
			prefix:   "failed to merge the stemcell overlay: ",
		},
		"evaluate": {
			manifest: "name: " + sentinel + "\ncopy: (( grab meta.missing ))\n",
			prefix:   "failed to evaluate the manifest after the stemcell overlay merge: ",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := MergeStemcellOverlay(tc.manifest, targetStemcellOS, targetStemcellVersion)
			if err == nil {
				t.Fatal("the overlay succeeded")
			}

			message := err.Error()

			if !strings.HasPrefix(message, tc.prefix) {
				t.Errorf("error %q does not start with %q", message, tc.prefix)
			}

			if count := strings.Count(message, "stemcell overlay"); count != 1 {
				t.Errorf("error names the stemcell overlay %d times: %q", count, message)
			}

			if strings.Contains(message, sentinel) {
				t.Errorf("error quotes a manifest value: %q", message)
			}
		})
	}
}
