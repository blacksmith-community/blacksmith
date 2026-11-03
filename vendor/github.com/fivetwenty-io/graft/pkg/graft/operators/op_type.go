package operators

import (
	"fmt"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// TypeOperator reports the graft-level type name of its single argument:
// "string", "int", "float", "bool", "array", "map", or "null".
// Classification defers to GetOperandType, with "array" written in
// place of GetOperandType's own "list" string, and "map" left as-is — the
// only two vocabularies differ; see interpretation decision T-1 for the
// "null" (not "nil") spelling.
type TypeOperator struct{}

// Setup initializes the operator.
func (TypeOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (TypeOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what the operator depends on.
func (TypeOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the operator.
func (TypeOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( type ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( type ... )) operation at $.%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("type operator requires exactly one argument, got %d", len(args))
	}

	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("  error was: %s", err)
		return nil, err
	}

	name, err := graftTypeName(val)
	if err != nil {
		return nil, err
	}

	return &Response{
		Type:  Replace,
		Value: name,
	}, nil
}

// graftTypeName maps a resolved value onto the spec's §4.1 vocabulary.
// GetOperandType.String() returns "list" for a slice, but the documented
// (( type ... )) vocabulary is "array"; every other classification is
// reused verbatim. TypeUnknown — a value GetOperandType cannot classify at
// all (a struct, channel, function, etc. reaching here through some other
// operator's output) — is the one case GetOperandType itself cannot name,
// so it is reported as an error rather than silently returned as a string.
func graftTypeName(val interface{}) (string, error) {
	switch GetOperandType(val) {
	case TypeList:
		return "array", nil
	case TypeUnknown:
		return "", fmt.Errorf("type operator cannot classify value of type %T", val)
	case TypeInt, TypeFloat, TypeString, TypeBool, TypeMap, TypeNull:
		return GetOperandType(val).String(), nil
	}
	return "", fmt.Errorf("type operator cannot classify value of type %T", val)
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("type", TypeOperator{})
}
