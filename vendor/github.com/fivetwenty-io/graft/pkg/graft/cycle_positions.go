package graft

import (
	"strings"

	"github.com/fivetwenty-io/graft/internal/srcpos"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
)

// sourceIndexes holds one position index per merge input, in merge
// order. It is built lazily, only after a cycle has been detected;
// nothing here runs on a successful merge.
type sourceIndexes struct {
	names   []string
	indexes []*srcpos.Index // parallel to names; nil for an opaque input
	// allIndexed is false when any input is opaque. An opaque input
	// cannot be ruled out as a node's origin, which disables the
	// expression fallback for the whole merge.
	allIndexed bool
}

// buildSourceIndexes parses each operator-bearing input into a position
// index.
//
// Each input is put through the same pure rewrites ParseYAML applies, in
// ParseYAML's own order, so paths and lines describe the document the
// merge actually parsed. Control-flow expansion is deliberately not
// reproduced: the expander rewrites a document wholesale, so no mapping
// from expanded text back to original lines exists, and the CLI marks
// such an input opaque instead.
func buildSourceIndexes(refs []SourceRef) *sourceIndexes {
	si := &sourceIndexes{allIndexed: true}
	for _, r := range refs {
		si.names = append(si.names, r.Name)
		if r.Opaque {
			si.indexes = append(si.indexes, nil)
			si.allIndexed = false
			continue
		}
		if len(r.Bytes) == 0 {
			// A complete, legitimately empty index: this input has no
			// operator text, so it can be ruled out as a node's origin.
			si.indexes = append(si.indexes, srcpos.Build(r.Name, nil))
			continue
		}
		data, _ := yamlprep.Prepare(r.Bytes)
		si.indexes = append(si.indexes, srcpos.Build(r.Name, data))
	}
	return si
}

// resolve locates one operator call in the merge's inputs.
//
// Stage one tries the operator's name-keyed path against every input in
// reverse merge order, then its numeric path the same way; first
// verified hit wins. A hit counts only when the indexed expression is
// the operator's own text:
// the same path can name a different operator in a different input - a
// numeric list slot in particular, since registerOpcall (evaluator.go)
// sets canonical to the literal numeric path and where to the
// name-resolved one, and list indices drift across merges while name
// keys do not. Verifying the expression is what keeps the printed line
// and the printed expression describing the same piece of source.
//
// Stage two runs only when stage one found nothing. It never invents a
// position, and never names a file unless that file is the only
// candidate.
func (si *sourceIndexes) resolve(op *Opcall) interfaces.Position {
	if op == nil {
		return interfaces.Position{}
	}

	expr := strings.TrimSpace(op.src)

	if pos, ok := si.resolveByPath(candidatePaths(op), expr); ok {
		return pos
	}

	if pos, ok := si.resolveByExpr(expr); ok {
		return pos
	}

	// With exactly one input there is no rival candidate, so the file
	// can be named even though the line cannot.
	if len(si.names) == 1 {
		return interfaces.Position{File: si.names[0]}
	}

	// In a multi-input merge, a node whose path is indexed nowhere
	// cannot be attributed to a file at all. A complete index that lacks
	// a path does not prove the operator is absent from that file: list
	// index drift is precisely the case where it is present under a
	// different path. Naming a file on that basis would be a guess.
	return interfaces.Position{}
}

// candidatePaths returns the paths worth trying for op, best first.
// registerOpcall (evaluator.go) sets canonical to the literal numeric
// path and where to the name-resolved one.
//
// The name-keyed path comes first, and the order matters. where is the
// node's path in the MERGED tree, so its value is by construction
// whatever the last input to write that path wrote, and resolveByPath
// walks the inputs in reverse merge order, reaching that input first.
// The numeric path carries no such guarantee: list indices drift across
// merges, so a numeric path can name one operator in the merged tree
// and a different one in the file that supplied it.
func candidatePaths(op *Opcall) []string {
	candidates := make([]string, 0, 2)
	if op.where != nil {
		candidates = append(candidates, op.where.String())
	}
	if op.canonical != nil {
		if c := op.canonical.String(); len(candidates) == 0 || c != candidates[0] {
			candidates = append(candidates, c)
		}
	}
	return candidates
}

// resolveByPath is stage one: try each candidate path, in order, against
// every input in reverse merge order, and accept the first hit whose
// indexed expression is expr.
//
// The candidate is the OUTER loop, so the better path is exhausted
// across all inputs before the weaker one is tried against any. Were the
// input the outer loop, the last input to be walked first would get to
// answer on its numeric path before any earlier input was consulted on
// the name-keyed one, and a list whose indices drifted would be
// attributed to whichever file happens to hold that index now.
//
// A path hit carrying different text is not the operator being resolved,
// so the search continues through the remaining inputs and the remaining
// candidates rather than stopping. The cross-check and the loop order do
// different jobs and both are needed: the cross-check alone cannot
// discriminate when two inputs carry byte-identical text, and the loop
// order alone would accept an unverified hit. Only when no combination
// verifies does stage one give up.
func (si *sourceIndexes) resolveByPath(candidates []string, expr string) (interfaces.Position, bool) {
	for _, p := range candidates {
		for i := len(si.indexes) - 1; i >= 0; i-- {
			idx := si.indexes[i]
			if idx == nil {
				continue
			}
			e, ok := idx.Lookup(p)
			if !ok || e.Expr != expr {
				continue
			}
			return e.Pos, true
		}
	}
	return interfaces.Position{}, false
}

// resolveByExpr is stage two, run only when stage one found nothing.
// Uniqueness is computed over the union of all indexes, never within one
// file: scoping it per file lets one file's unique copy of an expression
// claim a node that lives in another, and duplicate short expressions
// across files are routine. When any input is opaque the rung is skipped
// entirely, because an unindexed input cannot be ruled out as the node's
// origin and a unique match elsewhere would confidently cite the wrong
// file.
func (si *sourceIndexes) resolveByExpr(expr string) (interfaces.Position, bool) {
	if !si.allIndexed {
		return interfaces.Position{}, false
	}

	total := 0
	var hit srcpos.Entry
	for _, idx := range si.indexes {
		if idx == nil {
			continue
		}
		total += idx.CountExpr(expr)
		if e, ok := idx.ByExpr(expr); ok {
			hit = e
		}
	}
	if total != 1 {
		return interfaces.Position{}, false
	}
	return hit.Pos, true
}

// buildCycleError assembles the reported error from the extracted cycle
// paths and the operator set still in play. It is called only from the
// cycle branch of kahnSort, so index construction stays off the success
// path.
func buildCycleError(refs []SourceRef, paths []string, all map[string]*Opcall) *CycleError {
	ce := &CycleError{}
	for _, r := range refs {
		ce.Inputs = append(ce.Inputs, r.Name)
	}

	si := buildSourceIndexes(refs)
	for _, p := range paths {
		n := CycleNode{Path: p}
		if op, ok := all[p]; ok && op != nil {
			n.Expr = strings.TrimSpace(op.src)
			n.Pos = si.resolve(op)
		}
		ce.Nodes = append(ce.Nodes, n)
	}
	return ce
}
