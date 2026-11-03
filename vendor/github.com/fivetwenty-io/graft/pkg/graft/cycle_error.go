package graft

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
)

// cycleExprMaxRunes bounds how much of one file-derived string reaches
// output. A cycle block is a diagnostic, not a document dump.
const cycleExprMaxRunes = 120

// sanitizeDisplay makes one file-derived string safe to print inside an
// error block. It applies to paths as well as expressions and filenames:
// a YAML mapping key carries exactly the same payloads an operator
// expression can, and a path is printed twice - once in the chain line
// and once in its detail line.
//
// Three properties matter, in order:
//
//   - No escape bytes reach a terminal, so stderr stays clean on a
//     non-tty (the genesis stderr contract).
//   - The result occupies exactly one output line, so it cannot forge a
//     line beginning " - $." that genesis would scrape as an error.
//   - The result is bounded, so a pathological value cannot flood the
//     block.
//
// Two things sanitizeDisplay deliberately does NOT do. It does not
// neutralize "@X{...}" color directives: whether those render or strip
// is a decision the outer ansi layer makes based on whether color is
// enabled on the destination stream (see plans/cycle-detection-provenance.md,
// "Closing the color-directive hole"), and it is orthogonal to display
// safety - a directive left in the text is inert document content until
// something downstream chooses to interpret it. And it does not
// guarantee the result is inert against every possible downstream
// interpreter; it guarantees plain, single-line, valid-UTF-8 output
// containing no raw escape bytes, which is the contract this package's
// callers need.
func sanitizeDisplay(s string) string {
	s = ansi.StripEscapes(s)

	var b strings.Builder
	b.Grow(len(s))

	runeCount := 0
	truncated := false
	for _, r := range s {
		rep := displayReplacement(r)

		repRunes := utf8.RuneCountInString(rep)
		if runeCount+repRunes > cycleExprMaxRunes {
			truncated = true
			break
		}
		b.WriteString(rep)
		runeCount += repRunes
	}

	out := b.String()
	if truncated {
		out += "..."
	}
	return out
}

// sanitizeFilename makes one input filename safe to print. It carries
// sanitizeDisplay's guarantees unchanged - escape sequences stripped,
// control and line-breaking runes escaped, output bounded - and differs
// in one respect: it truncates from the LEFT, marking the result with a
// leading "...".
//
// The direction is what makes the result useful. A filename's
// distinguishing part is its tail, so cutting the tail off leaves a path
// nobody can open, and several inputs under one deep workspace root
// collapse to the same visible text, which defeats the numbered inputs
// list. The rune cap the spec sanctions is for expressions; applying it
// to a 209-character absolute path is what produced that.
//
// The tail is measured one whole replacement at a time, so the cut can
// never land inside a multi-character escape such as a literal
// LINE SEPARATOR rendered as \u2028.
//
// The input is scanned backwards and the scan stops as soon as the
// budget is spent, so a caller that hands a library a megabyte-long
// filename pays for the ~120 runes that get rendered rather than for the
// whole string. Only the surviving tail is ever expanded.
func sanitizeFilename(s string) string {
	s = ansi.StripEscapes(s)

	const marker = "..."
	budget := cycleExprMaxRunes - utf8.RuneCountInString(marker)

	// cut is the start of the longest suffix whose replacements fit in
	// budget; it is fixed the moment one more replacement would overrun,
	// and the scan then runs on only far enough to learn whether the
	// whole string fits under the untruncated cap.
	cut := 0
	cutFound := false
	total := 0
	for i := len(s); i > 0; {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		n := utf8.RuneCountInString(displayReplacement(r))
		if !cutFound && total+n > budget {
			cut, cutFound = i, true
		}
		total += n
		if total > cycleExprMaxRunes {
			return marker + expandReplacements(s[cut:])
		}
		i -= size
	}
	return expandReplacements(s)
}

// expandReplacements renders every rune of s through displayReplacement.
// Both callers bound s to roughly cycleExprMaxRunes runes first.
func expandReplacements(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteString(displayReplacement(r))
	}
	return b.String()
}

