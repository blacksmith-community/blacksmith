package graft

import (
	"fmt"
)

// Precedence levels for operators.
type Precedence int

// Precedence constants.
const (
	// PrecedenceLowest is the lowest precedence level.
	PrecedenceLowest Precedence = iota
	// PrecedenceTernary is for ternary operators (? :).
	PrecedenceTernary
	// PrecedenceOr is for logical OR (||).
	PrecedenceOr
	// PrecedenceAnd is for logical AND (&&).
	PrecedenceAnd
	// PrecedenceEquality is for equality operators (== !=).
	PrecedenceEquality
	// PrecedenceComparison is for comparison operators (< > <= >=).
	PrecedenceComparison
	// PrecedenceAdditive is for additive operators (+ -).
	PrecedenceAdditive
	// PrecedenceMultiplicative is for multiplicative operators (* / %).
	PrecedenceMultiplicative
	// PrecedenceUnary is for unary operators (! -).
	PrecedenceUnary
	// PrecedenceCall is for operator calls.
	PrecedenceCall
	// PrecedenceHighest is the highest precedence (literals, refs, parens).
	PrecedenceHighest
)

// Associativity rules for operators.
type Associativity int

// Associativity constants.
const (
	// LeftAssociative indicates left-to-right associativity.
	LeftAssociative Associativity = iota
	// RightAssociative indicates right-to-left associativity.
	RightAssociative
)

// Additional precedence constants to match parser needs.
const (
	PrecedenceLogicalOr      = PrecedenceOr
	PrecedenceAddition       = PrecedenceAdditive
	PrecedenceMultiplication = PrecedenceMultiplicative
	PrecedencePostfix        = PrecedenceCall
)

// Additional associativity constants.
const (
	AssociativityLeft  = LeftAssociative
	AssociativityRight = RightAssociative
)

// OperatorInfo stores metadata about operators.
type OperatorInfo struct {
	Name          string
	Precedence    Precedence
	Associativity Associativity
	MinArgs       int
	MaxArgs       int // -1 for unlimited
	Phase         OperatorPhase
}

// OperatorRegistry manages operator metadata.
type OperatorRegistry struct {
	operators map[string]*OperatorInfo
}

// NewOperatorRegistry creates a new operator registry.
func NewOperatorRegistry() *OperatorRegistry {
	registry := &OperatorRegistry{
		operators: make(map[string]*OperatorInfo),
	}

	// Populate with all known operators
	for name, info := range OperatorInfoRegistry {
		infoCopy := info // Copy to avoid pointer issues
		registry.operators[name] = &infoCopy
	}

	return registry
}

// Register adds an operator to the registry.
func (r *OperatorRegistry) Register(info *OperatorInfo) {
	r.operators[info.Name] = info
}

// Get retrieves operator info.
func (r *OperatorRegistry) Get(name string) *OperatorInfo {
	return r.operators[name]
}

