package upgrade

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

const releaseManifest = `name: valkey-standalone-x
releases:
- name: valkey-forge
  version: latest
- name: bpm
  version: "1.4.20"
  url: https://example.test/bpm
  sha1: sha256:abc123
stemcells:
- alias: default
  os: ubuntu-jammy
  version: "1.921"
`

func releaseVersions(t *testing.T, manifestYAML string) map[string]string {
	t.Helper()

	var m map[interface{}]interface{}
	if err := yaml.Unmarshal([]byte(manifestYAML), &m); err != nil {
		t.Fatalf("parse merged manifest: %v", err)
	}

	out := map[string]string{}

	rels, _ := m["releases"].([]interface{})
	for _, r := range rels {
		rm, _ := r.(map[interface{}]interface{})
		name, _ := rm["name"].(string)
		out[name] = fmt.Sprintf("%v", rm["version"])
	}

	return out
}

// The load-bearing assumption: spruce merges the releases list BY NAME, so overlaying the
// forge version updates only it and leaves bpm (and its url/sha1) untouched.
func TestMergeReleaseOverlay_UpdatesForgeKeepsOthers(t *testing.T) {
	out, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{{Name: "valkey-forge", Version: "1.4.4"}})
	if err != nil {
		t.Fatal(err)
	}

	vers := releaseVersions(t, out)
	if vers["valkey-forge"] != "1.4.4" {
		t.Fatalf("valkey-forge = %q, want 1.4.4", vers["valkey-forge"])
	}

	if vers["bpm"] != "1.4.20" {
		t.Fatalf("bpm = %q, want 1.4.20 (must be untouched)", vers["bpm"])
	}

	// bpm's url/sha1 must survive the merge, not just its version.
	if !strings.Contains(out, "sha256:abc123") || !strings.Contains(out, "https://example.test/bpm") {
		t.Fatalf("bpm url/sha1 dropped by the merge:\n%s", out)
	}
}

// A target for a release NOT in the manifest must be skipped (not appended) — the safety
// property that makes heterogeneous batch selections harmless.
func TestMergeReleaseOverlay_SkipsAbsentReleases(t *testing.T) {
	out, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{
		{Name: "valkey-forge", Version: "1.4.4"}, // present → bumped
		{Name: "routing", Version: "0.999.0"},    // absent  → must NOT be added
	})
	if err != nil {
		t.Fatal(err)
	}

	vers := releaseVersions(t, out)
	if vers["valkey-forge"] != "1.4.4" {
		t.Fatalf("valkey-forge = %q, want 1.4.4", vers["valkey-forge"])
	}

	if _, added := vers["routing"]; added {
		t.Fatalf("routing was appended to a manifest that didn't have it:\n%s", out)
	}
}

// Bumping multiple present releases at once works.
func TestMergeReleaseOverlay_BumpsMultiplePresent(t *testing.T) {
	out, err := MergeReleaseOverlay(releaseManifest, []ReleaseTarget{
		{Name: "valkey-forge", Version: "1.4.4"},
		{Name: "bpm", Version: "1.4.21"},
	})
	if err != nil {
		t.Fatal(err)
	}

	vers := releaseVersions(t, out)
	if vers["valkey-forge"] != "1.4.4" || vers["bpm"] != "1.4.21" {
		t.Fatalf("expected valkey-forge=1.4.4 bpm=1.4.21, got %v", vers)
	}
}

func TestMergeReleaseOverlay_NoTargetsIsNoOp(t *testing.T) {
	out, err := MergeReleaseOverlay(releaseManifest, nil)
	if err != nil {
		t.Fatal(err)
	}

	if out != releaseManifest {
		t.Fatalf("expected unchanged manifest for empty targets, got:\n%s", out)
	}
}
