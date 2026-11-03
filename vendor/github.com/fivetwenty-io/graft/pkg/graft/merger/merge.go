// Package merger provides document merging functionality for graft.
package merger

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Operator-matching regexes for the merge walk, compiled once at package
// init. These functions recurse over every node of every document, so the
// patterns must not be compiled per call.
var (
	mergeRx    = regexp.MustCompile(`^\s*\Q((\E\s*merge\s*.*\Q))\E`)
	pruneRx    = regexp.MustCompile(`^\s*\Q((\E\s*prune\s*\Q))\E`)
	sortRx     = regexp.MustCompile(`^\s*\Q((\E\s*sort(?:\s+by\s+(.*?))?\s*\Q))\E$`)
	keyMergeRx = regexp.MustCompile(`^\Q((\E\s*merge(?:\s+on\s+(.*?))?\s*\Q))\E$`)

	mergeRegEx                = regexp.MustCompile(`^\Q((\E\s*merge\s*\Q))\E$`)
	mergeOnKeyRegEx           = regexp.MustCompile(`^\Q((\E\s*merge\s+(on)\s+(.+)\s*\Q))\E$`)
	replaceRegEx              = regexp.MustCompile(`^\Q((\E\s*replace\s*\Q))\E$`)
	inlineRegEx               = regexp.MustCompile(`^\Q((\E\s*inline\s*\Q))\E$`)
	appendRegEx               = regexp.MustCompile(`^\Q((\E\s*append\s*\Q))\E$`)
	prependRegEx              = regexp.MustCompile(`^\Q((\E\s*prepend\s*\Q))\E$`)
	insertByIdxRegEx          = regexp.MustCompile(`^\Q((\E\s*insert\s+(after|before)\s+(\d+)\s*\Q))\E$`)
	insertByNameRegEx         = regexp.MustCompile(`^\Q((\E\s*insert\s+(after|before)\s+([^ ]+)?\s*"(.+)"\s*\Q))\E$`)
	deleteByIdxRegEx          = regexp.MustCompile(`^\Q((\E\s*delete\s+(-?\d+)\s*\Q))\E$`)
	deleteByNameRegEx         = regexp.MustCompile(`^\Q((\E\s*delete\s+([^ ]+)?\s*"(.+)"\s*\Q))\E$`)
	deleteByNameUnquotedRegEx = regexp.MustCompile(`^\Q((\E\s*delete\s+([^ ]+)?\s*(.+)\s*\Q))\E$`)

	// Any argument-bearing delete form (the \S excludes the bare
	// "(( delete ))", which passes through as literal text for spruce
	// parity — see mergeMap's guard).
	deleteOutsideListRegEx = regexp.MustCompile(`^\Q((\E\s*delete\s+\S.*\Q))\E$`)
)

// IsDeleteMarkerWithArgs reports whether s is an argument-bearing
// (( delete ... )) marker — the form mergeMap rejects outside a list. The
// bare "(( delete ))" is not one (it passes through for spruce parity).
// Exported so the simple-merge fast path can route documents carrying the
// marker to this merger and its guard.
func IsDeleteMarkerWithArgs(s string) bool {
	return deleteOutsideListRegEx.MatchString(s)
}

// Default array merge key constant.
const (
	defaultArrayMergeKey = "name"
)

// ErrorContext represents contexts where errors have special meaning.
type ErrorContext uint

const (
	eContextAll          ErrorContext = 0
	eContextDefaultMerge ErrorContext = 1 << iota
)

// WarningError represents a warning that can be printed.
type WarningError struct {
	warning string
	context ErrorContext
}

func (e WarningError) Error() string {
	return e.warning
}

// Warn prints the warning message to stderr.
func (e WarningError) Warn() {
	log.PrintStdErrf("warning: %s\n", e.warning)
}

// HasContext checks if the error has the given context.
func (e WarningError) HasContext(context ErrorContext) bool {
	return e.context == 0 || (context&e.context > 0)
}

// NewWarningError creates a new warning error.
func NewWarningError(context ErrorContext, format string, args ...interface{}) WarningError {
	return WarningError{
		warning: ansi.Sprintf(format, args...),
		context: context,
	}
}

type listOp int

const (
	listOpMergeDefault listOp = iota
	listOpMergeOnKey
	listOpMergeInline
	listOpReplace
	listOpInsert
	listOpDelete
)

// MultiError represents multiple errors.
type MultiError struct {
	Errors []error
}

func (e MultiError) Error() string {
	if len(e.Errors) == 0 {
		return "no errors"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}

	// For multiple errors, list them all
	msgs := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		msgs = append(msgs, err.Error())
	}
	return fmt.Sprintf("%d errors occurred:\n  %s", len(e.Errors), strings.Join(msgs, "\n  "))
}

// Append adds an error to the MultiError collection.
func (e *MultiError) Append(err error) {
	if err != nil {
		e.Errors = append(e.Errors, err)
	}
}

// Count returns the number of errors in the collection.
func (e *MultiError) Count() int {
	return len(e.Errors)
}

// MergeMetadata contains information about special operators encountered during merge.
type MergeMetadata struct {
	PrunePaths []string          // Paths that should be pruned
	SortPaths  map[string]string // Paths that should be sorted -> sort order
}

// Merger handles YAML/map merging operations.
type Merger struct {
	AppendByDefault bool

	Errors MultiError

	// Metadata about operations encountered during merge
	metadata MergeMetadata

	// Memory tracker for recording changes
	memory interfaces.MemoryTracker
}

// ModificationDefinition encapsulates the details of an array modification:
// (1) the type of modification, e.g. insert, delete, replace
// (2) an optional guide to the specific part of the array to be modified,
//
//	for example the index at which an insertion should be done
//
// (3) an optional list of entries to be added or merged into the array.
type ModificationDefinition struct {
	listOp listOp

	index    int
	key      string
	name     string
	relative string

	list []interface{}
}

// Merge merges multiple maps together.
func Merge(l ...map[string]interface{}) (map[string]interface{}, error) {
	m := &Merger{}
	root := map[string]interface{}{}
	for _, next := range l {
		_ = m.Merge(root, next)
	}
	return root, m.Error()
}

