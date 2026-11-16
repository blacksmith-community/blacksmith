package yaml

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/cursorpath"
	"github.com/fivetwenty-io/graft/internal/humanreport"
	"github.com/fivetwenty-io/graft/internal/yamldiff"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// DiffOptions changes how Diff compares. The zero value compares the way
// graft diff does.
type DiffOptions struct {
	// IgnoreOrderChanges leaves out changes that only reorder a list or
	// the documents of a stream.
	IgnoreOrderChanges bool
}

// ChangeKind tells what a Change did to the value at its path.
type ChangeKind int

const (
	// ChangeAdded means entries were added to the collection at Path.
	ChangeAdded ChangeKind = iota + 1
	// ChangeRemoved means entries were removed from the collection at Path.
	ChangeRemoved
	// ChangeModified means the value at Path changed.
	ChangeModified
	// ChangeOrder means the entries at Path, or the documents of the
	// stream, were reordered.
	ChangeOrder
)

// String returns "added", "removed", "modified", or "order".
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeModified:
		return "modified"
	case ChangeOrder:
		return "order"
	}
	return "ChangeKind(" + strconv.Itoa(int(k)) + ")"
}

// Change is one difference. For an addition or a removal, Path names the
// collection that gained or lost entries, and To or From holds them. For
// a modification, Path names the value that changed. Document is the
// document's index in the stream, or -1 for a change in document order.
type Change struct {
	Document int
	Path     string
	Kind     ChangeKind
	From     *Node
	To       *Node
}

// DiffReport is the result of Diff.
type DiffReport struct {
	changes []Change
	report  yamldiff.Report
}

// Changes returns a copy of the changes, in report order.
func (r *DiffReport) Changes() []Change {
	return append([]Change(nil), r.changes...)
}

// Empty reports whether the inputs have no differences.
func (r *DiffReport) Empty() bool {
	return len(r.changes) == 0
}

// ReportStyle says how Write prints a report. The zero value prints
// plain text in the narrow layout graft diff uses when it can't read a
// terminal width.
type ReportStyle struct {
	Color     bool
	TrueColor bool
	Width     int
}

// Write prints the report as graft diff prints it, without the final
// blank line graft diff adds after it. A report that can't be rendered
// comes back as a *DiffError, and an error from w comes back as it is.
func (r *DiffReport) Write(w io.Writer, style ReportStyle) error {
	err := humanreport.Write(w, r.report, humanreport.Options{
		Color:     style.Color,
		TrueColor: style.TrueColor,
		Width:     style.Width,
	})
	var fatal *humanreport.FatalError
	if errors.As(err, &fatal) {
		return &DiffError{Reason: "the report could not be written", err: err}
	}
	return err
}

// DiffError is a pair of inputs that Diff can't compare. Reason is fixed
// text and never quotes the input.
type DiffError struct {
	Reason string
	err    error
}

// Error returns "yaml: diff: " and the reason.
func (e *DiffError) Error() string {
	return "yaml: diff: " + e.Reason
}

// Unwrap returns the error from the comparison.
func (e *DiffError) Unwrap() error {
	return e.err
}

// Diff compares two YAML or JSON inputs, each of which may hold several
// documents, with the engine graft diff uses. It never reads TOML, which
// graft diff reads only when it opens a file. TOML that starts with a
// table header fails to parse, and TOML made of key = value lines reads
// as one YAML string. A parse failure comes back as a *DecodeError, and
// inputs it can't compare come back as a *DiffError.
func Diff(a, b []byte, opts *DiffOptions) (*DiffReport, error) {
	fromDocs, err := yamldiff.ParseDocuments(a)
	if err != nil {
		return nil, translate(err)
	}
	toDocs, err := yamldiff.ParseDocuments(b)
	if err != nil {
		return nil, translate(err)
	}

	report, err := yamldiff.CompareInputFiles(
		yamldiff.InputFile{Documents: fromDocs},
		yamldiff.InputFile{Documents: toDocs},
	)
	if err != nil {
		reason := "the inputs could not be compared"
		if errors.Is(err, yamldiff.ErrDocumentCount) {
			reason = "the inputs hold different numbers of documents"
		}
		return nil, &DiffError{Reason: reason, err: err}
	}

	if opts != nil && opts.IgnoreOrderChanges {
		report.Diffs = withoutOrderChanges(report.Diffs)
	}

	from, to := newDiffConverter(a), newDiffConverter(b)
	index := yamldiff.NewKeyIndex()
	var changes []Change
	for _, d := range report.Diffs {
		document, path := -1, ""
		if d.Path != nil {
			document = d.Path.DocumentIdx
			path = diffPath(d.Path.PathElements, diffRoots(report, d.Path), index)
		}
		for _, detail := range d.Details {
			changes = append(changes, Change{
				Document: document,
				Path:     path,
				Kind:     changeKind(detail.Kind),
				From:     from.node(detail.From),
				To:       to.node(detail.To),
			})
		}
	}
	return &DiffReport{changes: changes, report: report}, nil
}

