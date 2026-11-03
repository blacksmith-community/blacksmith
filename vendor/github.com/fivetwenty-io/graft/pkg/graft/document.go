package graft

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// document implements the Document interface.
type document struct {
	data map[string]interface{}

	// history is non-nil only when this document was returned by
	// Execute() on a merge chain where document-memory tracking was
	// active (see mergeBuilderImpl.attachHistory in
	// merge_builder_impl.go); nil for every document built any other
	// way (NewDocument, ParseYAML/ParseJSON/ParseFile/ParseReader,
	// Clone, Prune, CherryPick, SortKeys). History() returns
	// emptyHistory{} when it is nil, so callers never see a nil History
	// interface.
	history *historyImpl
}

// NewDocument creates a new document from a map.
func NewDocument(data map[string]interface{}) Document {
	if data == nil {
		data = make(map[string]interface{})
	}
	return &document{data: data}
}

// NewDocumentFromInterface creates a document from any interface{}.
func NewDocumentFromInterface(data interface{}) (Document, error) {
	switch v := data.(type) {
	case map[string]interface{}:
		return NewDocument(v), nil
	case nil:
		return NewDocument(nil), nil
	default:
		return nil, NewValidationError(fmt.Sprintf("cannot create document from type %T", data))
	}
}

// Get retrieves a value at the given path.
func (d *document) Get(path string) (interface{}, error) {
	if path == "" || path == "$" {
		return d.data, nil
	}

	cursor, err := tree.ParseCursor(path)
	if err != nil {
		return nil, NewValidationErrorWithCause(fmt.Sprintf("invalid path '%s': %v", path, err), err)
	}

	value, err := cursor.Resolve(d.data)
	if err != nil {
		return nil, NewEvaluationError(path, fmt.Sprintf("path not found: %v", err), err)
	}

	return value, nil
}

// Set sets a value at the given path.
func (d *document) Set(path string, value interface{}) error {
	if path == "" || path == "$" {
		if mapValue, ok := value.(map[string]interface{}); ok {
			d.data = mapValue
			return nil
		}
		return NewValidationError("cannot set root to non-map value")
	}

	cursor, err := tree.ParseCursor(path)
	if err != nil {
		return NewValidationErrorWithCause(fmt.Sprintf("invalid path '%s': %v", path, err), err)
	}

	return cursor.Set(d.data, value)
}

// Delete removes a value at the given path.
func (d *document) Delete(path string) error {
	if path == "" || path == "$" {
		return NewValidationError("cannot delete root")
	}

	cursor, err := tree.ParseCursor(path)
	if err != nil {
		return NewValidationErrorWithCause(fmt.Sprintf("invalid path '%s': %v", path, err), err)
	}

	return cursor.Delete(d.data)
}

// GetString retrieves a string value at the given path.
func (d *document) GetString(path string) (string, error) {
	val, err := d.Get(path)
	if err != nil {
		return "", err
	}
	if str, ok := val.(string); ok {
		return str, nil
	}
	return "", NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a string (got %T)", path, val), ErrTypeMismatch)
}

// GetInt retrieves an integer value at the given path.
func (d *document) GetInt(path string) (int, error) {
	val, err := d.Get(path)
	if err != nil {
		return 0, err
	}

	switch v := val.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		// JSON numbers are parsed as float64
		if v == float64(int(v)) {
			return int(v), nil
		}
		return 0, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a whole number (got %f)", path, v), ErrTypeMismatch)
	case float32:
		if v == float32(int(v)) {
			return int(v), nil
		}
		return 0, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a whole number (got %f)", path, v), ErrTypeMismatch)
	default:
		return 0, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a number (got %T)", path, val), ErrTypeMismatch)
	}
}

// GetBool retrieves a boolean value at the given path.
func (d *document) GetBool(path string) (bool, error) {
	val, err := d.Get(path)
	if err != nil {
		return false, err
	}
	if b, ok := val.(bool); ok {
		return b, nil
	}
	return false, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a boolean (got %T)", path, val), ErrTypeMismatch)
}

