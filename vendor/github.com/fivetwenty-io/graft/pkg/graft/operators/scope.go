// Package operators provides operator implementations for graft expressions.
package operators

import (
	"fmt"
	"time"
)

// -----------------------------------------------------------------------------
// Scope - Variable scope management for control flow blocks
// -----------------------------------------------------------------------------

// MaxScopeDepth is the maximum nesting depth allowed for scopes.
// This prevents stack overflow from deeply nested control flow structures.
const MaxScopeDepth = 100

// Scope represents a variable scope for control flow blocks.
// Scopes form a chain where child scopes can access parent variables,
// but parent scopes cannot access child variables.
type Scope struct {
	parent    *Scope
	variables map[string]interface{}
	depth     int
}

// NewScope creates a new root scope with no parent.
func NewScope() *Scope {
	return &Scope{
		variables: make(map[string]interface{}),
		depth:     0,
	}
}

// NewChildScope creates a child scope that inherits from this scope.
// Returns an error if the maximum scope depth would be exceeded.
func (s *Scope) NewChildScope() (*Scope, error) {
	if s.depth >= MaxScopeDepth {
		return nil, fmt.Errorf("maximum scope depth exceeded (%d)", MaxScopeDepth)
	}
	return &Scope{
		parent:    s,
		variables: make(map[string]interface{}),
		depth:     s.depth + 1,
	}, nil
}

// Set sets a variable in the current scope.
// If the variable exists in a parent scope, this creates a shadow
// in the current scope (the parent's value remains unchanged).
func (s *Scope) Set(name string, value interface{}) {
	s.variables[name] = value
}

// Get retrieves a variable, searching up the scope chain.
// Returns the value and true if found, nil and false otherwise.
func (s *Scope) Get(name string) (interface{}, bool) {
	if value, exists := s.variables[name]; exists {
		return value, true
	}
	if s.parent != nil {
		return s.parent.Get(name)
	}
	return nil, false
}

// Has checks if a variable exists in any scope (current or parent).
func (s *Scope) Has(name string) bool {
	_, exists := s.Get(name)
	return exists
}

// SetLocal sets a variable only in the current scope, creating a shadow
// of any variable with the same name in a parent scope.
// This is equivalent to Set but makes the intent explicit.
func (s *Scope) SetLocal(name string, value interface{}) {
	s.variables[name] = value
}

// Update updates a variable in the scope where it was originally defined.
// If the variable exists in the current scope, it's updated here.
// If not, the update propagates to parent scopes.
// Returns true if the variable was found and updated, false otherwise.
func (s *Scope) Update(name string, value interface{}) bool {
	if _, exists := s.variables[name]; exists {
		s.variables[name] = value
		return true
	}
	if s.parent != nil {
		return s.parent.Update(name, value)
	}
	return false
}

// Depth returns the current nesting depth of this scope.
// The root scope has depth 0.
func (s *Scope) Depth() int {
	return s.depth
}

// Parent returns the parent scope, or nil for root scopes.
func (s *Scope) Parent() *Scope {
	return s.parent
}

// All returns all variables visible from this scope as a map.
// Variables from parent scopes are included, but if a variable is
// shadowed in a child scope, only the child's value is included.
func (s *Scope) All() map[string]interface{} {
	result := make(map[string]interface{})
	s.collectAll(result)
	return result
}

// collectAll is a helper that collects variables from parent to child.
// Child variables override parent variables with the same name.
func (s *Scope) collectAll(result map[string]interface{}) {
	if s.parent != nil {
		s.parent.collectAll(result)
	}
	for k, v := range s.variables {
		result[k] = v
	}
}

// Local returns only the variables defined directly in this scope,
// not including variables from parent scopes.
func (s *Scope) Local() map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range s.variables {
		result[k] = v
	}
	return result
}

// Clear removes all variables from the current scope.
// Parent scope variables remain accessible.
func (s *Scope) Clear() {
	s.variables = make(map[string]interface{})
}

