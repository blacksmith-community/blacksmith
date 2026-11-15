package audit_test

import (
	"reflect"
	"testing"
	"time"

	"blacksmith/internal/audit"
)

const (
	flatName  = "name"
	flatToken = "metadata.token"
	diffKey   = "changed"
)

func TestValueReadsAsTheOperatorTypedIt(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		value interface{}
		want  string
	}{
		{value: nil, want: ""},
		{value: "5m", want: "5m"},
		{value: 5, want: "5"},
		{value: float64(5), want: "5"},
		{value: true, want: "true"},
		{value: 30 * time.Minute, want: (30 * time.Minute).String()},
	} {
		if got := audit.Value(testCase.value); got != testCase.want {
			t.Errorf("Value(%#v) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

// A nested secret keeps the name of the key that holds it, so it is hidden
// by that name.
func TestFlattenKeysNestedValuesByPath(t *testing.T) {
	t.Parallel()

	flat := audit.Flatten(map[string]interface{}{
		flatName:   "cf",
		"metadata": map[string]interface{}{"token": "x", "labels": []interface{}{"a", "b"}},
		"empty":    map[string]interface{}{},
		"none":     []interface{}{},
	})

	want := map[string]string{flatName: "cf", flatToken: "x", "metadata.labels.0": "a", "metadata.labels.1": "b", "empty": "{}", "none": "[]"}
	if !reflect.DeepEqual(flat, want) {
		t.Fatalf("Flatten = %v, want %v", flat, want)
	}
}

func TestFlattenJSONUsesTheJSONNames(t *testing.T) {
	t.Parallel()

	flat, err := audit.FlattenJSON(struct {
		WarningDays  int  `json:"warning_days"`
		ProbeEnabled bool `json:"probe_enabled"`
	}{WarningDays: 30, ProbeEnabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(flat, map[string]string{"warning_days": "30", "probe_enabled": "true"}) {
		t.Fatalf("FlattenJSON = %v", flat)
	}
}

func TestDiffListsAddedChangedAndRemovedKeys(t *testing.T) {
	t.Parallel()

	changes := audit.Diff(
		map[string]string{"same": "1", diffKey: "a", "removed": "r", "blank": ""},
		map[string]string{"same": "1", diffKey: "b", "added": "n", "blank": ""},
	)

	want := []audit.KeyChange{
		{Key: "added", Old: "", New: "n"},
		{Key: diffKey, Old: "a", New: "b"},
		{Key: "removed", Old: "r", New: ""},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("Diff = %+v, want %+v", changes, want)
	}
}
