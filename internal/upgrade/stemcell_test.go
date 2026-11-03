//nolint:testpackage // the test merges the overlay onto the package's test manifest
package upgrade

import (
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
