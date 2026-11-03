package graft

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

//nolint:gochecknoinits // Operator registry initialization must happen at package load time
func init() {
	// Ensure the DefaultRegistry is populated with complete metadata on first init.
	// All operator init() functions have already registered into DefaultRegistry via
	// RegisterUnifiedOperator, so this call only fills in metadata-only gaps.
	if DefaultRegistry.Count() == 0 {
		// Register any metadata-only entries from OperatorInfoRegistry
		if err := MigrateFromLegacyRegistries(); err != nil {
			log.DEBUG("Warning: Failed to migrate to unified registry: %v", err)
		}

		// Ensure all operators have complete metadata entries
		if err := PopulateCompleteRegistry(); err != nil {
			log.DEBUG("Warning: Failed to populate complete registry: %v", err)
		}
	}

	log.DEBUG("Operators initialized")
}

// ParseOpcallFunc is the registered parser function.
// The operators package sets this to its actual implementation.
var ParseOpcallFunc func(phase OperatorPhase, src string) (*Opcall, error)

// ParseOpcallForEngineFunc is the registered engine-aware parser function.
// The operators package sets this to its actual implementation alongside
// ParseOpcallFunc (same init(), same reason: operators imports graft, not
// the reverse, so the parser can only be reached through a hook variable).
var ParseOpcallForEngineFunc func(e Engine, phase OperatorPhase, src string) (*Opcall, error)

// ParseOpcall parses an operator call expression.
// It delegates to the registered parser function.
func ParseOpcall(phase OperatorPhase, src string) (*Opcall, error) {
	if ParseOpcallFunc != nil {
		return ParseOpcallFunc(phase, src)
	}
	return nil, fmt.Errorf("parser not initialized - operators package must be imported")
}

// ParseOpcallForEngine parses an operator call expression, resolving
// operator names against the engine's local registry (custom operators,
// RegisterOperator overrides) before falling back to the process-global
// DefaultRegistry. A nil engine, or an engine with no local override for an
// operator name, resolves identically to ParseOpcall.
func ParseOpcallForEngine(e Engine, phase OperatorPhase, src string) (*Opcall, error) {
	if ParseOpcallForEngineFunc != nil {
		return ParseOpcallForEngineFunc(e, phase, src)
	}
	return nil, fmt.Errorf("parser not initialized - operators package must be imported")
}

// RegisterOp is a helper function to register operators.
func RegisterOp(name string, op Operator) {
	if err := RegisterUnifiedOperator(name, op); err != nil {
		log.DEBUG("Warning: Failed to register %s in unified registry: %v", name, err)
	}
}

// DEBUG is a helper function for debug logging.
//
//nolint:goprintffuncname // DEBUG is an established name used throughout the codebase
func DEBUG(format string, args ...interface{}) {
	log.DEBUG(format, args...)
}

// TRACE is a helper function for trace logging.
//
//nolint:goprintffuncname // TRACE is an established name used throughout the codebase
func TRACE(format string, args ...interface{}) {
	log.TRACE(format, args...)
}

// SetupOperators initializes operators for a given phase.
func SetupOperators(phase OperatorPhase) error {
	// Operators are now registered through the engine or globally through init()
	// This function is kept for backward compatibility
	return nil
}

// OperatorFor returns the operator for the given name.
func OperatorFor(name string) Operator {
	if op, exists := DefaultRegistry.GetImplementation(name); exists {
		return op
	}

	// Return a NullOperator for unknown operators
	return NullOperator{Missing: name}
}

// OperatorForEngine returns the operator for the given name, preferring the
// engine's local registry (RegisterOperator overrides, WithCustomOperator
// entries applied at construction) over the process-global DefaultRegistry.
// A nil engine, or an engine that has no entry (local or inherited) for
// name, resolves identically to OperatorFor: the engine's registry is a
// full clone of DefaultRegistry at construction time, so any built-in
// operator resolves through either path, and a genuinely unknown name falls
// through to OperatorFor's NullOperator here too.
func OperatorForEngine(e Engine, name string) Operator {
	if e != nil {
		if op, exists := e.GetOperator(name); exists {
			return op
		}
	}
	return OperatorFor(name)
}

// NullOperator is a placeholder operator for unknown operations.
type NullOperator struct {
	Missing string
}

