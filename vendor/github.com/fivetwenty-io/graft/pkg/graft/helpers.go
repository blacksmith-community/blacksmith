package graft

import (
	"fmt"
	"reflect"
	"sort"
)

// deepEqual performs a deep comparison of two values.
func deepEqual(a, b interface{}) bool {
	return reflect.DeepEqual(a, b)
}

// deepCopyHelper creates a deep copy of the given value.
func deepCopyHelper(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			result[k] = deepCopyHelper(v)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v := range val {
			result[i] = deepCopyHelper(v)
		}
		return result
	default:
		// For primitive types, return as-is
		return v
	}
}

// DeepCopyMap creates a deep copy of a map[string]interface{}.
func DeepCopyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result, ok := deepCopyHelper(m).(map[string]interface{})
	if !ok {
		return nil
	}
	return result
}

// SortList sorts a list of items based on a sort key
// This is a helper that delegates to the operators package implementation.
//
//nolint:gocyclo // list sorting requires type checking each element
func SortList(path string, list []interface{}, sortKey string) error {
	// Handle empty list
	if len(list) == 0 {
		return nil
	}

	// Type checking
	var commonType string
	hasInconsistentMaps := false

	for i, entry := range list {
		var typeName string

		if entry == nil {
			typeName = valueTypeNil
		} else {
			switch v := entry.(type) {
			case string:
				typeName = valueTypeString
			case int, int32, int64:
				typeName = valueTypeInt
			case float32, float64:
				typeName = "float64"
			case []interface{}:
				// Special error for lists of lists
				return fmt.Errorf("$.%s is a list with list entries (not a list with maps, strings or numbers)", path)
			case map[string]interface{}:
				// Always consider maps as maps for type checking
				typeName = valueTypeMap

				// Check if it's a named-entry map
				if sortKey != "" {
					if _, hasKey := v[sortKey]; !hasKey {
						hasInconsistentMaps = true
					}
				} else {
					// Auto-detect sort key from first map
					if i == 0 {
						for _, field := range []string{"name", "key", "id"} {
							if _, ok := v[field]; ok {
								sortKey = field
								break
							}
						}
					}
				}
			default:
				// Get the reflect kind
				reflectType := reflect.TypeOf(entry)
				if reflectType != nil {
					typeName = reflectType.Kind().String()
				} else {
					typeName = valueTypeUnknown
				}
			}
		}

		// Set or check common type
		if i == 0 {
			commonType = typeName
		} else if commonType != typeName {
			// Different types detected
			if typeName == valueTypeNil || commonType == valueTypeNil {
				return fmt.Errorf("$.%s is a list with different types (not a list with homogeneous entry types)", path)
			}
			return fmt.Errorf("$.%s is a list with different types (not a list with homogeneous entry types)", path)
		}
	}

	// Check for inconsistent map entries
	if commonType == valueTypeMap && hasInconsistentMaps && sortKey != "" {
		return fmt.Errorf("$.%s is a list with map entries, where some do not contain %s (not a list with map entries each containing %s)", path, sortKey, sortKey)
	}

	// Sort the list
	sort.Slice(list, func(i, j int) bool {
		return universalLess(list[i], list[j], sortKey)
	})

	return nil
}

// universalLess compares two values for sorting.
func universalLess(a, b interface{}, key string) bool {
	switch aVal := a.(type) {
	case string:
		bVal, ok := b.(string)
		if !ok {
			return false
		}
		return aVal < bVal

	case float64:
		bVal, ok := b.(float64)
		if !ok {
			return false
		}
		return aVal < bVal

	case int:
		bVal, ok := b.(int)
		if !ok {
			return false
		}
		return aVal < bVal

	case map[string]interface{}:
		entryB, ok := b.(map[string]interface{})
		if !ok {
			return false
		}
		return universalLess(aVal[key], entryB[key], key)
	}

	return false
}
