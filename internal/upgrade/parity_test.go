package upgrade_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/upgrade"
)

const (
	parityStemcellOS      = "ubuntu-noble"
	parityStemcellVersion = "1.99"
)

// parityReleaseTargets name the releases the rendered manifests hold, a
// release none of them holds, and the bpm release every plan pins.
//
//nolint:gochecknoglobals // constant test data
var parityReleaseTargets = []upgrade.ReleaseTarget{
	{Name: "bpm", Version: "99.0.1"},
	{Name: "valkey-forge", Version: "99.1.0"},
	{Name: "redis-forge", Version: "99.2.0"},
	{Name: "rabbitmq-forge", Version: "99.3.0"},
	{Name: "postgresql-forge", Version: "99.4.0"},
	{Name: "mariadb-forge", Version: "99.5.0"},
	{Name: "kubernetes-forge", Version: "99.6.0"},
	{Name: "absent-release", Version: "99.7.0"},
}

// TestMergeStemcellOverlayParity records the manifest the stemcell overlay
// produces for every rendered manifest, or the error it fails with.
//
//nolint:paralleltest // the merge uses spruce, which keeps global state
func TestMergeStemcellOverlayParity(t *testing.T) {
	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			golden := filepath.Join("testdata", "parity", "stemcell", rendered.Name+".yml")

			merged, err := upgrade.MergeStemcellOverlay(rendered.Body, parityStemcellOS, parityStemcellVersion)
			if err != nil {
				parity.Golden(t, golden, "ERROR: "+err.Error()+"\n")

				return
			}

			parity.GoldenYAML(t, golden, merged)
		})
	}
}

// TestGetCurrentStemcellParity records the stemcell each rendered manifest
// reports, and the same call on the overlay's output.
//
//nolint:paralleltest // the merge uses spruce, which keeps global state
func TestGetCurrentStemcellParity(t *testing.T) {
	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			recorded := map[string]interface{}{}

			recorded["rendered"] = currentStemcell(rendered.Body)

			merged, err := upgrade.MergeStemcellOverlay(rendered.Body, parityStemcellOS, parityStemcellVersion)
			if err == nil {
				recorded["merged"] = currentStemcell(merged)
			}

			parity.Golden(t, filepath.Join("testdata", "parity", "current-stemcell", rendered.Name+".dump"), parity.Dump(recorded))
		})
	}
}

func currentStemcell(manifest string) map[string]interface{} {
	stemcellOS, version, err := upgrade.GetCurrentStemcell(manifest)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	return map[string]interface{}{"os": stemcellOS, "version": version}
}

// TestMergeReleaseOverlayParity checks the manifest and the applied targets
// the release bump produces for every rendered manifest against the
// recordings made when spruce's YAML library wrote them, and records what
// the graft writer makes of them beside those.
func TestMergeReleaseOverlayParity(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			merged, applied, err := upgrade.MergeReleaseOverlay(rendered.Body, parityReleaseTargets)
			checkReleaseParity(t, "release", rendered.Name, "", merged, applied, err)
		})
	}
}

// movedReleaseCases lists the odd inputs whose result is meant to differ
// from the recording, and why. A plain y is a string to Psych and to graft,
// and the recording kept it as a boolean, which is yaml.v2's reading. Graft
// decodes every mapping key to a string, so an integer key comes back quoted.
//
//nolint:gochecknoglobals // constant test data
var movedReleaseCases = map[string]string{
	"quoted-boolean-names": "a plain y is a string, so the release named y is bumped",
	"scalars-and-anchors":  "a plain y stays the string y instead of becoming true",
	"integer-keys":         "map[string]any; no forge template has non-string keys",
}

