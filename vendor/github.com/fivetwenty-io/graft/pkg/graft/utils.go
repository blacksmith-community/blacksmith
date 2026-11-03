// Package graft provides YAML/JSON configuration merging utilities.
package graft

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// PathSegmentType represents the type of a path segment.
type PathSegmentType int

const (
	// PathSegmentField represents a field access (e.g., "database" in "database.host").
	PathSegmentField PathSegmentType = iota
	// PathSegmentIndex represents a numeric index (e.g., "[0]" in "items[0]").
	PathSegmentIndex
	// PathSegmentKeyMatch represents a key=value match (e.g., "[name=foo]" in "items[name=foo]").
	PathSegmentKeyMatch
)

// String literal constants for boolean/null values.
const (
	utilLiteralTrue  = "true"
	utilLiteralFalse = "false"
)

// String returns a string representation of the PathSegmentType.
func (t PathSegmentType) String() string {
	switch t {
	case PathSegmentField:
		return "Field"
	case PathSegmentIndex:
		return "Index"
	case PathSegmentKeyMatch:
		return "KeyMatch"
	default:
		return "Unknown"
	}
}

// KeyMatch represents a key=value match condition for array element selection.
type KeyMatch struct {
	Key   string
	Value string
}

// PathSegment represents a single segment in a parsed path.
type PathSegment struct {
	Type  PathSegmentType
	Key   string    // For field access
	Index int       // For numeric index
	Match *KeyMatch // For key=value matching
}

// String returns a string representation of the PathSegment.
func (s PathSegment) String() string {
	switch s.Type {
	case PathSegmentField:
		return s.Key
	case PathSegmentIndex:
		return fmt.Sprintf("[%d]", s.Index)
	case PathSegmentKeyMatch:
		if s.Match != nil {
			return fmt.Sprintf("[%s=%s]", s.Match.Key, s.Match.Value)
		}
		return "[]"
	default:
		return ""
	}
}

// ParsePath parses a dot-notation path into segments.
// Examples:
//   - "database.host" -> [Field("database"), Field("host")]
//   - "items[0].name" -> [Field("items"), Index(0), Field("name")]
//   - "items[name=foo].value" -> [Field("items"), KeyMatch("name", "foo"), Field("value")]
//   - "[0].name" -> [Index(0), Field("name")]
func ParsePath(path string) ([]PathSegment, error) {
	if path == "" {
		return []PathSegment{}, nil
	}

	var segments []PathSegment
	i := 0
	n := len(path)

	for i < n {
		// Skip leading dot if not at start
		if path[i] == '.' {
			i++
			if i >= n {
				return nil, fmt.Errorf("unexpected end of path after '.'")
			}
		}

		// Check for bracket notation
		if path[i] == '[' {
			seg, consumed, err := parseBracketSegment(path[i:])
			if err != nil {
				return nil, err
			}
			segments = append(segments, seg)
			i += consumed
			continue
		}

		// Parse field name
		seg, consumed, err := parseFieldSegment(path[i:])
		if err != nil {
			return nil, err
		}
		if consumed > 0 {
			segments = append(segments, seg)
			i += consumed
		}
	}

	return segments, nil
}

// parseBracketSegment parses a bracket segment like "[0]" or "[name=foo]".
func parseBracketSegment(s string) (PathSegment, int, error) {
	if s == "" || s[0] != '[' {
		return PathSegment{}, 0, fmt.Errorf("expected '['")
	}

	// Find the closing bracket
	closeIdx := strings.Index(s, "]")
	if closeIdx == -1 {
		return PathSegment{}, 0, fmt.Errorf("unclosed bracket in path")
	}

	inner := s[1:closeIdx]
	consumed := closeIdx + 1

	// Check if it's a key=value match
	if eqIdx := strings.Index(inner, "="); eqIdx != -1 {
		key := inner[:eqIdx]
		value := inner[eqIdx+1:]

		// Handle quoted values
		value = unquotePathValue(value)

		return PathSegment{
			Type: PathSegmentKeyMatch,
			Match: &KeyMatch{
				Key:   key,
				Value: value,
			},
		}, consumed, nil
	}

	// Must be a numeric index
	idx, err := strconv.Atoi(inner)
	if err != nil {
		return PathSegment{}, 0, fmt.Errorf("invalid array index: %s", inner)
	}

	return PathSegment{
		Type:  PathSegmentIndex,
		Index: idx,
	}, consumed, nil
}

