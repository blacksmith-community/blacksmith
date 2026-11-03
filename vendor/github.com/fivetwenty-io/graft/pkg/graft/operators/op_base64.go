package operators

import (
	"encoding/base64"
	"fmt"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Base64Operator ...
type Base64Operator struct{}

// Setup ...
func (Base64Operator) Setup() error {
	return nil
}

// Phase ...
func (Base64Operator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies ...
func (Base64Operator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run ...
func (Base64Operator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( base64 ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( base64 ... )) operation at $%s\n", ev.Here)

	if len(args) != 1 {
		return nil, fmt.Errorf("base64 operator requires exactly one string or reference argument, got %d arguments", len(args))
	}

	// Use ResolveOperatorArgument to handle nested expressions
	val, err := ResolveOperatorArgument(ev, args[0])
	if err != nil {
		DEBUG("  arg[0]: failed to resolve expression to a concrete value")
		DEBUG("     [0]: error was: %s", err)
		return nil, err
	}

	if val == nil {
		return nil, ansi.Errorf("@R{base64 operator argument resolved to nil}")
	}

	// Convert to string
	var contents string
	switch v := val.(type) {
	case string:
		DEBUG("  resolved to string: '%s'", v)
		contents = v
	default:
		DEBUG("  resolved to non-string: %T = %v", v, v)
		return nil, fmt.Errorf("base64 operator requires a string value, got %T", v)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(contents))
	DEBUG("  resolved (( base64 ... )) operation to the string:\n    \"%s\"", encoded)

	return &Response{
		Type:  Replace,
		Value: encoded,
	}, nil
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("base64", Base64Operator{})
}
