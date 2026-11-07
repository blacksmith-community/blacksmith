package operators

import (
	"fmt"
	"strconv"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/oploc"
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

// bracketDependencies returns the paths that a grab argument with a
// dynamic bracket key, such as "tgt[k]", depends on beyond the path as
// written, which reads each key's name as a literal node. Each bracket
// key's own path is a hard dependency, so the grab runs after whatever
// computes the key. When every key already holds its value, the path
// with those values in place is another hard dependency, so the grab
// also runs after whatever computes the value it reaches.
//
// When an operator still has to compute a key, the target cannot be
// known yet. Every operator under the path before that key, other than
// the grab itself, is then a soft dependency: the evaluator orders the
// grab after it unless that would close a cycle. When the target is
// known, every operator under it is a soft dependency instead, as it is
// for a path with no bracket key. A path that starts
// with such a key, as "$[k]" does, has the whole document before it. A
// LogicalOr argument contributes the dependencies of both sides,
// because grab may try either.
func bracketDependencies(ev *Evaluator, arg *Expr, locs []*tree.Cursor) (hard, soft []*tree.Cursor) {
	if arg == nil {
		return nil, nil
	}
	if arg.Type == LogicalOr {
		h1, s1 := bracketDependencies(ev, arg.Left, locs)
		h2, s2 := bracketDependencies(ev, arg.Right, locs)
		return append(h1, h2...), append(s1, s2...)
	}
	if arg.Type != Reference || arg.Reference == nil || !hasDynamicBracket(arg.Reference.Nodes, arg.BracketedNodes) {
		return nil, nil
	}
	return referenceBracketDependencies(ev, arg.Reference.Nodes, arg.BracketedNodes, locs)
}

// referenceBracketDependencies is bracketDependencies for one reference,
// given as its nodes and the flags that mark which of them were written
// in brackets. A key that cannot be read, or that comes from a literal,
// leaves the target unknown and adds no soft dependencies, because the
// grab fails on that key when it runs.
func referenceBracketDependencies(ev *Evaluator, nodes []string, bracketed []bool, locs []*tree.Cursor) (hard, soft []*tree.Cursor) {
	target := make([]string, 0, len(nodes))
	known := true
	for i, node := range nodes {
		if i >= len(bracketed) || !bracketed[i] || !isDynamicBracketNode(node) {
			target = append(target, node)
			continue
		}
		key, err := tree.ParseCursor(node)
		if err != nil {
			return hard, soft
		}
		hard = append(hard, key)
		if !known {
			continue
		}
		name, state := bracketKeyNow(ev, node, key, locs)
		switch state {
		case keyHeld:
			target = append(target, name)
		case keyComputed:
			soft = []*tree.Cursor{canonicalPath(ev, &tree.Cursor{Nodes: append([]string(nil), target...)})}
			known = false
		case keyUnusable:
			known = false
		}
	}
	if known {
		hard = append(hard, &tree.Cursor{Nodes: target})
		soft = []*tree.Cursor{canonicalPath(ev, &tree.Cursor{Nodes: append([]string(nil), target...)})}
	}
	return hard, soft
}

// bracketKeyState says what a bracket key's path holds while the
// dependency graph is built.
type bracketKeyState int

const (
	// keyHeld means the path holds the key's value already.
	keyHeld bracketKeyState = iota
	// keyComputed means an operator computes the key's value.
	keyComputed
	// keyUnusable means the path cannot name a key, because it does not
	// resolve, comes from a literal, or holds no scalar.
	keyUnusable
)

// bracketKeyNow reports what the bracket key node, parsed as key, holds
// in ev.Tree before any operator runs, and the key's name when the tree
// holds it already. A key at or under the path of an operator in locs
// is still to be computed.
func bracketKeyNow(ev *Evaluator, node string, key *tree.Cursor, locs []*tree.Cursor) (string, bracketKeyState) {
	if oploc.For(ev, locs, ev.Tree).AtOrAbove(canonicalPath(ev, key).Nodes) {
		return "", keyComputed
	}
	val, err := key.ResolveLiteral(ev.Tree)
	if err != nil {
		return "", keyUnusable
	}
	plain, fromLiteral := literal.StripReport(val)
	if fromLiteral {
		return "", keyUnusable
	}
	name, err := bracketKeyToNode(node, plain)
	if err != nil {
		return "", keyUnusable
	}
	return name, keyHeld
}

// canonicalPath returns c with list entries named by index, the form
// the dependency graph keys operators by, or c itself when it does not
// resolve against ev.Tree.
func canonicalPath(ev *Evaluator, c *tree.Cursor) *tree.Cursor {
	return oploc.Canonical(c, ev.Tree)
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
// against ev.Tree and returns the map/list key its value stands for. A
// literal never names a key in the path grab reads, so a value that is a
// literal, sits inside one, or holds one is refused.
func resolveBracketNode(node string, ev *Evaluator) (string, error) {
	cursor, err := tree.ParseCursor(node)
	if err != nil {
		return "", fmt.Errorf("invalid bracketed key reference %q: %w", node, err)
	}

	val, err := cursor.ResolveLiteral(ev.Tree)
	if err != nil {
		return "", fmt.Errorf("unable to resolve bracketed key reference %q: %w", node, err)
	}

	plain, fromLiteral := literal.StripReport(val)
	if fromLiteral {
		return "", literal.Refuse("grab", "a key in a path")
	}
	return bracketKeyToNode(node, plain)
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