// MergeWithMetadata performs merge and returns metadata about operations encountered.
func MergeWithMetadata(l ...map[string]interface{}) (map[string]interface{}, *MergeMetadata, error) {
	m := &Merger{
		metadata: MergeMetadata{
			SortPaths: make(map[string]string),
		},
	}
	root := map[string]interface{}{}
	for _, next := range l {
		_ = m.Merge(root, next)
	}
	return root, &m.metadata, m.Error()
}

// MergeWithMemory performs merge with memory tracking.
func MergeWithMemory(memory interfaces.MemoryTracker, l ...map[string]interface{}) (map[string]interface{}, error) {
	m := &Merger{
		memory: memory,
		metadata: MergeMetadata{
			SortPaths: make(map[string]string),
		},
	}
	root := map[string]interface{}{}
	for _, next := range l {
		_ = m.Merge(root, next)
	}
	return root, m.Error()
}

// Error returns the accumulated errors from merge operations.
func (m *Merger) Error() error {
	if m.Errors.Count() > 0 {
		return m.Errors
	}
	return nil
}

// GetMetadata returns the metadata collected during merge.
func (m *Merger) GetMetadata() *MergeMetadata {
	return &m.metadata
}

// SetMetadata sets the metadata for the merger.
func (m *Merger) SetMetadata(metadata *MergeMetadata) {
	m.metadata = *metadata
}

// SetMemoryTracker sets the memory tracker for recording changes.
func (m *Merger) SetMemoryTracker(memory interfaces.MemoryTracker) {
	m.memory = memory
}

func getDefaultIdentifierKey() string {
	// Use environment variable override, if set
	if os.Getenv("DEFAULT_ARRAY_MERGE_KEY") != "" {
		return os.Getenv("DEFAULT_ARRAY_MERGE_KEY")
	}

	// the built-in default: name
	return defaultArrayMergeKey
}

// GetIdentifierKey returns the array-merge identifier key that the merger
// uses to match array-of-maps entries across documents. It honors the
// DEFAULT_ARRAY_MERGE_KEY environment variable override and falls back to
// "name" when unset, matching spruce semantics. Exported so other packages
// (e.g. CLI path-based lookups for --prune/--cherry-pick) can stay
// consistent with the merge engine's identifier key.
func GetIdentifierKey() string {
	return getDefaultIdentifierKey()
}

func deepCopy(orig interface{}) interface{} {
	if orig == nil {
		return nil
	}

	switch v := orig.(type) {
	case map[string]interface{}:
		x := make(map[string]interface{})
		for k, val := range v {
			x[k] = deepCopy(val)
		}
		return x

	case map[interface{}]interface{}:
		x := make(map[interface{}]interface{})
		for k, val := range v {
			x[k] = deepCopy(val)
		}
		return x

	case []interface{}:
		x := make([]interface{}, len(v))
		for i, val := range v {
			x[i] = deepCopy(val)
		}
		return x

	default:
		return orig
	}
}

// toStringKeyMap converts map[interface{}]interface{} to map[string]interface{}
// and returns a mapping from string keys to original key values for restoring later.
func toStringKeyMap(m map[interface{}]interface{}) (map[string]interface{}, map[string]interface{}) {
	result := make(map[string]interface{}, len(m))
	keyMap := make(map[string]interface{}, len(m))
	for k, v := range m {
		sk := fmt.Sprintf("%v", k)
		result[sk] = v
		keyMap[sk] = k
	}
	return result, keyMap
}

// toOriginalKeyMap converts map[string]interface{} back to map[interface{}]interface{}
// using original key types from origKeys and newKeys maps.
func toOriginalKeyMap(m map[string]interface{}, origKeys, newKeys map[string]interface{}) map[interface{}]interface{} {
	result := make(map[interface{}]interface{}, len(m))
	for sk, v := range m {
		if origKeys != nil {
			if origKey, ok := origKeys[sk]; ok {
				result[origKey] = v
				continue
			}
		}
		if newKeys != nil {
			if newKey, ok := newKeys[sk]; ok {
				result[newKey] = v
				continue
			}
		}
		result[sk] = v
	}
	return result
}

// mergePath is a location in the merge walk, kept in two spellings.
// shown is the plain dotted path that errors, debug output, and history
// report, and it stays exactly as spruce prints it. exact spells every
// node that contains a '.' in brackets (tree.QuoteNode), so the prune and
// sort lists, which split their paths again later, keep a dotted list
// entry name or map key whole. For a path with no dotted node the two
// are identical.
type mergePath struct {
	shown string
	exact string
}

// key returns the path of the map key or named list entry k under p.
func (p mergePath) key(k string) mergePath {
	return mergePath{shown: p.shown + "." + k, exact: p.exact + "." + tree.QuoteNode(k)}
}

// index returns the path of list index i under p.
func (p mergePath) index(i int) mergePath {
	idx := strconv.Itoa(i)
	return mergePath{shown: p.shown + "." + idx, exact: p.exact + "." + idx}
}

// Merge merges map b into map a.
func (m *Merger) Merge(a, b map[string]interface{}) error {
	m.mergeMap(a, b, mergePath{shown: "$", exact: "$"})
	return m.Error()
}

// canonicalHistoryPath strips the merger's internal "$" root marker from a
// merge path so it matches the canonical no-"$" dotted history vocabulary
// (pkg/graft/tree.Cursor.String()) that evaluator.go and the normalized
// merge_builder_impl.go recording calls already use (P0-2). Every path this
// package builds is rooted at "$" (see Merge's initial mergeMap(a, b, "$")
// call), so path always starts with "$." here; do not call this on strings
// from any other source.
func canonicalHistoryPath(path string) string {
	return strings.TrimPrefix(path, "$.")
}

