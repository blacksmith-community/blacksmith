//nolint:testpackage // the manager's tests share the test manifest
package upgrade

import (
	"fmt"
	"slices"
	"testing"

	"gopkg.in/yaml.v2"
)

// releaseManifest is a deployment manifest with the forge release at
// latest and a bpm release pinned to a tarball compiled for its stemcell.
const releaseManifest = `name: valkey-standalone-x
releases:
- name: valkey-forge
  version: latest
- name: bpm
  version: "1.4.20"
  url: https://example.test/bpm
  sha1: sha256:abc123
  stemcell:
    os: ubuntu-noble
    version: "1.921"
  exported_from:
  - os: ubuntu-noble
    version: "1.921"
stemcells:
- alias: default
  os: ubuntu-noble
  version: "1.921"
`

// The forge release and the bpm release of releaseManifest, a release it
// does not hold, and the versions the tests upgrade them to.
const (
	forgeRelease        = "valkey-forge"
	forgeTargetVersion  = "1.4.4"
	bpmRelease          = "bpm"
	bpmTargetVersion    = "1.4.21"
	absentRelease       = "routing"
	absentTargetVersion = "0.999.0"
)

// manifestReleases returns each release of the manifest by name, as a map
// of its keys.
func manifestReleases(t *testing.T, manifestYAML string) map[string]map[interface{}]interface{} {
	t.Helper()

	var manifest map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		t.Fatalf("parsing the merged manifest: %v", err)
	}

	releases := map[string]map[interface{}]interface{}{}

	entries, _ := manifest["releases"].([]interface{})
	for _, entry := range entries {
		release, _ := entry.(map[interface{}]interface{})
		name, _ := release["name"].(string)
		releases[name] = release
	}

	return releases
}

// releaseVersion returns the version of the release in the manifest.
func releaseVersion(t *testing.T, manifestYAML, name string) string {
	t.Helper()

	release, ok := manifestReleases(t, manifestYAML)[name]
	if !ok {
		return ""
	}

	return fmt.Sprintf("%v", release["version"])
}

func TestMergeReleaseOverlayBumpsTheTargetAndKeepsTheOthers(t *testing.T) {
	t.Parallel()

	merged, applied, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{{Name: forgeRelease, Version: forgeTargetVersion}})
	if err != nil {
		t.Fatalf("merging the release overlay: %v", err)
	}

	if version := releaseVersion(t, merged, forgeRelease); version != forgeTargetVersion {
		t.Fatalf("expected valkey-forge at 1.4.4, got %q", version)
	}

	bpm := manifestReleases(t, merged)[bpmRelease]
	if bpm["version"] != "1.4.20" || bpm["url"] != "https://example.test/bpm" || bpm["sha1"] != "sha256:abc123" {
		t.Fatalf("expected bpm to keep its version, url, and sha1, got %v", bpm)
	}

	if !slices.Equal(applied, []ReleaseTarget{{Name: forgeRelease, Version: forgeTargetVersion}}) {
		t.Fatalf("expected the valkey-forge target to be applied, got %v", applied)
	}
}

func TestMergeReleaseOverlayDropsTheBumpedReleasesTarball(t *testing.T) {
	t.Parallel()

	merged, _, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{{Name: bpmRelease, Version: bpmTargetVersion}})
	if err != nil {
		t.Fatalf("merging the release overlay: %v", err)
	}

	bpm := manifestReleases(t, merged)[bpmRelease]
	if bpm["version"] != bpmTargetVersion {
		t.Fatalf("expected bpm at 1.4.21, got %v", bpm)
	}

	if _, hasURL := bpm["url"]; hasURL {
		t.Fatalf("expected the bumped bpm to drop the url of its old tarball, got %v", bpm)
	}

	if _, hasSHA1 := bpm["sha1"]; hasSHA1 {
		t.Fatalf("expected the bumped bpm to drop the sha1 of its old tarball, got %v", bpm)
	}

	if _, hasStemcell := bpm["stemcell"]; hasStemcell {
		t.Fatalf("expected the bumped bpm to drop the stemcell its old tarball was compiled for, got %v", bpm)
	}

	if _, hasExportedFrom := bpm["exported_from"]; hasExportedFrom {
		t.Fatalf("expected the bumped bpm to drop the stemcells its old tarball was exported from, got %v", bpm)
	}
}

func TestMergeReleaseOverlayNeverAddsARelease(t *testing.T) {
	t.Parallel()

	merged, applied, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{
		{Name: forgeRelease, Version: forgeTargetVersion}, {Name: absentRelease, Version: absentTargetVersion}})
	if err != nil {
		t.Fatalf("merging the release overlay: %v", err)
	}

	if _, added := manifestReleases(t, merged)[absentRelease]; added {
		t.Fatalf("expected routing to stay out of a manifest that lacks it, got:\n%s", merged)
	}

	if releaseVersion(t, merged, forgeRelease) != forgeTargetVersion {
		t.Fatalf("expected valkey-forge at 1.4.4, got:\n%s", merged)
	}

	if !slices.Equal(applied, []ReleaseTarget{{Name: forgeRelease, Version: forgeTargetVersion}}) {
		t.Fatalf("expected only the valkey-forge target to be applied, got %v", applied)
	}
}

func TestMergeReleaseOverlayBumpsSeveralReleases(t *testing.T) {
	t.Parallel()

	merged, applied, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{
		{Name: forgeRelease, Version: forgeTargetVersion}, {Name: bpmRelease, Version: bpmTargetVersion}})
	if err != nil {
		t.Fatalf("merging the release overlay: %v", err)
	}

	if releaseVersion(t, merged, forgeRelease) != forgeTargetVersion || releaseVersion(t, merged, bpmRelease) != bpmTargetVersion {
		t.Fatalf("expected valkey-forge at 1.4.4 and bpm at 1.4.21, got:\n%s", merged)
	}

	if len(applied) != 2 {
		t.Fatalf("expected both targets to be applied, got %v", applied)
	}
}

func TestMergeReleaseOverlayWithNothingToApplyKeepsTheManifest(t *testing.T) {
	t.Parallel()

	for name, targets := range map[string][]ReleaseTarget{
		"no targets":           nil,
		"only absent releases": {ReleaseTarget{Name: absentRelease, Version: absentTargetVersion}},
	} {
		merged, applied, err := MergeReleaseOverlay(releaseManifest, targets)
		if err != nil {
			t.Fatalf("%s: merging the release overlay: %v", name, err)
		}

		if merged != releaseManifest || len(applied) != 0 {
			t.Fatalf("%s: expected the manifest unchanged and nothing applied, got %v and:\n%s", name, applied, merged)
		}
	}
}

func TestMergeReleaseOverlayRejectsAManifestItCannotParse(t *testing.T) {
	t.Parallel()

	_, _, err := MergeReleaseOverlay("releases: [unclosed", []ReleaseTarget{{Name: bpmRelease, Version: bpmTargetVersion}})
	if err == nil {
		t.Fatal("expected an error for a manifest that is not YAML")
	}
}
