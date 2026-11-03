package utils_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"blacksmith/pkg/utils"

	"github.com/fivetwenty-io/graft/pkg/graft"
)

func TestDeinterfaceMapConvertsNestedMaps(t *testing.T) {
	t.Parallel()

	input := map[interface{}]interface{}{
		"meta": map[interface{}]interface{}{
			"list": []interface{}{map[interface{}]interface{}{"name": "z1"}, 2},
		},
		"value": "text",
	}

	want := map[string]interface{}{
		"meta": map[string]interface{}{
			"list": []interface{}{map[string]interface{}{"name": "z1"}, 2},
		},
		"value": "text",
	}

	got := utils.DeinterfaceMap(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeinterfaceMap() = %#v, want %#v", got, want)
	}
}

func TestWrapNestsDataUnderTheKeyPath(t *testing.T) {
	t.Parallel()

	got := utils.Wrap("meta.params", map[string]interface{}{"instance_id": "abc"})

	want := map[string]interface{}{
		"meta": map[string]interface{}{
			"params": map[string]interface{}{"instance_id": "abc"},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Wrap() = %#v, want %#v", got, want)
	}
}

// A nested map in request parameters must reach graft as one value, which
// a merge replaces whole and evaluation never looks inside, so Wrap marks
// each one as a graft literal. That includes a map that is an item of a
// top-level list. Strings, at the top level or in a list, stay as they are.
func TestWrapMarksNestedMapsAsLiterals(t *testing.T) {
	t.Parallel()

	nested := map[string]interface{}{"policy": "allkeys-lru"}
	item := map[string]interface{}{"name": "node"}
	list := []interface{}{item, "plain", []interface{}{item}}

	got := utils.Wrap("params", map[string]interface{}{"nested": nested, "list": list, "top": "text"})

	params, ok := got["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("params is %T, want map[string]interface{}", got["params"])
	}

	assertLiteral(t, "nested", params["nested"], nested)

	if params["top"] != "text" {
		t.Errorf("top-level string is %#v, want it unchanged", params["top"])
	}

	gotList, ok := params["list"].([]interface{})
	if !ok || len(gotList) != len(list) {
		t.Fatalf("list value is %T of the wrong length, want a list as long as the input", params["list"])
	}

	assertLiteral(t, "list item", gotList[0], item)

	if gotList[1] != "plain" {
		t.Errorf("string list item is %#v, want it unchanged", gotList[1])
	}

	inner, ok := gotList[2].([]interface{})
	if !ok || len(inner) != 1 {
		t.Fatalf("inner list is %T, want a list of one", gotList[2])
	}

	assertLiteral(t, "inner list item", inner[0], item)
}

func assertLiteral(t *testing.T, what string, got interface{}, want map[string]interface{}) {
	t.Helper()

	literal, ok := got.(graft.LiteralValue)
	if !ok {
		t.Errorf("%s is %T, want a graft literal", what, got)

		return
	}

	if !reflect.DeepEqual(literal.Value(), want) {
		t.Errorf("%s literal holds %#v, want %#v", what, literal.Value(), want)
	}
}

func TestWrapOfNilDataIsAnEmptyMap(t *testing.T) {
	t.Parallel()

	got := utils.Wrap("params", nil)

	params, ok := got["params"].(map[string]interface{})
	if !ok || len(params) != 0 {
		t.Fatalf("Wrap(nil) params = %#v, want an empty map", got["params"])
	}
}

func TestMergeFilesReportsEachRepeatedKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	overlay := filepath.Join(dir, "overlay.yml")

	writeFile(t, base, "name: first\nname: second\nmeta:\n  size: small\n")
	writeFile(t, overlay, "meta:\n  size: medium\n  size: large\n")

	var heard []string

	merged, err := utils.MergeFiles(func(file, key string) {
		heard = append(heard, filepath.Base(file)+" "+key)
	}, base, overlay)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if want := []string{"base.yml name", "overlay.yml meta.size"}; !reflect.DeepEqual(heard, want) {
		t.Errorf("reported %q, want %q", heard, want)
	}

	want := map[string]interface{}{"name": "second", "meta": map[string]interface{}{"size": "large"}}
	if !reflect.DeepEqual(merged, want) {
		t.Errorf("merged %#v, want %#v", merged, want)
	}
}

func TestMergeFilesWithoutAFuncRefusesARepeatedKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	overlay := filepath.Join(dir, "overlay.yml")

	writeFile(t, base, "name: only\n")
	writeFile(t, overlay, "name: first\nname: second\n")

	_, err := utils.MergeFiles(nil, base, overlay)
	if err == nil || !strings.Contains(err.Error(), "overlay.yml") {
		t.Fatalf("merge error = %v, want a failure naming overlay.yml", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()

	err := os.WriteFile(path, []byte(body), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

// TestMergeFilesKeepsAMapKeyPruneAndAppliesAListEntryPrune pins what
// graft's merger does with (( prune )) when MergeFiles merges without
// evaluating. A prune that replaces a map key leaves the earlier content
// in place, and nothing prunes it later. A prune over a key the base does
// not have stays as the literal text. A prune given as a list entry
// removes the entry at that index during the merge.
func TestMergeFilesKeepsAMapKeyPruneAndAppliesAListEntryPrune(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "manifest.yml")
	overlay := filepath.Join(dir, "params.yml")

	writeFile(t, base, "meta:\n  size: small\nlist:\n- a\n- b\n")
	writeFile(t, overlay, "meta: (( prune ))\nfresh: (( prune ))\nlist:\n- (( prune ))\n")

	merged, err := utils.MergeFiles(nil, base, overlay)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	want := map[string]interface{}{
		"meta":  map[string]interface{}{"size": "small"},
		"fresh": "(( prune ))",
		"list":  []interface{}{"b"},
	}
	if !reflect.DeepEqual(merged, want) {
		t.Errorf("merged %#v, want %#v", merged, want)
	}
}