// OperatorInfoRegistry contains metadata for all operators.
var OperatorInfoRegistry = map[string]OperatorInfo{
	"vault-try": {
		Name:       "vault-try",
		Precedence: PrecedenceCall,
		MinArgs:    2, // at least one path + default
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"grab": {
		Name:       "grab",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"concat": {
		Name:       "concat",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"static_ips": {
		Name:       "static_ips",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"join": {
		Name:       "join",
		Precedence: PrecedenceCall,
		MinArgs:    2, // separator and list
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"base64": {
		Name:       "base64",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"base64-decode": {
		Name:       "base64-decode",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"file": {
		Name:       "file",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    2,
		Phase:      EvalPhase,
	},
	"keys": {
		Name:       "keys",
		Precedence: PrecedenceCall,
		MinArgs:    0,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"stringify": {
		Name:       "stringify",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"negate": {
		Name:       "negate",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"empty": {
		Name:       "empty",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"null": {
		Name:       "null",
		Precedence: PrecedenceCall,
		MinArgs:    0,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"defer": {
		Name:       "defer",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      MergePhase,
	},
	"param": {
		Name:       "param",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      ParamPhase,
	},
	"vault": {
		Name:       "vault",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1, // unlimited args for paths with defaults
		Phase:      EvalPhase,
	},
	"calc": {
		Name:       "calc",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"sort": {
		Name:       "sort",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    2,
		Phase:      EvalPhase,
	},
	"shuffle": {
		Name:       "shuffle",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    2,
		Phase:      EvalPhase,
	},
	"ips": {
		Name:       "ips",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"inject": {
		Name:       "inject",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"load": {
		Name:       "load",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	"prune": {
		Name:       "prune",
		Precedence: PrecedenceCall,
		MinArgs:    0,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"cartesian-product": {
		Name:       "cartesian-product",
		Precedence: PrecedenceCall,
		MinArgs:    0,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"awsparam": {
		Name:       "awsparam",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"awssecret": {
		Name:       "awssecret",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	},
	"nats": {
		Name:       "nats",
		Precedence: PrecedenceCall,
		MinArgs:    1,
		MaxArgs:    2, // path and optional config
		Phase:      EvalPhase,
	},
	// Arithmetic operators
	"+": {
		Name:          "+",
		Precedence:    PrecedenceAdditive,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"-": {
		Name:          "-",
		Precedence:    PrecedenceAdditive,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"*": {
		Name:          "*",
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"/": {
		Name:          "/",
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"%": {
		Name:          "%",
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	// Comparison operators
	"==": {
		Name:          "==",
		Precedence:    PrecedenceEquality,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"!=": {
		Name:          "!=",
		Precedence:    PrecedenceEquality,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"<": {
		Name:          "<",
		Precedence:    PrecedenceComparison,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	">": {
		Name:          ">",
		Precedence:    PrecedenceComparison,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"<=": {
		Name:          "<=",
		Precedence:    PrecedenceComparison,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	">=": {
		Name:          ">=",
		Precedence:    PrecedenceComparison,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	// Boolean operators
	"&&": {
		Name:          "&&",
		Precedence:    PrecedenceAnd,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"||": {
		Name:          "||",
		Precedence:    PrecedenceOr,
		Associativity: LeftAssociative,
		MinArgs:       2,
		MaxArgs:       2,
		Phase:         EvalPhase,
	},
	"!": {
		Name:       "!",
		Precedence: PrecedenceUnary,
		MinArgs:    1,
		MaxArgs:    1,
		Phase:      EvalPhase,
	},
	// Ternary operator
	"?:": {
		Name:          "?:",
		Precedence:    PrecedenceTernary,
		Associativity: RightAssociative,
		MinArgs:       3,
		MaxArgs:       3,
		Phase:         EvalPhase,
	},
	// Split operator
	"split": {
		Name:       "split",
		Precedence: PrecedenceCall,
		MinArgs:    2,
		MaxArgs:    3, // string, separator, optional limit
		Phase:      EvalPhase,
	},
}

// GetOperatorInfo retrieves operator metadata.
func GetOperatorInfo(name string) (OperatorInfo, bool) {
	info, ok := OperatorInfoRegistry[name]
	return info, ok
}

// IsRegisteredOperator checks if a name is a registered operator.
func IsRegisteredOperator(name string) bool {
	_, ok := OperatorInfoRegistry[name]
	return ok
}

// ValidateOperatorArgs validates argument count for an operator.
func ValidateOperatorArgs(opName string, argCount int) error {
	info, ok := GetOperatorInfo(opName)
	if !ok {
		return fmt.Errorf("unknown operator: %s", opName)
	}

	if argCount < info.MinArgs {
		return fmt.Errorf("operator %s requires at least %d arguments, got %d",
			opName, info.MinArgs, argCount)
	}

	if info.MaxArgs != -1 && argCount > info.MaxArgs {
		return fmt.Errorf("operator %s accepts at most %d arguments, got %d",
			opName, info.MaxArgs, argCount)
	}

	return nil
}
