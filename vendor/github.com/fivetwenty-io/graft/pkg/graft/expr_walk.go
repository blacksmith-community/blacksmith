package graft

// Walk traverses e and every expression reachable from it in pre-order: fn
// is called on e first, then, if fn returns true, on each of e's children
// in left-to-right order (Left, then Right, then each element of
// Call.Args(), when those fields are populated). Returning false from fn
// prunes e's subtree — its children are skipped — but the walk continues
// with whatever remains: an ancestor's later children, or later elements
// of an enclosing Call.Args() list. This is the same boolean-return
// convention as go/ast.Inspect.
//
// Walk is nil-safe: a nil e, or an *Expr whose Left, Right, or Call is
// nil, is simply not descended into. A nil fn makes Walk a no-op.
//
// Walk does not switch on e.Type. It follows only the Left, Right, and
// Call fields that Expr actually carries, so it requires no change when a
// new ExprType is added, as long as the new node stores its children in
// those same fields (every node kind in the parser does, including
// OperatorCall's n-ary arguments via Call.Args()).
func Walk(e *Expr, fn func(*Expr) bool) {
	if e == nil || fn == nil {
		return
	}
	if !fn(e) {
		return
	}
	if e.Left != nil {
		Walk(e.Left, fn)
	}
	if e.Right != nil {
		Walk(e.Right, fn)
	}
	if e.Call != nil {
		for _, arg := range e.Call.Args() {
			Walk(arg, fn)
		}
	}
}

// Visitor dispatches on an Expr's evaluation shape rather than on its exact
// ExprType, so the interface stays small and does not need one method per
// ExprType constant. VisitOther is the catch-all: every ExprType that does
// not have a dedicated method — today that is List, Or, RegexpMatch,
// BoshVar, VaultGroup, and VaultChoice, none of which the parser produces
// (see evaluate_infix.go) — and any ExprType added in the future, both
// route to VisitOther. That is deliberate: it lets a new ExprType land
// without breaking every existing Visitor implementation, unlike an
// interface with one method per constant, which every implementer would
// have to update in lockstep.
type Visitor interface {
	// VisitLiteral handles a Literal node (e.Literal holds the value).
	VisitLiteral(e *Expr) interface{}
	// VisitReference handles a Reference node (e.Reference holds the
	// resolved *tree.Cursor, when the parse produced one).
	VisitReference(e *Expr) interface{}
	// VisitOperatorCall handles an OperatorCall node — a nested operator
	// invocation such as (( grab a )) or (( concat a b )). e.Call carries
	// the resolved Operator and its arguments; e.Operator carries the name
	// as written.
	VisitOperatorCall(e *Expr) interface{}
	// VisitBinaryOp handles every two-operand infix node: Addition,
	// Subtraction, Multiplication, Division, Modulo, Equal, NotEqual,
	// LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
	// LogicalAnd, and LogicalOr. e.Left and e.Right are both populated.
	VisitBinaryOp(e *Expr) interface{}
	// VisitUnaryOp handles the one unary node the parser produces, Negate
	// (( !expr )). e.Left is the operand; e.Right is unused.
	VisitUnaryOp(e *Expr) interface{}
	// VisitEnvVar handles an EnvVar node (e.Name holds the variable name,
	// without the leading "$").
	VisitEnvVar(e *Expr) interface{}
	// VisitOther is the catch-all described above.
	VisitOther(e *Expr) interface{}
}

// Accept dispatches e to the Visitor method matching its ExprType and
// returns that method's result. Accept does not recurse into e's
// children — a Visitor implementation that wants a full-tree visit calls
// Accept (or Walk) itself from inside its own methods, on e.Left, e.Right,
// or e.Call.Args() as appropriate.
//
// Accept is nil-safe: a nil e or a nil v returns nil without calling any
// Visitor method — there is nothing to dispatch on, or nothing to dispatch
// to.
func Accept(e *Expr, v Visitor) interface{} {
	if e == nil || v == nil {
		return nil
	}
	switch e.Type {
	case Literal:
		return v.VisitLiteral(e)
	case Reference:
		return v.VisitReference(e)
	case OperatorCall:
		return v.VisitOperatorCall(e)
	case EnvVar:
		return v.VisitEnvVar(e)
	case Negate:
		return v.VisitUnaryOp(e)
	case Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, LogicalOr:
		return v.VisitBinaryOp(e)
	default:
		// List, Or, RegexpMatch, BoshVar, VaultGroup, VaultChoice today,
		// and any ExprType added after this switch was written.
		return v.VisitOther(e)
	}
}
