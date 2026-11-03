package graft

import (
	"fmt"
	"sync"
)

// UnifiedOperatorEntry combines operator metadata with its implementation.
type UnifiedOperatorEntry struct {
	// Metadata (from OperatorInfo)
	Name          string
	Precedence    Precedence
	Associativity Associativity
	MinArgs       int
	MaxArgs       int // -1 for unlimited
	Phase         OperatorPhase

	// Implementation
	Implementation Operator
}

// UnifiedOperatorRegistry is the single source of truth for all operators.
type UnifiedOperatorRegistry struct {
	operators map[string]*UnifiedOperatorEntry
	mu        sync.RWMutex
}

// DefaultRegistry is the global unified registry instance.
// Operators register into this via init(); each engine clones from it.
var DefaultRegistry = NewUnifiedOperatorRegistry()

// NewUnifiedOperatorRegistry creates a new unified operator registry.
func NewUnifiedOperatorRegistry() *UnifiedOperatorRegistry {
	return &UnifiedOperatorRegistry{
		operators: make(map[string]*UnifiedOperatorEntry),
	}
}

// Register adds or updates an operator in the registry.
func (r *UnifiedOperatorRegistry) Register(entry *UnifiedOperatorEntry) error {
	if entry == nil {
		return fmt.Errorf("operator entry cannot be nil")
	}
	if entry.Name == "" {
		return fmt.Errorf("operator name cannot be empty")
	}
	if entry.Implementation == nil {
		return fmt.Errorf("operator implementation cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.operators[entry.Name] = entry
	return nil
}

// RegisterOperator is a convenience method for registering an operator with metadata.
func (r *UnifiedOperatorRegistry) RegisterOperator(name string, op Operator, info OperatorInfo) error {
	entry := &UnifiedOperatorEntry{
		Name:           name,
		Precedence:     info.Precedence,
		Associativity:  info.Associativity,
		MinArgs:        info.MinArgs,
		MaxArgs:        info.MaxArgs,
		Phase:          info.Phase,
		Implementation: op,
	}
	return r.Register(entry)
}

// Get retrieves a complete operator entry.
func (r *UnifiedOperatorRegistry) Get(name string) (*UnifiedOperatorEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.operators[name]
	return entry, exists
}

// GetImplementation retrieves just the operator implementation.
func (r *UnifiedOperatorRegistry) GetImplementation(name string) (Operator, bool) {
	entry, exists := r.Get(name)
	if !exists {
		return nil, false
	}
	return entry.Implementation, true
}

// GetMetadata retrieves just the operator metadata.
func (r *UnifiedOperatorRegistry) GetMetadata(name string) (OperatorInfo, bool) {
	entry, exists := r.Get(name)
	if !exists {
		return OperatorInfo{}, false
	}

	return OperatorInfo{
		Name:          entry.Name,
		Precedence:    entry.Precedence,
		Associativity: entry.Associativity,
		MinArgs:       entry.MinArgs,
		MaxArgs:       entry.MaxArgs,
		Phase:         entry.Phase,
	}, true
}

// IsRegistered checks if an operator is registered.
func (r *UnifiedOperatorRegistry) IsRegistered(name string) bool {
	_, exists := r.Get(name)
	return exists
}

// ListOperators returns all registered operator names.
func (r *UnifiedOperatorRegistry) ListOperators() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.operators))
	for name := range r.operators {
		names = append(names, name)
	}
	return names
}

// GetByPhase returns all operators for a specific phase.
func (r *UnifiedOperatorRegistry) GetByPhase(phase OperatorPhase) []*UnifiedOperatorEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*UnifiedOperatorEntry
	for _, entry := range r.operators {
		if entry.Phase == phase {
			result = append(result, entry)
		}
	}
	return result
}

// ValidateArgs validates argument count for an operator.
func (r *UnifiedOperatorRegistry) ValidateArgs(opName string, argCount int) error {
	entry, exists := r.Get(opName)
	if !exists {
		return fmt.Errorf("unknown operator: %s", opName)
	}

	if argCount < entry.MinArgs {
		return fmt.Errorf("operator %s requires at least %d arguments, got %d",
			opName, entry.MinArgs, argCount)
	}

	if entry.MaxArgs != -1 && argCount > entry.MaxArgs {
		return fmt.Errorf("operator %s accepts at most %d arguments, got %d",
			opName, entry.MaxArgs, argCount)
	}

	return nil
}

// Clear removes all operators from the registry.
func (r *UnifiedOperatorRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.operators = make(map[string]*UnifiedOperatorEntry)
}

// Count returns the number of registered operators.
func (r *UnifiedOperatorRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.operators)
}

// Clone creates a copy of the registry.
func (r *UnifiedOperatorRegistry) Clone() *UnifiedOperatorRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	newRegistry := NewUnifiedOperatorRegistry()
	for name, entry := range r.operators {
		// Create a copy of the entry
		entryCopy := *entry
		newRegistry.operators[name] = &entryCopy
	}

	return newRegistry
}

// Migration helpers to ease transition from old registries

// RegisterUnifiedOperator registers an operator into the DefaultRegistry.
func RegisterUnifiedOperator(name string, op Operator) error {
	// Look up metadata from OperatorInfoRegistry if it exists
	if info, exists := OperatorInfoRegistry[name]; exists {
		return DefaultRegistry.RegisterOperator(name, op, info)
	}

	// If no metadata exists, create default metadata
	defaultInfo := OperatorInfo{
		Name:       name,
		Precedence: PrecedenceCall,
		MinArgs:    0,
		MaxArgs:    -1,
		Phase:      EvalPhase,
	}

	return DefaultRegistry.RegisterOperator(name, op, defaultInfo)
}

