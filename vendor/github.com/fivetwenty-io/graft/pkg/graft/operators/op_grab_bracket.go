package operators

import (
	"fmt"
	"strconv"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// resolveGrabDynamicBrackets substitutes bracket-notation segments of a
// grab reference (e.g. "key[lookup]") with the scalar value found by
// resolving the bracketed segment as its own path against the document
// tree, mirroring spruce's dynamic bracket-key resolution for
// (( grab )). It mutates arg.Reference.Nodes in place.
//
// Three kinds of bracketed segments are left untouched, matching spruce:
//   - segments that are empty (should not occur after parsing, guarded
//     defensively)
//   - segments starting with '$', which are environment-variable
//     references resolved separately by the existing $VAR expansion path
//   - segments that parse as an integer, which are array indices rather
//     than dynamic key references
//
// Any other bracketed segment is resolved as a path against ev.Tree; the
// result must be a scalar (string, int, int64, float64, or bool) or an
// error is returned, since a bracketed key must ultimately resolve to a
// single map/list key.
func resolveGrabDynamicBrackets(arg *Expr, ev *Evaluator) error {
	if arg == nil || arg.Reference == nil {
		return nil
	}

	nodes := arg.Reference.Nodes
	bracketed := arg.BracketedNodes

	if !hasDynamicBracket(nodes, bracketed) {
		return nil
	}

	resolved := make([]string, len(nodes))
	copy(resolved, nodes)

	for i, node := range nodes {
		if i >= len(bracketed) || !bracketed[i] || !isDynamicBracketNode(node) {
			continue
		}

		key, err := resolveBracketNode(node, ev)
		if err != nil {
			return err
		}
		resolved[i] = key
	}

	arg.Reference.Nodes = resolved

	// Clear the flags so re-invocation (e.g. Dependencies followed by Run
	// against the same *Expr) treats the already-substituted nodes as
	// literal keys instead of re-resolving them.
	for i := range bracketed {
		bracketed[i] = false
	}

	return nil
}

// hasDynamicBracket reports whether any node is a bracketed segment that
// needs resolving, which is what makes the substitution pass below worth
// running at all. bracketed is indexed in parallel with nodes but is not
// guaranteed to be as long.
func hasDynamicBracket(nodes []string, bracketed []bool) bool {
	for i, b := range bracketed {
		if b && i < len(nodes) && isDynamicBracketNode(nodes[i]) {
			return true
		}
	}
	return false
}

// resolveBracketNode resolves one dynamic bracketed node as a path
// against ev.Tree and returns the map/list key its value stands for.
func resolveBracketNode(node string, ev *Evaluator) (string, error) {
	cursor, err := tree.ParseCursor(node)
	if err != nil {
		return "", fmt.Errorf("invalid bracketed key reference %q: %w", node, err)
	}

	val, err := cursor.Resolve(ev.Tree)
	if err != nil {
		return "", fmt.Errorf("unable to resolve bracketed key reference %q: %w", node, err)
	}

	return bracketKeyToNode(node, val)
}

// isDynamicBracketNode reports whether a bracketed path node should be
// treated as a dynamic key reference: non-empty, not an environment
// variable ("$..."), not a plain integer (an array index), and not a list
// predicate ("field=value", e.g. "name=primary" in
// "servers[name=primary]"). Predicate segments are left as literal node
// text so tree.Resolve's predicate matcher can run instead of this
// function trying (and failing) to resolve them as their own path - the
// regression guard: a container that turns out to be
// a map at resolve time still gets a plain key lookup on the literal
// "field=value" string, so a genuine map key containing "=" is unaffected.
func isDynamicBracketNode(node string) bool {
	if node == "" || node[0] == '$' {
		return false
	}
	if _, err := strconv.ParseInt(node, 10, 64); err == nil {
		return false
	}
	if tree.IsPredicateSegment(node) {
		return false
	}
	return true
}

// bracketKeyToNode converts a resolved bracketed-reference value into its
// string node representation, or returns an error if the value is not a
// scalar.
func bracketKeyToNode(node string, val interface{}) (string, error) {
	switch v := val.(type) {
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return "", fmt.Errorf("bracketed key reference %q resolved to %T; expected a scalar (string, int, float, or bool)", node, val)
	}
}
