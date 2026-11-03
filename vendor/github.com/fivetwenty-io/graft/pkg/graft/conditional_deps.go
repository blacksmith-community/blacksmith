package graft

import (
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// DependencyType indicates whether a dependency is conditional or unconditional.
type DependencyType int

const (
	// UnconditionalDependency is always evaluated.
	UnconditionalDependency DependencyType = iota
	// ConditionalDependency may not be evaluated due to short-circuit logic.
	ConditionalDependency
)

// TrackedDependency represents a dependency with metadata about its type.
type TrackedDependency struct {
	Path *tree.Cursor
	Type DependencyType
}

// ConditionalDependencies enhances the Expr type to track conditional dependencies.
func (e *Expr) ConditionalDependencies(ev *Evaluator, locs []*tree.Cursor) []*TrackedDependency {
	deps := []*TrackedDependency{}

	switch e.Type {
	case Reference:
		if e.Reference != nil {
			deps = append(deps, &TrackedDependency{
				Path: e.Reference,
				Type: UnconditionalDependency,
			})
		}

	case OperatorCall:
		if e.Call != nil {
			// Get raw dependencies from the operator call
			rawDeps := e.Call.Dependencies(ev, locs)
			// Convert to tracked dependencies (all unconditional for now)
			for _, dep := range rawDeps {
				deps = append(deps, &TrackedDependency{
					Path: dep,
					Type: UnconditionalDependency,
				})
			}
		}

	case LogicalOr:
		// For || operator, track dependencies specially
		if e.Left != nil {
			// Left side is always evaluated
			leftDeps := e.Left.ConditionalDependencies(ev, locs)
			deps = append(deps, leftDeps...)
		}

		if e.Right != nil {
			// Right side dependencies are conditional
			// They're only evaluated if left side fails
			rightDeps := e.Right.ConditionalDependencies(ev, locs)

			// Check if left side is a literal - if so, right side won't execute
			if e.Left != nil && e.Left.Type == Literal {
				// Mark all right-side dependencies as conditional
				for _, dep := range rightDeps {
					dep.Type = ConditionalDependency
				}
			}

			deps = append(deps, rightDeps...)
		}

	case Literal, List, Or, Negate, Addition, Subtraction, Multiplication, Division, Modulo,
		Equal, NotEqual, LessThan, LessThanOrEqual, GreaterThan, GreaterThanOrEqual,
		LogicalAnd, RegexpMatch, EnvVar, BoshVar, VaultGroup, VaultChoice:
		// For other types, check left and right expressions
		if e.Left != nil {
			deps = append(deps, e.Left.ConditionalDependencies(ev, locs)...)
		}
		if e.Right != nil {
			deps = append(deps, e.Right.ConditionalDependencies(ev, locs)...)
		}
	}

	return deps
}

// ExtractUnconditionalPaths extracts only unconditional dependency paths.
func ExtractUnconditionalPaths(deps []*TrackedDependency) []*tree.Cursor {
	paths := []*tree.Cursor{}
	for _, dep := range deps {
		if dep.Type == UnconditionalDependency {
			paths = append(paths, dep.Path)
		}
	}
	return paths
}

// ExtractAllPaths extracts all dependency paths (both conditional and unconditional).
func ExtractAllPaths(deps []*TrackedDependency) []*tree.Cursor {
	paths := make([]*tree.Cursor, 0, len(deps))
	for _, dep := range deps {
		paths = append(paths, dep.Path)
	}
	return paths
}