// parseFieldSegment parses a field name segment.
func parseFieldSegment(s string) (PathSegment, int, error) {
	if s == "" {
		return PathSegment{}, 0, nil
	}

	// Check for escaped field name (in quotes)
	if s[0] == '"' {
		closeIdx := strings.Index(s[1:], "\"")
		if closeIdx == -1 {
			return PathSegment{}, 0, fmt.Errorf("unclosed quote in path")
		}
		key := s[1 : closeIdx+1]
		return PathSegment{
			Type: PathSegmentField,
			Key:  key,
		}, closeIdx + 2, nil
	}

	// Regular field name - ends at '.', '[', or end of string
	var i int
	for i = 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == '[' {
			break
		}
	}

	if i == 0 {
		return PathSegment{}, 0, nil
	}

	return PathSegment{
		Type: PathSegmentField,
		Key:  s[:i],
	}, i, nil
}

// unquotePathValue removes quotes from a value if present.
func unquotePathValue(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// JoinPath joins path segments into a dot-notation string.
func JoinPath(segments ...string) string {
	if len(segments) == 0 {
		return ""
	}

	var result strings.Builder
	for i, seg := range segments {
		if seg == "" {
			continue
		}

		// Check if segment is a bracket notation
		if strings.HasPrefix(seg, "[") {
			result.WriteString(seg)
			continue
		}

		// Add dot separator if not first segment. Bracket segments return
		// above, so a previous segment ending in "]" carried the bracket
		// inside a longer segment and still takes a separator.
		if i > 0 && result.Len() > 0 {
			result.WriteString(".")
		}

		result.WriteString(seg)
	}

	return result.String()
}

// AppendPath appends a segment to an existing path.
func AppendPath(base, segment string) string {
	if base == "" {
		return segment
	}
	if segment == "" {
		return base
	}

	// If segment starts with '[', don't add a dot
	if strings.HasPrefix(segment, "[") {
		return base + segment
	}

	return base + "." + segment
}

// ParentPath returns the parent of a path.
// "database.host" -> "database"
// "database" -> ""
// "items[0].name" -> "items[0]".
func ParentPath(path string) string {
	if path == "" {
		return ""
	}

	segments, err := ParsePath(path)
	if err != nil || len(segments) <= 1 {
		return ""
	}

	// Rebuild path without last segment
	var result strings.Builder
	for i, seg := range segments[:len(segments)-1] {
		if i > 0 && seg.Type == PathSegmentField {
			result.WriteString(".")
		}
		result.WriteString(seg.String())
	}

	return result.String()
}

// BaseName returns the last segment of a path.
// "database.host" -> "host"
// "items[0].name" -> "name"
// "items[0]" -> "[0]".
func BaseName(path string) string {
	if path == "" {
		return ""
	}

	segments, err := ParsePath(path)
	if err != nil || len(segments) == 0 {
		return ""
	}

	return segments[len(segments)-1].String()
}

// PathMatches checks if a path matches a pattern.
// Supports * for single segment, ** for multiple segments.
func PathMatches(path, pattern string) bool {
	if pattern == "" {
		return path == ""
	}

	pathSegs, err := ParsePath(path)
	if err != nil {
		return false
	}

	patternSegs, err := parsePatternSegments(pattern)
	if err != nil {
		return false
	}

	return matchSegments(pathSegs, patternSegs, 0, 0)
}

// parsePatternSegments parses a pattern into segments, handling * and ** wildcards.
//
//nolint:unparam // returns error for interface consistency and future pattern validation
func parsePatternSegments(pattern string) ([]string, error) {
	if pattern == "" {
		return []string{}, nil
	}

	// Split by dots, but keep ** as a single segment
	var segments []string
	parts := strings.Split(pattern, ".")

	for _, part := range parts {
		if part != "" {
			segments = append(segments, part)
		}
	}

	return segments, nil
}

// matchSegments recursively matches path segments against pattern segments.
//
//nolint:gocyclo // recursive pattern matching with multiple conditions is inherently complex
func matchSegments(pathSegs []PathSegment, patternSegs []string, pathIdx, patternIdx int) bool {
	// Both exhausted - match
	if pathIdx >= len(pathSegs) && patternIdx >= len(patternSegs) {
		return true
	}

	// Pattern exhausted but path remains - no match
	if patternIdx >= len(patternSegs) {
		return false
	}

	patternSeg := patternSegs[patternIdx]

	// Handle ** (match zero or more segments)
	if patternSeg == "**" {
		// Try matching zero segments
		if matchSegments(pathSegs, patternSegs, pathIdx, patternIdx+1) {
			return true
		}

		// Try matching one or more segments
		for i := pathIdx; i < len(pathSegs); i++ {
			if matchSegments(pathSegs, patternSegs, i+1, patternIdx+1) {
				return true
			}
		}

		return false
	}

	// Path exhausted but pattern remains - no match (unless remaining pattern is all **)
	if pathIdx >= len(pathSegs) {
		for i := patternIdx; i < len(patternSegs); i++ {
			if patternSegs[i] != "**" {
				return false
			}
		}
		return true
	}

	pathSeg := pathSegs[pathIdx]

	// Handle * (match single segment)
	if patternSeg == "*" {
		return matchSegments(pathSegs, patternSegs, pathIdx+1, patternIdx+1)
	}

	// Handle bracket patterns
	if strings.HasPrefix(patternSeg, "[") && strings.HasSuffix(patternSeg, "]") {
		if !matchBracketPattern(pathSeg, patternSeg) {
			return false
		}
		return matchSegments(pathSegs, patternSegs, pathIdx+1, patternIdx+1)
	}

	// Exact match
	if pathSeg.Type == PathSegmentField && pathSeg.Key == patternSeg {
		return matchSegments(pathSegs, patternSegs, pathIdx+1, patternIdx+1)
	}

	return false
}

// isAllDigits reports whether s is non-empty and entirely ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// matchBracketPattern matches a path segment against a bracket pattern.
// Index patterns ([0], [*]) also accept an all-digit field segment: both
// diff change paths and recorded history paths spell list indices in
// dotted form ("jobs.0.name"), which ParsePath classifies as fields, so
// without this equivalence a bracket index pattern could never match
// either caller's paths.
func matchBracketPattern(pathSeg PathSegment, pattern string) bool {
	inner := pattern[1 : len(pattern)-1]

	// Key=* pattern (any value for key). Located by the first "=*" in the
	// pattern, so everything before it is the key.
	if eqStarIdx := strings.Index(inner, "=*"); eqStarIdx != -1 {
		return matchBracketKey(pathSeg, inner[:eqStarIdx])
	}

	// Key=value match
	if eqIdx := strings.Index(inner, "="); eqIdx != -1 {
		return matchBracketKey(pathSeg, inner[:eqIdx]) &&
			pathSeg.Match.Value == unquotePathValue(inner[eqIdx+1:])
	}

	return matchBracketIndex(pathSeg, inner)
}

// matchBracketKey reports whether pathSeg is a key match on key, without
// regard to the value it matches on.
func matchBracketKey(pathSeg PathSegment, key string) bool {
	return pathSeg.Type == PathSegmentKeyMatch && pathSeg.Match != nil && pathSeg.Match.Key == key
}

// matchBracketIndex matches the value-free bracket forms: "*", which any
// list position satisfies, and a literal index.
func matchBracketIndex(pathSeg PathSegment, inner string) bool {
	if inner == "*" {
		return pathSeg.Type == PathSegmentIndex || pathSeg.Type == PathSegmentKeyMatch ||
			(pathSeg.Type == PathSegmentField && isAllDigits(pathSeg.Key))
	}

	if idx, err := strconv.Atoi(inner); err == nil {
		return (pathSeg.Type == PathSegmentIndex && pathSeg.Index == idx) ||
			(pathSeg.Type == PathSegmentField && pathSeg.Key == inner)
	}

	return false
}

// PathHasPrefix checks if path starts with prefix.
//
//nolint:gocyclo // path segment comparison requires checking multiple types
func PathHasPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	if path == "" {
		return false
	}

	pathSegs, err := ParsePath(path)
	if err != nil {
		return false
	}

	prefixSegs, err := ParsePath(prefix)
	if err != nil {
		return false
	}

	if len(prefixSegs) > len(pathSegs) {
		return false
	}

	for i, prefixSeg := range prefixSegs {
		pathSeg := pathSegs[i]

		if prefixSeg.Type != pathSeg.Type {
			return false
		}

		switch prefixSeg.Type {
		case PathSegmentField:
			if prefixSeg.Key != pathSeg.Key {
				return false
			}
		case PathSegmentIndex:
			if prefixSeg.Index != pathSeg.Index {
				return false
			}
		case PathSegmentKeyMatch:
			if prefixSeg.Match == nil || pathSeg.Match == nil {
				return false
			}
			if prefixSeg.Match.Key != pathSeg.Match.Key ||
				prefixSeg.Match.Value != pathSeg.Match.Value {
				return false
			}
		}
	}

	return true
}