// Setup initializes the operator.
func (NullOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (NullOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (NullOperator) Dependencies(ev *Evaluator, args []*Expr, locs, auto []*tree.Cursor) []*tree.Cursor {
	return nil
}

// Run executes the operator.
func (n NullOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	// For unknown operators, return the original operator call string unchanged
	// This allows the template to be processed again later or remain as-is

	// Reconstruct the original operator call
	argStrings := make([]string, 0, len(args))
	for _, arg := range args {
		argStrings = append(argStrings, renderPassthroughArg(arg))
	}

	var argsStr string
	if len(argStrings) > 0 {
		argsStr = " " + argStrings[0]
		for _, arg := range argStrings[1:] {
			argsStr += " " + arg
		}
	}

	originalCall := fmt.Sprintf("(( %s%s ))", n.Missing, argsStr)

	return &Response{
		Type:  Replace,
		Value: originalCall,
	}, nil
}

// renderPassthroughArg reconstructs an argument's original source text for
// the unregistered-operator passthrough, so the call round-trips intact
// for a later pass (multi-pass genesis templating, engines with the
// operator registered). Mirrors op_defer.go's reconstructExpr, the same
// reconstruction defer uses.
func renderPassthroughArg(e *Expr) string {
	if e == nil {
		return ""
	}

	switch e.Type {
	case Literal:
		if e.Literal == nil {
			return "nil"
		}
		if s, ok := e.Literal.(string); ok {
			return fmt.Sprintf("%q", s)
		}
		return fmt.Sprintf("%v", e.Literal)

	case Reference:
		if e.Reference != nil {
			return e.Reference.String()
		}
		return ""

	case EnvVar:
		return fmt.Sprintf("$%s", e.Name)

	case LogicalOr:
		return fmt.Sprintf("%s || %s", renderPassthroughArg(e.Left), renderPassthroughArg(e.Right))

	case OperatorCall:
		op := e.Op()
		if op == "" {
			return e.String()
		}
		args := e.Args()
		if len(args) == 0 {
			return op
		}
		argStrs := make([]string, len(args))
		for i, arg := range args {
			argStrs[i] = renderPassthroughArg(arg)
		}
		return fmt.Sprintf("%s %s", op, strings.Join(argStrs, " "))

	case List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, RegexpMatch, BoshVar, VaultGroup, VaultChoice:
		return e.String()
	}
	return e.String()
}

// NewOpcall creates a new operator call.
func NewOpcall(op Operator, args []*Expr, src string) *Opcall {
	return &Opcall{
		op:   op,
		args: args,
		src:  src,
	}
}

// NewOpcallForExpr creates the operator call for evaluating a nested
// OperatorCall expression, carrying the "@target" and ":nocache" modifier
// the parser recorded on expr — NewOpcall cannot express either, and
// dropping them made "(( concat "p-" (vault:nocache "x") ))" silently
// serve a cached secret while the top-level form bypassed the cache. The
// operator is passed in rather than taken from expr.Call so callers can
// resolve it against the evaluating engine's registry (engine-local custom
// operators; see operators' evaluateNestedOperator).
func NewOpcallForExpr(op Operator, expr *Expr, src string) *Opcall {
	return &Opcall{
		op:      op,
		args:    expr.Args(),
		src:     src,
		name:    expr.Op(),
		target:  expr.Target,
		noCache: expr.HasModifier("nocache"),
	}
}

// DefaultKeyGenerator returns a key generator function
// This seems to be used for generating unique keys, possibly for caching.
func DefaultKeyGenerator() func() (string, error) {
	counter := 0
	return func() (string, error) {
		counter++
		return fmt.Sprintf("key-%d", counter), nil
	}
}

// isPruneOperator checks if a value represents a prune operator.
func isPruneOperator(val interface{}) bool {
	if str, ok := val.(string); ok {
		// Match patterns like "(( prune ))" with optional whitespace
		matched, _ := regexp.MatchString(`^\s*\(\(\s*prune\s*\)\)\s*$`, str)
		if matched {
			DEBUG("isPruneOperator: detected prune operator: %q", str)
		}
		return matched
	}

	// Also check for Opcall structures that represent prune operations
	if opcall, ok := val.(*Opcall); ok {
		if opcall != nil && opcall.op != nil {
			// Check if this is a prune operator
			if _, isPrune := opcall.op.(interface{ String() string }); isPrune {
				// This is a more complex check - for now, just check the source
				if strings.Contains(opcall.src, "prune") {
					DEBUG("isPruneOperator: detected prune opcall: %v", opcall.src)
					return true
				}
			}
		}
	}

	DEBUG("isPruneOperator: not a prune operator: %T %v", val, val)
	return false
}

// Merge merges two data structures.
func Merge(dst, src interface{}) error {
	// Deep merge implementation for maps
	dstMap, dstOk := dst.(map[string]interface{})
	srcMap, srcOk := src.(map[string]interface{})

	if !dstOk || !srcOk {
		return fmt.Errorf("Merge: both arguments must be maps")
	}

	// Deep merge all keys from src to dst
	for k, srcVal := range srcMap {
		if dstVal, exists := dstMap[k]; exists {
			// If destination has a prune operator, preserve it (prune takes precedence)
			if isPruneOperator(dstVal) {
				DEBUG("Merge: preserving prune operator at key %v", k)
				continue
			}

			// If both are maps, merge recursively
			if dstSubMap, dstIsMap := dstVal.(map[string]interface{}); dstIsMap {
				if srcSubMap, srcIsMap := srcVal.(map[string]interface{}); srcIsMap {
					err := Merge(dstSubMap, srcSubMap)
					if err != nil {
						return err
					}
					continue
				}
			}
		}
		// Otherwise just copy the value
		dstMap[k] = srcVal
	}

	return nil
}

// DebugOn returns true if debugging is enabled (log.DebugOn, toggled by the
// CLI's -d/--debug flag or by an engine constructed with WithDebugLogging
// or WithTraceLevel(TraceLevelDebug) / WithTraceLevel(TraceLevelTrace)).
func DebugOn() bool {
	return log.DebugOn
}
