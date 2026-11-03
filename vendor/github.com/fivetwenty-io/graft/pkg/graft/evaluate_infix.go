package graft

import "fmt"

// infixOperatorSymbols maps each wired infix ExprType to the symbol under
// which its operator is registered (see the RegisterOp calls enumerated in
// pkg/graft/operators). The six ExprType members not present here — List,
// Or, RegexpMatch, BoshVar, VaultGroup, VaultChoice — are never produced by
// the parser and stay unsupported; see EvaluateInfix.
var infixOperatorSymbols = map[ExprType]string{
	Negate:             "!",
	Addition:           "+",
	Subtraction:        "-",
	Multiplication:     "*",
	Division:           "/",
	Modulo:             "%",
	Equal:              "==",
	NotEqual:           "!=",
	LessThan:           "<",
	LessThanOrEqual:    "<=",
	GreaterThan:        ">",
	GreaterThanOrEqual: ">=",
	LogicalAnd:         "&&",
}

// EvaluateInfix evaluates an infix expression node by dispatching to the
// registered operator for its symbol. Operands are the node's own children
// via Expr.Args (Left/Right for binary nodes, [Left] for unary Negate), so
// recursion descends a finite parse tree and terminates structurally: a
// nested infix operand (e.g. the inner Addition in "1 + 2 + 3") resolves
// through operators.ResolveOperatorArgument, which calls back into
// EvaluateInfix for that operand's own node, never re-entering this node.
//
// This function calls the operator's Run directly, not Opcall.Run. The
// caller — the outer Opcall wrapping the whole expression (parser.go's
// synthetic exprOperator) — already applies the single "$.<path>: " prefix
// via its own Opcall.Run; wrapping again here would double it.
func EvaluateInfix(ev *Evaluator, e *Expr) (interface{}, error) {
	if e == nil {
		return nil, fmt.Errorf("nil expression")
	}

	symbol, wired := infixOperatorSymbols[e.Type]
	if !wired {
		return nil, fmt.Errorf("unsupported expression type for evaluation: %d", e.Type)
	}

	op := OperatorForEngine(ev.engine, symbol)
	if _, isNull := op.(NullOperator); isNull {
		return nil, fmt.Errorf("unknown operator: %s", symbol)
	}

	resp, err := op.Run(ev, e.Args())
	if err != nil {
		return nil, err
	}
	return resp.Value, nil
}
