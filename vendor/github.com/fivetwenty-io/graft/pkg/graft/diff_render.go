package graft

// Alias decision (see the naming-conflict note in diff_changes.go): this
// file imports fmt normally (no ansi alias) and imports
// internal/utils/ansi under its own name for the raw color-code constants
// (ansi.RedFg, ansi.GreenFg, ...). Every call site below that wants color
// goes through the local colorize() helper, which only applies those
// codes when opts.Color is true -- it deliberately never touches
// ansi.Color()/ansi.IsColorEnabled() (the package-wide, mutable, global
// toggle diff.go's Diffable.String() rendering and pkg/graft/diff_test.go
// use). Reading DiffOptions.Color per call, rather than a shared global,
// keeps renderer output deterministic and safe to call concurrently
// regardless of what any other code in the process has done with
// ansi.Color(); see c2-notes.md for the full rationale.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
)

// colorize wraps s in the given raw ANSI color code (one of the ansi.*Fg
// constants) followed by ansi.ResetCode, but only when opts.Color is set;
// otherwise s is returned unchanged. opts is assumed non-nil (every
// renderer entry point below defaults it before calling into rendering
// helpers).
func colorize(opts *DiffOptions, code, s string) string {
	if !opts.Color || code == "" {
		return s
	}
	return code + s + ansi.ResetCode
}

// changeListHeader renders the "N changes detected:" summary line shared
// by WriteChangeList, WriteUnified, WriteSideBySide, and WriteMergeTree
// (unless opts.OmitHeader is set).
func changeListHeader(n int) string {
	switch n {
	case 0:
		return "no changes detected\n"
	case 1:
		return "1 change detected:\n"
	default:
		return fmt.Sprintf("%d changes detected:\n", n)
	}
}

// changeSymbolAndColor returns the single-character marker and the raw
// ANSI color code (see colorize) renderers use for a given ChangeType.
func changeSymbolAndColor(t ChangeType) (string, string) {
	switch t {
	case ChangeAdded:
		return "+", ansi.GreenFg
	case ChangeRemoved:
		return "-", ansi.RedFg
	case ChangeTypeChanged:
		return "!", ansi.YellowFg
	default: // ChangeModified
		return "~", ansi.CyanFg
	}
}

