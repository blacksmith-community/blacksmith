package graft

import (
	"regexp"
	"strings"
)

// DiffOptions configures how DiffDocuments (and Engine.DiffWithOptions)
// compute and render a diff between two documents.
type DiffOptions struct {
	// Color enables ANSI color codes in rendered output. Renderers
	// implement this locally (see diff_render.go); it does not read or
	// mutate the package-wide internal/utils/ansi color toggle.
	Color bool

	// Width is the target column width used by WriteSideBySide to size its
	// two columns. Values <= 0 fall back to 80.
	Width int

	// Context bounds how many lines of an old/new value's YAML rendering
	// WriteUnified shows before eliding the rest with a "... (N more
	// lines)" marker. 0 (the default) shows every line.
	Context int

	// IgnorePaths excludes any change whose path matches one of these
	// patterns from the result. Patterns use the PathMatches grammar: "*"
	// matches exactly one path segment, "**" matches zero or more.
	IgnorePaths []string

	// OnlyPaths, if non-empty, restricts the result to changes whose path
	// matches at least one of these patterns (same grammar as
	// IgnorePaths). Applied before IgnorePaths.
	OnlyPaths []string

	// IgnoreArrayOrder treats non-keyed ("simple") lists as multisets, at
	// any nesting depth: two simple lists containing the same elements in
	// a different order compare as unchanged. Keyed lists (entries with a
	// name/id/key field) are already matched by key rather than position
	// and are unaffected by this option.
	IgnoreArrayOrder bool

	// IgnoreWhitespace trims leading/trailing whitespace and collapses
	// internal whitespace runs to a single space in every string scalar,
	// at any nesting depth, before comparing old and new values.
	IgnoreWhitespace bool

	// OmitHeader suppresses the summary header ("N changes detected:")
	// renderers otherwise print before the body.
	OmitHeader bool

	// ShowTypes annotates each WriteChangeList entry with the graft value
	// Type (scalar/map/simple list/keyed list) of its old and new value.
	ShowTypes bool
}

// DefaultDiffOptions returns the default diff behavior: no color, an
// 80-column side-by-side width, unbounded unified-diff context, no path
// filters, and strict (order- and whitespace-sensitive) comparison.
func DefaultDiffOptions() *DiffOptions {
	return &DiffOptions{
		Width:   80,
		Context: 0,
	}
}

// prepareDiffInputs applies the pre-pass transforms requested by opts
// (IgnoreArrayOrder, IgnoreWhitespace) to independent copies of a and b,
// leaving the originals untouched. The unmodified Diff/diffMaps/
// diffSimpleLists/diffKeyedLists comparison engine (diff.go) is then run
// against the copies, so no option state is threaded through the core
// comparison algorithm.
func prepareDiffInputs(a, b interface{}, opts *DiffOptions) (interface{}, interface{}) {
	if opts == nil {
		return a, b
	}
	if opts.IgnoreArrayOrder {
		a = canonicalizeArrayOrder(a)
		b = canonicalizeArrayOrder(b)
	}
	if opts.IgnoreWhitespace {
		a = normalizeWhitespace(a)
		b = normalizeWhitespace(b)
	}
	return a, b
}

// whitespaceRunRe matches one or more consecutive whitespace characters,
// used by normalizeWhitespace to collapse internal runs to a single space.
var whitespaceRunRe = regexp.MustCompile(`\s+`)

// normalizeWhitespace returns a copy of v with every string scalar
// (at any nesting depth, in maps and lists) trimmed of leading/trailing
// whitespace and with internal whitespace runs collapsed to a single
// space. Map/list structure, keys, and non-string scalars are returned
// unchanged. v itself is not mutated.
func normalizeWhitespace(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return whitespaceRunRe.ReplaceAllString(strings.TrimSpace(val), " ")
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			out[k] = normalizeWhitespace(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = normalizeWhitespace(item)
		}
		return out
	default:
		return v
	}
}

// filterChanges applies opts.OnlyPaths and opts.IgnorePaths to changes,
// preserving the input order. OnlyPaths, if non-empty, is applied first:
// a change must match at least one OnlyPaths pattern to survive.
// IgnorePaths then drops any change matching at least one of its
// patterns, including ones that survived OnlyPaths.
func filterChanges(changes []Change, opts *DiffOptions) []Change {
	if opts == nil || (len(opts.OnlyPaths) == 0 && len(opts.IgnorePaths) == 0) {
		return changes
	}

	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		if len(opts.OnlyPaths) > 0 && !matchesAnyPattern(c.Path, opts.OnlyPaths) {
			continue
		}
		if len(opts.IgnorePaths) > 0 && matchesAnyPattern(c.Path, opts.IgnorePaths) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// matchesAnyPattern reports whether path matches at least one pattern,
// using PathMatches (utils.go).
func matchesAnyPattern(path string, patterns []string) bool {
	for _, p := range patterns {
		if PathMatches(path, p) {
			return true
		}
	}
	return false
}