// ToString converts any value to string.
//
//nolint:gocyclo // type switch handles all Go primitive types
func ToString(v interface{}) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	case bool:
		if val {
			return utilLiteralTrue
		}
		return utilLiteralFalse
	case int:
		return strconv.Itoa(val)
	case int8:
		return strconv.FormatInt(int64(val), 10)
	case int16:
		return strconv.FormatInt(int64(val), 10)
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case uint:
		return strconv.FormatUint(uint64(val), 10)
	case uint8:
		return strconv.FormatUint(uint64(val), 10)
	case uint16:
		return strconv.FormatUint(uint64(val), 10)
	case uint32:
		return strconv.FormatUint(uint64(val), 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case fmt.Stringer:
		return val.String()
	case error:
		return val.Error()
	default:
		// Try JSON encoding for complex types
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}

// ToInt converts value to int, returns error if not convertible.
//
//nolint:gocyclo // type switch handles all Go numeric types
func ToInt(v interface{}) (int, error) {
	if v == nil {
		return 0, fmt.Errorf("cannot convert nil to int")
	}

	switch val := v.(type) {
	case int:
		return val, nil
	case int8:
		return int(val), nil
	case int16:
		return int(val), nil
	case int32:
		return int(val), nil
	case int64:
		if val > math.MaxInt || val < math.MinInt {
			return 0, fmt.Errorf("int64 value %d overflows int", val)
		}
		return int(val), nil
	case uint:
		if uint64(val) > uint64(math.MaxInt) {
			return 0, fmt.Errorf("uint value %d overflows int", val)
		}
		return int(val), nil // #nosec G115 - bounds checked above
	case uint8:
		return int(val), nil
	case uint16:
		return int(val), nil
	case uint32:
		return int(val), nil
	case uint64:
		if val > uint64(math.MaxInt) {
			return 0, fmt.Errorf("uint64 value %d overflows int", val)
		}
		return int(val), nil
	case float32:
		return int(val), nil
	case float64:
		return int(val), nil
	case string:
		// Try parsing as int
		if i, err := strconv.Atoi(val); err == nil {
			return i, nil
		}
		// Try parsing as float then convert
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return int(f), nil
		}
		return 0, fmt.Errorf("cannot convert string %q to int", val)
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		i, err := val.Int64()
		if err != nil {
			return 0, err
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// ToInt64 converts value to int64.
//
//nolint:gocyclo // type switch handles all Go numeric types
func ToInt64(v interface{}) (int64, error) {
	if v == nil {
		return 0, fmt.Errorf("cannot convert nil to int64")
	}

	switch val := v.(type) {
	case int:
		return int64(val), nil
	case int8:
		return int64(val), nil
	case int16:
		return int64(val), nil
	case int32:
		return int64(val), nil
	case int64:
		return val, nil
	case uint:
		if uint64(val) > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("uint value %d overflows int64", val)
		}
		return int64(val), nil // #nosec G115 - bounds checked above
	case uint8:
		return int64(val), nil
	case uint16:
		return int64(val), nil
	case uint32:
		return int64(val), nil
	case uint64:
		if val > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("uint64 value %d overflows int64", val)
		}
		return int64(val), nil
	case float32:
		return int64(val), nil
	case float64:
		return int64(val), nil
	case string:
		// Try parsing as int64
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i, nil
		}
		// Try parsing as float then convert
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return int64(f), nil
		}
		return 0, fmt.Errorf("cannot convert string %q to int64", val)
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		return val.Int64()
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}