func (m *Merger) mergeMap(orig, n map[string]interface{}, at mergePath) {
	node := at.shown
	// Guarded: getMapKeys builds a fresh key slice per call, per node.
	if log.DebugOn {
		log.DEBUG("mergeMap: merging at node %s", node)
		log.DEBUG("mergeMap: orig keys = %v", getMapKeys(orig))
		log.DEBUG("mergeMap: new keys = %v", getMapKeys(n))
	}
	for k, val := range n {
		child := at.key(k)
		path := child.shown
		if s, ok := val.(string); ok && mergeRx.MatchString(s) {
			m.Errors.Append(ansi.Errorf("@m{%s}: @R{inappropriate use of} @c{(( merge ))} @R{operator outside of a list} (this is @G{graft}, after all)", path))
		}
		// Argument-bearing delete only has meaning as a list entry;
		// anywhere else it would survive the merge as literal data (spruce
		// rejects the same input from its eval phase). The bare
		// "(( delete ))" form is deliberately not guarded: spruce passes
		// it through as literal text, and graft keeps that parity.
		if s, ok := val.(string); ok && deleteOutsideListRegEx.MatchString(s) {
			m.Errors.Append(ansi.Errorf("@m{%s}: @R{inappropriate use of} @c{(( delete ))} @R{operator outside of a list}", path))
		}

		// Guarded: boxing val twice per key escapes the whole subtree.
		if log.DebugOn {
			log.DEBUG("%s: val = %v (type: %T)", path, val, val)
		}

		// Note: We don't skip nil values here - in YAML, null is a valid value
		// that should be preserved, not cause key deletion

		if _, exists := orig[k]; exists {
			log.DEBUG("%s: found upstream, merging it", path)
			oldValue := orig[k]
			result := m.mergeObj(orig[k], val, child)
			// Always set the result, even if nil (null is a valid YAML value)
			orig[k] = result

			// Record change if memory tracking is enabled. path carries the
			// merger's internal "$" root marker (used in error messages the
			// Genesis contract pins byte-for-byte); history recording uses
			// the canonical no-"$" dotted vocabulary (P0-2) so a path
			// touched in both the merge and eval phases lands in one
			// NodeHistory bucket instead of two disjoint ones.
			if m.memory != nil && m.memory.IsEnabled() {
				_ = m.memory.RecordMergeChange(canonicalHistoryPath(path), oldValue, result, "merge")
			}
		} else {
			// delete-if-present against a key the base document doesn't
			// have at all: the delete has nothing to act on, and the key
			// itself must not be materialized (not even as an empty
			// list) - see isPureDeleteOnlyArray's doc comment.
			if arr, ok := val.([]interface{}); ok && isPureDeleteOnlyArray(arr) {
				log.DEBUG("%s: not found upstream, skipping (pure delete-if-present array against an absent key)", path)
				continue
			}

			log.DEBUG("%s: not found upstream, adding it", path)
			result := m.mergeObj(nil, deepCopy(val), child)
			// Always add the result, even if nil (null is a valid YAML value)
			orig[k] = result

			// Record addition if memory tracking is enabled (see the
			// canonicalHistoryPath note above).
			if m.memory != nil && m.memory.IsEnabled() {
				_ = m.memory.RecordMergeChange(canonicalHistoryPath(path), nil, result, "add")
			}
		}
	}
}

// MergeObj merges two objects at node, a plain dotted path.
func (m *Merger) MergeObj(orig interface{}, n interface{}, node string) interface{} {
	return m.mergeObj(orig, n, mergePath{shown: node, exact: node})
}

// MergeObjQuoted is MergeObj for a location whose nodes may contain dots.
// node is the plain dotted path that errors and history report, and
// quoted is the same location spelled by tree.Cursor.Quoted, which is the
// path a prune or sort marker found here is queued under.
func (m *Merger) MergeObjQuoted(orig interface{}, n interface{}, node, quoted string) interface{} {
	return m.mergeObj(orig, n, mergePath{shown: node, exact: quoted})
}

