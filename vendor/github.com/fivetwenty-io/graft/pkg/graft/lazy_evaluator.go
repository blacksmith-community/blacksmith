package graft

import (
	"fmt"
	"sync"
	"time"
)

// LazyExpression represents an expression that is evaluated only when needed.
type LazyExpression struct {
	expr          *Expr
	evaluator     *Evaluator
	lazyEvaluator *LazyEvaluator // Reference to the lazy evaluator that owns this
	result        interface{}
	error         error
	evaluated     bool
	mu            sync.RWMutex
	deps          []*LazyExpression // Dependencies
}

// LazyEvaluator manages lazy evaluation of expressions.
type LazyEvaluator struct {
	expressions map[string]*LazyExpression
	mu          sync.RWMutex
	stats       LazyEvaluationStats
}

// LazyEvaluationStats tracks lazy evaluation performance.
type LazyEvaluationStats struct {
	TotalExpressions int64
	EvaluatedCount   int64
	SkippedCount     int64
	CacheHits        int64
	EvaluationTime   time.Duration
	DependencyChecks int64
}

// NewLazyEvaluator creates a new lazy evaluator.
func NewLazyEvaluator() *LazyEvaluator {
	return &LazyEvaluator{
		expressions: make(map[string]*LazyExpression),
	}
}

// NewLazyExpression creates a new lazy expression.
func NewLazyExpression(expr *Expr, evaluator *Evaluator) *LazyExpression {
	return &LazyExpression{
		expr:      expr,
		evaluator: evaluator,
		evaluated: false,
	}
}

// NewLazyExpressionWithEvaluator creates a new lazy expression with reference to its lazy evaluator.
func NewLazyExpressionWithEvaluator(expr *Expr, evaluator *Evaluator, lazyEvaluator *LazyEvaluator) *LazyExpression {
	return &LazyExpression{
		expr:          expr,
		evaluator:     evaluator,
		lazyEvaluator: lazyEvaluator,
		evaluated:     false,
	}
}

// Evaluate forces evaluation of the lazy expression.
func (le *LazyExpression) Evaluate() (interface{}, error) {
	le.mu.Lock()
	defer le.mu.Unlock()

	if le.evaluated {
		return le.result, le.error
	}

	// Check dependencies first
	for _, dep := range le.deps {
		if _, err := dep.Evaluate(); err != nil {
			le.error = fmt.Errorf("dependency evaluation failed: %w", err)
			return nil, le.error
		}
	}

	// Evaluate the expression
	start := time.Now()
	result, err := le.evaluateExpression()
	duration := time.Since(start)

	le.result = result
	le.error = err
	le.evaluated = true

	// Update stats
	if le.lazyEvaluator != nil {
		le.lazyEvaluator.updateStats(duration, true)
	} else {
		// Fall back to global evaluator if no specific evaluator is set
		GlobalLazyEvaluator.updateStats(duration, true)
	}

	return le.result, le.error
}

// evaluateExpression performs the actual evaluation.
func (le *LazyExpression) evaluateExpression() (interface{}, error) {
	if le.expr == nil || le.evaluator == nil {
		return nil, fmt.Errorf("invalid lazy expression: missing expr or evaluator")
	}

	// Evaluate based on expression type
	switch le.expr.Type {
	case Literal:
		return le.expr.Literal, nil

	case Reference:
		if le.expr.Reference == nil {
			return nil, fmt.Errorf("invalid reference expression")
		}
		result, err := le.expr.Reference.Resolve(le.evaluator.Tree)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve reference: %w", err)
		}
		return result, nil

	case OperatorCall:
		// For operator calls, we need to evaluate through the normal operator system
		// This is simplified - in practice, we'd need proper operator lookup and execution
		opName := le.expr.Op()
		args := le.expr.Args()

		operator := OperatorForEngine(le.evaluator.engine, opName)
		if operator == nil {
			return nil, fmt.Errorf("unknown operator: %s", opName)
		}

		response, err := operator.Run(le.evaluator, args)
		if err != nil {
			return nil, err
		}

		return response.Value, nil

	case List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, LogicalOr, RegexpMatch, EnvVar, BoshVar, VaultGroup, VaultChoice:
		return nil, fmt.Errorf("unsupported expression type for lazy evaluation: %d", le.expr.Type)
	}
	return nil, fmt.Errorf("unsupported expression type for lazy evaluation: %d", le.expr.Type)
}

// IsEvaluated returns whether the expression has been evaluated.
func (le *LazyExpression) IsEvaluated() bool {
	le.mu.RLock()
	defer le.mu.RUnlock()
	return le.evaluated
}

// AddDependency adds a dependency to this lazy expression.
func (le *LazyExpression) AddDependency(dep *LazyExpression) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.deps = append(le.deps, dep)
}