// ToFloat64 converts value to float64.
//
//nolint:gocyclo // type switch handles all Go numeric types
func ToFloat64(v interface{}) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("cannot convert nil to float64")
	}

	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int:
		return float64(val), nil
	case int8:
		return float64(val), nil
	case int16:
		return float64(val), nil
	case int32:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case uint:
		return float64(val), nil
	case uint8:
		return float64(val), nil
	case uint16:
		return float64(val), nil
	case uint32:
		return float64(val), nil
	case uint64:
		return float64(val), nil
	case string:
		return strconv.ParseFloat(val, 64)
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		return val.Float64()
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

// ToBool converts value to bool.
// "true", "yes", "1", "on", true -> true
// "false", "no", "0", "off", false -> false.
func ToBool(v interface{}) (bool, error) {
	if v == nil {
		return false, nil
	}

	switch val := v.(type) {
	case bool:
		return val, nil
	case string:
		lower := strings.ToLower(strings.TrimSpace(val))
		switch lower {
		case "true", "yes", "1", "on", "t", "y":
			return true, nil
		case "false", "no", "0", "off", "f", "n", "":
			return false, nil
		default:
			return false, fmt.Errorf("cannot convert string %q to bool", val)
		}
	case int, int8, int16, int32, int64:
		i, _ := ToInt64(val)
		return i != 0, nil
	case uint, uint8, uint16, uint32, uint64:
		i, _ := ToInt64(val)
		return i != 0, nil
	case float32, float64:
		f, _ := ToFloat64(val)
		return f != 0, nil
	default:
		return false, fmt.Errorf("cannot convert %T to bool", v)
	}
}