// mergeObj is MergeObj at a location carried in both of its spellings
// (see mergePath).
//
//nolint:gocyclo // mergeObj handles prune/sort operators and multiple type combinations
func (m *Merger) mergeObj(orig interface{}, n interface{}, at mergePath) interface{} {
	node := at.shown
	// prune/sort operator special behavior I:
	// operator is defined in the original object and will now be overwritten by
	// the new value. Therefore, remember that the operator was here at that path
	//
	// prune/sort operator special behavior II:
	// operator is defined in the new object and would therefore overwrite the
	// original content. In this case, keep the original content as it is and mark
	// that the operator occurred at this path
	//
	// common requirement is that both original and new object values are strings
	origString, origOk := orig.(string)
	newString, newOk := n.(string)
	// Guarded: this runs at every node and boxes both subtrees twice.
	if log.DebugOn {
		log.DEBUG("MergeObj at %s: orig=%T(%v) new=%T(%v)", node, orig, orig, n, n)
	}
	switch {
	case origOk && pruneRx.MatchString(origString):
		log.DEBUG("%s: a (( prune )) operator is about to be replaced, check if its path needs to be saved", node)
		log.DEBUG("MergeObj: prune operator being replaced: orig=%q, new=%v", origString, n)
		m.addToPruneListIfNecessary(at.exact)
		return n // Return the new value, not the prune operator

	case newOk && pruneRx.MatchString(newString):
		// Check if we're in an array context based on the path
		pathParts := tree.SplitPath(at.exact)
		if len(pathParts) > 0 {
			lastPart := pathParts[len(pathParts)-1]
			if _, err := strconv.Atoi(lastPart); err == nil {
				// This is an array index - don't mark for deletion during merge
				// Let the evaluation phase handle it
				log.DEBUG("MergeObj: In array context, prune operator at index")
				// Don't add to prune list: m.addToPruneListIfNecessary(node)
				// Return the prune operator for evaluation
				return "(( prune ))"
			}
		}

		// For non-array contexts (regular map keys), a prune operator
		// landing on content an earlier document already set queues the
		// path and leaves that content alone, whatever its type. The
		// value stays readable by operators that reference the path -
		// (( grab )) and friends see what was there, not the marker -
		// and the queued path is removed from the output afterwards,
		// which also covers --skip-eval, where no evaluator runs to
		// resolve a marker left behind.
		//
		// With no original content there is nothing to preserve and
		// nothing to queue: the marker is returned for the evaluator to
		// resolve, and stays literal under --skip-eval, as in spruce.
		if orig != nil {
			log.DEBUG("%s: prune operator replacing existing content, marking for prune but preserving original value", node)
			m.addToPruneListIfNecessary(at.exact)
			return orig
		}
		log.DEBUG("%s: prune operator with no original content, returning operator for evaluation", node)
		return newString

	case origOk && sortRx.MatchString(origString):
		log.DEBUG("%s: a (( sort )) operator is about to be replaced, check if its path needs to be saved", node)
		m.addToSortListIfNecessary(origString, at.exact)

	case newOk && sortRx.MatchString(newString) && orig != nil:
		log.DEBUG("%s: a (( sort )) operator is about to replace existing content, check if its path needs to be saved", node)
		m.addToSortListIfNecessary(newString, at.exact)
		return orig
	}

	switch t := n.(type) {
	case map[string]interface{}:
		switch origMap := orig.(type) {
		case map[string]interface{}:
			log.DEBUG("%s: performing map merge", node)
			m.mergeMap(origMap, t, at)
			return orig

		case map[interface{}]interface{}:
			log.DEBUG("%s: performing map merge (converting orig from interface keys)", node)
			stringOrig, origKeys := toStringKeyMap(origMap)
			m.mergeMap(stringOrig, t, at)
			return toOriginalKeyMap(stringOrig, origKeys, nil)

		case nil:
			newOrig := map[string]interface{}{}
			m.mergeMap(newOrig, t, at)
			return newOrig

		default:
			log.DEBUG("%s: replacing with new data (original was not a map)", node)
			return t
		}

	case map[interface{}]interface{}:
		stringN, newKeys := toStringKeyMap(t)
		switch origMap := orig.(type) {
		case map[interface{}]interface{}:
			log.DEBUG("%s: performing map merge (both have interface keys)", node)
			stringOrig, origKeys := toStringKeyMap(origMap)
			m.mergeMap(stringOrig, stringN, at)
			return toOriginalKeyMap(stringOrig, origKeys, newKeys)

		case map[string]interface{}:
			log.DEBUG("%s: performing map merge (converting new from interface keys)", node)
			m.mergeMap(origMap, stringN, at)
			return orig

		case nil:
			newOrig := map[string]interface{}{}
			m.mergeMap(newOrig, stringN, at)
			return toOriginalKeyMap(newOrig, nil, newKeys)

		default:
			log.DEBUG("%s: replacing with new data (original was not a map)", node)
			return t
		}

	case []interface{}:
		switch origArr := orig.(type) {
		case []interface{}:
			log.DEBUG("%s: performing array merge", node)
			if nArr, ok := n.([]interface{}); ok {
				return m.mergeArray(origArr, nArr, at)
			}
			return origArr

		case nil:
			emptyOrig := []interface{}{}
			if nArr, ok := n.([]interface{}); ok {
				return m.mergeArray(emptyOrig, nArr, at)
			}
			return emptyOrig

		default:
			log.DEBUG("%s: replacing with new data (original was not an array)", node)
			return t
		}

	default:
		log.DEBUG("%s: replacing with new data (new data is neither map nor array)", node)
		return t
	}
}

