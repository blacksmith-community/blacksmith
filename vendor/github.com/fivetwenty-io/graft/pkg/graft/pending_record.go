package graft

import (
	"github.com/fivetwenty-io/graft/internal/oploc"
	"github.com/fivetwenty-io/graft/internal/pending"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ErrNotEvaluated is the error that every (( grab )) of an operator
// that has not run yet matches with errors.Is. A grab fails this way
// when the value it would hand on holds an operator of the same phase
// that has not run, and the dependency graph could not order the grab
// after it, so the grab never passes that operator's text on as data.
// The match holds through the errors that Merge(...).Execute and
// Evaluate return, and through the error list a merge with several
// failures returns, so a caller can tell this failure apart from every
// other.
var ErrNotEvaluated = pending.ErrNotEvaluated

//nolint:gochecknoinits // the operators package reads the pending record and the path index through these hooks from its first call
func init() {
	pending.SetLookup(func(ev any) *pending.Record {
		if e, ok := ev.(*Evaluator); ok && e != nil {
			return e.pending
		}
		return nil
	})
	pending.SetOuterArgs(func(ev any, args any) bool {
		e, ok := ev.(*Evaluator)
		if !ok || e == nil || e.outerCall == nil {
			return false
		}
		a, ok := args.([]*Expr)
		outer := e.outerCall.args
		return ok && len(a) > 0 && len(a) == len(outer) && &a[0] == &outer[0]
	})
	oploc.SetLookup(func(ev any) *oploc.Index {
		if e, ok := ev.(*Evaluator); ok && e != nil {
			return e.locIndex
		}
		return nil
	})
}

// startPending gives ev a record of ops as operators that have not run
// yet, and returns a function that puts back the record ev held before.
// RunOp marks each op done as it runs, so while a phase runs the record
// holds exactly the operators still to run.
func (ev *Evaluator) startPending(ops []*Opcall) func() {
	rec := pending.NewRecord()
	for _, op := range ops {
		if at := pendingPath(op); at != nil && op.where != nil {
			rec.Add(at.Nodes, op.where.String(), parentOf(ev.Tree, at))
		}
	}
	saved := ev.pending
	ev.pending = rec
	return func() { ev.pending = saved }
}

// parentOf returns the container that holds the value at the path at in
// root, or nil when at is the root or its parent does not resolve.
func parentOf(root map[string]interface{}, at *tree.Cursor) interface{} {
	if len(at.Nodes) == 0 {
		return nil
	}
	parent := &tree.Cursor{Nodes: at.Nodes[:len(at.Nodes)-1]}
	o, err := parent.Resolve(root)
	if err != nil {
		return nil
	}
	return o
}

// donePending records on ev that op has run, whether or not it
// succeeded.
func (ev *Evaluator) donePending(op *Opcall) {
	if at := pendingPath(op); at != nil {
		ev.pending.Done(at.Nodes)
	}
}

// pendingPath returns the path op is recorded under, which is its
// canonical path, or its own path when it has none.
func pendingPath(op *Opcall) *tree.Cursor {
	if op == nil {
		return nil
	}
	if op.canonical != nil {
		return op.canonical
	}
	return op.where
}