// GetUnifiedOperator retrieves an operator implementation (backward compatible).
func GetUnifiedOperator(name string) (Operator, bool) {
	return DefaultRegistry.GetImplementation(name)
}

// MigrateFromLegacyRegistries populates the unified registry from the OperatorInfoRegistry.
// Since OpRegistry has been removed, this only registers metadata-only entries.
func MigrateFromLegacyRegistries() error {
	// Register any operators that are only in OperatorInfoRegistry
	for name, info := range OperatorInfoRegistry {
		if !DefaultRegistry.IsRegistered(name) {
			// Create a NullOperator for metadata-only entries
			nullOp := NullOperator{Missing: name}
			if err := DefaultRegistry.RegisterOperator(name, nullOp, info); err != nil {
				return fmt.Errorf("failed to register metadata-only operator %s: %w", name, err)
			}
		}
	}

	return nil
}

// PopulateCompleteRegistry populates the unified registry with ALL known operators
// This ensures we have a complete set including those defined in StandardOperatorRegistry.
func PopulateCompleteRegistry() error {
	// Register all standard operators with complete metadata
	operators := []struct {
		name          string
		precedence    Precedence
		associativity Associativity
		minArgs       int
		maxArgs       int
		phase         OperatorPhase
	}{
		// Arithmetic operators (infix)
		{"+", PrecedenceAddition, LeftAssociative, 2, 2, EvalPhase},
		{"-", PrecedenceAddition, LeftAssociative, 2, 2, EvalPhase},
		{"*", PrecedenceMultiplication, LeftAssociative, 2, 2, EvalPhase},
		{"/", PrecedenceMultiplication, LeftAssociative, 2, 2, EvalPhase},
		{"%", PrecedenceMultiplication, LeftAssociative, 2, 2, EvalPhase},

		// Comparison operators (infix)
		{"==", PrecedenceEquality, LeftAssociative, 2, 2, EvalPhase},
		{"!=", PrecedenceEquality, LeftAssociative, 2, 2, EvalPhase},
		{"<", PrecedenceComparison, LeftAssociative, 2, 2, EvalPhase},
		{">", PrecedenceComparison, LeftAssociative, 2, 2, EvalPhase},
		{"<=", PrecedenceComparison, LeftAssociative, 2, 2, EvalPhase},
		{">=", PrecedenceComparison, LeftAssociative, 2, 2, EvalPhase},

		// Logical operators (infix)
		{"&&", PrecedenceAnd, LeftAssociative, 2, 2, EvalPhase},
		{"||", PrecedenceOr, LeftAssociative, 2, 2, EvalPhase},

		// Unary operators (prefix)
		{"!", PrecedenceUnary, RightAssociative, 1, 1, EvalPhase},

		// Ternary operator
		{"?:", PrecedenceTernary, RightAssociative, 3, 3, EvalPhase},

		// Function-style operators
		{"concat", PrecedenceCall, LeftAssociative, 1, -1, MergePhase},
		{"defer", PrecedenceCall, LeftAssociative, 1, -1, MergePhase},
		{"grab", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"vault", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"awsparam", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"awssecret", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"base64", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"calc", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"empty", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"envvar", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"except", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"inject", PrecedenceCall, LeftAssociative, 2, 2, EvalPhase},
		{"join", PrecedenceCall, LeftAssociative, 2, -1, EvalPhase},
		{"json", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"jsonpath", PrecedenceCall, LeftAssociative, 2, 2, EvalPhase},
		{"jsonschema", PrecedenceCall, LeftAssociative, 2, 2, EvalPhase},
		{"keys", PrecedenceCall, LeftAssociative, 0, -1, EvalPhase},
		{"load", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"param", PrecedenceCall, LeftAssociative, 1, 1, ParamPhase},
		{"prune", PrecedenceCall, LeftAssociative, 0, -1, EvalPhase},
		{"raw_env", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"regexp", PrecedenceCall, LeftAssociative, 2, 3, EvalPhase},
		{"sort", PrecedenceCall, LeftAssociative, 0, 1, EvalPhase},
		{"split", PrecedenceCall, LeftAssociative, 2, 2, EvalPhase},
		{"static_ips", PrecedenceCall, LeftAssociative, 1, -1, EvalPhase},
		{"stringify", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"yamlencode", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
		{"yamldecode", PrecedenceCall, LeftAssociative, 1, 1, EvalPhase},
	}

	// Register all operators, using existing implementation if available
	for _, op := range operators {
		// Check if we already have a real (non-null) implementation
		var impl Operator
		if existing, exists := DefaultRegistry.Get(op.name); exists && existing.Implementation != nil {
			// Whatever is registered wins, NullOperator included: a
			// NullOperator placeholder is what a not-yet-imported
			// operator is supposed to resolve to.
			impl = existing.Implementation
		} else {
			// No implementation found, use NullOperator
			impl = NullOperator{Missing: op.name}
		}

		// Create the unified entry
		entry := &UnifiedOperatorEntry{
			Name:           op.name,
			Precedence:     op.precedence,
			Associativity:  op.associativity,
			MinArgs:        op.minArgs,
			MaxArgs:        op.maxArgs,
			Phase:          op.phase,
			Implementation: impl,
		}

		if err := DefaultRegistry.Register(entry); err != nil {
			return fmt.Errorf("failed to register operator %s: %w", op.name, err)
		}
	}

	return nil
}