//nolint:gocyclo // mergeArray handles many array modification operations
func (m *Merger) mergeArray(orig []interface{}, n []interface{}, at mergePath) []interface{} {
	node := at.shown
	simpleList := isSimpleList(orig)
	if len(orig) == 0 {
		// isSimpleList has no entries to inspect on an empty original
		// array, so it always reports "not simple" - which used to
		// misroute a keyed delete/insert marker into map-key lookup even
		// when the incoming list is plainly scalar (e.g. an absent base
		// key merging a literal scalar entry alongside a delete marker
		// would fail canKeyMergeArray against that literal). Fall back to
		// the incoming list's own literal content to decide instead.
		simpleList = !containsMapEntry(n)
	}
	modificationDefinitions := getArrayModifications(n, simpleList)
	log.DEBUG("%s: performing %d modification operations against list", node, len(modificationDefinitions))
	log.DEBUG("%s: original list has %d items, new list has %d items", node, len(orig), len(n))

	// Create a copy of orig for the (multiple) modifications that are about to happen
	result := make([]interface{}, len(orig))
	copy(result, orig)

	// Process the modifications definitions that were found in the new list
	for i, modificationDefinition := range modificationDefinitions {
		log.DEBUG("  #%d %#v", i, modificationDefinition)
		log.DEBUG("  modification: op=%v, key=%v, name=%v, index=%v, list_len=%d",
			modificationDefinition.listOp, modificationDefinition.key, modificationDefinition.name,
			modificationDefinition.index, len(modificationDefinition.list))

		// insert/delete operations will use a list index later in this loop block
		var idx int

		// Special tag for default behavior. Cannot be invoked explicitly by users
		if modificationDefinition.listOp == listOpMergeDefault {
			// Skip default merge if the list is empty (all entries were operators)
			if len(modificationDefinitions[0].list) > 0 {
				result = m.mergeArrayDefault(orig, modificationDefinitions[0].list, at)
			}
			continue
		}

		// Perform a merge on key list modification (merge in new list on original)
		if modificationDefinition.listOp == listOpMergeOnKey {
			key := modificationDefinition.key
			if key == "" {
				key = getDefaultIdentifierKey()
			}

			if err := canKeyMergeArray("new", modificationDefinition.list, node, key); err != nil {
				m.Errors.Append(err)
				return nil
			}
			if err := canKeyMergeArray("original", orig, node, key); err != nil {
				m.Errors.Append(err)
				return nil
			}

			result = m.mergeArrayByKey(result, modificationDefinition.list, at, key)
			continue
		}

		// Perform a merge inline list modification
		if modificationDefinition.listOp == listOpMergeInline {
			result = m.mergeArrayInline(result, modificationDefinition.list, at)
			continue
		}

		// Perform a list replacement modification
		if modificationDefinition.listOp == listOpReplace {
			result = make([]interface{}, len(modificationDefinition.list))
			copy(result, modificationDefinition.list)
			continue
		}

		// Perform insert, delete, append, prepend operation
		switch {
		case modificationDefinition.key == "" && modificationDefinition.name == "": // Index comes directly from operation definition
			idx = modificationDefinition.index

			// Replace the -1 marker with the actual 'end' index of the array
			if idx == -1 {
				idx = len(result)
			}
		case modificationDefinition.key == "" && modificationDefinition.name != "": // Simple list: the entry value itself is the anchor
			name := modificationDefinition.name
			isDelete := modificationDefinition.listOp == listOpDelete
			if isDelete {
				// Sanity check for delete operation, ensure no orphan entries follow the operator definition
				if len(modificationDefinition.list) > 0 {
					m.Errors.Append(ansi.Errorf("@m{%s}: @R{item in array directly after} @c{(( delete \"%s\" ))} @r{must be one of the array operators 'append', 'prepend', 'delete', or 'insert'}", node, name))
					return nil
				}
			}

			// Look up the index of the specified modification point (based solely on the entry value)
			idx = getIndexOfSimpleEntry(result, name)
			if idx < 0 {
				if isDelete {
					// delete-if-present: a delete target that is not in the
					// original list is a silent no-op, not an error. This
					// intentionally diverges from insert, whose target must
					// always exist.
					continue
				}
				m.Errors.Append(ansi.Errorf("@m{%s}: @R{unable to find specified modification point with} @c{'%s'}", node, name))
				return nil
			}
		default: // Index look-up based on key and name
			key := modificationDefinition.key
			name := modificationDefinition.name
			isDelete := modificationDefinition.listOp == listOpDelete

			// getArrayModifications only keeps an explicit key against a
			// simple list so it can be rejected here - a scalar entry has no
			// key to match on, and the marker must never leak into the output
			if simpleList {
				m.Errors.Append(ansi.Errorf("@m{%s}: @R{unable to insert, because the keyed insertion point} @c{'%s: %s'} @R{cannot target entries in a list of scalars}", node, key, name))
				return nil
			}

			// Sanity check original list, list must contain key/id based entries
			if err := canKeyMergeArray("original", result, node, key); err != nil {
				m.Errors.Append(err)
				return nil
			}

			// Sanity check new list, depending on the operation type (delete or insert)
			if isDelete {
				// Sanity check for delete operation, ensure no orphan entries follow the operator definition
				if len(modificationDefinition.list) > 0 {
					m.Errors.Append(ansi.Errorf("@m{%s}: @R{item in array directly after} @c{(( delete %s \"%s\" ))} @r{must be one of the array operators 'append', 'prepend', 'delete', or 'insert'}", node, key, name))
					return nil
				}
			} else {
				// Sanity check new list, list must contain key/id based entries
				if err := canKeyMergeArray("new", modificationDefinition.list, node, key); err != nil {
					m.Errors.Append(err)
					return nil
				}

				// Since we have a way to identify indiviual entries based on their key/id, we can sanity check for possible duplicates
				for _, entry := range modificationDefinition.list {
					obj, ok := entry.(map[string]interface{})
					if !ok {
						continue
					}
					entryNameVal, ok := obj[key].(string)
					if !ok {
						continue
					}
					if getIndexOfEntry(result, key, entryNameVal) >= 0 {
						m.Errors.Append(ansi.Errorf("@m{%s}: @R{unable to insert, because new list entry} @c{'%s: %s'} @R{is detected multiple times}", node, key, entryNameVal))
						return nil
					}
				}
			}

			// Look up the index of the specified insertion point (based on its key/name)
			idx = getIndexOfEntry(result, key, name)
			if idx < 0 {
				if isDelete {
					// delete-if-present: a delete target that is not in the
					// original list is a silent no-op, not an error. This
					// intentionally diverges from insert, whose target must
					// always exist.
					continue
				}
				m.Errors.Append(ansi.Errorf("@m{%s}: @R{unable to find specified modification point with} @c{'%s: %s'}", node, key, name))
				return nil
			}
		}

		// If after is specified, add one to the index to actually put the entry where it is expected
		if modificationDefinition.relative == "after" {
			idx++
		}

		// Back out if idx is smaller than 0, or greater than the length (for inserts), or greater/equal than the length (for deletes)
		if (idx < 0) || (modificationDefinition.listOp != listOpDelete && idx > len(result)) || (modificationDefinition.listOp == listOpDelete && idx >= len(result)) {
			m.Errors.Append(ansi.Errorf("@m{%s}: @R{unable to modify the list, because specified index} @c{%d} @R{is out of bounds}", node, idx))
			return nil
		}

		if modificationDefinition.listOp != listOpDelete {
			log.DEBUG("%s: inserting %d new elements to existing array at index %d", node, len(modificationDefinition.list), idx)
			result = insertIntoList(result, idx, modificationDefinition.list)
		} else {
			log.DEBUG("%s: deleting element at array index %d", node, idx)
			result = deleteIndexFromList(result, idx)
		}
	}

	return result
}

// The magic which chooses to merge, append, or inline based on the contents of
// the array.
func (m *Merger) mergeArrayDefault(orig, n []interface{}, at mergePath) []interface{} {
	node := at.shown
	log.DEBUG("%s: performing index-based array merge", node)
	log.DEBUG("%s: mergeArrayDefault - orig len=%d, new len=%d", node, len(orig), len(n))
	var err error
	key := getDefaultIdentifierKey()

	if err = canKeyMergeArray("original", orig, node, key); err == nil {
		if err = canKeyMergeArray("new", n, node, key); err == nil {
			return m.mergeArrayByKey(orig, n, at, key)
		}
	}

	// Warn the user about any unintuitive behavior that may have gotten us here.
	var warning WarningError
	if errors.As(err, &warning) && warning.HasContext(eContextDefaultMerge) {
		mergeStratStr := "inline"
		if m.AppendByDefault {
			mergeStratStr = "append"
		}
		warning.Warn()
		NewWarningError(eContextDefaultMerge, "@Y{Falling back to %s merge strategy}", mergeStratStr).Warn()
	}

	if m.AppendByDefault {
		return append(orig, n...)
	}
	return m.mergeArrayInline(orig, n, at)
}