// displayReplacement returns how one rune renders inside sanitizeDisplay's
// output: itself, unchanged, for ordinary printable text, or an escaped
// form for anything that could break the single-line-output guarantee.
func displayReplacement(r rune) string {
	switch {
	case r == '\n':
		return `\n`
	case r == '\r':
		return `\r`
	case r == '\t':
		return `\t`
	case r == '\u0085', r == '\u2028', r == '\u2029':
		// NEL, LINE SEPARATOR, and PARAGRAPH SEPARATOR: like \n and \r,
		// terminals and editors treat these as line breaks, so leaving
		// them literal would violate the single-output-line guarantee.
		return fmt.Sprintf(`\u%04x`, r)
	case r < 0x20 || r == 0x7f:
		return fmt.Sprintf(`\x%02x`, r)
	default:
		return string(r)
	}
}

// CycleNode is one operator on a detected data-flow cycle.
type CycleNode struct {
	// Path is the operator call's canonical dotted path.
	Path string
	// Expr is the operator source as written.
	Expr string
	// Pos is where Expr was written. File may be set with Line == 0 when
	// only the file could be established, and both may be empty when the
	// node could not be attributed at all. A position is never invented.
	Pos interfaces.Position
}

// CycleError reports an operator data-flow cycle with enough provenance
// to fix it: the merge's inputs in order, and every operator on the
// cycle attributed to a file and line.
//
// Error() renders "cycle detected in operator data-flow graph" as its
// first line, then an indented block. The chain repeats its first node
// at the end, so the last two lines always name the two operators whose
// edge closes the loop.
type CycleError struct {
	// Inputs are the merge input names in merge order. May be empty, in
	// which case the "inputs:" block is omitted rather than printed
	// empty.
	Inputs []string
	// Nodes are the cycle's operators in reference order - each node's
	// expression references the next - rotated to start at the
	// lexicographically smallest path.
	Nodes []CycleNode
}

// Unwrap reports this as a dependency cycle, so
// errors.Is(err, ErrDependencyCycle) answers true for a cycle surfaced
// by the merge path as well as one surfaced by
// DependencyGraph.TopologicalSort.
func (e *CycleError) Unwrap() error { return ErrDependencyCycle }

// Error renders the cycle block. Every file-derived string passes
// through sanitizeDisplay, and the whole block is built with plain fmt:
// no part of it is color-processed, so document content cannot inject
// escapes or forge a line beginning " - $.".
func (e *CycleError) Error() string {
	var b strings.Builder
	b.WriteString("cycle detected in operator data-flow graph")

	for i, name := range e.Inputs {
		if i == 0 {
			b.WriteString("\n   inputs:")
		}
		fmt.Fprintf(&b, "\n     [%d] %s", i+1, sanitizeFilename(name))
	}

	if len(e.Nodes) == 0 {
		return b.String()
	}

	chain := make([]string, 0, len(e.Nodes)+1)
	for _, n := range e.Nodes {
		chain = append(chain, sanitizeDisplay(n.Path))
	}
	chain = append(chain, sanitizeDisplay(e.Nodes[0].Path))
	fmt.Fprintf(&b, "\n   cycle (%d %s): %s",
		len(e.Nodes), pluralNodes(len(e.Nodes)), strings.Join(chain, " -> "))

	// A cycle of three or more nodes repeats its first node, so the
	// closing edge is always the final two lines. A one- or two-node
	// cycle needs no duplicate: with at most two detail lines, the first
	// and last line already are the two ends of the (only) edge, so
	// appending a third line would repeat information rather than add
	// it - a self-cycle's single line already names both ends, and a
	// two-node cycle's two lines already do too (line 1 is where the
	// closing edge lands, line 2 is where it originates).
	lines := e.Nodes
	if len(e.Nodes) > 2 {
		lines = append(append([]CycleNode(nil), e.Nodes...), e.Nodes[0])
	}
	for _, n := range lines {
		fmt.Fprintf(&b, "\n     %s  %s: %s",
			cycleLocation(n.Pos), sanitizeDisplay(n.Path), sanitizeDisplay(n.Expr))
	}

	return b.String()
}

func pluralNodes(n int) string {
	if n == 1 {
		return "node"
	}
	return "nodes"
}

// cycleLocation renders one node's position, degrading rather than
// inventing: "file:line" when both are known, "file" when only the file
// could be established, and "<unknown>" when neither could.
func cycleLocation(p interfaces.Position) string {
	switch {
	case p.File != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d", sanitizeFilename(p.File), p.Line)
	case p.File != "":
		return sanitizeFilename(p.File)
	default:
		return "<unknown>"
	}
}
