package graft

import (
	"errors"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ErrOperatorFuncNilFn is returned by OperatorFunc.Run when Fn was never set.
var ErrOperatorFuncNilFn = errors.New("graft: OperatorFunc.Fn is nil")

// OperatorFunc adapts a plain function into the Operator interface, for
// custom operators that need only evaluation logic and neither
// initialization nor custom dependency discovery.
//
// Fn receives arguments unevaluated, exactly as any other Operator.Run
// implementation does: args are the raw *Expr call tree, not resolved
// values. Operators that want pre-evaluated arguments should call
// EvaluateOperatorArgs(ev, args) as the first line of Fn.
//
//	timestampOp := &graft.OperatorFunc{
//	    OpPhase: graft.EvalPhase,
//	    Fn: func(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
//	        return &graft.Response{Type: graft.Replace, Value: time.Now().Unix()}, nil
//	    },
//	}
//	engine.RegisterOperator("timestamp", timestampOp)
type OperatorFunc struct {
	// Fn implements Operator.Run. Required; Run returns
	// ErrOperatorFuncNilFn if Fn is nil.
	Fn func(ev *Evaluator, args []*Expr) (*Response, error)

	// OpPhase implements Operator.Phase. Defaults to MergePhase (the zero
	// value of OperatorPhase) if left unset; operators that read runtime
	// state (environment variables, sub-expression results) should set
	// this to EvalPhase explicitly.
	OpPhase OperatorPhase

	// DependsOn implements Operator.Dependencies. If nil, Dependencies
	// returns auto unchanged, matching the behavior of operators with no
	// additional dependencies of their own.
	DependsOn func(ev *Evaluator, args []*Expr, locs, auto []*tree.Cursor) []*tree.Cursor
}

// Setup implements Operator.Setup. OperatorFunc requires no initialization.
func (f *OperatorFunc) Setup() error {
	return nil
}

// Run implements Operator.Run by delegating to Fn.
func (f *OperatorFunc) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	if f.Fn == nil {
		return nil, ErrOperatorFuncNilFn
	}
	return f.Fn(ev, args)
}

// Dependencies implements Operator.Dependencies by delegating to DependsOn
// when set, or returning auto unchanged otherwise.
func (f *OperatorFunc) Dependencies(ev *Evaluator, args []*Expr, locs, auto []*tree.Cursor) []*tree.Cursor {
	if f.DependsOn != nil {
		return f.DependsOn(ev, args, locs, auto)
	}
	return auto
}

// Phase implements Operator.Phase by returning OpPhase.
func (f *OperatorFunc) Phase() OperatorPhase {
	return f.OpPhase
}