// describeValue renders v as a single-line-trimmed YAML fragment for
// inline display in WriteChangeList/WriteMergeTree, or "null" for a nil
// value (used for the unset side of an add/remove Change).
func describeValue(v interface{}) string {
	if v == nil {
		return "null"
	}
	b, err := MarshalYAML(v)
	if err != nil {
		// MarshalYAML only fails on values goccy/go-yaml cannot encode at
		// all (e.g. a channel or func smuggled in via a custom Document
		// implementation); falling back to %v keeps rendering total
		// instead of losing the whole diff over one unencodable value.
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSpace(string(b))
}

// describeValueInline is describeValue, collapsed to a single line by
// joining a multi-line YAML block (e.g. a whole added/removed map or list
// subtree) with "; " between its lines. Used by WriteChangeList and
// WriteMergeTree, which are one-line-per-change/one-line-per-node formats;
// WriteUnified and WriteSideBySide use valueLines instead and render each
// line of a multi-line value on its own row.
func describeValueInline(v interface{}) string {
	s := describeValue(v)
	if !strings.Contains(s, "\n") {
		return s
	}
	return strings.Join(strings.Split(s, "\n"), "; ")
}

// valueTypeLabel returns the graft value Type name of v ("scalar", "map",
// "simple list", "keyed list"), or "none" for a nil value.
func valueTypeLabel(v interface{}) string {
	if v == nil {
		return "none"
	}
	return typeof(v).String()
}

// valueLines splits describeValue(v) into its constituent lines, used by
// the two line-oriented renderers (WriteUnified, WriteSideBySide). A nil
// value ("null") is still one line, so old/new columns line up even when
// one side is unset.
func valueLines(v interface{}) []string {
	return strings.Split(describeValue(v), "\n")
}

// -----------------------------------------------------------------------
// WriteChangeList: one line per change, e.g.
//   + servers[name=web].port added: 8080
//   ~ servers[name=web].host modified: web1 -> web2
//   - servers[name=old] removed: {name: old, port: 8080}
// -----------------------------------------------------------------------

func (r *diffResult) WriteChangeList(w io.Writer, opts *DiffOptions) error {
	if opts == nil {
		opts = DefaultDiffOptions()
	}

	var buf strings.Builder
	if !opts.OmitHeader {
		buf.WriteString(changeListHeader(len(r.changes)))
	}
	for _, c := range r.changes {
		buf.WriteString(renderChangeListLine(c, opts))
	}

	_, err := io.WriteString(w, buf.String())
	return err
}

func renderChangeListLine(c Change, opts *DiffOptions) string {
	symbol, color := changeSymbolAndColor(c.Type)

	var b strings.Builder
	fmt.Fprintf(&b, "  %s %s", colorize(opts, color, symbol), c.Path)
	if opts.ShowTypes {
		fmt.Fprintf(&b, " (%s -> %s)", valueTypeLabel(c.OldValue), valueTypeLabel(c.NewValue))
	}

	switch c.Type {
	case ChangeAdded:
		fmt.Fprintf(&b, " added: %s", describeValueInline(c.NewValue))
	case ChangeRemoved:
		fmt.Fprintf(&b, " removed: %s", describeValueInline(c.OldValue))
	case ChangeModified:
		fmt.Fprintf(&b, " modified: %s -> %s", describeValueInline(c.OldValue), describeValueInline(c.NewValue))
	case ChangeTypeChanged:
		fmt.Fprintf(&b, " changed type: %s -> %s", describeValueInline(c.OldValue), describeValueInline(c.NewValue))
	}
	b.WriteString("\n")
	return b.String()
}

// -----------------------------------------------------------------------
// WriteUnified: a per-change unified-diff-style hunk, e.g.
//   @@ servers[name=web].host @@
//   -web1
//   +web2
// -----------------------------------------------------------------------

func (r *diffResult) WriteUnified(w io.Writer, opts *DiffOptions) error {
	if opts == nil {
		opts = DefaultDiffOptions()
	}

	var buf strings.Builder
	if !opts.OmitHeader {
		buf.WriteString(changeListHeader(len(r.changes)))
	}
	for _, c := range r.changes {
		buf.WriteString(renderUnifiedHunk(c, opts))
	}

	_, err := io.WriteString(w, buf.String())
	return err
}

func renderUnifiedHunk(c Change, opts *DiffOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@@ %s @@\n", c.Path)

	switch c.Type {
	case ChangeAdded:
		writeUnifiedLines(&b, opts, "+", ansi.GreenFg, valueLines(c.NewValue))
	case ChangeRemoved:
		writeUnifiedLines(&b, opts, "-", ansi.RedFg, valueLines(c.OldValue))
	default: // ChangeModified, ChangeTypeChanged
		writeUnifiedLines(&b, opts, "-", ansi.RedFg, valueLines(c.OldValue))
		writeUnifiedLines(&b, opts, "+", ansi.GreenFg, valueLines(c.NewValue))
	}

	return b.String()
}

// writeUnifiedLines writes each line of lines to b, prefixed with prefix
// and colorized with color. When opts.Context > 0 and there are more
// lines than that, only the first opts.Context lines are written,
// followed by a same-colored "... (N more lines)" marker; Context == 0
// (the DefaultDiffOptions() value) writes every line.
func writeUnifiedLines(b *strings.Builder, opts *DiffOptions, prefix, color string, lines []string) {
	limit := len(lines)
	truncated := false
	if opts.Context > 0 && len(lines) > opts.Context {
		limit = opts.Context
		truncated = true
	}

	for _, line := range lines[:limit] {
		fmt.Fprintf(b, "%s\n", colorize(opts, color, prefix+line))
	}
	if truncated {
		fmt.Fprintf(b, "%s\n", colorize(opts, color, prefix+"... ("+strconv.Itoa(len(lines)-limit)+" more lines)"))
	}
}

// -----------------------------------------------------------------------
// WriteSideBySide: two columns (old | new), sized from opts.Width.
// -----------------------------------------------------------------------

func (r *diffResult) WriteSideBySide(w io.Writer, opts *DiffOptions) error {
	if opts == nil {
		opts = DefaultDiffOptions()
	}

	width := opts.Width
	if width <= 0 {
		width = 80
	}
	colWidth := (width - 3) / 2 // " | " separator is 3 columns
	if colWidth < 1 {
		colWidth = 1
	}

	var buf strings.Builder
	if !opts.OmitHeader {
		buf.WriteString(changeListHeader(len(r.changes)))
	}
	for _, c := range r.changes {
		buf.WriteString(renderSideBySideBlock(c, opts, colWidth))
	}

	_, err := io.WriteString(w, buf.String())
	return err
}

func renderSideBySideBlock(c Change, opts *DiffOptions, colWidth int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", colorize(opts, ansi.CyanFg, c.Path))

	left := valueLines(c.OldValue)
	right := valueLines(c.NewValue)
	rows := len(left)
	if len(right) > rows {
		rows = len(right)
	}

	leftColor, rightColor := sideBySideColors(c.Type)

	for i := 0; i < rows; i++ {
		var l, rgt string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rgt = right[i]
		}
		fmt.Fprintf(&b, "  %s | %s\n",
			colorize(opts, leftColor, padOrTruncate(l, colWidth)),
			colorize(opts, rightColor, truncateOnly(rgt, colWidth)))
	}

	return b.String()
}

// sideBySideColors returns the (left, right) column colors for a
// ChangeType: an add has nothing on the left, a remove has nothing on the
// right, and a modification colors both.
func sideBySideColors(t ChangeType) (string, string) {
	switch t {
	case ChangeAdded:
		return "", ansi.GreenFg
	case ChangeRemoved:
		return ansi.RedFg, ""
	default: // ChangeModified, ChangeTypeChanged
		return ansi.RedFg, ansi.GreenFg
	}
}

