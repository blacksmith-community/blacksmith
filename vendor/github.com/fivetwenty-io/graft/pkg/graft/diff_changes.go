package graft

// This file is intentionally not aliasing internal/utils/ansi as fmt (the
// convention diff.go uses). Flattening and JSON/YAML serialization never
// emit "@X{...}" color markup, so there is no reason to route them through
// ansi.Sprintf/ansi.Errorf; using the real fmt/errors keeps stack traces
// and %w wrapping ordinary. diff_render.go, which does color-aware
// rendering, documents its own (different) alias decision.
import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// ChangeType classifies a single Change produced by flattening a diff.
type ChangeType int

const (
	// ChangeAdded means the value at Path exists only in the new document.
	ChangeAdded ChangeType = iota
	// ChangeRemoved means the value at Path exists only in the old document.
	ChangeRemoved
	// ChangeModified means the scalar value at Path differs between the
	// old and new document, with both sides the same graft value Type.
	ChangeModified
	// ChangeTypeChanged means the value at Path changed graft value Type
	// (e.g. a scalar became a map) between the old and new document.
	ChangeTypeChanged
)

// String returns a human-readable label for c, used by the WriteChangeList
// and WriteMergeTree renderers and by ToJSON's "type" field.
func (c ChangeType) String() string {
	switch c {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeModified:
		return "modified"
	case ChangeTypeChanged:
		return "type changed"
	default:
		return "unknown"
	}
}

// Change describes a single addition, removal, or modification found by
// diffing two documents.
type Change struct {
	// Type classifies the change.
	Type ChangeType
	// Path identifies where in the document tree the change occurred.
	// Map fields are dot-separated ("meta.name"); list entries use
	// bracket notation, either a numeric index for simple lists
	// ("servers[0]") or a "field=value" predicate for keyed lists
	// ("servers[name=web]"), matching the grammar ParsePath/PathMatches
	// (utils.go) already understand. A field name containing '.', '[',
	// ']', or '"' is wrapped in double quotes (ParsePath's own escape for
	// such fields); a field name containing a literal '"' is not
	// round-trippable through Path and is a known limitation shared with
	// every other path-string API in this package.
	Path string
	// OldValue is the value at Path in the old document. Unset (nil) for
	// ChangeAdded.
	OldValue interface{}
	// NewValue is the value at Path in the new document. Unset (nil) for
	// ChangeRemoved.
	NewValue interface{}
}

// DiffResult is the outcome of comparing two documents: the flattened list
// of changes between them, queryable by kind or path, plus renderers and
// serializers.
//
// Note on naming: pkg/graft already exports a package-level function
// Diff(a, b interface{}) (Diffable, error) (diff.go) that predates this
// type and cannot be renamed without breaking existing callers, so the
// result type here is DiffResult rather than the more obvious "Diff".
//
// DiffResult is not intended to be implemented outside this package.
// DiffDocuments and Engine.Diff/DiffWithOptions only ever return the
// package's own *diffResult; new methods may be added to this interface in
// minor releases.
type DiffResult interface {
	// Changes returns every change found, in a deterministic but not
	// globally path-sorted order: within each map/list node, additions
	// are listed before removals, which are listed before recursing into
	// changed common entries (itself in sorted-key order); this matches
	// the traversal order the legacy Diffable.String() rendering
	// (diff.go) already uses, and is stable across runs for the same
	// input.
	Changes() []Change
	// HasChanges reports whether Changes() is non-empty.
	HasChanges() bool
	// Added returns the subset of Changes() with Type == ChangeAdded.
	Added() []Change
	// Removed returns the subset of Changes() with Type == ChangeRemoved.
	Removed() []Change
	// Modified returns the subset of Changes() with Type == ChangeModified
	// or Type == ChangeTypeChanged: everything that is neither a pure
	// addition nor a pure removal.
	Modified() []Change
	// ChangesAtPath returns every change whose Path is exactly path, or a
	// descendant of it (e.g. path "servers[name=web]" also matches
	// "servers[name=web].port").
	ChangesAtPath(path string) []Change

	// WriteSideBySide, WriteUnified, WriteChangeList, and WriteMergeTree
	// render the result to w; see diff_render.go. opts may be nil, in
	// which case DefaultDiffOptions() is used for rendering knobs
	// (Width/Context/OmitHeader/ShowTypes/Color). The IgnorePaths/
	// OnlyPaths/IgnoreArrayOrder/IgnoreWhitespace fields of opts, if set,
	// have no effect here: those only apply at diff-computation time
	// (DiffDocuments/Engine.DiffWithOptions), not at render time.
	WriteSideBySide(w io.Writer, opts *DiffOptions) error
	WriteUnified(w io.Writer, opts *DiffOptions) error
	WriteChangeList(w io.Writer, opts *DiffOptions) error
	WriteMergeTree(w io.Writer, opts *DiffOptions) error

	// ToJSON renders Changes() as a JSON array of
	// {"type","path","old_value","new_value"} objects.
	ToJSON() ([]byte, error)
	// ToYAML renders Changes() as a YAML sequence of the same shape.
	ToYAML() ([]byte, error)
}