// ToSlice converts value to []interface{}.
func ToSlice(v interface{}) ([]interface{}, error) {
	if v == nil {
		return nil, nil
	}

	switch val := v.(type) {
	case []interface{}:
		return val, nil
	case []string:
		result := make([]interface{}, len(val))
		for i, s := range val {
			result[i] = s
		}
		return result, nil
	case []int:
		result := make([]interface{}, len(val))
		for i, n := range val {
			result[i] = n
		}
		return result, nil
	case []float64:
		result := make([]interface{}, len(val))
		for i, f := range val {
			result[i] = f
		}
		return result, nil
	case []bool:
		result := make([]interface{}, len(val))
		for i, b := range val {
			result[i] = b
		}
		return result, nil
	default:
		// Use reflection for other slice types
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			result := make([]interface{}, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				result[i] = rv.Index(i).Interface()
			}
			return result, nil
		}
		return nil, fmt.Errorf("cannot convert %T to slice", v)
	}
}

// ToStringSlice converts value to []string.
func ToStringSlice(v interface{}) ([]string, error) {
	if v == nil {
		return nil, nil
	}

	switch val := v.(type) {
	case []string:
		return val, nil
	case []interface{}:
		result := make([]string, len(val))
		for i, item := range val {
			result[i] = ToString(item)
		}
		return result, nil
	default:
		// Use reflection for other slice types
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			result := make([]string, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				result[i] = ToString(rv.Index(i).Interface())
			}
			return result, nil
		}

		// Single value to single-element slice
		return []string{ToString(v)}, nil
	}
}

// ToMap converts value to map[string]interface{}.
func ToMap(v interface{}) (map[string]interface{}, error) {
	if v == nil {
		return nil, nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		return val, nil
	default:
		// Use reflection for other map types
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Map {
			result := make(map[string]interface{})
			for _, key := range rv.MapKeys() {
				result[ToString(key.Interface())] = rv.MapIndex(key).Interface()
			}
			return result, nil
		}
		return nil, fmt.Errorf("cannot convert %T to map", v)
	}
}