// padOrTruncate right-pads s with spaces to width, or truncates it (with
// a trailing "..." when there is room for one) if it is longer than
// width, so side-by-side columns stay aligned.
func padOrTruncate(s string, width int) string {
	if len(s) > width {
		return truncateOnly(s, width)
	}
	return s + strings.Repeat(" ", width-len(s))
}

// truncateOnly shortens s to width (with a trailing "..." when there is
// room for one), leaving it as-is if it already fits. Used directly for
// WriteSideBySide's right-hand column, which -- being the last column on
// the line -- needs the same width bound as the left column but no
// trailing padding.
func truncateOnly(s string, width int) string {
	if len(s) <= width {
		return s
	}
	if width <= 3 {
		return s[:width]
	}
	return s[:width-3] + "..."
}

// -----------------------------------------------------------------------
// WriteMergeTree: changes grouped into a nested tree that mirrors the
// document shape, e.g.
//   servers:
//     [name=web]:
//       port: ~ 8080 -> 9090
//     [name=old]: - removed: {name: old, port: 8080}
//   meta:
//     version: ~ 1 -> 2
// -----------------------------------------------------------------------

// mergeTreeNode is one node of the tree WriteMergeTree renders. change is
// set when this exact node is itself a Change; children/order hold any
// deeper path segments seen under it. order preserves first-insertion
// order (itself deterministic: it follows the deterministic Changes()
// order), rather than sorting keys again, so sibling changes and sibling
// substructure interleave in the same order they were flattened in.
type mergeTreeNode struct {
	change   *Change
	children map[string]*mergeTreeNode
	order    []string
}

func newMergeTreeNode() *mergeTreeNode {
	return &mergeTreeNode{children: make(map[string]*mergeTreeNode)}
}

func (n *mergeTreeNode) child(key string) *mergeTreeNode {
	if c, ok := n.children[key]; ok {
		return c
	}
	c := newMergeTreeNode()
	n.children[key] = c
	n.order = append(n.order, key)
	return c
}

// buildMergeTree groups changes by their ParsePath segments. A change
// whose Path fails to parse (see the Path field's known limitation for
// keys containing a literal '"') is attached directly under the root
// using its raw, unparsed path string rather than being dropped.
func buildMergeTree(changes []Change) *mergeTreeNode {
	root := newMergeTreeNode()
	for i := range changes {
		c := &changes[i]

		segs, err := ParsePath(c.Path)
		if err != nil || len(segs) == 0 {
			root.child(c.Path).change = c
			continue
		}

		node := root
		for _, seg := range segs {
			node = node.child(seg.String())
		}
		node.change = c
	}
	return root
}

func (r *diffResult) WriteMergeTree(w io.Writer, opts *DiffOptions) error {
	if opts == nil {
		opts = DefaultDiffOptions()
	}

	var buf strings.Builder
	if !opts.OmitHeader {
		buf.WriteString(changeListHeader(len(r.changes)))
	}
	writeMergeTreeNode(&buf, opts, buildMergeTree(r.changes), 0)

	_, err := io.WriteString(w, buf.String())
	return err
}

func writeMergeTreeNode(b *strings.Builder, opts *DiffOptions, n *mergeTreeNode, depth int) {
	indent := strings.Repeat("  ", depth)

	for _, key := range n.order {
		child := n.children[key]

		if child.change != nil && len(child.children) == 0 {
			fmt.Fprintf(b, "%s%s\n", indent, renderMergeTreeLeaf(key, *child.change, opts))
			continue
		}

		fmt.Fprintf(b, "%s%s:\n", indent, key)
		if child.change != nil {
			// A change recorded exactly at a node that also has deeper
			// children cannot happen from flattenDiff's own output today
			// (Added/Removed/Modified/TypeChanged are all terminal, never
			// followed by a deeper change at the same path), but handling
			// it here rather than silently dropping child.change keeps
			// this renderer correct for any future Change producer, not
			// just DiffDocuments.
			fmt.Fprintf(b, "%s  %s\n", indent, renderMergeTreeLeaf("(self)", *child.change, opts))
		}
		writeMergeTreeNode(b, opts, child, depth+1)
	}
}

func renderMergeTreeLeaf(key string, c Change, opts *DiffOptions) string {
	symbol, color := changeSymbolAndColor(c.Type)
	marker := colorize(opts, color, symbol)

	switch c.Type {
	case ChangeAdded:
		return fmt.Sprintf("%s: %s %s", key, marker, describeValueInline(c.NewValue))
	case ChangeRemoved:
		return fmt.Sprintf("%s: %s %s", key, marker, describeValueInline(c.OldValue))
	default: // ChangeModified, ChangeTypeChanged
		return fmt.Sprintf("%s: %s %s -> %s", key, marker, describeValueInline(c.OldValue), describeValueInline(c.NewValue))
	}
}
