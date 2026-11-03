package utils_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/utils"
)

// mergeOverlays are layers merged over the blacksmith-metadata document.
// They hold the operators the broker's own layering relies on, and a few
// that change shape.
var mergeOverlays = []parity.Document{
	{Name: "grab", Body: "properties:\n  copy: (( grab name ))\n  deep: (( grab properties.blacksmith.plan_id ))\n"},
	{Name: "concat", Body: "properties:\n  joined: (( concat name \"-\" properties.blacksmith.plan_id ))\n"},
	{Name: "append-list", Body: "releases:\n- (( append ))\n- name: extra\n  version: 9.9.9\n"},
	{Name: "prepend-list", Body: "releases:\n- (( prepend ))\n- name: first\n  version: 0.0.1\n"},
	{Name: "inline-merge", Body: "instance_groups:\n- (( inline ))\n- name: fixture-group\n  instances: 5\n"},
	{Name: "delete", Body: "properties:\n  blacksmith: (( delete ))\n"},
	{Name: "prune", Body: "scratch: here\nmeta:\n  blacksmith: (( prune ))\n"},
	{Name: "param", Body: "properties:\n  needed: (( param \"say something\" ))\n"},
	{Name: "scalars", Body: "properties:\n  truthy: [yes, no, on, off]\n  numbers: [1e3, 0755, 0x1f, 1.10]\n  dates: [2026-01-02]\n  version: 1.10\n"},
	{Name: "replace-map", Body: "properties:\n  blacksmith:\n    plan_id: replaced\n"},
	{Name: "list-over-map", Body: "properties:\n- not\n- a\n- map\n"},
	{Name: "null-over-map", Body: "properties: ~\n"},
	{Name: "non-string-keys", Body: "properties:\n  1: one\n  true: two\n"},
	{Name: "unterminated", Body: "properties: {a: 1\n"},
	{Name: "empty", Body: ""},
}