// WrapExpression creates a lazy wrapper around an expression.
func (lev *LazyEvaluator) WrapExpression(expr *Expr, evaluator *Evaluator) *LazyExpression {
	lev.mu.Lock()
	defer lev.mu.Unlock()

	// Generate a key for the expression
	key := fmt.Sprintf("%p_%d", expr, time.Now().UnixNano())

	lazy := NewLazyExpressionWithEvaluator(expr, evaluator, lev)
	lev.expressions[key] = lazy
	lev.stats.TotalExpressions++

	return lazy
}

// EvaluateIfNeeded evaluates an expression only if its result is actually needed.
func (lev *LazyEvaluator) EvaluateIfNeeded(expr *Expr, evaluator *Evaluator) (interface{}, error) {
	lazy := lev.WrapExpression(expr, evaluator)

	// Check if this expression is actually needed for the final result
	if !lev.isExpressionNeeded(expr, evaluator) {
		lev.stats.SkippedCount++
		return nil, nil // Skip evaluation
	}

	return lazy.Evaluate()
}

// isExpressionNeeded determines if an expression needs to be evaluated
// This is a simplified heuristic - in practice, this would involve
// dependency analysis and dead code detection.
func (lev *LazyEvaluator) isExpressionNeeded(expr *Expr, evaluator *Evaluator) bool {
	// Simple heuristics:
	// 1. If expression is a literal, always evaluate (cheap)
	// 2. If expression is a reference to a commonly used path, evaluate
	// 3. If expression is an operator call that affects the current path, evaluate

	switch expr.Type {
	case Literal:
		return true // Literals are cheap to evaluate

	case Reference:
		// Check if this reference path is used elsewhere
		return true // For now, always evaluate references

	case OperatorCall:
		// Check if this operator call affects the current evaluation path
		opName := expr.Op()

		// Some operators like 'vault' might be expensive and should be lazy
		expensiveOps := map[string]bool{
			"vault":     true,
			"vault-try": true,
			"file":      true,
			"awsparam":  true,
			"awssecret": true,
		}

		if expensiveOps[opName] {
			// Only evaluate if the result is actually used
			return lev.isResultUsed(expr, evaluator)
		}
		return true // Other operators, evaluate normally

	case List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, LogicalOr, RegexpMatch, EnvVar, BoshVar, VaultGroup, VaultChoice:
		return true
	}
	return true
}

// isResultUsed checks if the result of an expression is actually used.
func (lev *LazyEvaluator) isResultUsed(expr *Expr, evaluator *Evaluator) bool {
	// This is a simplified implementation
	// In practice, this would involve analyzing the evaluation tree
	// and checking if the current path contributes to the final result

	// For now, assume all expressions are used
	return true
}

// updateStats updates evaluation statistics.
func (lev *LazyEvaluator) updateStats(duration time.Duration, evaluated bool) {
	lev.mu.Lock()
	defer lev.mu.Unlock()

	if evaluated {
		lev.stats.EvaluatedCount++
		lev.stats.EvaluationTime += duration
	}
	lev.stats.DependencyChecks++
}

// GetStats returns current evaluation statistics.
func (lev *LazyEvaluator) GetStats() LazyEvaluationStats {
	lev.mu.RLock()
	defer lev.mu.RUnlock()
	return lev.stats
}

// Reset clears all lazy expressions and resets stats.
func (lev *LazyEvaluator) Reset() {
	lev.mu.Lock()
	defer lev.mu.Unlock()

	lev.expressions = make(map[string]*LazyExpression)
	lev.stats = LazyEvaluationStats{}
}

// GlobalLazyEvaluator is the global lazy evaluator instance.
var GlobalLazyEvaluator = NewLazyEvaluator()

// Helper functions for integration with existing evaluator

// ShouldUseLazyEvaluation determines if lazy evaluation should be used.
func ShouldUseLazyEvaluation(expr *Expr) bool {
	// Enable lazy evaluation for expensive operations
	if expr.Type == OperatorCall {
		opName := expr.Op()
		expensiveOps := map[string]bool{
			"vault":     true,
			"vault-try": true,
			"file":      true,
			"awsparam":  true,
			"awssecret": true,
		}
		return expensiveOps[opName]
	}

	return false
}

// LazyEvaluateExpression evaluates an expression lazily if beneficial.
func LazyEvaluateExpression(expr *Expr, evaluator *Evaluator) (interface{}, error) {
	if ShouldUseLazyEvaluation(expr) {
		return GlobalLazyEvaluator.EvaluateIfNeeded(expr, evaluator)
	}

	// Fall back to normal evaluation
	return evaluateExpressionNormally(expr, evaluator)
}

// evaluateExpressionNormally performs standard expression evaluation by
// delegating to EvaluateExpr and unwrapping the Response value.
func evaluateExpressionNormally(expr *Expr, evaluator *Evaluator) (interface{}, error) {
	resp, err := EvaluateExpr(expr, evaluator)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Value, nil
}
