package upgrade

import (
	"strings"
	"testing"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

const (
	directStemcellOS      = "ubuntu-noble"
	directStemcellVersion = "9.9"
)

// TestMergeStemcellOverlayCleanManifests pins the exact output for
// manifests that hold no operator text, so setting the stemcell directly
// keeps the behaviour the overlay always had. The first stemcell is the
// one that gets overwritten, and the output has sorted keys.
func TestMergeStemcellOverlayCleanManifests(t *testing.T) {
	t.Parallel()

	const stemcell = "- alias: default\n  os: ubuntu-noble\n  version: \"9.9\"\n"

	cases := map[string]struct {
		manifest string
		want     string
	}{
		"no stemcells key": {
			manifest: "name: x\nrelease: r\n",
			want:     "name: x\nrelease: r\nstemcells:\n" + stemcell,
		},
		"an empty stemcells list": {
			manifest: "name: x\nstemcells: []\n",
			want:     "name: x\nstemcells:\n" + stemcell,
		},
		"a stemcell with extra keys": {
			manifest: "name: x\nstemcells:\n- alias: old\n  name: bosh-old\n  os: ubuntu-jammy\n  version: \"1\"\n",
			want:     "name: x\nstemcells:\n- alias: default\n  name: bosh-old\n  os: ubuntu-noble\n  version: \"9.9\"\n",
		},
		"two stemcells with different aliases": {
			manifest: "name: x\nstemcells:\n- alias: a\n  os: o1\n  version: \"1\"\n- alias: b\n  os: o2\n  version: \"2\"\n",
			want:     "name: x\nstemcells:\n" + stemcell + "- alias: b\n  os: o2\n  version: \"2\"\n",
		},
		"a stemcell that is not a map": {
			manifest: "name: x\nstemcells:\n- plain\n",
			want:     "name: x\nstemcells:\n" + stemcell,
		},
		"top-level keys out of order": {
			manifest: "zeta: 1\nalpha: 2\nstemcells:\n- alias: default\n  os: o\n  version: \"1\"\nmid:\n  b: 1\n  a: 2\n",
			want:     "alpha: 2\nmid:\n  a: 2\n  b: 1\nstemcells:\n" + stemcell + "zeta: 1\n",
		},
		"an empty manifest": {
			manifest: "",
			want:     "stemcells:\n" + stemcell,
		},
		"a stemcells value that is not a list": {
			manifest: "name: x\nstemcells: nope\n",
			want:     "name: x\nstemcells:\n" + stemcell,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := MergeStemcellOverlay(tc.manifest, directStemcellOS, directStemcellVersion)
			if err != nil {
				t.Fatalf("setting the stemcell: %v", err)
			}

			if got != tc.want {
				t.Errorf("output differs\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestMergeStemcellOverlayLeavesMarkersAlone checks that merge markers in
// tenant text stay plain strings. A list, a nested list, and a map each
// hold a marker, and none of them may add, remove, or reorder anything.
func TestMergeStemcellOverlayLeavesMarkersAlone(t *testing.T) {
	t.Parallel()

	manifest := `name: x
stemcells:
- alias: old
  os: ubuntu-jammy
  version: "1"
tags:
- one
- (( append ))
- two
nested:
  items:
  - (( prepend ))
  - first
  - (( delete 1 ))
  - last
mapping:
  a: (( prune ))
  b: (( replace ))
  c: (( inline ))
  d: (( merge ))
  e: (( delete me ))
`

	got, err := MergeStemcellOverlay(manifest, directStemcellOS, directStemcellVersion)
	if err != nil {
		t.Fatalf("setting the stemcell: %v", err)
	}

	var decoded map[string]interface{}

	err = yaml.Unmarshal([]byte(got), &decoded)
	if err != nil {
		t.Fatalf("decoding the output: %v", err)
	}

	stemcells, _ := decoded["stemcells"].([]interface{})
	if len(stemcells) != 1 {
		t.Fatalf("stemcells holds %d entries, want 1", len(stemcells))
	}

	first, _ := stemcells[0].(map[string]interface{})
	if first["os"] != directStemcellOS || first["version"] != directStemcellVersion {
		t.Error("the stemcell was not set")
	}

	want := map[string]string{
		"tags":         "one|(( append ))|two",
		"nested.items": "(( prepend ))|first|(( delete 1 ))|last",
		"mapping":      "a=(( prune ))|b=(( replace ))|c=(( inline ))|d=(( merge ))|e=(( delete me ))",
	}

	nested, _ := decoded["nested"].(map[string]interface{})
	mapping, _ := decoded["mapping"].(map[string]interface{})

	check := func(label string, gotText string) {
		t.Helper()

		if gotText != want[label] {
			t.Errorf("%s changed: got %q, want %q", label, gotText, want[label])
		}
	}

	check("tags", joinList(decoded["tags"]))
	check("nested.items", joinList(nested["items"]))

	var parts []string

	for _, key := range []string{"a", "b", "c", "d", "e"} {
		text, _ := mapping[key].(string)
		parts = append(parts, key+"="+text)
	}

	check("mapping", strings.Join(parts, "|"))

	if len(mapping) != 5 {
		t.Errorf("mapping holds %d keys, want 5", len(mapping))
	}
}

func joinList(value interface{}) string {
	list, _ := value.([]interface{})

	parts := make([]string, 0, len(list))

	for _, item := range list {
		text, _ := item.(string)
		parts = append(parts, text)
	}

	return strings.Join(parts, "|")
}