// TestMergeFilesParity records what MergeFiles reads from one file and
// what it makes of layers merged over it. It lets a file repeat a key, as
// the catalog does for plan templates.
func TestMergeFilesParity(t *testing.T) {
	t.Parallel()

	tolerate := func(string, string) {}

	dir := t.TempDir()

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return path
	}

	describe := func(merged map[string]interface{}, err error) string {
		if err != nil {
			return "ERROR: " + strings.ReplaceAll(err.Error(), dir, "<dir>") + "\n"
		}

		return parity.Dump(merged)
	}

	var read strings.Builder

	for index, doc := range parity.Corpus(t) {
		path := write("read-"+strconv.Itoa(index)+".yml", doc.Body)
		merged, err := utils.MergeFiles(tolerate, path)
		read.WriteString(parity.Section(doc.Name, describe(merged, err)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity-graft", "read.dump"), read.String())

	var layered strings.Builder

	base := write("base.yml", mustDocument(t, "odd_blacksmith-metadata"))

	for index, overlay := range mergeOverlays {
		path := write("overlay-"+strconv.Itoa(index)+".yml", overlay.Body)
		merged, err := utils.MergeFiles(tolerate, base, path)
		layered.WriteString(parity.Section(overlay.Name, describe(merged, err)))
	}

	absent := filepath.Join(dir, "absent.yml")
	merged, err := utils.MergeFiles(tolerate, base, absent)
	layered.WriteString(parity.Section("absent-optional", describe(merged, err)))

	merged, err = utils.MergeFiles(tolerate, absent)
	layered.WriteString(parity.Section("absent-required", describe(merged, err)))

	stacked, err := utils.MergeFiles(tolerate, base,
		write("s1.yml", mergeOverlays[0].Body),
		write("s2.yml", mergeOverlays[2].Body),
		write("s3.yml", mergeOverlays[8].Body))
	layered.WriteString(parity.Section("three-layers", describe(stacked, err)))

	parity.Golden(t, filepath.Join("testdata", "parity-graft", "layered.dump"), layered.String())
}

func mustDocument(t *testing.T, name string) string {
	t.Helper()

	for _, doc := range parity.Corpus(t) {
		if doc.Name == name {
			return doc.Body
		}
	}

	t.Fatalf("no corpus document %s", name)

	return ""
}

// movedMergeLines lists, for each MergeFiles dump, the lines recorded with
// spruce and yaml.v2 that the graft recording drops and the lines it adds.
// Graft reads YAML 1.1, so 1e3, 1e10, 08, 09, y, and n stay strings, and a
// 40-digit sha1 stays a string where yaml.v2 made a lossy float. Graft
// decodes every mapping key to a string, and its parse errors read
// differently.
//
//nolint:gochecknoglobals // constant test data
var movedMergeLines = map[string]struct{ dropped, added []string }{
	"read.dump": {
		dropped: []string{
			"ERROR: failed to unmarshal YAML: yaml: unmarshal errors:",
			"  line 1: cannot unmarshal !!seq into map[string]interface {}",
			"ERROR: failed to unmarshal YAML: yaml: invalid map key: []interface {}{\"a\", \"b\"}",
			"$.releases[0].sha1\tfloat64\t1.2345678901234568e+38",
			"$.releases[1].sha1\tfloat64\t1e+10",
			"$.releases[6].version\tfloat64\t1000",
			"ERROR: failed to unmarshal YAML: yaml: unmarshal errors:",
			"  line 1: cannot unmarshal !!str `just a ...` into map[string]interface {}",
			"ERROR: failed to unmarshal YAML: yaml: line 2: did not find expected ',' or '}'",
			"$.properties.numbers[0]\tfloat64\t1000",
			"$.properties.octal-ish[0]\tfloat64\t8",
			"$.properties.octal-ish[1]\tfloat64\t9",
			"$.properties.truthy[4]\tbool\ttrue",
			"$.properties.truthy[5]\tbool\tfalse",
		},
		added: []string{
			"ERROR: failed to unmarshal YAML: yaml: cannot unmarshal []interface {} into Go value of type map[string]interface {}",
			"ERROR: failed to unmarshal YAML: yaml: line 7: found an invalid key for this map",
			"$.releases[0].sha1\tstring\t\"0123456789012345678901234567890123456789\"",
			"$.releases[1].sha1\tstring\t\"1e10\"",
			"$.releases[6].version\tstring\t\"1e3\"",
			"ERROR: failed to unmarshal YAML: yaml: cannot unmarshal string into Go value of type map[string]interface {}",
			"ERROR: failed to unmarshal YAML: yaml: line 2: could not find flow mapping end token '}'",
			"$.properties.numbers[0]\tstring\t\"1e3\"",
			"$.properties.octal-ish[0]\tstring\t\"08\"",
			"$.properties.octal-ish[1]\tstring\t\"09\"",
			"$.properties.truthy[4]\tstring\t\"y\"",
			"$.properties.truthy[5]\tstring\t\"n\"",
		},
	},
	"layered.dump": {
		dropped: []string{
			"$.properties.numbers[0]\tfloat64\t1000",
			"$.properties.1(int)\tstring\t\"one\"",
			"$.properties.true(bool)\tstring\t\"two\"",
			"ERROR: failed to unmarshal optional YAML file <dir>/overlay-13.yml: yaml: line 1: did not find expected ',' or '}'",
			"$.properties.numbers[0]\tfloat64\t1000",
		},
		added: []string{
			"$.properties.numbers[0]\tstring\t\"1e3\"",
			"$.properties.1\tstring\t\"one\"",
			"$.properties.true\tstring\t\"two\"",
			"ERROR: failed to unmarshal optional YAML file <dir>/overlay-13.yml: yaml: line 1: could not find flow mapping end token '}'",
			"$.properties.numbers[0]\tstring\t\"1e3\"",
		},
	},
}

// TestMergeFilesParityAgainstSpruce compares each graft recording of
// MergeFiles with the one recorded under spruce. Apart from the map type
// name, the two must hold the same lines except the ones movedMergeLines
// lists, and every listed line must still move.
func TestMergeFilesParityAgainstSpruce(t *testing.T) {
	t.Parallel()

	for name, moved := range movedMergeLines {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			old := readDump(t, filepath.Join("testdata", "parity", name))
			old = strings.ReplaceAll(old, "map[interface {}]interface {}", "map[string]interface {}")
			dropped, added := lineDifference(old, readDump(t, filepath.Join("testdata", "parity-graft", name)))

			if !slices.Equal(dropped, moved.dropped) {
				t.Errorf("dropped lines differ\n--- want\n%s\n--- got\n%s", strings.Join(moved.dropped, "\n"), strings.Join(dropped, "\n"))
			}

			if !slices.Equal(added, moved.added) {
				t.Errorf("added lines differ\n--- want\n%s\n--- got\n%s", strings.Join(moved.added, "\n"), strings.Join(added, "\n"))
			}
		})
	}
}

func readDump(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

// lineDifference returns, in order, the lines of old that recorded lacks and
// the lines of recorded that old lacks, counting a repeated line once per repeat.
func lineDifference(old, recorded string) ([]string, []string) {
	oldLines, newLines := strings.Split(old, "\n"), strings.Split(recorded, "\n")

	return missingLines(oldLines, newLines), missingLines(newLines, oldLines)
}

// missingLines returns the lines of from that against cannot account for.
func missingLines(from, against []string) []string {
	counts := make(map[string]int, len(against))
	for _, line := range against {
		counts[line]++
	}

	var missing []string

	for _, line := range from {
		if counts[line] > 0 {
			counts[line]--

			continue
		}

		missing = append(missing, line)
	}

	return missing
}
