package operators

import (
	"fmt"
	"os"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// RawEnvOperator retrieves an environment variable as a raw string without
// YAML unmarshaling: (( raw_env $PORT )) yields the string "8080" where
// (( grab $PORT )) would coerce it to the integer 8080. Ported from spruce's
// op_raw_env.go; its errors are deliberately plain fmt strings without ansi
// markup, matching spruce byte for byte.
type RawEnvOperator struct{}

// Setup ...
func (RawEnvOperator) Setup() error {
	return nil
}

// Phase ...
func (RawEnvOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (RawEnvOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run ...
func (RawEnvOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( raw_env ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( raw_env ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("raw_env operator requires exactly one argument")
	}

	// The leftmost leaf of the (possibly ||-chained) argument must be an
	// environment variable; fallback branches after || may be anything.
	leftmost := args[0]
	for leftmost.Type == LogicalOr {
		leftmost = leftmost.Left
	}
	if leftmost.Type != EnvVar {
		return nil, fmt.Errorf("raw_env operator only accepts environment variable arguments")
	}

	v, err := resolveRawEnv(ev, args[0])
	if err != nil {
		DEBUG("  %s", err)
		return nil, err
	}

	return &Response{Type: Replace, Value: v}, nil
}

// resolveRawEnv mirrors spruce's Expr.ResolveRawEnv walk: an EnvVar leaf
// resolves to its raw string via os.LookupEnv (a set-but-empty variable is a
// valid value, and no YAML coercion is applied), a LogicalOr tries its left
// branch raw before its right, and any other expression type falls through
// to the normal coercing evaluation path - so (( raw_env $A || 42 )) yields
// the integer 42 when $A is unset. That asymmetry is spruce's contract.
// ResolveOperatorArgument cannot serve the EnvVar leaf: it errors on empty
// values and coerces via yaml.Unmarshal, both of which raw_env exists to
// bypass.
func resolveRawEnv(ev *Evaluator, e *Expr) (interface{}, error) {
	switch e.Type {
	case EnvVar:
		v, ok := os.LookupEnv(e.Name)
		if !ok {
			return nil, fmt.Errorf("environment variable $%s is not set", e.Name)
		}
		return v, nil

	case LogicalOr:
		if v, err := resolveRawEnv(ev, e.Left); err == nil {
			return v, nil
		}
		return resolveRawEnv(ev, e.Right)

	default:
		return ResolveOperatorArgument(ev, e)
	}
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("raw_env", RawEnvOperator{})
}
