package operators

import (
	"os"
	"sync"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Type aliases for convenience.
//
//nolint:revive // Type block provides group documentation
type (
	Evaluator     = graft.Evaluator
	Expr          = graft.Expr
	Response      = graft.Response
	OperatorPhase = graft.OperatorPhase
	ExprType      = graft.ExprType
	Operator      = graft.Operator
	Engine        = graft.Engine
)

// Constants.
const (
	EvalPhase    = graft.EvalPhase
	MergePhase   = graft.MergePhase
	ParamPhase   = graft.ParamPhase
	Literal      = graft.Literal
	Reference    = graft.Reference
	EnvVar       = graft.EnvVar
	Replace      = graft.Replace
	Inject       = graft.Inject
	LogicalOr    = graft.LogicalOr
	OperatorCall = graft.OperatorCall

	// Additional ExprType constants.
	List               = graft.List
	Or                 = graft.Or
	Negate             = graft.Negate
	Addition           = graft.Addition
	Subtraction        = graft.Subtraction
	Multiplication     = graft.Multiplication
	Division           = graft.Division
	Modulo             = graft.Modulo
	Equal              = graft.Equal
	NotEqual           = graft.NotEqual
	LessThan           = graft.LessThan
	LessThanOrEqual    = graft.LessThanOrEqual
	GreaterThan        = graft.GreaterThan
	GreaterThanOrEqual = graft.GreaterThanOrEqual
	LogicalAnd         = graft.LogicalAnd
	RegexpMatch        = graft.RegexpMatch
	BoshVar            = graft.BoshVar
	VaultGroup         = graft.VaultGroup
	VaultChoice        = graft.VaultChoice
)

// DEBUG logs a debug message.
//
//nolint:goprintffuncname // DEBUG is an established name used throughout the codebase
func DEBUG(format string, args ...interface{}) {
	graft.DEBUG(format, args...)
}

// TRACE logs a trace message.
//
//nolint:goprintffuncname // TRACE is an established name used throughout the codebase
func TRACE(format string, args ...interface{}) {
	graft.TRACE(format, args...)
}

// OperatorFor returns the operator for the given name.
func OperatorFor(name string) Operator {
	return graft.OperatorFor(name)
}

// OperatorForEngine returns the operator for the given name, preferring the
// engine's local registry over the process-global DefaultRegistry. Used for
// nested operator resolution (an operator call as another operator's
// argument, e.g. (( concat "a" (upper "b") ))) so a custom operator
// registered on the engine is visible there too, not only at top level.
func OperatorForEngine(e Engine, name string) Operator {
	return graft.OperatorForEngine(e, name)
}

// ResolveEnv resolves environment variables in a slice of strings.
func ResolveEnv(nodes []string) []string {
	// Phase 1: Simple implementation
	for i, node := range nodes {
		if len(node) > 2 && node[0] == '$' {
			if val := os.Getenv(node[1:]); val != "" {
				nodes[i] = val
			}
		}
	}
	return nodes
}

// EvaluateExpr evaluates an expression.
func EvaluateExpr(expr *Expr, ev *Evaluator) (*Response, error) {
	return graft.EvaluateExpr(expr, ev)
}

// EvaluateOperatorArgs evaluates operator arguments and returns a list of values.
func EvaluateOperatorArgs(ev *Evaluator, args []*Expr) ([]interface{}, error) {
	values := make([]interface{}, len(args))
	for i, arg := range args {
		val, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			return nil, err
		}
		values[i] = val
	}
	return values, nil
}

// DeepCopyMap creates a deep copy of a map.
func DeepCopyMap(m map[string]interface{}) map[string]interface{} {
	return graft.DeepCopyMap(m)
}

// String pool for performance.
var stringPool = &sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 10)
		return &s
	},
}

// GetStringSlice gets a string slice from the pool.
func GetStringSlice() *[]string {
	sp, ok := stringPool.Get().(*[]string)
	if !ok {
		s := make([]string, 0, 10)
		return &s
	}
	*sp = (*sp)[:0]
	return sp
}

// PutStringSlice returns a string slice to the pool.
func PutStringSlice(s *[]string) {
	if s == nil || cap(*s) > 100 { // Don't pool very large slices
		return
	}
	stringPool.Put(s)
}

// DefaultKeyGenerator generates keys.
func DefaultKeyGenerator() func() (string, error) {
	return graft.DefaultKeyGenerator()
}

// Helper functions for merge operations

// Merge merges two data structures.
func Merge(dst, src interface{}) error {
	return graft.Merge(dst, src)
}

// DebugOn returns true if debugging is enabled.
func DebugOn() bool {
	return graft.DebugOn()
}

// WarningError is a type alias for graft.WarningError.
type WarningError = graft.WarningError

// OperatorCallCall is a constant alias for graft.OperatorCall.
const OperatorCallCall = graft.OperatorCall

// OperatorInfo is a type alias for interfaces.OperatorInfo.
type OperatorInfo = interfaces.OperatorInfo

// Associativity is a type alias for interfaces.Associativity.
type Associativity = interfaces.Associativity

// Precedence and associativity constants.
const (
	// PrecedencePostfix is the precedence for postfix operators.
	PrecedencePostfix = interfaces.PrecedenceCall
	// PrecedenceOr is the precedence for logical OR.
	PrecedenceOr = interfaces.PrecedenceOr
	// PrecedenceAnd is the precedence for logical AND.
	PrecedenceAnd = interfaces.PrecedenceAnd
	// PrecedenceEquality is the precedence for equality operators.
	PrecedenceEquality = interfaces.PrecedenceEquality
	// PrecedenceComparison is the precedence for comparison operators.
	PrecedenceComparison = interfaces.PrecedenceComparison
	// PrecedenceAdditive is the precedence for additive operators.
	PrecedenceAdditive = interfaces.PrecedenceAdditive
	// PrecedenceMultiplicative is the precedence for multiplicative operators.
	PrecedenceMultiplicative = interfaces.PrecedenceMultiplicative
	// PrecedenceTernary is the precedence for ternary operators.
	PrecedenceTernary = interfaces.PrecedenceTernary
	// PrecedenceUnary is the precedence for unary operators.
	PrecedenceUnary = interfaces.PrecedenceUnary
	// RightAssociative indicates right-to-left associativity.
	RightAssociative = interfaces.RightAssociative
	// LeftAssociative indicates left-to-right associativity.
	LeftAssociative = interfaces.LeftAssociative
)

// NewOperatorRegistry creates a new operator registry.
func NewOperatorRegistry() *graft.OperatorRegistry {
	return graft.NewOperatorRegistry()
}

// Additional imports that operators commonly need
// These are provided here so operators don't need to import them individually.
var (
	// tree package is used by all operators for Dependencies method.
	_ = tree.Cursor{}
)