//nolint:gocyclo // mergeArrayInline handles prune operators during inline merging
func (m *Merger) mergeArrayInline(orig, n []interface{}, at mergePath) []interface{} {
	node := at.shown
	// Guarded: boxes both whole arrays on every inline array merge.
	if log.DebugOn {
		log.DEBUG("%s: mergeArrayInline - orig=%v (len=%d), new=%v (len=%d)", node, orig, len(orig), n, len(n))
	}

	// First, scan the new array to identify which indices have prune operators
	prunedIndices := make(map[int]bool)
	for i, item := range n {
		if str, ok := item.(string); ok && pruneRx.MatchString(str) {
			prunedIndices[i] = true
			log.DEBUG("%s.%d: found prune operator in new array", node, i)
		}
	}

	// Merge arrays with prune handling
	merged := make([]interface{}, 0, len(orig)+len(n))

	// When we have prune operators, we need to handle the merge differently
	if len(prunedIndices) > 0 {
		log.DEBUG("%s: handling array merge with %d prune operators, orig len=%d, new len=%d", node, len(prunedIndices), len(orig), len(n))
		// Process all elements, but skip those marked for pruning
		for i := 0; i < len(orig); i++ {
			log.DEBUG("%s: processing original element at index %d", node, i)
			// Check if this index is being pruned by the new array
			if i < len(n) && prunedIndices[i] {
				log.DEBUG("%s: skipping element at index %d due to prune operator", node, i)
				// Skip this element entirely - it's being pruned
				continue
			}

			// If there's a corresponding element in the new array (and it's not a prune), merge them
			if i < len(n) && !prunedIndices[i] {
				mergedItem := m.mergeObj(orig[i], n[i], at.index(len(merged)))
				merged = append(merged, mergedItem)
			} else {
				// No corresponding element in new array, keep the original
				log.DEBUG("%s: keeping original element at index %d (no corresponding new element)", node, i)
				merged = append(merged, orig[i])
			}
		}

		// Add any additional elements from the new array that are beyond the original array's length
		for i := len(orig); i < len(n); i++ {
			if !prunedIndices[i] {
				mergedItem := m.mergeObj(nil, n[i], at.index(i))
				merged = append(merged, mergedItem)
			}
		}
	} else {
		// No prune operators, use the standard merge logic
		// Process elements that exist in both arrays
		for i := 0; i < len(orig) && i < len(n); i++ {
			mergedItem := m.mergeObj(orig[i], n[i], at.index(i))
			merged = append(merged, mergedItem)
		}

		// Process remaining elements from original array (if any)
		for i := len(n); i < len(orig); i++ {
			merged = append(merged, orig[i])
		}

		// Process remaining elements from new array (if any)
		for i := len(orig); i < len(n); i++ {
			child := at.index(i)
			path := child.shown

			// If this index should be pruned, skip it
			if prunedIndices[i] {
				log.DEBUG("%s: pruning list entry at index %d", path, i)
				continue
			}

			log.DEBUG("%s: appending new data to existing array", path)
			mergedItem := m.mergeObj(nil, n[i], child)
			merged = append(merged, mergedItem)
		}
	}

	return merged
}

func (m *Merger) mergeArrayByKey(orig, n []interface{}, at mergePath, key string) []interface{} {
	merged := make([]interface{}, 0, len(orig)+len(n))
	newMap := make(map[string]interface{})

	for _, o := range n {
		obj, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		keyVal := fmt.Sprintf("%v", obj[key])
		newMap[keyVal] = obj
	}

	for _, o := range orig {
		obj, ok := o.(map[string]interface{})
		if !ok {
			continue
		}
		keyVal := fmt.Sprintf("%v", obj[key])
		child := at.key(keyVal)
		var mergedItem interface{}

		if _, ok := newMap[keyVal]; ok {
			mergedItem = m.mergeObj(obj, newMap[keyVal], child)
			delete(newMap, keyVal)
		} else {
			mergedItem = m.mergeObj(nil, obj, child)
		}

		// Don't check for prune operators here - they should be handled at the field level
		merged = append(merged, mergedItem)
	}

	i := 0
	for _, obj := range n {
		objMap, ok := obj.(map[string]interface{})
		if !ok {
			continue
		}
		keyVal := fmt.Sprintf("%v", objMap[key])
		if _, ok := newMap[keyVal]; ok {
			child := at.index(i)
			log.DEBUG("%s: appending new data to merged array", child.shown)
			mergedItem := m.mergeObj(nil, objMap, child)

			// Don't check for prune operators here - they should be handled at the field level
			merged = append(merged, mergedItem)
			i++
		}
	}

	return merged
}

