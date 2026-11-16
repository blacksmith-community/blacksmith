package operators

import (
	"github.com/fivetwenty-io/graft/internal/oploc"
	"github.com/fivetwenty-io/graft/internal/prunekeys"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// locsUnder returns every loc below the path that ref names. An
// operator that reads the whole value at a path depends on each of
// them, so the operators inside that value run before it reads it, and
// a read of the operator's own ancestor closes a cycle in the data-flow
// graph instead of handing back operator text. The path itself needs
// no entry here, because the automatic dependencies already hold it.
// Both paths are compared in canonical form, through the index the
// evaluator built for locs, so each call costs the locs it returns
// rather than every loc in the phase.
func locsUnder(ev *Evaluator, ref *tree.Cursor, locs []*tree.Cursor) []*tree.Cursor {
	if ref == nil {
		return nil
	}
	canon, err := ref.Canonical(ev.Tree)
	if err != nil || len(canon.Nodes) == 0 {
		return nil
	}
	return oploc.For(ev, locs, ev.Tree).Under(canon.Nodes, canon.Nodes)
}

// readDeps returns locsUnder for each reference whose value arg can
// hand to its operator whole: arg itself when it is a reference, and
// the operands of a || chain in arg. With fallbacks false, only the
// left operand of each || is followed, for an operator that refuses a
// map or a list either way and only needs the cycle to be reported. A
// nested operator call is left to that operator's own dependencies.
func readDeps(ev *Evaluator, arg *Expr, locs []*tree.Cursor, fallbacks bool) []*tree.Cursor {
	if arg == nil {
		return nil
	}
	switch arg.Type {
	case Reference:
		return locsUnder(ev, arg.Reference, locs)
	case LogicalOr:
		deps := readDeps(ev, arg.Left, locs, fallbacks)
		if fallbacks {
			deps = append(deps, readDeps(ev, arg.Right, locs, fallbacks)...)
		}
		return deps
	default:
		return nil
	}
}

// prunedKeyHolders returns the path of each operator that sits above a
// key that will be pruned, for an operator that leaves such keys out of
// what it reads. A key given to --prune can be reached only through
// what such an operator hands back, as x.bb is with x: (( grab m )), and
// until that operator runs the key is not found in the map that holds
// it. A reader that runs after the operator finds the key, so it leaves
// the key out in either mode and in any order, as the output does. The
// answer is the same for every reader in a phase, so the index of the
// phase's operators finds it once.
func prunedKeyHolders(ev *Evaluator, locs []*tree.Cursor) []*tree.Cursor {
	if len(locs) == 0 {
		return nil
	}
	idx := oploc.For(ev, locs, ev.Tree)
	return idx.Holders(func() []*tree.Cursor {
		return findPrunedKeyHolders(ev, idx)
	})
}

// findPrunedKeyHolders does the work of prunedKeyHolders against idx.
func findPrunedKeyHolders(ev *Evaluator, idx *oploc.Index) []*tree.Cursor {
	var out []*tree.Cursor
	for _, path := range prunekeys.For(ev) {
		c, err := tree.ParseCursor(path)
		if err != nil {
			continue
		}
		for n := 1; n < len(c.Nodes); n++ {
			above := &tree.Cursor{Nodes: c.Nodes[:n]}
			canon := oploc.Canonical(above, ev.Tree)
			if idx.At(canon.Nodes) {
				out = append(out, canon)
				break
			}
		}
	}
	return out
}