// GetSlice retrieves a slice value at the given path.
func (d *document) GetSlice(path string) ([]interface{}, error) {
	val, err := d.Get(path)
	if err != nil {
		return nil, err
	}
	if slice, ok := val.([]interface{}); ok {
		return slice, nil
	}
	return nil, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a slice (got %T)", path, val), ErrTypeMismatch)
}

// GetMap retrieves a map value at the given path.
func (d *document) GetMap(path string) (map[string]interface{}, error) {
	val, err := d.Get(path)
	if err != nil {
		return nil, err
	}

	switch v := val.(type) {
	case map[string]interface{}:
		return v, nil
	default:
		return nil, NewValidationErrorWithCause(fmt.Sprintf("value at path '%s' is not a map (got %T)", path, val), ErrTypeMismatch)
	}
}

// Keys returns all top-level keys.
func (d *document) Keys() []string {
	keys := make([]string, 0, len(d.data))
	for k := range d.data {
		keys = append(keys, k)
	}
	return keys
}

// ToMap returns the underlying map representation.
func (d *document) ToMap() map[string]interface{} {
	return d.data
}

// convertToJSONCompatible ensures all nested maps are map[string]interface{}.
func convertToJSONCompatible(v interface{}) interface{} {
	switch v := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{})
		for k, val := range v {
			m[k] = convertToJSONCompatible(val)
		}
		return m
	case []interface{}:
		arr := make([]interface{}, len(v))
		for i, val := range v {
			arr[i] = convertToJSONCompatible(val)
		}
		return arr
	default:
		return v
	}
}

// ToYAML converts the document to YAML bytes. It routes through
// MarshalYAML so library output carries the same spruce-compatible key
// ordering and special-float quoting as CLI output.
func (d *document) ToYAML() ([]byte, error) {
	return MarshalYAML(d.data)
}

// ToJSON converts the document to JSON bytes.
func (d *document) ToJSON() ([]byte, error) {
	// Convert to JSON-compatible format first
	jsonData := convertToJSONCompatible(d.data)
	return json.Marshal(jsonData)
}

// RawData returns the underlying data structure.
func (d *document) RawData() interface{} {
	return d.data
}

// Clone creates a deep copy of the document.
func (d *document) Clone() Document {
	cloned := deepCopy(d.data)
	if clonedMap, ok := cloned.(map[string]interface{}); ok {
		return NewDocument(clonedMap)
	}
	// Fallback - this shouldn't happen
	return NewDocument(make(map[string]interface{}))
}

// deepCopy performs a deep copy of the data structure.
func deepCopy(src interface{}) interface{} {
	switch v := src.(type) {
	case map[string]interface{}:
		dst := make(map[string]interface{})
		for key, value := range v {
			dst[key] = deepCopy(value)
		}
		return dst

	case []interface{}:
		dst := make([]interface{}, len(v))
		for i, value := range v {
			dst[i] = deepCopy(value)
		}
		return dst

	default:
		// For primitive types and other types, return as-is
		// This handles strings, numbers, booleans, etc.
		return v
	}
}

// pathParts splits a path into its components.
func pathParts(path string) []string {
	if path == "" || path == "$" {
		return nil
	}

	// Remove leading $ if present
	if strings.HasPrefix(path, "$.") {
		path = path[2:]
	} else if path == "$" {
		return nil
	}

	return strings.Split(path, ".")
}

// parseIndex extracts array index from a path component like "items[0]".
func parseIndex(component string) (key string, index int, hasIndex bool) {
	if !strings.Contains(component, "[") {
		return component, 0, false
	}

	parts := strings.SplitN(component, "[", 2)
	if len(parts) != 2 {
		return component, 0, false
	}

	indexStr := strings.TrimSuffix(parts[1], "]")
	index, err := strconv.Atoi(indexStr)
	if err != nil {
		return component, 0, false
	}

	return parts[0], index, true
}

// CreateEmptyDocument creates a new empty document.
func CreateEmptyDocument() Document {
	return NewDocument(make(map[string]interface{}))
}