// TestMergeReleaseOverlayParityHandlesOddInput checks how the bump treats
// input that is not a deployed manifest.
func TestMergeReleaseOverlayParityHandlesOddInput(t *testing.T) {
	t.Parallel()

	inputs := map[string]string{
		"not-yaml":             "name: [unterminated\n",
		"no-releases":          "name: bare\n",
		"releases-not-a-list":  "releases: nope\n",
		"release-not-a-map":    "releases:\n- just-a-string\n- name: bpm\n  version: 1\n",
		"numeric-version":      "releases:\n- name: bpm\n  version: 1.4\n  url: https://example.test/bpm.tgz\n  sha1: sha256:fixture\n",
		"scalars-and-anchors":  "releases:\n- &r {name: bpm, version: \"1\", stemcell: {os: x, version: y}}\n- *r\n",
		"integer-keys":         "1: one\nreleases:\n- name: bpm\n  version: 2\n",
		"quoted-boolean-names": "releases:\n- name: \"yes\"\n  version: 1\n- name: y\n  version: 1\n",
	}

	targets := []upgrade.ReleaseTarget{{Name: "bpm", Version: "99"}, {Name: "yes", Version: "98"}, {Name: "y", Version: "97"}}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			merged, applied, err := upgrade.MergeReleaseOverlay(input, targets)
			checkReleaseParity(t, "release-odd", name, movedReleaseCases[name], merged, applied, err)
		})
	}
}

// checkReleaseParity compares the bump's result with the recording in
// testdata/parity/<dir>, which spruce-era code made and which stays as it
// was, then records the result in testdata/parity/<dir>-graft. The manifest
// has to match as a YAML tree and decode, the type of every scalar has to
// stay the same though its style may change, and the applied targets have
// to match byte for byte. A case in movedReleaseCases is meant to differ,
// and the test requires that it does, so the exemption cannot go stale. A
// failure compares only that both fail, since the error text is graft's.
func checkReleaseParity(t *testing.T, dir, name, moved, merged string, applied []upgrade.ReleaseTarget, err error) {
	t.Helper()

	old := filepath.Join("testdata", "parity", dir, name)
	graft := filepath.Join("testdata", "parity", dir+"-graft", name)

	oldYAML := readRecording(t, old+".yml")

	if err != nil {
		if !strings.HasPrefix(oldYAML, "ERROR:") {
			t.Errorf("the recording holds a manifest, but the bump failed: %v", err)
		}

		parity.Golden(t, graft+".yml", "ERROR: "+err.Error()+"\n")

		return
	}

	if strings.HasPrefix(oldYAML, "ERROR:") {
		t.Fatalf("the recording is an error, but the bump made a manifest")
	}

	oldDump := readRecording(t, old+".dump")
	dump := parity.Dump(applied)

	if moved != "" {
		if len(parity.YAMLDifferences([]byte(oldYAML), []byte(merged))) == 0 && dump == oldDump {
			t.Errorf("the case is exempt (%s) but matches the recording", moved)
		}
	} else {
		parity.CompareYAML(t, []byte(oldYAML), []byte(merged))

		wantTypes, gotTypes := scalarTypes(readRecording(t, old+".scalars")), scalarTypes(parity.Scalars(merged))
		if !reflect.DeepEqual(wantTypes, gotTypes) {
			t.Errorf("scalar types differ\n--- want\n%s\n--- got\n%s", strings.Join(wantTypes, "\n"), strings.Join(gotTypes, "\n"))
		}

		if dump != oldDump {
			t.Errorf("applied targets differ\n--- want\n%s\n--- got\n%s", oldDump, dump)
		}
	}

	parity.GoldenYAML(t, graft+".yml", merged)
	parity.Golden(t, graft+".dump", dump)
}

func readRecording(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path) //nolint:gosec // a golden file
	if err != nil {
		t.Fatalf("read the recording: %s", err)
	}

	return string(body)
}

// scalarTypes lists a Scalars record without the style column and in sorted
// order, so that a scalar may change from plain to quoted, and a key may
// move, while its tag and YAML 1.1 type stay.
func scalarTypes(scalars string) []string {
	var lines []string

	for _, line := range strings.Split(strings.TrimSuffix(scalars, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) > 2 {
			fields = append(fields[:1], fields[2:]...)
		}

		lines = append(lines, strings.Join(fields, "\t"))
	}

	sort.Strings(lines)

	return lines
}
