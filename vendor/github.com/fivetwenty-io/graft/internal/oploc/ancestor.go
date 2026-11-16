package oploc

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// RefuseAncestor returns a cycle error when here, the path of a running
// operator, sits under target, the path that operator reads and hands on
// whole. The value it would hand on is the container that holds the
// operator, so the document would end up holding itself. Both paths are
// compared in canonical form, so a name or predicate segment that picks
// the same list item as an index is caught too, while the error names
// both paths as they are spelled. A target that does not resolve against
// root is left to the caller's not-found error.
func RefuseAncestor(here, target *tree.Cursor, root interface{}) error {
	if here == nil || target == nil {
		return nil
	}
	canon, err := target.Canonical(root)
	if err != nil {
		return nil //nolint:nilerr // an unresolved target gets the caller's not-found error
	}
	if !Canonical(here, root).Under(canon) {
		return nil
	}
	return cycleError(here, target)
}

// RefuseSelfHolding returns a cycle error when val, the value an
// operator at here is about to write, would make one of here's
// ancestors in root hold itself: val is that ancestor's map, or a list
// whose entries include the ancestor's entry on the way to here. A list
// cut from an ancestor list counts only when its span covers that
// entry. It is the evaluator's last guard for any operator whose read
// RefuseAncestor did not see.
func RefuseSelfHolding(here *tree.Cursor, val, root interface{}) error {
	if here == nil {
		return nil
	}
	val, _ = literal.Unwrap(val)
	switch val.(type) {
	case map[string]interface{}, []interface{}:
	default:
		return nil
	}
	nodes := Canonical(here, root).Nodes
	if len(nodes) != len(here.Nodes) {
		nodes = here.Nodes
	}
	for n := len(nodes) - 1; n > 0; n-- {
		o, err := (&tree.Cursor{Nodes: nodes[:n]}).Resolve(root)
		if err != nil {
			continue
		}
		if holds(val, o, nodes[n]) {
			return cycleError(here, &tree.Cursor{Nodes: here.Nodes[:n]})
		}
	}
	return nil
}

// holds reports whether val holds the entry key of the container o: val
// is o's own map, or a non-empty list whose span of entries covers the
// entry key of the list o, read through a literal wrapper.
func holds(val, o interface{}, key string) bool {
	o, _ = literal.Unwrap(o)
	switch c := val.(type) {
	case map[string]interface{}:
		m, ok := o.(map[string]interface{})
		return ok && c != nil && m != nil &&
			reflect.ValueOf(c).Pointer() == reflect.ValueOf(m).Pointer()
	case []interface{}:
		l, ok := o.([]interface{})
		if !ok || len(c) == 0 {
			return false
		}
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(l) {
			return false
		}
		slot := reflect.ValueOf(&l[i]).Pointer()
		lo := reflect.ValueOf(&c[0]).Pointer()
		hi := reflect.ValueOf(&c[len(c)-1]).Pointer()
		return lo <= slot && slot <= hi
	}
	return false
}

// cycleError is the error both guards return. It names the two paths and
// quotes no value. It says "reads" because a grab, a ternary, and a bare
// || reference all reach it.
func cycleError(here, target *tree.Cursor) error {
	return fmt.Errorf("cycle detected: $.%s reads $.%s, which contains it", here, target)
}