// pruneNavigationResult holds the result of navigating through a path for pruning.
type pruneNavigationResult struct {
	current     map[string]interface{}
	lastList    []interface{}
	lastListKey string
	success     bool
}

// navigateForPrune navigates through the path segments to find the target for pruning.
func (d *document) navigateForPrune(segments []string, current map[string]interface{}) pruneNavigationResult {
	result := pruneNavigationResult{current: current, success: true}

	for i := 0; i < len(segments)-1; i++ {
		segment := segments[i]

		switch v := result.current[segment].(type) {
		case map[string]interface{}:
			result.current = v
		case []interface{}:
			if i == len(segments)-2 {
				result.lastList = v
				result.lastListKey = segment
			} else {
				nextSegment := segments[i+1]
				elem, skip := d.navigateThroughList(v, nextSegment)
				if elem != nil {
					result.current = elem
					i += skip
				} else {
					result.success = false
					return result
				}
			}
		default:
			result.success = false
			return result
		}
	}
	return result
}

// navigateThroughList finds an element in a list by index or name.
func (d *document) navigateThroughList(list []interface{}, segment string) (elem map[string]interface{}, depth int) {
	// Try numeric index first
	if index, err := strconv.Atoi(segment); err == nil {
		if index >= 0 && index < len(list) {
			if elem, ok := list[index].(map[string]interface{}); ok {
				return elem, 1
			}
		}
	}

	// Try to find by name field
	for _, item := range list {
		if elem, ok := item.(map[string]interface{}); ok {
			if name, exists := elem["name"]; exists {
				if nameStr, ok := name.(string); ok && nameStr == segment {
					return elem, 1
				}
			}
		}
	}
	return nil, 0
}

// Prune removes one or more keys from the document, returning a new
// document with those keys removed. Each key is resolved and pruned
// independently against the same clone, in the order given; a key that
// does not resolve (missing path, list index out of range) is silently
// skipped, matching the prior single-key behavior. Calling Prune with no
// keys returns an unmodified clone.
func (d *document) Prune(keys ...string) Document {
	cloned := d.Clone().(*document) //nolint:errcheck // Clone always returns *document

	for _, key := range keys {
		cloned.pruneKeyInPlace(key)
	}

	return cloned
}

// pruneKeyInPlace removes a single key from d's own data in place. Callers
// must only invoke this on a document that is already an isolated clone
// (see Prune); it mutates d.data directly rather than copying first.
func (d *document) pruneKeyInPlace(key string) {
	// Split on dots, keeping a bracketed dotted node (as the (( prune ))
	// operator queues one) whole
	segments := tree.SplitPath(key)
	if len(segments) == 1 {
		delete(d.data, segments[0])
		return
	}

	result := d.navigateForPrune(segments, d.data)
	if !result.success {
		return
	}

	finalSegment := segments[len(segments)-1]
	if result.lastList != nil {
		d.pruneFromList(result.current, result.lastListKey, result.lastList, finalSegment)
	} else {
		delete(result.current, finalSegment)
	}
}

// pruneFromList removes an element from a list by index.
func (d *document) pruneFromList(current map[string]interface{}, listKey string, list []interface{}, indexStr string) {
	index, err := strconv.Atoi(indexStr)
	if err != nil || index < 0 || index >= len(list) {
		return
	}
	newList := make([]interface{}, 0, len(list)-1)
	newList = append(newList, list[:index]...)
	newList = append(newList, list[index+1:]...)
	current[listKey] = newList
}

// listItemEntry tracks list items with their indices for cherry-pick sorting.
type listItemEntry struct {
	listKey string
	index   int
	item    interface{}
}

// CherryPick creates a new document with only the specified keys.
func (d *document) CherryPick(keys ...string) Document {
	picked := make(map[string]interface{})
	listItems := make([]listItemEntry, 0)

	for _, keyPath := range keys {
		if !strings.Contains(keyPath, ".") {
			d.cherryPickSimpleKey(picked, keyPath)
		} else {
			segments := strings.Split(keyPath, ".")
			if len(segments) == 2 {
				d.cherryPickListEntry(segments, &listItems)
			} else {
				d.cherryPickNestedPath(picked, segments, keyPath)
			}
		}
	}

	d.addSortedListItems(picked, listItems)
	return NewDocument(picked)
}

