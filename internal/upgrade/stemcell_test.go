//nolint:testpackage // the test merges the overlay onto the package's test manifest
package upgrade

import (
	"sync"
	"testing"

	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
)

// TestStemcellOverlayMergesDoNotRace runs overlay merges alongside the
// manifest package's merges. Spruce keeps global state, so the race
// detector fails the test unless both take the same lock.
//
//nolint:paralleltest // the merges race other spruce users unless locked
func TestStemcellOverlayMergesDoNotRace(t *testing.T) {
	plan := services.Plan{
		ID: "overlay-race",
		Manifest: map[interface{}]interface{}{
			"name": "overlay-race",
			"stemcells": []interface{}{
				map[interface{}]interface{}{"alias": "default", "os": targetStemcellOS, "version": "1.1"},
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

			_, err := manifest.GenManifest(plan)
			if err != nil {
				t.Errorf("generating the manifest: %v", err)
			}
		}()
	}

	merges.Wait()
}
