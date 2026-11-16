package operators

import (
	"strings"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/internal/pending"
	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// pastGrab says what resolvePastGrabs found at a path.
type pastGrab int

const (
	// pastFound means the path names a value.
	pastFound pastGrab = iota
	// pastMissing means the path names nothing.
	pastMissing
	// pastUnknown means the path passes through an operator whose
	// value cannot be known before it runs.
	pastUnknown
)

// maxGrabHops bounds the grabs that resolvePastGrabs reads through
// for one path, so grabs that name each other end the walk.
const maxGrabHops = 64

// resolvePastGrabs returns the value at the path nodes as the tree
// holds it once each grab on the way has run. A grab hands on the value
// its path names, not a copy, so the value a grab that has not run yet
// will hand on is found at its path now. A grab of two or more paths
// hands on one list of the entries they name. Two operators can each
// wait for the other, and then one of them runs before a grab above a
// key to prune, so a reader of a map finds the map that holds the key
// whether or not that grab has run. The answer is pastUnknown when the
// path passes through any other operator that has not run, or a grab
// whose path an operator or an environment variable computes.
func resolvePastGrabs(ev *Evaluator, nodes []string) (interface{}, pastGrab) {
	return walkPastGrabs(ev, ev.Tree, nodes, maxGrabHops)
}

// walkPastGrabs is resolvePastGrabs from the value cur, with hops
// grabs left to read through.
func walkPastGrabs(ev *Evaluator, cur interface{}, nodes []string, hops int) (interface{}, pastGrab) {
	rec := pending.For(ev)
	for _, k := range nodes {
		next, err := (&tree.Cursor{Nodes: []string{k}}).Resolve(cur)
		if err != nil {
			return nil, pastMissing
		}
		if src, ok := next.(string); ok {
			if _, waiting := rec.In(cur, k); waiting {
				if hops == 0 {
					return nil, pastUnknown
				}
				val, found := grabValue(ev, src, hops-1)
				if found != pastFound {
					return nil, found
				}
				next = val
			}
		}
		cur = next
	}
	return cur, pastFound
}

// grabValue returns the value that the operator call src, which has not
// run yet, will hand on, when it is a grab.
func grabValue(ev *Evaluator, src string, hops int) (interface{}, pastGrab) {
	op, err := graft.ParseOpcallForEngine(graft.EngineOf(ev), EvalPhase, src)
	if err != nil || op == nil {
		return nil, pastUnknown
	}
	if _, ok := op.Operator().(GrabOperator); !ok {
		return nil, pastUnknown
	}
	args := op.Args()
	if len(args) == 1 {
		return grabArgValue(ev, args[0], hops)
	}
	flat := []interface{}{}
	for _, arg := range args {
		val, found := grabArgValue(ev, arg, hops)
		if found != pastFound {
			return nil, pastUnknown
		}
		switch v := val.(type) {
		case []interface{}:
			flat = append(flat, v...)
		case literal.Value:
			items, ok := v.Value().([]interface{})
			if !ok {
				flat = append(flat, val)
				continue
			}
			for _, item := range items {
				flat = append(flat, literal.Wrap(item))
			}
		default:
			flat = append(flat, val)
		}
	}
	return flat, pastFound
}

// grabArgValue returns the value a grab reads for the argument arg: the
// value at a path, the first side of a || that names one, or a literal.
func grabArgValue(ev *Evaluator, arg *Expr, hops int) (interface{}, pastGrab) {
	if arg == nil {
		return nil, pastUnknown
	}
	switch arg.Type {
	case Reference:
		if arg.Reference == nil {
			return nil, pastUnknown
		}
		for _, n := range arg.Reference.Nodes {
			if strings.Contains(n, "[") {
				return nil, pastUnknown
			}
		}
		return walkPastGrabs(ev, ev.Tree, arg.Reference.Nodes, hops)
	case LogicalOr:
		val, found := grabArgValue(ev, arg.Left, hops)
		if found != pastMissing {
			return val, found
		}
		return grabArgValue(ev, arg.Right, hops)
	case Literal:
		return arg.Literal, pastFound
	default:
		return nil, pastUnknown
	}
}