// TypeOf returns the graft type name for a value.
// Returns: "string", "int", "float", "bool", "map", "array", "null".
//
//nolint:gocyclo // type switch handles all Go primitive types plus reflection fallback
func TypeOf(v interface{}) string {
	if v == nil {
		return "null"
	}

	switch v.(type) {
	case string:
		return valueTypeString
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return valueTypeInt
	case float32, float64:
		return "float"
	case bool:
		return valueTypeBool
	case map[string]interface{}:
		return valueTypeMap
	case []interface{}, []string, []int, []float64, []bool:
		return "array"
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.String:
			return "string"
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return "int"
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return "int"
		case reflect.Float32, reflect.Float64:
			return "float"
		case reflect.Bool:
			return "bool"
		case reflect.Map:
			return "map"
		case reflect.Slice, reflect.Array:
			return "array"
		case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
			reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer, reflect.Struct, reflect.UnsafePointer:
			return valueTypeUnknown
		}
		return valueTypeUnknown
	}
}

// IsEmpty checks if value is empty (nil, "", 0, [], {}).
//
//nolint:gocyclo // type switch handles all Go primitive types plus reflection fallback
func IsEmpty(v interface{}) bool {
	if v == nil {
		return true
	}

	switch val := v.(type) {
	case string:
		return val == ""
	case int:
		return val == 0
	case int8:
		return val == 0
	case int16:
		return val == 0
	case int32:
		return val == 0
	case int64:
		return val == 0
	case uint:
		return val == 0
	case uint8:
		return val == 0
	case uint16:
		return val == 0
	case uint32:
		return val == 0
	case uint64:
		return val == 0
	case float32:
		return val == 0
	case float64:
		return val == 0
	case bool:
		return !val
	case []interface{}:
		return len(val) == 0
	case []string:
		return len(val) == 0
	case []int:
		return len(val) == 0
	case map[string]interface{}:
		return len(val) == 0
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice:
			return rv.Len() == 0
		case reflect.Pointer, reflect.Interface:
			return rv.IsNil()
		case reflect.String:
			return rv.Len() == 0
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return rv.Int() == 0
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return rv.Uint() == 0
		case reflect.Float32, reflect.Float64:
			return rv.Float() == 0
		case reflect.Bool:
			return !rv.Bool()
		case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
			reflect.Func, reflect.Struct, reflect.UnsafePointer:
			return false
		}
		return false
	}
}

// IsNil checks if value is nil.
func IsNil(v interface{}) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	case reflect.Invalid, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.String, reflect.Struct, reflect.UnsafePointer:
		return false
	}
	return false
}

// DeepEqual compares two values deeply.
func DeepEqual(a, b interface{}) bool {
	return reflect.DeepEqual(a, b)
}