// cherryPickSimpleKey handles simple key cherry-picking.
func (d *document) cherryPickSimpleKey(picked map[string]interface{}, key string) {
	if val, exists := d.data[key]; exists {
		picked[key] = deepCopy(val)
	}
}

// cherryPickListEntry handles cherry-picking list entries by index or name.
func (d *document) cherryPickListEntry(segments []string, listItems *[]listItemEntry) {
	listKey := segments[0]
	listItemKey := segments[1]

	listVal, exists := d.data[listKey]
	if !exists {
		return
	}

	list, ok := listVal.([]interface{})
	if !ok {
		return
	}

	foundItem, itemIndex := d.findListItem(list, listItemKey)
	if foundItem != nil && itemIndex >= 0 {
		*listItems = append(*listItems, listItemEntry{
			listKey: listKey,
			index:   itemIndex,
			item:    deepCopy(foundItem),
		})
	}
}

// findListItem finds an item in a list by index or identifier field.
func (d *document) findListItem(list []interface{}, key string) (item interface{}, idx int) {
	// Try numeric index first
	if numIdx, err := strconv.Atoi(key); err == nil {
		if numIdx >= 0 && numIdx < len(list) {
			return list[numIdx], numIdx
		}
		return nil, -1
	}

	// Look for named item by identifier fields
	for i, elem := range list {
		if itemMap, ok := elem.(map[string]interface{}); ok {
			for _, idField := range []string{"key", "id", "name"} {
				if idVal, hasId := itemMap[idField]; hasId {
					if idStr, ok := idVal.(string); ok && idStr == key {
						return elem, i
					}
				}
			}
		}
	}
	return nil, -1
}

// cherryPickNestedPath handles cherry-picking nested paths.
func (d *document) cherryPickNestedPath(picked map[string]interface{}, segments []string, keyPath string) {
	val, err := d.Get(keyPath)
	if err != nil || val == nil {
		return
	}

	current := picked
	for i := 0; i < len(segments)-1; i++ {
		if _, exists := current[segments[i]]; !exists {
			current[segments[i]] = make(map[string]interface{})
		}
		if m, ok := current[segments[i]].(map[string]interface{}); ok {
			current = m
		} else {
			return
		}
	}
	if len(segments) > 0 {
		current[segments[len(segments)-1]] = deepCopy(val)
	}
}

// addSortedListItems adds sorted list items to the picked document.
func (d *document) addSortedListItems(picked map[string]interface{}, listItems []listItemEntry) {
	// Sort list items by index in descending order
	for i := 0; i < len(listItems)-1; i++ {
		for j := i + 1; j < len(listItems); j++ {
			if listItems[i].listKey == listItems[j].listKey && listItems[i].index < listItems[j].index {
				listItems[i], listItems[j] = listItems[j], listItems[i]
			}
		}
	}

	// Group items by list key
	listsByKey := make(map[string][]interface{})
	for _, entry := range listItems {
		listsByKey[entry.listKey] = append(listsByKey[entry.listKey], entry.item)
	}

	// Add to picked document
	for listKey, items := range listsByKey {
		picked[listKey] = items
	}
}

// GetData returns the underlying data (for backward compatibility).
func (d *document) GetData() interface{} {
	return d.data
}

// GetInt64 retrieves an int64 value at the given path.
func (d *document) GetInt64(path string) (int64, error) {
	val, err := d.Get(path)
	if err != nil {
		return 0, err
	}

	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		if v == float64(int64(v)) {
			return int64(v), nil
		}
		return 0, withHiddenCause(fmt.Sprintf("value at path %s is a float, not an integer", path), ErrTypeMismatch)
	default:
		return 0, withHiddenCause(fmt.Sprintf("value at path %s is not an integer (got %T)", path, val), ErrTypeMismatch)
	}
}

