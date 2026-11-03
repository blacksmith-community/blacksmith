// Package interfaces defines core types for the graft parser and evaluator.
package interfaces

import (
	"sort"
	"sync"
)

// Phase string constant.
const (
	phaseUnknown = "unknown"
)

// OperatorPhase indicates when an operator should execute during document processing.
type OperatorPhase int

const (
	// MergePhase operators run during document merging to affect structure.
	MergePhase OperatorPhase = iota
	// ParamPhase operators validate required parameters before evaluation.
	ParamPhase
	// EvalPhase operators run during standard evaluation.
	EvalPhase
)

// String returns a human-readable name for the operator phase.
func (p OperatorPhase) String() string {
	switch p {
	case MergePhase:
		return "merge"
	case ParamPhase:
		return "param"
	case EvalPhase:
		return "eval"
	default:
		return phaseUnknown
	}
}

// OperatorCategory groups operators by their function.
type OperatorCategory int

const (
	// CategoryData operators work with data manipulation and transformation.
	CategoryData OperatorCategory = iota
	// CategoryArithmetic operators perform mathematical operations.
	CategoryArithmetic
	// CategoryString operators manipulate string values.
	CategoryString
	// CategoryLogic operators perform boolean logic operations.
	CategoryLogic
	// CategoryComparison operators compare values.
	CategoryComparison
	// CategoryArray operators work with arrays/lists.
	CategoryArray
	// CategoryControl operators handle control flow.
	CategoryControl
	// CategoryExternal operators fetch data from external sources.
	CategoryExternal
	// CategoryType operators work with type checking and conversion.
	CategoryType
	// CategoryIP operators perform IP address calculations.
	CategoryIP
)

// String returns a human-readable name for the operator category.
func (c OperatorCategory) String() string {
	switch c {
	case CategoryData:
		return "data"
	case CategoryArithmetic:
		return "arithmetic"
	case CategoryString:
		return "string"
	case CategoryLogic:
		return "logic"
	case CategoryComparison:
		return "comparison"
	case CategoryArray:
		return "array"
	case CategoryControl:
		return "control"
	case CategoryExternal:
		return "external"
	case CategoryType:
		return "type"
	case CategoryIP:
		return "ip"
	default:
		return phaseUnknown
	}
}

// Precedence defines operator precedence levels for expression parsing.
// Higher values indicate higher precedence (evaluated first).
type Precedence int

const (
	// PrecedenceLowest is the lowest precedence level.
	PrecedenceLowest Precedence = iota
	// PrecedenceTernary is for ternary conditional operator (?:).
	PrecedenceTernary
	// PrecedenceOr is for logical OR (||).
	PrecedenceOr
	// PrecedenceAnd is for logical AND (&&).
	PrecedenceAnd
	// PrecedenceEquality is for equality operators (==, !=).
	PrecedenceEquality
	// PrecedenceComparison is for comparison operators (<, >, <=, >=).
	PrecedenceComparison
	// PrecedenceAdditive is for additive operators (+, -).
	PrecedenceAdditive
	// PrecedenceMultiplicative is for multiplicative operators (*, /, %).
	PrecedenceMultiplicative
	// PrecedenceUnary is for unary operators (!, -).
	PrecedenceUnary
	// PrecedenceCall is for function calls and primary expressions.
	PrecedenceCall
)

// Associativity defines how operators of the same precedence are grouped.
type Associativity int

const (
	// LeftAssociative operators group left to right (a + b + c = (a + b) + c).
	LeftAssociative Associativity = iota
	// RightAssociative operators group right to left (a = b = c = a = (b = c)).
	RightAssociative
	// NonAssociative operators cannot be chained.
	NonAssociative
)

// OperatorInfo contains metadata about an operator.
type OperatorInfo struct {
	// Name is the operator's identifier (e.g., "grab", "concat", "+").
	Name string

	// MinArgs is the minimum number of arguments required.
	MinArgs int

	// MaxArgs is the maximum number of arguments allowed.
	// Use -1 for variadic operators (unlimited arguments).
	MaxArgs int

	// Description provides a brief explanation of the operator's purpose.
	Description string

	// Category groups the operator by function.
	Category OperatorCategory

	// Phase indicates when the operator should execute.
	Phase OperatorPhase

	// Precedence defines evaluation order for expression operators.
	Precedence Precedence

	// Associativity defines grouping for same-precedence operators.
	Associativity Associativity

	// IsUnary indicates if the operator is a unary operator.
	IsUnary bool

	// IsBinary indicates if the operator is a binary operator.
	IsBinary bool

	// Aliases are alternative names for the operator.
	Aliases []string
}