// getArrayModifications returns a list of ModificationDefinition objects with
// information on which array operations to apply to which entries. The first
// object in the returned will always represent the default merge behavior.
//
//nolint:gocyclo // parses many regex patterns for array operations
func getArrayModifications(obj []interface{}, simpleList bool) []ModificationDefinition {
	// Starts with an entry representing the default merge behavior
	result := []ModificationDefinition{{listOp: listOpMergeDefault}}

	// easy shortcircuit
	if len(obj) == 0 {
		return result
	}

	for _, entry := range obj {
		e, isString := entry.(string)
		switch {
		case !isString:
			// Do absolutely nothing

		case mergeRegEx.MatchString(e): // check for (( merge ))
			result = append(result, ModificationDefinition{listOp: listOpMergeOnKey})
			continue

		case mergeOnKeyRegEx.MatchString(e): // check for (( merge on "key" ))
			/* #0 is the whole string,
			 * #1 is string 'on'
			 * #2 is the named-entry identifying key
			 */
			if captures := mergeOnKeyRegEx.FindStringSubmatch(e); len(captures) == 3 {
				key := strings.TrimSpace(captures[2])
				result = append(result, ModificationDefinition{listOp: listOpMergeOnKey, key: key})
				continue
			}

		case inlineRegEx.MatchString(e): // check for (( inline ))
			result = append(result, ModificationDefinition{listOp: listOpMergeInline})
			continue

		case replaceRegEx.MatchString(e): // check for (( replace ))
			result = append(result, ModificationDefinition{listOp: listOpReplace})
			continue

		case appendRegEx.MatchString(e): // check for (( append ))
			result = append(result, ModificationDefinition{listOp: listOpInsert, index: -1})
			continue

		case prependRegEx.MatchString(e): // check for (( prepend ))
			result = append(result, ModificationDefinition{listOp: listOpInsert, index: 0})
			continue

		case insertByIdxRegEx.MatchString(e): // check for (( insert ... <idx> ))
			/* #0 is the whole string,
			 * #1 is after or before
			 * #2 is the insertion index
			 */
			if captures := insertByIdxRegEx.FindStringSubmatch(e); len(captures) == 3 {
				relative := strings.TrimSpace(captures[1])
				position := strings.TrimSpace(captures[2])
				if idx, err := strconv.Atoi(position); err == nil {
					result = append(result, ModificationDefinition{listOp: listOpInsert, index: idx, relative: relative})
					continue
				}
			}

		case insertByNameRegEx.MatchString(e): // check for (( insert ... "<name>" ))
			/* #0 is the whole string,
			 * #1 is after or before
			 * #2 contains the optional '<key>' string
			 * #3 is finally the target "<name>" string
			 */
			entryStr, ok := entry.(string)
			if !ok {
				continue
			}
			if captures := insertByNameRegEx.FindStringSubmatch(entryStr); len(captures) == 4 {
				relative := strings.TrimSpace(captures[1])
				key := strings.TrimSpace(captures[2])
				name := strings.TrimSpace(captures[3])

				// A simple list has no identifier key - the anchor is the
				// entry value itself, so the key must stay empty. An explicit
				// key on a simple list is kept for mergeArray to reject with
				// a targeted error rather than silently dropping the marker.
				if !simpleList && key == "" {
					key = getDefaultIdentifierKey()
				}

				result = append(result, ModificationDefinition{listOp: listOpInsert, relative: relative, key: key, name: name})
				continue
			}

		case deleteByIdxRegEx.MatchString(e): // check for (( delete <idx> ))
			/* #0 is the whole string,
			 * #1 is idx
			 */
			if captures := deleteByIdxRegEx.FindStringSubmatch(e); len(captures) == 2 {
				position := strings.TrimSpace(captures[1])
				if idx, err := strconv.Atoi(position); err == nil {
					result = append(result, ModificationDefinition{listOp: listOpDelete, index: idx})
					continue
				}
			}

		case deleteByNameRegEx.MatchString(e): // check for (( delete "<name>" ))
			/* #0 is the whole string,
			 * #1 contains the optional '<key>' string
			 * #2 is finally the target "<name>" string
			 */
			if captures := deleteByNameRegEx.FindStringSubmatch(e); len(captures) == 3 {
				key := strings.TrimSpace(captures[1])
				name := strings.TrimSpace(captures[2])

				// illegal state for simple lists, if you have a text with whitespaces, we want to enforce people using quotes
				if simpleList && key != "" {
					continue
				}

				if !simpleList && key == "" {
					key = getDefaultIdentifierKey()
				}

				result = append(result, ModificationDefinition{listOp: listOpDelete, key: key, name: name})
				continue
			}

		case deleteByNameUnquotedRegEx.MatchString(e): // check for (( delete "<name>" ))
			/* #0 is the whole string,
			 * #1 contains the optional '<key>' string
			 * #2 is finally the target "<name>" string
			 */
			if captures := deleteByNameUnquotedRegEx.FindStringSubmatch(e); len(captures) == 3 {
				key := strings.TrimSpace(captures[1])
				name := strings.TrimSpace(captures[2])

				// illegal state for simple lists, if you have a text with whitespaces, we want to enforce people using quotes
				if simpleList && key != "" && name != "" {
					continue
				}

				if name == "" {
					name = key
					if !simpleList {
						key = getDefaultIdentifierKey()
					} else {
						key = ""
					}
				}

				result = append(result, ModificationDefinition{listOp: listOpDelete, key: key, name: name})
				continue
			}
		}

		lastResultIdx := len(result) - 1

		// Add the current entry to the 'current' modification definition record (gathering the list)
		result[lastResultIdx].list = append(result[lastResultIdx].list, entry)
	}

	return result
}

// containsMapEntry reports whether list has any map[string]interface{}
// entry among its elements. Array-merge marker strings and literal
// scalars are both plain strings, so this only ever flags genuine
// list-of-maps content; used to classify an empty original array (which
// isSimpleList can't classify on its own) from the incoming list's own
// literal shape instead.
func containsMapEntry(list []interface{}) bool {
	for _, item := range list {
		if _, ok := item.(map[string]interface{}); ok {
			return true
		}
	}
	return false
}

// isPureDeleteOnlyArray reports whether arr consists solely of delete
// markers (index or name/value form), with no other array-merge markers
// and no literal data entries. Used to detect the delete-if-present
// case where the base document has no key at all for this array: since
// delete-if-present is a no-op when the target is missing, materializing
// the key at all - even as an empty list - would be wrong. An absent key
// and an empty list are different downstream (an empty `foo: []` can
// override a lower-precedence document's non-empty default for `foo`;
// an absent key leaves it alone).
func isPureDeleteOnlyArray(arr []interface{}) bool {
	if len(arr) == 0 {
		return false
	}

	mods := getArrayModifications(arr, !containsMapEntry(arr))
	if len(mods) < 2 {
		// No operators matched at all - the whole array is literal data.
		return false
	}
	if len(mods[0].list) > 0 {
		// Literal entries outside of (before/after) any marker: this is
		// not a pure-delete-only array, so the key still gets created.
		return false
	}
	for _, mod := range mods[1:] {
		if mod.listOp != listOpDelete {
			return false
		}
	}
	return true
}

func isSimpleList(list []interface{}) bool {
	// Guarded: boxes the whole list on every key-merge eligibility check.
	if log.DebugOn {
		log.DEBUG("Going to validate if this is a simple list: %v", list)
	}

	if len(list) == 0 {
		return false
	}

	var hash_count int
	for _, item := range list {
		if _, ok := item.(map[string]interface{}); ok {
			hash_count++
		}
	}
	if hash_count == 0 {
		log.DEBUG("Working on a simple list")
	}
	return hash_count == 0
}

