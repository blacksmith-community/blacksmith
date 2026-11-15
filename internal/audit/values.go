package audit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// Unknown stands for an old value the broker could not read before the
// change.
const Unknown = "(unknown)"

// Value is one setting as a change record holds it, the text of a string
// and the JSON of anything else, so a number, a boolean, and a string all
// read as the operator typed them. A nil value is empty.
func Value(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}

	return string(encoded)
}

// Flatten turns a settings object into one entry for each value it holds,
// keyed by its dotted path, so a nested secret keeps the name of the key
// that holds it and is hidden by that name. A list's items are keyed by
// their index. Anything that is not a map or a list is one value.
func Flatten(settings map[string]interface{}) map[string]string {
	flat := make(map[string]string)

	for key, value := range settings {
		flattenInto(flat, key, value)
	}

	return flat
}

// FlattenJSON flattens any value that encodes as a JSON object, such as a
// settings struct, keyed by its JSON field names.
func FlattenJSON(settings interface{}) (map[string]string, error) {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encoding the settings: %w", err)
	}

	var decoded map[string]interface{}

	err = json.Unmarshal(encoded, &decoded)
	if err != nil {
		return nil, fmt.Errorf("decoding the settings: %w", err)
	}

	return Flatten(decoded), nil
}

func flattenInto(flat map[string]string, key string, value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if len(typed) == 0 {
			flat[key] = "{}"

			return
		}

		for child, inner := range typed {
			flattenInto(flat, key+"."+child, inner)
		}
	case []interface{}:
		if len(typed) == 0 {
			flat[key] = "[]"

			return
		}

		for index, inner := range typed {
			flattenInto(flat, key+"."+strconv.Itoa(index), inner)
		}
	default:
		flat[key] = Value(value)
	}
}

// KeyChange is one key whose value differs between two flattened
// settings. A key only the new settings hold was added, and one only the
// old settings hold was removed.
type KeyChange struct {
	Key string
	Old string
	New string
}

// Diff lists the keys whose values differ between the old and the new
// settings, in key order.
func Diff(old, current map[string]string) []KeyChange {
	keys := make(map[string]struct{}, len(old)+len(current))

	for key := range old {
		keys[key] = struct{}{}
	}

	for key := range current {
		keys[key] = struct{}{}
	}

	changes := make([]KeyChange, 0, len(keys))

	for key := range keys {
		before, had := old[key]
		after, has := current[key]

		if had == has && before == after {
			continue
		}

		changes = append(changes, KeyChange{Key: key, Old: before, New: after})
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })

	return changes
}
