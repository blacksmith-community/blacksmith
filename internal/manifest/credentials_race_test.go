package manifest_test

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"
)

// Concurrent renders for different instances each see their own
// CREDENTIALS path. Every caller first writes CREDENTIALS for its own
// instance, the way callers did before GenManifest owned the variable, and
// no caller renders until all of them have written it. A render that reads
// the variable outside the manifest lock therefore sees another instance's
// path every time.
//
//nolint:paralleltest // the test sets an environment variable
func TestGenManifestRendersEachInstancesOwnCredentialsPath(t *testing.T) {
	t.Setenv("CREDENTIALS", "")

	const renders = 8

	var written, done sync.WaitGroup

	written.Add(renders)
	done.Add(renders)

	manifests := make([]string, renders)
	errs := make([]error, renders)

	for index := range renders {
		go func() {
			defer done.Done()

			instanceID := raceInstanceID(index)
			plan := services.Plan{Manifest: map[string]interface{}{
				"vault_path": `(( concat $CREDENTIALS "" ))`,
			}}

			_ = os.Setenv("CREDENTIALS", "secret/"+instanceID) //nolint:usetesting // restored by t.Setenv above

			written.Done()
			written.Wait()

			manifests[index], errs[index] = manifestPkg.GenManifest(plan, instanceID)
		}()
	}

	done.Wait()

	for index := range renders {
		if errs[index] != nil {
			t.Fatalf("render %d: %s", index, errs[index])
		}

		for other := range renders {
			line := "vault_path: secret/" + raceInstanceID(other) + "\n"
			if found := strings.Contains(manifests[index], line); found != (other == index) {
				t.Errorf("render %d: contains the path of instance %d is %t", index, other, found)
			}
		}
	}
}

func raceInstanceID(index int) string { return "race-instance-" + strconv.Itoa(index) + "-x" }
