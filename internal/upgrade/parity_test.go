package upgrade_test

import (
	"path/filepath"
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

			parity.Golden(t, golden, merged)
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

// TestMergeReleaseOverlayParity records the manifest and the applied
// targets the release bump produces for every rendered manifest.
func TestMergeReleaseOverlayParity(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			base := filepath.Join("testdata", "parity", "release", rendered.Name)

			merged, applied, err := upgrade.MergeReleaseOverlay(rendered.Body, parityReleaseTargets)
			if err != nil {
				parity.Golden(t, base+".yml", "ERROR: "+err.Error()+"\n")

				return
			}

			parity.Golden(t, base+".yml", merged)
			parity.Golden(t, base+".dump", parity.Dump(applied))
		})
	}
}

// TestMergeReleaseOverlayParityHandlesOddInput records how the bump treats
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

			base := filepath.Join("testdata", "parity", "release-odd", name)

			merged, applied, err := upgrade.MergeReleaseOverlay(input, targets)
			if err != nil {
				parity.Golden(t, base+".yml", "ERROR: "+err.Error()+"\n")

				return
			}

			parity.Golden(t, base+".yml", merged)
			parity.Golden(t, base+".dump", parity.Dump(applied))
		})
	}
}