// diffResult is the concrete DiffResult implementation returned by
// DiffDocuments and the Engine Diff/DiffWithOptions methods.
type diffResult struct {
	changes []Change
}

func (r *diffResult) Changes() []Change {
	out := make([]Change, len(r.changes))
	copy(out, r.changes)
	return out
}

func (r *diffResult) HasChanges() bool {
	return len(r.changes) > 0
}

func (r *diffResult) Added() []Change {
	return r.byType(ChangeAdded)
}

func (r *diffResult) Removed() []Change {
	return r.byType(ChangeRemoved)
}

func (r *diffResult) Modified() []Change {
	out := make([]Change, 0, len(r.changes))
	for _, c := range r.changes {
		if c.Type == ChangeModified || c.Type == ChangeTypeChanged {
			out = append(out, c)
		}
	}
	return out
}

func (r *diffResult) byType(t ChangeType) []Change {
	out := make([]Change, 0, len(r.changes))
	for _, c := range r.changes {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out
}

func (r *diffResult) ChangesAtPath(path string) []Change {
	out := make([]Change, 0)
	for _, c := range r.changes {
		if c.Path == path || strings.HasPrefix(c.Path, path+".") || strings.HasPrefix(c.Path, path+"[") {
			out = append(out, c)
		}
	}
	return out
}

// changeJSON is the wire shape ToJSON/ToYAML emit for each Change.
type changeJSON struct {
	Type     string      `json:"type" yaml:"type"`
	Path     string      `json:"path" yaml:"path"`
	OldValue interface{} `json:"old_value" yaml:"old_value"`
	NewValue interface{} `json:"new_value" yaml:"new_value"`
}

func (r *diffResult) toWireForm() []changeJSON {
	out := make([]changeJSON, len(r.changes))
	for i, c := range r.changes {
		out[i] = changeJSON{
			Type:     c.Type.String(),
			Path:     c.Path,
			OldValue: convertToJSONCompatible(c.OldValue),
			NewValue: convertToJSONCompatible(c.NewValue),
		}
	}
	return out
}

func (r *diffResult) ToJSON() ([]byte, error) {
	return json.Marshal(r.toWireForm())
}

func (r *diffResult) ToYAML() ([]byte, error) {
	return MarshalYAML(r.toWireForm())
}

// DiffDocuments computes a DiffResult between a and b using opts (nil
// selects DefaultDiffOptions()). It reuses the existing Diff comparison
// engine (diff.go) unchanged: IgnoreArrayOrder/IgnoreWhitespace are
// applied as a pre-pass over independent copies of a and b's raw data
// (prepareDiffInputs), and IgnorePaths/OnlyPaths filter the flattened
// Change list afterward.
func DiffDocuments(a, b Document, opts *DiffOptions) (DiffResult, error) {
	if a == nil || b == nil {
		return nil, NewValidationError("diff: both documents must be non-nil")
	}
	if opts == nil {
		opts = DefaultDiffOptions()
	}

	aRaw, bRaw := prepareDiffInputs(a.RawData(), b.RawData(), opts)

	d, err := Diff(aRaw, bRaw)
	if err != nil {
		return nil, err
	}

	changes := flattenDiff("", aRaw, bRaw, d)
	changes = filterChanges(changes, opts)

	return &diffResult{changes: changes}, nil
}

// flattenDiff descends a Diffable comparison result (diff.go's DiffMap /
// DiffList / DiffScalar / DiffType / DiffNone, as produced by Diff)
// together with the original values it was computed from, emitting one
// Change per added/removed subtree, changed scalar, or type change.
//
// aVal and bVal are the values at path in the original a/b trees. They
// carry information the Diffable tree itself discards: the raw,
// pre-yamlmarshal scalar values for Change.OldValue/NewValue (DiffScalar
// only stores the already-YAML-marshaled strings), and which field a
// keyed list was matched on (diffKeyedLists computes that from `key :=
// keyed(la)` and then throws it away, keeping only the resulting
// value-keyed maps) so keyed-list paths can render as "[field=value]"
// rather than a bare "[value]", which ParsePath cannot parse and
// PathMatches would then always fail to match.
func flattenDiff(path string, aVal, bVal interface{}, d Diffable) []Change {
	switch diff := d.(type) {
	case DiffNone:
		// DiffNone marks an unchanged leaf; only ever reached if a caller
		// hands flattenDiff a DiffNone directly (Diff() itself never
		// returns one at the top level -- DiffNone only appears wrapped
		// inside DiffMap.Added/Removed and DiffList.Added/Removed, which
		// flattenDiffMap/flattenDiffList read directly via .Value()
		// rather than recursing through flattenDiff).
		return nil
	case DiffType:
		return []Change{{Type: ChangeTypeChanged, Path: path, OldValue: diff.Old, NewValue: diff.New}}
	case DiffScalar:
		if !diff.Changed() {
			return nil
		}
		return []Change{{Type: ChangeModified, Path: path, OldValue: aVal, NewValue: bVal}}
	case DiffMap:
		return flattenDiffMap(path, aVal, bVal, diff)
	case DiffList:
		return flattenDiffList(path, aVal, bVal, diff)
	default:
		// Diffable has exactly the five implementations above (diff.go);
		// this default exists only to satisfy the compiler/linter and
		// never executes against Diff()'s own output.
		return nil
	}
}

// flattenDiffMap flattens a DiffMap node. aVal/bVal are expected to be the
// map[string]interface{} that produced d; a failed assertion (e.g. a nil
// aVal for an all-added subtree) degrades to a nil map, so member lookups
// below safely return the zero value rather than panicking.
func flattenDiffMap(path string, aVal, bVal interface{}, d DiffMap) []Change {
	aMap, _ := aVal.(map[string]interface{})
	bMap, _ := bVal.(map[string]interface{})

	var changes []Change

	for _, k := range sortkeys(d.Added) {
		changes = append(changes, Change{Type: ChangeAdded, Path: mapKeySegment(path, k), NewValue: bMap[k]})
	}
	for _, k := range sortkeys(d.Removed) {
		changes = append(changes, Change{Type: ChangeRemoved, Path: mapKeySegment(path, k), OldValue: aMap[k]})
	}
	for _, k := range sortkeys(d.Common) {
		child := d.Common[k]
		if !child.Changed() {
			continue
		}
		changes = append(changes, flattenDiff(mapKeySegment(path, k), aMap[k], bMap[k], child)...)
	}

	return changes
}

// flattenDiffList flattens a DiffList node. It is reached only when
// typeof(aVal) == typeof(bVal) (Diff dispatches a DiffType change instead
// otherwise), so recomputing typeof(aVal) here reproduces exactly the
// keyed-vs-simple classification Diff already used to decide between
// diffKeyedLists and diffSimpleLists.
func flattenDiffList(path string, aVal, bVal interface{}, d DiffList) []Change {
	aList, _ := aVal.([]interface{})
	bList, _ := bVal.([]interface{})

	isKeyed := typeof(aVal) == KeyedList

	var keyField string
	var aByKey, bByKey map[string]interface{}
	if isKeyed {
		// Mirrors diffKeyedLists (diff.go) exactly: the key field is
		// always derived from aList (la) only, never bList, so d.Common/
		// d.Added/d.Removed's map keys line up with aByKey/bByKey below.
		keyField = keyed(aList)
		aByKey = mapify(aList, keyField)
		bByKey = mapify(bList, keyField)
	}

	listSegment := func(k string) string {
		if isKeyed {
			return path + "[" + keyField + "=" + k + "]"
		}
		return path + "[" + k + "]"
	}

	var changes []Change

	for _, k := range sortkeys(d.Added) {
		changes = append(changes, Change{Type: ChangeAdded, Path: listSegment(k), NewValue: d.Added[k].Value()})
	}
	for _, k := range sortkeys(d.Removed) {
		changes = append(changes, Change{Type: ChangeRemoved, Path: listSegment(k), OldValue: d.Removed[k].Value()})
	}
	for _, k := range sortkeys(d.Common) {
		child := d.Common[k]
		if !child.Changed() {
			continue
		}

		var elemA, elemB interface{}
		if isKeyed {
			elemA, elemB = aByKey[k], bByKey[k]
		} else if idx, err := strconv.Atoi(k); err == nil {
			if idx >= 0 && idx < len(aList) {
				elemA = aList[idx]
			}
			if idx >= 0 && idx < len(bList) {
				elemB = bList[idx]
			}
		}

		changes = append(changes, flattenDiff(listSegment(k), elemA, elemB, child)...)
	}

	return changes
}

// mapKeySegment appends key as a map-field path segment onto base,
// quoting it (ParsePath's own quoted-field escape) when it contains a
// character that would otherwise be misparsed as path grammar.
func mapKeySegment(base, key string) string {
	if strings.ContainsAny(key, ".[]\"") {
		return AppendPath(base, "\""+key+"\"")
	}
	return AppendPath(base, key)
}
