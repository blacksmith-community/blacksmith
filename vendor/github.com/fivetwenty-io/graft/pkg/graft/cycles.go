package graft

import (
	"sort"
	"strings"
)

// findCycles returns every elementary cycle the white/gray/black walk
// reaches, each as canonical paths in dependency-then-dependent order
// (path[i+1] depends on path[i] running first, and path[0] depends on
// the last element, closing the cycle; the closing edge is not repeated
// in the slice).
//
// This is the single cycle-walking implementation in the package.
// DependencyGraph.DetectCycles is a thin wrapper over it, and
// extractCycle selects and reorients one of its results rather than
// walking the graph a second time, so the two call sites cannot drift.
//
// The same cycle may be reported more than once if more than one node on
// it is also reachable from outside the cycle.
func findCycles(dependents map[string][]string, order []string) [][]string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(order))
	var cycles [][]string
	var stack []string

	var visit func(path string)
	visit = func(path string) {
		color[path] = gray
		stack = append(stack, path)

		for _, next := range dependents[path] {
			switch color[next] {
			case white:
				visit(next)
			case gray:
				// next is still on the current DFS stack: the slice from
				// next's position to the top is one elementary cycle, in
				// dependency-then-dependent order.
				if start := indexOfString(stack, next); start >= 0 {
					cycle := append([]string(nil), stack[start:]...)
					cycles = append(cycles, cycle)
				}
			case black:
				// next was already fully explored via a different path;
				// no new cycle through here.
			}
		}

		stack = stack[:len(stack)-1]
		color[path] = black
	}

	for _, path := range order {
		if color[path] == white {
			visit(path)
		}
	}

	return cycles
}

// extractCycle returns one cycle in reference order - the direction a
// user reads, where each node's expression references the next - rotated
// to start at its lexicographically smallest path. It returns nil if the
// graph is acyclic.
//
// The result is deterministic but not necessarily the shortest cycle in
// the graph: a single DFS reports the back-edge cycles for one traversal
// order, which is a subset of all elementary cycles. Enumerating every
// one would need Johnson's algorithm, which this feature does not need.
func extractCycle(dependents map[string][]string, order []string) []string {
	cycles := findCycles(dependents, order)
	if len(cycles) == 0 {
		return nil
	}

	best := cycles[0]
	for _, c := range cycles[1:] {
		if lessCycle(c, best) {
			best = c
		}
	}

	// Reverse first: findCycles reports dependency-then-dependent order
	// (edges[i][0] must run before edges[i][1], evaluator.go's
	// dataFlowGraph.edges), while rendering reads the opposite way.
	// Rotation preserves orientation and so cannot substitute for this.
	rev := make([]string, len(best))
	for i, p := range best {
		rev[len(best)-1-i] = p
	}

	// Rotate second. Reversing a rotated slice does not preserve the
	// smallest-first property, so the order of these two steps is
	// load-bearing.
	smallest := 0
	for i, p := range rev {
		if p < rev[smallest] {
			smallest = i
		}
	}
	out := make([]string, 0, len(rev))
	out = append(out, rev[smallest:]...)
	out = append(out, rev[:smallest]...)
	return out
}

// lessCycle orders candidate cycles: shortest first, then by content, so
// repeated runs over the same graph always report the same cycle.
func lessCycle(a, b []string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return strings.Join(a, "\x00") < strings.Join(b, "\x00")
}

// dependentsFromEdges projects the evaluator's *Opcall edge list onto the
// canonical-path adjacency findCycles consumes. buildDependencyGraph can
// report the same (dependency, dependent) pair more than once - an
// operator whose two arguments resolve to the same producing opcall - so
// pairs are deduplicated, and both the adjacency lists and the returned
// visit order are sorted, making extraction deterministic.
func dependentsFromEdges(edges [][]*Opcall) (map[string][]string, []string) {
	dependents := make(map[string][]string)
	nodes := make(map[string]bool)
	seen := make(map[string]bool)

	for _, pair := range edges {
		if len(pair) != 2 || pair[0] == nil || pair[1] == nil {
			continue
		}
		if pair[0].canonical == nil || pair[1].canonical == nil {
			continue
		}
		dep := pair[0].canonical.String()
		dependent := pair[1].canonical.String()
		nodes[dep] = true
		nodes[dependent] = true

		key := dep + "\x00" + dependent
		if seen[key] {
			continue
		}
		seen[key] = true
		dependents[dep] = append(dependents[dep], dependent)
	}

	for path := range dependents {
		sort.Strings(dependents[path])
	}

	order := make([]string, 0, len(nodes))
	for path := range nodes {
		order = append(order, path)
	}
	sort.Strings(order)

	return dependents, order
}