// OperatorRegistry manages registered operators.
type OperatorRegistry struct {
	mu        sync.RWMutex
	operators map[string]*OperatorInfo
	aliases   map[string]string // Maps alias -> canonical name
}

// globalRegistry is the default operator registry.
var globalRegistry = NewOperatorRegistry()

// NewOperatorRegistry creates a new empty operator registry.
func NewOperatorRegistry() *OperatorRegistry {
	return &OperatorRegistry{
		operators: make(map[string]*OperatorInfo),
		aliases:   make(map[string]string),
	}
}

// RegisterOperator adds an operator to the global registry.
func RegisterOperator(name string, info OperatorInfo) {
	globalRegistry.Register(name, info)
}

// LookupOperator retrieves an operator from the global registry.
func LookupOperator(name string) (OperatorInfo, bool) {
	return globalRegistry.Lookup(name)
}

// ListOperators returns all operator names from the global registry.
func ListOperators() []string {
	return globalRegistry.List()
}

// ListOperatorsByCategory returns operators in a category from the global registry.
func ListOperatorsByCategory(category OperatorCategory) []string {
	return globalRegistry.ListByCategory(category)
}

// Register adds an operator to this registry.
func (r *OperatorRegistry) Register(name string, info OperatorInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure the name in info matches the registration name
	info.Name = name
	r.operators[name] = &info

	// Register aliases
	for _, alias := range info.Aliases {
		r.aliases[alias] = name
	}
}

// Lookup retrieves an operator by name or alias.
func (r *OperatorRegistry) Lookup(name string) (OperatorInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Check for direct match
	if info, ok := r.operators[name]; ok {
		return *info, true
	}

	// Check aliases
	if canonical, ok := r.aliases[name]; ok {
		if info, ok := r.operators[canonical]; ok {
			return *info, true
		}
	}

	return OperatorInfo{}, false
}

// List returns all registered operator names (not including aliases).
func (r *OperatorRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.operators))
	for name := range r.operators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListByCategory returns operator names in the specified category.
