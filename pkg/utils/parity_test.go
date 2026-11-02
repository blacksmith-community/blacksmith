package utils_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/utils"
)

// mergeOverlays are layers merged over the blacksmith-metadata document.
// They hold the spruce operators the broker's own layering relies on, and a
// few that change shape.
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
// what it makes of layers merged over it.
func TestMergeFilesParity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		return path
	}

	describe := func(merged map[interface{}]interface{}, err error) string {
		if err != nil {
			return "ERROR: " + strings.ReplaceAll(err.Error(), dir, "<dir>") + "\n"
		}

		return parity.Dump(merged)
	}

	var read strings.Builder

	for index, doc := range parity.Corpus(t) {
		path := write("read-"+strconv.Itoa(index)+".yml", doc.Body)
		merged, err := utils.MergeFiles(path)
		read.WriteString(parity.Section(doc.Name, describe(merged, err)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "read.dump"), read.String())

	var layered strings.Builder

	base := write("base.yml", mustDocument(t, "odd_blacksmith-metadata"))

	for index, overlay := range mergeOverlays {
		path := write("overlay-"+strconv.Itoa(index)+".yml", overlay.Body)
		merged, err := utils.MergeFiles(base, path)
		layered.WriteString(parity.Section(overlay.Name, describe(merged, err)))
	}

	absent := filepath.Join(dir, "absent.yml")
	merged, err := utils.MergeFiles(base, absent)
	layered.WriteString(parity.Section("absent-optional", describe(merged, err)))

	merged, err = utils.MergeFiles(absent)
	layered.WriteString(parity.Section("absent-required", describe(merged, err)))

	stacked, err := utils.MergeFiles(base,
		write("s1.yml", mergeOverlays[0].Body),
		write("s2.yml", mergeOverlays[2].Body),
		write("s3.yml", mergeOverlays[8].Body))
	layered.WriteString(parity.Section("three-layers", describe(stacked, err)))

	parity.Golden(t, filepath.Join("testdata", "parity", "layered.dump"), layered.String())
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