// Delete removes a variable from the current scope.
// Does not affect parent scopes.
// Returns true if the variable existed in this scope, false otherwise.
func (s *Scope) Delete(name string) bool {
	if _, exists := s.variables[name]; exists {
		delete(s.variables, name)
		return true
	}
	return false
}

// Size returns the number of variables defined directly in this scope.
func (s *Scope) Size() int {
	return len(s.variables)
}

// TotalSize returns the total number of unique variables visible from this scope.
func (s *Scope) TotalSize() int {
	return len(s.All())
}

// -----------------------------------------------------------------------------
// LoopLimitError - Error for loop safety limits
// -----------------------------------------------------------------------------

// The limit kinds a LoopLimitError.Type can carry.
const (
	loopLimitIterations = "iterations"
	loopLimitDuration   = "duration"
)

// LoopLimitError is returned when a loop exceeds its safety limits.
type LoopLimitError struct {
	// Type indicates what limit was exceeded: "iterations" or "duration"
	Type string

	// Limit is the configured limit that was exceeded
	Limit int

	// Iterations is the number of iterations that were executed
	Iterations int
}

// Error returns a human-readable error message.
func (e *LoopLimitError) Error() string {
	if e.Type == loopLimitIterations {
		return fmt.Sprintf("loop exceeded maximum iterations (%d)", e.Limit)
	}
	return fmt.Sprintf("loop exceeded maximum duration (%d seconds, %d iterations)",
		e.Limit, e.Iterations)
}

// IsIterationLimit returns true if the error is due to exceeding iteration limit.
func (e *LoopLimitError) IsIterationLimit() bool {
	return e.Type == loopLimitIterations
}

// IsDurationLimit returns true if the error is due to exceeding duration limit.
func (e *LoopLimitError) IsDurationLimit() bool {
	return e.Type == loopLimitDuration
}

// -----------------------------------------------------------------------------
// LoopGuard - Safety limits for loops
// -----------------------------------------------------------------------------

// Default limits for loop safety.
const (
	// DefaultMaxIterations is the default maximum number of loop iterations.
	DefaultMaxIterations = 10000

	// DefaultMaxDuration is the default maximum duration for loop execution.
	DefaultMaxDuration = 30 * time.Second
)

// LoopGuard provides safety limits for loops to prevent infinite execution.
// It tracks iteration count and elapsed time, returning an error if limits
// are exceeded.
type LoopGuard struct {
	maxIterations int
	iterations    int
	startTime     time.Time
	maxDuration   time.Duration
}

// NewLoopGuard creates a new loop guard with default limits.
func NewLoopGuard() *LoopGuard {
	return &LoopGuard{
		maxIterations: DefaultMaxIterations,
		iterations:    0,
		startTime:     time.Now(),
		maxDuration:   DefaultMaxDuration,
	}
}

// WithMaxIterations sets a custom iteration limit and returns the guard
// for method chaining.
func (g *LoopGuard) WithMaxIterations(maxIter int) *LoopGuard {
	g.maxIterations = maxIter
	return g
}

// WithMaxDuration sets a custom duration limit and returns the guard
// for method chaining.
func (g *LoopGuard) WithMaxDuration(d time.Duration) *LoopGuard {
	g.maxDuration = d
	return g
}

// Check increments the iteration counter and checks all limits.
// Returns nil if within limits, or a LoopLimitError if exceeded.
// This should be called at the beginning of each loop iteration.
func (g *LoopGuard) Check() error {
	g.iterations++

	if g.iterations > g.maxIterations {
		return &LoopLimitError{
			Type:       loopLimitIterations,
			Limit:      g.maxIterations,
			Iterations: g.iterations,
		}
	}

	if time.Since(g.startTime) > g.maxDuration {
		return &LoopLimitError{
			Type:       loopLimitDuration,
			Limit:      int(g.maxDuration.Seconds()),
			Iterations: g.iterations,
		}
	}

	return nil
}

// Iterations returns the current iteration count.
func (g *LoopGuard) Iterations() int {
	return g.iterations
}