func (r *OperatorRegistry) ListByCategory(category OperatorCategory) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name, info := range r.operators {
		if info.Category == category {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ListByPhase returns operator names that run in the specified phase.
func (r *OperatorRegistry) ListByPhase(phase OperatorPhase) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name, info := range r.operators {
		if info.Phase == phase {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Count returns the number of registered operators.
func (r *OperatorRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.operators)
}

// GetGlobalRegistry returns the global operator registry for advanced use cases.
func GetGlobalRegistry() *OperatorRegistry {
	return globalRegistry
}

// IsOperatorName returns true if the given name is a registered operator.
func IsOperatorName(name string) bool {
	_, found := globalRegistry.Lookup(name)
	return found
}

// OperatorPrecedence returns the precedence level for an operator.
// Returns PrecedenceLowest if the operator is not found.
func OperatorPrecedence(op string) Precedence {
	info, found := globalRegistry.Lookup(op)
	if !found {
		return PrecedenceLowest
	}
	return info.Precedence
}

// IsUnaryOperator returns true if the operator is a unary operator.
func IsUnaryOperator(op string) bool {
	info, found := globalRegistry.Lookup(op)
	if !found {
		return false
	}
	return info.IsUnary
}

// IsBinaryOperator returns true if the operator is a binary operator.
func IsBinaryOperator(op string) bool {
	info, found := globalRegistry.Lookup(op)
	if !found {
		return false
	}
	return info.IsBinary
}

// GetOperatorAssociativity returns the associativity for an operator.
// Returns LeftAssociative if the operator is not found.
func GetOperatorAssociativity(op string) Associativity {
	info, found := globalRegistry.Lookup(op)
	if !found {
		return LeftAssociative
	}
	return info.Associativity
}

// ValidateOperatorArgs checks if the argument count is valid for an operator.
// Returns true if the count is within the operator's min/max bounds.
func ValidateOperatorArgs(op string, argCount int) bool {
	info, found := globalRegistry.Lookup(op)
	if !found {
		return false
	}

	if argCount < info.MinArgs {
		return false
	}

	if info.MaxArgs >= 0 && argCount > info.MaxArgs {
		return false
	}

	return true
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	// Register all built-in operators
	registerDataOperators()
	registerArithmeticOperators()
	registerStringOperators()
	registerLogicOperators()
	registerComparisonOperators()
	registerArrayOperators()
	registerControlOperators()
	registerExternalOperators()
	registerTypeOperators()
	registerIPOperators()
}

// registerDataOperators registers data manipulation operators.
func registerDataOperators() {
	RegisterOperator("grab", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Reference values from the document tree",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("static", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Reference static values without evaluation",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("param", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     1,
		Description: "Mark a required parameter",
		Category:    CategoryData,
		Phase:       ParamPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("inject", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Inject map contents at parent level",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("prune", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     -1,
		Description: "Mark keys for removal after merge",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("sort", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     2,
		Description: "Sort array elements",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("uniq", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Remove duplicate elements from array",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("reverse", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     1,
		Description: "Reverse array or string",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("defer", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Defer evaluation to later phase",
		Category:    CategoryData,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("stringify", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Convert value to YAML string representation",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("parse", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Parse YAML/JSON string into data structure",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("base64", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Base64 encode a string",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("base64-decode", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Base64 decode a string",
		Category:    CategoryData,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerArithmeticOperators registers arithmetic operators.
func registerArithmeticOperators() {
	RegisterOperator("+", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Add two values",
		Category:      CategoryArithmetic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceAdditive,
		Associativity: LeftAssociative,
		IsBinary:      true,
	})

	RegisterOperator("-", OperatorInfo{
		MinArgs:       1,
		MaxArgs:       2,
		Description:   "Subtract values or negate",
		Category:      CategoryArithmetic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceAdditive,
		Associativity: LeftAssociative,
		IsUnary:       true,
		IsBinary:      true,
	})

	RegisterOperator("*", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Multiply two values",
		Category:      CategoryArithmetic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		IsBinary:      true,
	})

	RegisterOperator("/", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Divide two values",
		Category:      CategoryArithmetic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		IsBinary:      true,
	})

	RegisterOperator("%", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Modulo (remainder)",
		Category:      CategoryArithmetic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceMultiplicative,
		Associativity: LeftAssociative,
		IsBinary:      true,
		Aliases:       []string{"mod"},
	})

	RegisterOperator("calc", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Evaluate complex mathematical expression",
		Category:    CategoryArithmetic,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerStringOperators registers string manipulation operators.
func registerStringOperators() {
	RegisterOperator("concat", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Concatenate strings",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("join", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     2,
		Description: "Join array elements with separator",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("split", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     2,
		Description: "Split string by delimiter (PCRE regex)",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("substr", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     3,
		Description: "Extract substring",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("replace", OperatorInfo{
		MinArgs:     3,
		MaxArgs:     3,
		Description: "Replace occurrences in string",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("trim", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     2,
		Description: "Remove whitespace from string ends",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("upper", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Convert string to uppercase",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("lower", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Convert string to lowercase",
		Category:    CategoryString,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerLogicOperators registers boolean logic operators.
func registerLogicOperators() {
	RegisterOperator("||", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Logical OR with fallback semantics",
		Category:      CategoryLogic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceOr,
		Associativity: LeftAssociative,
		IsBinary:      true,
		Aliases:       []string{"or"},
	})

	RegisterOperator("&&", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Logical AND",
		Category:      CategoryLogic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceAnd,
		Associativity: LeftAssociative,
		IsBinary:      true,
		Aliases:       []string{"and"},
	})

	RegisterOperator("!", OperatorInfo{
		MinArgs:       1,
		MaxArgs:       1,
		Description:   "Logical NOT",
		Category:      CategoryLogic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceUnary,
		Associativity: RightAssociative,
		IsUnary:       true,
		Aliases:       []string{"not"},
	})

	RegisterOperator("?:", OperatorInfo{
		MinArgs:       3,
		MaxArgs:       3,
		Description:   "Ternary conditional operator",
		Category:      CategoryLogic,
		Phase:         EvalPhase,
		Precedence:    PrecedenceTernary,
		Associativity: RightAssociative,
		Aliases:       []string{"ternary"},
	})
}

// registerComparisonOperators registers comparison operators.
func registerComparisonOperators() {
	RegisterOperator("==", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Equality comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceEquality,
		Associativity: LeftAssociative,
		IsBinary:      true,
		Aliases:       []string{"eq"},
	})

	RegisterOperator("!=", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Inequality comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceEquality,
		Associativity: LeftAssociative,
		IsBinary:      true,
		Aliases:       []string{"ne"},
	})

	RegisterOperator("<", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Less than comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceComparison,
		Associativity: NonAssociative,
		IsBinary:      true,
		Aliases:       []string{"lt"},
	})

	RegisterOperator("<=", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Less than or equal comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceComparison,
		Associativity: NonAssociative,
		IsBinary:      true,
		Aliases:       []string{"le"},
	})

	RegisterOperator(">", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Greater than comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceComparison,
		Associativity: NonAssociative,
		IsBinary:      true,
		Aliases:       []string{"gt"},
	})

	RegisterOperator(">=", OperatorInfo{
		MinArgs:       2,
		MaxArgs:       2,
		Description:   "Greater than or equal comparison",
		Category:      CategoryComparison,
		Phase:         EvalPhase,
		Precedence:    PrecedenceComparison,
		Associativity: NonAssociative,
		IsBinary:      true,
		Aliases:       []string{"ge"},
	})
}

// registerArrayOperators registers array manipulation operators.
func registerArrayOperators() {
	RegisterOperator("append", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     -1,
		Description: "Append elements to end of array",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("prepend", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     -1,
		Description: "Prepend elements to beginning of array",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("insert", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     2,
		Description: "Insert element at specific position",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("delete", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Delete element at index",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("index", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     2,
		Description: "Get element at index",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("length", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get length of array or string",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
		Aliases:     []string{"len"},
	})

	RegisterOperator("first", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get first element of array",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("last", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get last element of array",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("flatten", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Flatten nested arrays",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("inline", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "Merge arrays by index",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("merge", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     2,
		Description: "Merge arrays by key",
		Category:    CategoryArray,
		Phase:       MergePhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("shuffle", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     1,
		Description: "Randomize array order",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("cartesian-product", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     -1,
		Description: "Compute cartesian product of arrays",
		Category:    CategoryArray,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerControlOperators registers control flow operators.
func registerControlOperators() {
	RegisterOperator("if", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Conditional block start",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("elif", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Else-if conditional branch",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("else", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "Else conditional branch",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("fi", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "End conditional block",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("for", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     3,
		Description: "For loop iteration",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("while", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "While loop",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("done", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "End loop block",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("case", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Pattern matching block start",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("when", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Pattern match clause",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("default", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "Default pattern match clause",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("esac", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "End pattern matching block",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("in", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     2,
		Description: "Loop iteration variable binding",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("range", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     3,
		Description: "Generate numeric range",
		Category:    CategoryControl,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerExternalOperators registers external data source operators.
func registerExternalOperators() {
	RegisterOperator("vault", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Fetch secret from HashiCorp Vault",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("awsparam", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Fetch parameter from AWS SSM Parameter Store",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
		Aliases:     []string{"awssm"},
	})

	RegisterOperator("awssecret", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     2,
		Description: "Fetch secret from AWS Secrets Manager",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
		Aliases:     []string{"awssecrets"},
	})

	RegisterOperator("nats", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Fetch value from NATS KV store",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("file", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Read file contents",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("load", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Load and parse YAML/JSON file",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("env", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     2,
		Description: "Read environment variable",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("raw_env", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Read environment variable as raw string without YAML coercion",
		Category:    CategoryExternal,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerTypeOperators registers type checking and conversion operators.
func registerTypeOperators() {
	RegisterOperator("type", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get type of value",
		Category:    CategoryType,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("empty", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Check if value is empty",
		Category:    CategoryType,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("null", OperatorInfo{
		MinArgs:     0,
		MaxArgs:     0,
		Description: "Null/nil value",
		Category:    CategoryType,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
		Aliases:     []string{"nil"},
	})

	RegisterOperator("keys", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get keys from map",
		Category:    CategoryType,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("values", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     1,
		Description: "Get values from map",
		Category:    CategoryType,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}

// registerIPOperators registers IP address manipulation operators.
func registerIPOperators() {
	RegisterOperator("ips", OperatorInfo{
		MinArgs:     2,
		MaxArgs:     3,
		Description: "IP address arithmetic",
		Category:    CategoryIP,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})

	RegisterOperator("static_ips", OperatorInfo{
		MinArgs:     1,
		MaxArgs:     -1,
		Description: "Generate static IP addresses (BOSH-style)",
		Category:    CategoryIP,
		Phase:       EvalPhase,
		Precedence:  PrecedenceCall,
	})
}
