package utils_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"blacksmith/pkg/utils"
)

func TestInterfaceMapConvertsNestedMapsAndLists(t *testing.T) {
	t.Parallel()

	input := map[string]interface{}{
		"name":    "redis",
		"count":   3,
		"ratio":   1.5,
		"enabled": true,
		"missing": nil,
		"tags":    []string{"a", "b"},
		"meta": map[string]interface{}{
			"params": map[string]interface{}{"port": 6379},
			"empty":  map[string]interface{}{},
		},
		"jobs": []interface{}{
			map[string]interface{}{"name": "node", "ips": []interface{}{"10.0.0.1"}},
			"plain",
			[]interface{}{map[string]interface{}{"deep": "yes"}},
		},
		"decoded": map[interface{}]interface{}{
			"inner": map[string]interface{}{"key": "value"},
		},
	}

	want := map[interface{}]interface{}{
		"name":    "redis",
		"count":   3,
		"ratio":   1.5,
		"enabled": true,
		"missing": nil,
		"tags":    []string{"a", "b"},
		"meta": map[interface{}]interface{}{
			"params": map[interface{}]interface{}{"port": 6379},
			"empty":  map[interface{}]interface{}{},
		},
		"jobs": []interface{}{
			map[interface{}]interface{}{"name": "node", "ips": []interface{}{"10.0.0.1"}},
			"plain",
			[]interface{}{map[interface{}]interface{}{"deep": "yes"}},
		},
		"decoded": map[interface{}]interface{}{
			"inner": map[interface{}]interface{}{"key": "value"},
		},
	}

	got := utils.InterfaceMap(input)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InterfaceMap() = %#v, want %#v", got, want)
	}
}

func TestInterfaceMapCopiesRatherThanSharing(t *testing.T) {
	t.Parallel()

	nested := map[string]interface{}{"key": "value"}
	list := []interface{}{"first"}
	input := map[string]interface{}{"nested": nested, "list": list}

	got := utils.InterfaceMap(input)

	gotNested, ok := got["nested"].(map[interface{}]interface{})
	if !ok {
		t.Fatalf("nested value is %T, want map[interface{}]interface{}", got["nested"])
	}

	gotNested["key"] = "changed"

	gotList, ok := got["list"].([]interface{})
	if !ok {
		t.Fatalf("list value is %T, want []interface{}", got["list"])
	}

	gotList[0] = "changed"

	if nested["key"] != "value" {
		t.Error("changing the converted map changed the input map")
	}

	if list[0] != "first" {
		t.Error("changing the converted list changed the input list")
	}
}

func TestInterfaceMapKeepsNilAndEmpty(t *testing.T) {
	t.Parallel()

	if got := utils.InterfaceMap(nil); got != nil {
		t.Errorf("InterfaceMap(nil) = %#v, want nil", got)
	}

	got := utils.InterfaceMap(map[string]interface{}{})
	if got == nil || len(got) != 0 {
		t.Errorf("InterfaceMap(empty) = %#v, want an empty non-nil map", got)
	}

	var nilList []interface{}

	withNilList := utils.InterfaceMap(map[string]interface{}{"list": nilList})
	if list, ok := withNilList["list"].([]interface{}); !ok || list != nil {
		t.Errorf("a nil list converted to %#v, want a nil []interface{}", withNilList["list"])
	}
}

func TestInterfaceMapRoundTripsWithDeinterfaceMap(t *testing.T) {
	t.Parallel()

	input := map[string]interface{}{
		"meta": map[string]interface{}{
			"list": []interface{}{map[string]interface{}{"name": "z1"}, 2},
		},
		"value": "text",
	}

	got := utils.DeinterfaceMap(utils.InterfaceMap(input))
	if !reflect.DeepEqual(got, input) {
		t.Fatalf("round trip = %#v, want %#v", got, input)
	}
}

func TestWrapNestsDataUnderTheKeyPath(t *testing.T) {
	t.Parallel()

	got := utils.Wrap("meta.params", map[string]interface{}{"instance_id": "abc"})

	want := map[interface{}]interface{}{
		"meta": map[interface{}]interface{}{
			"params": map[interface{}]interface{}{"instance_id": "abc"},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Wrap() = %#v, want %#v", got, want)
	}
}

// A nested map in request parameters reaches spruce as one value, which a
// merge replaces whole, so Wrap re-keys only the top level of its data.
func TestWrapPassesValuesThroughUnchanged(t *testing.T) {
	t.Parallel()

	nested := map[string]interface{}{"policy": "allkeys-lru"}
	list := []interface{}{map[string]interface{}{"name": "node"}}

	got := utils.Wrap("params", map[string]interface{}{"nested": nested, "list": list})

	params, ok := got["params"].(map[interface{}]interface{})
	if !ok {
		t.Fatalf("params is %T, want map[interface{}]interface{}", got["params"])
	}

	if _, ok := params["nested"].(map[string]interface{}); !ok {
		t.Errorf("nested value is %T, want it passed through as map[string]interface{}", params["nested"])
	}

	gotList, ok := params["list"].([]interface{})
	if !ok || len(gotList) != 1 {
		t.Fatalf("list value is %#v, want the input list", params["list"])
	}

	if _, ok := gotList[0].(map[string]interface{}); !ok {
		t.Errorf("list element is %T, want it passed through as map[string]interface{}", gotList[0])
	}
}

func TestWrapOfNilDataIsAnEmptyMap(t *testing.T) {
	t.Parallel()

	got := utils.Wrap("params", nil)

	params, ok := got["params"].(map[interface{}]interface{})
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