// DeepGet retrieves a value at a path from a nested structure.
//
//nolint:gocyclo // path traversal requires handling multiple segment types
func DeepGet(data interface{}, path string) (interface{}, error) {
	if path == "" {
		return data, nil
	}

	segments, err := ParsePath(path)
	if err != nil {
		return nil, err
	}

	current := data
	for _, seg := range segments {
		if current == nil {
			return nil, fmt.Errorf("cannot traverse nil at path segment %s", seg.String())
		}

		switch seg.Type {
		case PathSegmentField:
			m, err := ToMap(current)
			if err != nil {
				return nil, fmt.Errorf("cannot access field %q on non-map type %T", seg.Key, current)
			}
			val, ok := m[seg.Key]
			if !ok {
				return nil, fmt.Errorf("key %q not found", seg.Key)
			}
			current = val

		case PathSegmentIndex:
			slice, err := ToSlice(current)
			if err != nil {
				return nil, fmt.Errorf("cannot index non-slice type %T", current)
			}
			if seg.Index < 0 || seg.Index >= len(slice) {
				return nil, fmt.Errorf("index %d out of range (length %d)", seg.Index, len(slice))
			}
			current = slice[seg.Index]

		case PathSegmentKeyMatch:
			slice, err := ToSlice(current)
			if err != nil {
				return nil, fmt.Errorf("cannot search non-slice type %T", current)
			}
			found := false
			for _, item := range slice {
				m, err := ToMap(item)
				if err != nil {
					continue
				}
				if val, ok := m[seg.Match.Key]; ok && ToString(val) == seg.Match.Value {
					current = item
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("no element matching [%s=%s]", seg.Match.Key, seg.Match.Value)
			}
		}
	}

	return current, nil
}

// DeepSet sets a value at a path in a nested structure.
// Creates intermediate maps as needed.
func DeepSet(data interface{}, path string, value interface{}) error {
	if path == "" {
		return fmt.Errorf("cannot set value at empty path")
	}

	segments, err := ParsePath(path)
	if err != nil {
		return err
	}

	return deepSetRecursive(data, segments, 0, value)
}

// deepSetRecursive is a helper for DeepSet.
//
//nolint:gocyclo // recursive path setting requires handling multiple segment types
func deepSetRecursive(data interface{}, segments []PathSegment, idx int, value interface{}) error {
	if idx >= len(segments) {
		return nil
	}

	seg := segments[idx]
	isLast := idx == len(segments)-1

	switch seg.Type {
	case PathSegmentField:
		m, err := ToMap(data)
		if err != nil {
			return fmt.Errorf("cannot access field %q on non-map type %T", seg.Key, data)
		}

		if isLast {
			m[seg.Key] = value
			return nil
		}

		// Create intermediate map if needed
		next, ok := m[seg.Key]
		if !ok {
			// Look ahead to determine what type to create
			nextSeg := segments[idx+1]
			if nextSeg.Type == PathSegmentIndex || nextSeg.Type == PathSegmentKeyMatch {
				next = make([]interface{}, 0)
			} else {
				next = make(map[string]interface{})
			}
			m[seg.Key] = next
		}

		return deepSetRecursive(next, segments, idx+1, value)

	case PathSegmentIndex:
		m, ok := data.(map[string]interface{})
		if !ok {
			return fmt.Errorf("cannot traverse to index in non-map parent")
		}

		// Find the parent key (previous segment)
		if idx == 0 {
			return fmt.Errorf("cannot set index without parent field")
		}
		prevSeg := segments[idx-1]
		if prevSeg.Type != PathSegmentField {
			return fmt.Errorf("index must follow field segment")
		}

		slice, ok := m[prevSeg.Key].([]interface{})
		if !ok {
			return fmt.Errorf("cannot index non-slice at %s", prevSeg.Key)
		}

		if seg.Index < 0 || seg.Index >= len(slice) {
			return fmt.Errorf("index %d out of range (length %d)", seg.Index, len(slice))
		}

		if isLast {
			slice[seg.Index] = value
			return nil
		}

		return deepSetRecursive(slice[seg.Index], segments, idx+1, value)

	case PathSegmentKeyMatch:
		slice, err := ToSlice(data)
		if err != nil {
			return fmt.Errorf("cannot search non-slice type %T", data)
		}

		for i, item := range slice {
			m, err := ToMap(item)
			if err != nil {
				continue
			}
			if val, ok := m[seg.Match.Key]; ok && ToString(val) == seg.Match.Value {
				if isLast {
					slice[i] = value
					return nil
				}
				return deepSetRecursive(slice[i], segments, idx+1, value)
			}
		}
		return fmt.Errorf("no element matching [%s=%s]", seg.Match.Key, seg.Match.Value)
	}

	return nil
}

// DeepDelete removes a value at a path.
func DeepDelete(data interface{}, path string) error {
	if path == "" {
		return fmt.Errorf("cannot delete at empty path")
	}

	segments, err := ParsePath(path)
	if err != nil {
		return err
	}

	if len(segments) == 1 {
		seg := segments[0]
		if seg.Type == PathSegmentField {
			m, mapErr := ToMap(data)
			if mapErr != nil {
				return fmt.Errorf("cannot delete from non-map type %T", data)
			}
			delete(m, seg.Key)
			return nil
		}
		return fmt.Errorf("can only delete field segments at root")
	}

	// Navigate to parent
	parent, err := DeepGet(data, ParentPath(path))
	if err != nil {
		return err
	}

	lastSeg := segments[len(segments)-1]

	switch lastSeg.Type {
	case PathSegmentField:
		m, err := ToMap(parent)
		if err != nil {
			return fmt.Errorf("cannot delete from non-map type %T", parent)
		}
		delete(m, lastSeg.Key)

	case PathSegmentIndex:
		// For array deletion, we need to modify the parent map's slice
		return fmt.Errorf("array element deletion requires parent modification")

	case PathSegmentKeyMatch:
		return fmt.Errorf("key-match element deletion requires parent modification")
	}

	return nil
}

// DeepCopy creates a deep copy of a value.
func DeepCopy(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			result[k] = DeepCopy(v)
		}
		return result

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v := range val {
			result[i] = DeepCopy(v)
		}
		return result

	case []string:
		result := make([]string, len(val))
		copy(result, val)
		return result

	case []int:
		result := make([]int, len(val))
		copy(result, val)
		return result

	case []float64:
		result := make([]float64, len(val))
		copy(result, val)
		return result

	case []bool:
		result := make([]bool, len(val))
		copy(result, val)
		return result

	case string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		// Primitive types are immutable, return as-is
		return val

	default:
		// Use JSON round-trip for unknown types
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		var result interface{}
		if err := json.Unmarshal(b, &result); err != nil {
			return v
		}
		return result
	}
}