// GetFloat64 retrieves a float64 value at the given path.
func (d *document) GetFloat64(path string) (float64, error) {
	val, err := d.Get(path)
	if err != nil {
		return 0, err
	}

	switch v := val.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, withHiddenCause(fmt.Sprintf("value at path %s is not a number (got %T)", path, val), ErrTypeMismatch)
	}
}

// GetStringSlice retrieves a string slice value at the given path.
func (d *document) GetStringSlice(path string) ([]string, error) {
	val, err := d.Get(path)
	if err != nil {
		return nil, err
	}

	slice, ok := val.([]interface{})
	if !ok {
		return nil, withHiddenCause(fmt.Sprintf("value at path %s is not a slice (got %T)", path, val), ErrTypeMismatch)
	}

	result := make([]string, 0, len(slice))
	for i, item := range slice {
		str, ok := item.(string)
		if !ok {
			return nil, withHiddenCause(fmt.Sprintf("item at index %d in slice at path %s is not a string (got %T)", i, path, item), ErrTypeMismatch)
		}
		result = append(result, str)
	}
	return result, nil
}

// GetMapStringString retrieves a string-to-string map at the given path.
func (d *document) GetMapStringString(path string) (map[string]string, error) {
	val, err := d.Get(path)
	if err != nil {
		return nil, err
	}

	rawMap, ok := val.(map[string]interface{})
	if !ok {
		return nil, withHiddenCause(fmt.Sprintf("value at path %s is not a map (got %T)", path, val), ErrTypeMismatch)
	}

	result := make(map[string]string)
	for k, v := range rawMap {
		value, ok := v.(string)
		if !ok {
			return nil, withHiddenCause(fmt.Sprintf("map at path %s contains non-string value for key %s: %v", path, k, v), ErrTypeMismatch)
		}
		result[k] = value
	}
	return result, nil
}

// CheckForCycles detects circular references in a data structure.
// It traverses maps and slices up to maxDepth levels deep, returning an error
// if a cycle is detected or the maximum depth is exceeded.
func CheckForCycles(root interface{}, maxDepth int) error {
	visited := make(map[uintptr]bool)

	var check func(o interface{}, depth int) error
	check = func(o interface{}, depth int) error {
		if depth == 0 {
			return ansi.Errorf("@*{Hit max recursion depth. You seem to have a self-referencing dataset}")
		}

		switch v := o.(type) {
		case map[string]interface{}:
			ptr := reflect.ValueOf(v).Pointer()
			if visited[ptr] {
				return ansi.Errorf("@*{Hit max recursion depth. You seem to have a self-referencing dataset}")
			}
			visited[ptr] = true
			for _, val := range v {
				if err := check(val, depth-1); err != nil {
					return err
				}
			}
			delete(visited, ptr)

		case []interface{}:
			ptr := reflect.ValueOf(v).Pointer()
			if visited[ptr] {
				return ansi.Errorf("@*{Hit max recursion depth. You seem to have a self-referencing dataset}")
			}
			visited[ptr] = true
			for _, val := range v {
				if err := check(val, depth-1); err != nil {
					return err
				}
			}
			delete(visited, ptr)
		}

		return nil
	}

	return check(root, maxDepth)
}

// String returns the string value at path, or "" if the path does not
// exist or the value is not a string. Use GetString for the
// error-returning form.
func (d *document) String(path string) string {
	v, err := d.GetString(path)
	if err != nil {
		return ""
	}
	return v
}

// Int returns the int value at path, or 0 if the path does not exist or
// the value is not a whole number. Use GetInt for the error-returning
// form.
func (d *document) Int(path string) int {
	v, err := d.GetInt(path)
	if err != nil {
		return 0
	}
	return v
}

// Int64 returns the int64 value at path, or 0 if the path does not exist
// or the value is not a whole number. Use GetInt64 for the
// error-returning form.
func (d *document) Int64(path string) int64 {
	v, err := d.GetInt64(path)
	if err != nil {
		return 0
	}
	return v
}

