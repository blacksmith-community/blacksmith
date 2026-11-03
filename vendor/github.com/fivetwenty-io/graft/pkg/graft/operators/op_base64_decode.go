package operators

import (
	"encoding/base64"
	"fmt"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Base64DecodeOperator ...
type Base64DecodeOperator struct{}

// Setup ...
func (Base64DecodeOperator) Setup() error {
	return nil
}

// Phase ...
func (Base64DecodeOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (Base64DecodeOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run ...
func (Base64DecodeOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( base64-decode ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( base64-decode ... )) operation at $%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("base64-decode operator requires exactly one string or reference argument")
	}

	// Use ResolveOperatorArgument to handle nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("     [0]: error was: %s", err)
		return nil, err
	}

	if val == nil {
		return nil, ansi.Errorf("@R{base64-decode operator argument resolved to nil}")
	}

	// Convert to string
	var contents string
	switch v := val.(type) {
	case string:
		DEBUG("  resolved to string: '%s'", v)
		contents = v
	default:
		DEBUG("  resolved to non-string: %T = %v", v, v)
		return nil, ansi.Errorf("@R{tried to base64 decode} @c{%v}@R{, which is not a string}", v)
	}

	decoded, err := base64.StdEncoding.DecodeString(contents)
	if err != nil {
		return nil, ansi.Errorf("@R{base64 decoding failed:} @c{%s}", err)
	}

	DEBUG("  resolved (( base64-decode ... )) operation to the string:\n    \"%s\"", string(decoded))

	return &Response{
		Type:  Replace,
		Value: string(decoded),
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("base64-decode", Base64DecodeOperator{})
}