func shouldKeyMergeArray(obj []interface{}) (bool, string) {
	key := getDefaultIdentifierKey()

	if len(obj) >= 1 && obj[0] != nil && reflect.TypeOf(obj[0]).Kind() == reflect.String {
		objStr, ok := obj[0].(string)
		if !ok {
			return false, ""
		}
		if keyMergeRx.MatchString(objStr) {
			keys := keyMergeRx.FindStringSubmatch(objStr)
			if keys[1] != "" {
				key = keys[1]
			}
			return true, key
		}
	}
	return false, ""
}

// keyMergeError is canKeyMergeArray's probe-failure error. The default
// array merge (mergeArrayDefault) calls canKeyMergeArray speculatively
// on every array pair and silently falls back when it fails, so on the
// common path the error is created and discarded without ever being
// printed. Formatting the ansi-colored message is deferred to Error()
// so the discarded case costs a small allocation instead of a full
// color-template render; the rendered text is byte-identical to the
// eager ansi.Errorf calls this replaces.
type keyMergeError struct {
	kind     keyMergeErrKind
	disp     string
	node     string
	key      string
	idx      int
	typeName string
}

type keyMergeErrKind int

const (
	keyMergeNilObject keyMergeErrKind = iota
	keyMergeNotMap
	keyMergeNotStringMap
	keyMergeMissingKey
)

func (e *keyMergeError) Error() string {
	switch e.kind {
	case keyMergeNilObject:
		return ansi.Sprintf("@m{%s.%d}: @R{%s object is nil - cannot merge by key}", e.node, e.idx, e.disp)
	case keyMergeNotMap:
		return ansi.Sprintf("@m{%s.%d}: @R{%s object is a} @c{%s}@R{, not a} @c{map} @R{- cannot merge by key}", e.node, e.idx, e.disp, e.typeName)
	case keyMergeNotStringMap:
		return ansi.Sprintf("@m{%s.%d}: @R{%s object is not a map[string]interface{} - cannot merge by key}", e.node, e.idx, e.disp)
	default: // keyMergeMissingKey
		return ansi.Sprintf("@m{%s.%d}: @R{%s object does not contain the key} @c{'%s'}@R{ - cannot merge by key}", e.node, e.idx, e.disp, e.key)
	}
}

func canKeyMergeArray(disp string, array []interface{}, node string, key string) error {
	// ensure that all elements of `array` are maps,
	// and that they contain the key `key`

	for i, o := range array {
		if o == nil {
			return &keyMergeError{kind: keyMergeNilObject, disp: disp, node: node, idx: i}
		}
		if reflect.TypeOf(o).Kind() != reflect.Map {
			return &keyMergeError{kind: keyMergeNotMap, disp: disp, node: node, idx: i, typeName: reflect.TypeOf(o).Kind().String()}
		}

		obj, ok := o.(map[string]interface{})
		if !ok {
			return &keyMergeError{kind: keyMergeNotStringMap, disp: disp, node: node, idx: i}
		}
		if _, ok := obj[key]; !ok {
			return &keyMergeError{kind: keyMergeMissingKey, disp: disp, node: node, key: key, idx: i}
		}

		// Verify that the target key has a hashable value (i.e. a value that is not itself a hash or sequence)
		targetValue := obj[key]
		_, isMap := targetValue.(map[string]interface{})
		_, isSlice := targetValue.([]interface{})
		if isMap || isSlice {
			return NewWarningError(eContextDefaultMerge, "@m{%s.%d}: @R{%s object's key} @c{'%s'} @R{cannot have a value which is a hash or sequence - cannot merge by key}", node, i, disp, key)
		}
	}
	return nil
}

func getIndexOfSimpleEntry(list []interface{}, name string) int {
	for i, entry := range list {
		if s, ok := entry.(string); ok && s == name {
			return i
		}
	}
	return -1
}

func getIndexOfEntry(list []interface{}, key string, name string) int {
	for i, entry := range list {
		if reflect.TypeOf(entry).Kind() == reflect.Map {
			obj, ok := entry.(map[string]interface{})
			if !ok {
				continue
			}
			if obj[key] == name {
				return i
			}
		}
	}

	return -1
}

func insertIntoList(orig []interface{}, idx int, list []interface{}) []interface{} {
	out := make([]interface{}, 0, len(orig)+len(list))
	out = append(out, orig[:idx]...)
	out = append(out, list...)
	out = append(out, orig[idx:]...)

	return out
}

func deleteIndexFromList(orig []interface{}, idx int) []interface{} {
	tmp := make([]interface{}, len(orig))
	copy(tmp, orig)

	return append(tmp[:idx], tmp[idx+1:]...)
}

// addToPruneListIfNecessary adds a path to the prune list if not already present.
func (m *Merger) addToPruneListIfNecessary(path string) {
	// Remove "$." prefix if present
	cleanPath := strings.TrimPrefix(path, "$.")

	// Check if already in list
	for _, p := range m.metadata.PrunePaths {
		if p == cleanPath {
			return
		}
	}

	log.DEBUG("merger: adding '%s' to the list of paths to prune", cleanPath)
	m.metadata.PrunePaths = append(m.metadata.PrunePaths, cleanPath)
}

// addToSortListIfNecessary adds a path to the sort list with its sort order.
func (m *Merger) addToSortListIfNecessary(operator, path string) {
	// Extract sort order from operator if present
	matches := sortRx.FindStringSubmatch(operator)

	sortKey := ""
	if len(matches) > 1 && matches[1] != "" {
		sortKey = strings.TrimSpace(matches[1])
	}

	// Remove "$." prefix if present
	cleanPath := strings.TrimPrefix(path, "$.")

	log.DEBUG("merger: adding '%s' to the list of paths to sort (by: %s)", cleanPath, sortKey)
	if m.metadata.SortPaths == nil {
		m.metadata.SortPaths = make(map[string]string)
	}
	m.metadata.SortPaths[cleanPath] = sortKey
}

func getMapKeys(m map[string]interface{}) []interface{} {
	keys := make([]interface{}, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
