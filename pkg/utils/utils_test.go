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
			"params": map[string]interface{}{"instance_id": graft.NewLiteral("abc")},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Wrap() = %#v, want %#v", got, want)
	}
}

// No request value may reach a graft operator, so Wrap marks each map and
// each string as a graft literal, at the top level and in a list at any
// depth. A list stays a list, so it still merges item by item, and a
// number, a bool, or a nil stays as it is.
func TestWrapMarksRequestValuesAsLiterals(t *testing.T) {
	t.Parallel()

	nested := map[string]interface{}{"policy": "allkeys-lru"}
	item := map[string]interface{}{"name": "node"}
	list := []interface{}{item, "plain", []interface{}{item, "inner"}, 3.0, true, nil}

	got := utils.Wrap("params", map[string]interface{}{
		"nested": nested, "list": list, "top": "text", "count": 2.0, "flag": false, "empty": nil,
	})

	params, ok := got["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("params is %T, want map[string]interface{}", got["params"])
	}

	assertLiteral(t, "nested", params["nested"], nested)

	assertLiteral(t, "top-level string", params["top"], "text")

	for key, want := range map[string]interface{}{"count": 2.0, "flag": false, "empty": nil} {
		if got, present := params[key]; !present || got != want {
			t.Errorf("top-level %s is %#v, want it unchanged", key, got)
		}
	}

	gotList, ok := params["list"].([]interface{})
	if !ok || len(gotList) != len(list) {
		t.Fatalf("list value is %T of the wrong length, want a list as long as the input", params["list"])
	}

	assertLiteral(t, "list item", gotList[0], item)
	assertLiteral(t, "string list item", gotList[1], "plain")

	inner, ok := gotList[2].([]interface{})
	if !ok || len(inner) != 2 {
		t.Fatalf("inner list is %T, want a list of two", gotList[2])
	}

	assertLiteral(t, "inner list item", inner[0], item)
	assertLiteral(t, "inner list string", inner[1], "inner")

	for i, want := range []interface{}{3.0, true, nil} {
		if got := gotList[3+i]; got != want {
			t.Errorf("list item %d is %#v, want it unchanged", 3+i, got)
		}
	}
}

func assertLiteral(t *testing.T, what string, got interface{}, want interface{}) {
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