// Elapsed returns the time elapsed since the guard was created or reset.
func (g *LoopGuard) Elapsed() time.Duration {
	return time.Since(g.startTime)
}

// Reset resets the guard for reuse, clearing the iteration counter
// and resetting the start time.
func (g *LoopGuard) Reset() {
	g.iterations = 0
	g.startTime = time.Now()
}

// MaxIterations returns the configured maximum iterations limit.
func (g *LoopGuard) MaxIterations() int {
	return g.maxIterations
}

// MaxDuration returns the configured maximum duration limit.
func (g *LoopGuard) MaxDuration() time.Duration {
	return g.maxDuration
}

// RemainingIterations returns the number of iterations remaining before
// the limit is reached. Returns 0 if limit is already exceeded.
func (g *LoopGuard) RemainingIterations() int {
	remaining := g.maxIterations - g.iterations
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingDuration returns the time remaining before the duration limit
// is reached. Returns 0 if limit is already exceeded.
func (g *LoopGuard) RemainingDuration() time.Duration {
	remaining := g.maxDuration - time.Since(g.startTime)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// -----------------------------------------------------------------------------
// ScopeStack - Stack of scopes for nested control flow
// -----------------------------------------------------------------------------

// ScopeStack manages a stack of scopes for nested control flow constructs.
// It always maintains at least one scope (the root scope) which cannot be popped.
type ScopeStack struct {
	scopes []*Scope
}

// NewScopeStack creates a new scope stack with a root scope.
func NewScopeStack() *ScopeStack {
	return &ScopeStack{
		scopes: []*Scope{NewScope()},
	}
}

// Push creates and pushes a new child scope onto the stack.
// Returns an error if the maximum scope depth would be exceeded.
func (ss *ScopeStack) Push() error {
	current := ss.Current()
	child, err := current.NewChildScope()
	if err != nil {
		return err
	}
	ss.scopes = append(ss.scopes, child)
	return nil
}

// Pop removes and returns the top scope from the stack.
// Returns nil if attempting to pop the root scope (root cannot be popped).
func (ss *ScopeStack) Pop() *Scope {
	if len(ss.scopes) <= 1 {
		return nil // Can't pop root scope
	}
	popped := ss.scopes[len(ss.scopes)-1]
	ss.scopes = ss.scopes[:len(ss.scopes)-1]
	return popped
}

// Current returns the scope at the top of the stack.
func (ss *ScopeStack) Current() *Scope {
	return ss.scopes[len(ss.scopes)-1]
}

// Root returns the root scope at the bottom of the stack.
func (ss *ScopeStack) Root() *Scope {
	return ss.scopes[0]
}

// Depth returns the current nesting depth (number of scopes above root).
// Root scope has depth 0.
func (ss *ScopeStack) Depth() int {
	return len(ss.scopes) - 1
}

// Size returns the total number of scopes in the stack.
func (ss *ScopeStack) Size() int {
	return len(ss.scopes)
}

// Get retrieves a variable from the current scope (including parent lookup).
func (ss *ScopeStack) Get(name string) (interface{}, bool) {
	return ss.Current().Get(name)
}

// Set sets a variable in the current scope.
func (ss *ScopeStack) Set(name string, value interface{}) {
	ss.Current().Set(name, value)
}

// Has checks if a variable exists in any visible scope.
func (ss *ScopeStack) Has(name string) bool {
	return ss.Current().Has(name)
}

// Update updates a variable in the scope where it was defined.
func (ss *ScopeStack) Update(name string, value interface{}) bool {
	return ss.Current().Update(name, value)
}

// All returns all variables visible from the current scope.
func (ss *ScopeStack) All() map[string]interface{} {
	return ss.Current().All()
}

// Clear clears all non-root scopes, leaving only the root scope.
func (ss *ScopeStack) Clear() {
	ss.scopes = ss.scopes[:1]
}

// Reset clears all scopes including the root and creates a fresh root scope.
func (ss *ScopeStack) Reset() {
	ss.scopes = []*Scope{NewScope()}
}
