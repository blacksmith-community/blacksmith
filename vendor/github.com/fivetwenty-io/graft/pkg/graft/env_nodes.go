package graft

import (
	"os"
	"slices"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// SubstituteEnvNodes replaces each node of the form $NAME with the value
// of the environment variable NAME when it is set and not empty. A lone
// "$" and a node whose variable is unset or empty stay as written. It
// returns a new slice and never changes nodes.
func SubstituteEnvNodes(nodes []string) []string {
	out := make([]string, len(nodes))
	for i, node := range nodes {
		out[i] = node
		if len(node) > 1 && node[0] == '$' {
			if val := os.Getenv(node[1:]); val != "" {
				out[i] = val
			}
		}
	}
	return out
}

// substituteReferenceEnv replaces the $NAME nodes of a reference's
// cursor with their values when the reference is parsed, so the
// dependency graph orders the operator after whatever it reads.
// Operators never expand a node again, so a value that itself starts
// with '$' is a key. A bracket node that a variable replaced is a key
// from then on, not a path to resolve, so its flag in bracketed is
// cleared. It returns the nodes as written when any node changed, and
// nil when none did, so the Expr can write the path back out as written.
func substituteReferenceEnv(cursor *tree.Cursor, bracketed []bool) []string {
	out := SubstituteEnvNodes(cursor.Nodes)
	if slices.Equal(out, cursor.Nodes) {
		return nil
	}
	for i := range out {
		if out[i] != cursor.Nodes[i] && i < len(bracketed) {
			bracketed[i] = false
		}
	}
	written := cursor.Nodes
	cursor.Nodes = out
	return written
}