// withoutOrderChanges returns diffs without any order change, and without
// any diff that held nothing else. It leaves diffs itself unchanged.
func withoutOrderChanges(diffs []yamldiff.Diff) []yamldiff.Diff {
	var kept []yamldiff.Diff
	for _, d := range diffs {
		var details []yamldiff.Detail
		for _, detail := range d.Details {
			if detail.Kind != yamldiff.ORDERCHANGE {
				details = append(details, detail)
			}
		}
		if len(details) > 0 {
			kept = append(kept, yamldiff.Diff{Path: d.Path, Details: details})
		}
	}
	return kept
}

func changeKind(k yamldiff.DetailKind) ChangeKind {
	switch k {
	case yamldiff.ADDITION:
		return ChangeAdded
	case yamldiff.REMOVAL:
		return ChangeRemoved
	case yamldiff.ORDERCHANGE:
		return ChangeOrder
	case yamldiff.MODIFICATION:
		return ChangeModified
	}
	return ChangeModified
}

// diffRoot is a document a change's path is checked against, and the
// side of the comparison it is on.
type diffRoot struct {
	node *yamlnode.Node
	from bool
}

// diffRoots returns the documents that path, a path the comparison
// walked, must name the same node in. A path with elements always
// starts in a from document. The to document is known only when the
// documents met by position, which is when the inputs carry no names.
func diffRoots(report yamldiff.Report, path *yamldiff.Path) []diffRoot {
	if len(path.PathElements) == 0 || path.Root == nil {
		return nil
	}
	var roots []diffRoot
	if doc := documentValue(path.Root.Documents, path.DocumentIdx); doc != nil {
		roots = append(roots, diffRoot{node: doc, from: true})
	}
	if len(report.To.Names) == 0 {
		if doc := documentValue(report.To.Documents, path.DocumentIdx); doc != nil {
			roots = append(roots, diffRoot{node: doc})
		}
	}
	return roots
}

// documentValue returns the value of the document at idx in docs, or nil.
func documentValue(docs []*yamlnode.Node, idx int) *yamlnode.Node {
	if idx < 0 || idx >= len(docs) {
		return nil
	}
	doc := docs[idx]
	if doc.Kind == yamlnode.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	return doc
}

// diffPath spells a path in graft's cursor syntax, so that Document
// reads it as naming the node the comparison found in each of roots. A
// segment the syntax can't spell, or one that Document would read as a
// different node, is written with a leading ']'. Every segment before it
// is well formed, so Document refuses the whole path as invalid rather
// than reading it as some other node.
func diffPath(elements []yamldiff.PathElement, roots []diffRoot, ix *yamldiff.KeyIndex) string {
	parts := make([]string, len(elements))
	steps := make([]string, len(elements))
	bad := -1
	for i, e := range elements {
		step, ok := pathStep(e, i == 0)
		steps[i] = step
		parts[i] = cursorpath.Quote(step)
		if !ok && bad < 0 {
			bad = i
			parts[i] = step
		}
	}
	if bad < 0 {
		for _, root := range roots {
			if i := wrongStep(root, elements, steps, ix); i >= 0 && (bad < 0 || i < bad) {
				bad = i
			}
		}
	}
	if bad >= 0 {
		parts[bad] = "]" + parts[bad]
	}
	return strings.Join(parts, ".")
}

// pathStep returns the segment Document reads as e, before any quoting,
// and whether the cursor syntax can spell it. first says e is the
// path's first element, where a lone "$" names the root.
func pathStep(e yamldiff.PathElement, first bool) (string, bool) {
	switch {
	case e.Key != "":
		step := e.Key + "=" + e.Name
		field, value, ok := cursorpath.Predicate(step)
		return step, ok && field == e.Key && value == e.Name &&
			!strings.ContainsAny(step, "[]")
	case e.Idx >= 0:
		return strconv.Itoa(e.Idx), true
	}
	return e.Name, e.Name != "" && !strings.ContainsAny(e.Name, "[]") &&
		(!first || e.Name != "$")
}

// wrongStep walks steps through root as Document does, beside the
// comparison's own walk of elements, and returns the index of the first
// step that reaches a different node, or -1. A step Document can't take
// at all ends the walk with -1, since Document then refuses the path.
// ix finds map entries without scanning, and serves every path under a
// map from one pass over it.
func wrongStep(root diffRoot, elements []yamldiff.PathElement, steps []string, ix *yamldiff.KeyIndex) int {
	cur := root.node
	for i, e := range elements {
		want, ok := e.StepIndexed(cur, root.from, ix)
		if !ok {
			return i
		}
		cur = yamlnode.FollowAlias(cur)
		var found *yamlnode.Node
		switch cur.Kind {
		case yamlnode.MappingNode:
			value, count := ix.ScalarValue(cur, steps[i])
			if count != 1 {
				return -1
			}
			found = value
		case yamlnode.SequenceNode:
			idx, _, err := findItem(cur, steps[i])
			if err != nil {
				return -1
			}
			found = cur.Content[idx]
		default:
			return -1
		}
		if yamlnode.FollowAlias(found) != want {
			return i
		}
		cur = want
	}
	return -1
}

// newDiffConverter returns a converter for the nodes read from src.
func newDiffConverter(src []byte) *converter {
	text := parsedText(src)
	return &converter{
		seen:  make(map[*yamlnode.Node]*Node),
		lines: bytes.Split(text, []byte("\n")),
		text:  &readText{doc: text},
	}
}