// Quote wraps a string in double quotes, escaping as needed.
func Quote(s string) string {
	return strconv.Quote(s)
}

// Unquote removes quotes and unescapes.
func Unquote(s string) (string, error) {
	if s == "" {
		return "", nil
	}

	// Check if already unquoted
	if s[0] != '"' && s[0] != '\'' && s[0] != '`' {
		return s, nil
	}

	// Use strconv.Unquote for double-quoted strings
	if s[0] == '"' {
		return strconv.Unquote(s)
	}

	// Handle single-quoted strings
	if s[0] == '\'' && len(s) >= 2 && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1], nil
	}

	// Handle backtick-quoted strings (raw strings)
	if s[0] == '`' && len(s) >= 2 && s[len(s)-1] == '`' {
		return s[1 : len(s)-1], nil
	}

	return s, nil
}

// Indent adds prefix to each line.
func Indent(s, prefix string) string {
	if s == "" {
		return ""
	}

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" || i < len(lines)-1 {
			lines[i] = prefix + line
		}
	}

	return strings.Join(lines, "\n")
}

// TrimIndent removes common leading whitespace from all lines.
func TrimIndent(s string) string {
	lines := strings.Split(s, "\n")

	// Find minimum indent
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeftFunc(line, unicode.IsSpace))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return s
	}

	// Remove minimum indent from each line
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}

	return strings.Join(lines, "\n")
}

// SegmentsToPath converts PathSegments back to a path string.
func SegmentsToPath(segments []PathSegment) string {
	if len(segments) == 0 {
		return ""
	}

	var result strings.Builder
	for i, seg := range segments {
		if i > 0 && seg.Type == PathSegmentField {
			result.WriteString(".")
		}
		result.WriteString(seg.String())
	}

	return result.String()
}

// SplitPath splits a path into its component strings.
func SplitPath(path string) []string {
	segments, err := ParsePath(path)
	if err != nil {
		return nil
	}

	result := make([]string, len(segments))
	for i, seg := range segments {
		result[i] = seg.String()
	}

	return result
}

// NormalizePath normalizes a path by parsing and re-serializing it.
func NormalizePath(path string) string {
	segments, err := ParsePath(path)
	if err != nil {
		return path
	}
	return SegmentsToPath(segments)
}

var operatorPattern = regexp.MustCompile(`\(\(\s*[^)]+\s*\)\)`)

// ContainsOperator checks if a string contains a graft operator expression.
func ContainsOperator(s string) bool {
	return operatorPattern.MatchString(s)
}

// ExtractOperatorContent extracts the content inside (( )).
func ExtractOperatorContent(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "((") || !strings.HasSuffix(s, "))") {
		return "", false
	}

	content := strings.TrimSpace(s[2 : len(s)-2])
	return content, true
}