// Float64 returns the float64 value at path, or 0 if the path does not
// exist or the value is not a number. Use GetFloat64 for the
// error-returning form.
func (d *document) Float64(path string) float64 {
	v, err := d.GetFloat64(path)
	if err != nil {
		return 0
	}
	return v
}

// Bool returns the bool value at path, or false if the path does not
// exist or the value is not a boolean. Use GetBool for the
// error-returning form.
func (d *document) Bool(path string) bool {
	v, err := d.GetBool(path)
	if err != nil {
		return false
	}
	return v
}

// Has reports whether path resolves to a value in the document.
func (d *document) Has(path string) bool {
	_, err := d.Get(path)
	return err == nil
}

// Paths returns every leaf path in the document (every path whose value
// is not itself a map or a list), in the same dotted, "$"-free canonical
// form as tree.Cursor.String() and every path this package accepts as
// input (e.g. "meta.instance_groups.0.name"), sorted lexicographically
// for stable output. A map or list with no elements contributes no path
// of its own, since it has no leaf value to report; every other returned
// path round-trips through Get.
func (d *document) Paths() []string {
	var paths []string
	collectPaths(d.data, "", &paths)
	sort.Strings(paths)
	return paths
}

// collectPaths recursively walks v, appending the dotted path of every
// leaf (non-map, non-slice) value it finds to paths. prefix is the
// already-built dotted path of v itself ("" at the document root).
func collectPaths(v interface{}, prefix string, paths *[]string) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, child := range val {
			next := k
			if prefix != "" {
				next = prefix + "." + k
			}
			collectPaths(child, next, paths)
		}
	case []interface{}:
		for i, child := range val {
			next := strconv.Itoa(i)
			if prefix != "" {
				next = prefix + "." + next
			}
			collectPaths(child, next, paths)
		}
	default:
		if prefix != "" {
			*paths = append(*paths, prefix)
		}
	}
}

// SortKeys returns a new Document built by a fresh recursive rebuild of
// every nested map and list. The result contains the same data as d;
// "sorted" describes how that data serializes, not how it is stored — Go
// maps have no persistent key order, and both this package's ToJSON
// (encoding/json) and ToYAML (goccy/go-yaml) already sort map[string]
// keys alphabetically when marshaling a plain map[string]interface{}.
// This method exists so callers get that same deterministic-output
// guarantee via RawData()/ToMap() without depending on a particular
// marshaler's default behavior, and mirrors (independently of, and
// without importing) postprocess.KeySorter's recursive-descent shape.
func (d *document) SortKeys() Document {
	sorted, _ := sortKeysRecursive(d.data).(map[string]interface{})
	return &document{data: sorted}
}

// sortKeysRecursive rebuilds v, recursing into every nested map and list.
func sortKeysRecursive(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, child := range val {
			result[k] = sortKeysRecursive(child)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, child := range val {
			result[i] = sortKeysRecursive(child)
		}
		return result
	default:
		return val
	}
}

// ToJSONIndent converts the document to indented JSON bytes, using indent
// as the per-level indentation string and no line prefix. This is a
// Document-level convenience; Engine.ToJSONIndent(doc, indent) remains
// the engine-level equivalent.
func (d *document) ToJSONIndent(indent string) ([]byte, error) {
	jsonData := convertToJSONCompatible(d.data)
	return json.MarshalIndent(jsonData, "", indent)
}

// History returns this document's recorded change history: a live view
// over the DocumentMemory active when it was produced (see
// mergeBuilderImpl.attachHistory), or emptyHistory{} - never a nil
// interface - if no tracking was active. d.history is set only by
// Execute() on the merge that produced this exact Document value; every
// other constructor (NewDocument, Clone, Prune, CherryPick, SortKeys, the
// Parse* family) leaves it nil, and this method returns emptyHistory{}
// for those too. Promoted onto *goPatchDocument via embedding (see
// gopatch_document.go); a go-patch document's embedded *document.history
// is always nil, so it always returns emptyHistory{}.
func (d *document) History() History {
	if d.history == nil {
		return emptyHistory{}
	}
	return d.history
}
