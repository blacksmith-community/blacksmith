package tree

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
)

// NameFields is a slice of common field names used for object identification.
var NameFields = []string{"name", "key", "id"}

// Canonical sentinels for errors.Is comparisons against this package's error
// types. They live here, rather than in pkg/graft, because the Is methods
// below must be defined on SyntaxError/TypeMismatchError/NotFoundError
// themselves (see the doc comments on those methods for why), and this
// package cannot import pkg/graft without creating an import cycle
// (pkg/graft already imports pkg/graft/tree). pkg/graft/errors.go
// re-exports these three values under the same names so library callers
// never need to import this package directly.
var (
	// ErrInvalidPath is the sentinel for SyntaxError: a path string could
	// not be parsed as a cursor.
	ErrInvalidPath = errors.New("invalid path")

	// ErrTypeMismatch is the sentinel for TypeMismatchError: a path
	// resolved to a value of a different type than expected.
	ErrTypeMismatch = errors.New("type mismatch")

	// ErrNotFound is the sentinel for NotFoundError: a path does not exist
	// in the data structure.
	ErrNotFound = errors.New("path not found")
)

// Cursor represents a path through YAML/JSON data structure.
type Cursor struct {
	Nodes []string
}

// SyntaxError represents a syntax error in path parsing.
type SyntaxError struct {
	Problem  string
	Position int
}

// Error returns the error message for SyntaxError.
func (e SyntaxError) Error() string {
	return fmt.Sprintf("syntax error: %s at position %d", e.Problem, e.Position)
}

// Is reports whether target is ErrInvalidPath, so callers can use
// errors.Is(err, tree.ErrInvalidPath) against a wrapped SyntaxError without
// depending on its Error() text. Declared with a value receiver, matching
// how SyntaxError is constructed and returned throughout this package
// (e.g. ParseCursor); a pointer-receiver Is here would never be found by
// errors.Is because the values placed in error chains are SyntaxError, not
// *SyntaxError.
func (e SyntaxError) Is(target error) bool {
	return target == ErrInvalidPath
}

// The phrasings a TypeMismatchError uses when a path descends into a
// value that cannot be descended into.
const (
	wantedContainer = "a map or a list"
	gotScalar       = "a scalar"
)

// TypeMismatchError represents a type mismatch during path resolution.
type TypeMismatchError struct {
	Path   []string
	Wanted string
	Got    string
	Value  interface{}
}

// Error returns the error message for TypeMismatchError with ANSI coloring.
func (e TypeMismatchError) Error() string {
	if e.Got == "" {
		return ansi.Sprintf("@c{%s} @R{is not} @m{%s}", strings.Join(e.Path, "."), e.Wanted)
	}
	if e.Value != nil {
		return ansi.Sprintf("@c{$.%s} @R{[=%v] is %s (not} @m{%s}@R{)}", strings.Join(e.Path, "."), e.Value, e.Got, e.Wanted)
	}
	return ansi.Sprintf("@C{$.%s} @R{is %s (not} @m{%s}@R{)}", strings.Join(e.Path, "."), e.Got, e.Wanted)
}

// Is reports whether target is ErrTypeMismatch. Value receiver: see
// SyntaxError.Is for why (TypeMismatchError values, not pointers, are what
// gets returned and wrapped, e.g. evaluator.go).
func (e TypeMismatchError) Is(target error) bool {
	return target == ErrTypeMismatch
}

// NotFoundError represents when a path cannot be found in the data structure.
type NotFoundError struct {
	Path []string
}

// Error returns the error message for NotFoundError with ANSI coloring.
func (e NotFoundError) Error() string {
	return ansi.Sprintf("@R{`}@c{$.%s}@R{` could not be found in the datastructure}", strings.Join(e.Path, "."))
}

// Is reports whether target is ErrNotFound. Value receiver: see
// SyntaxError.Is for why (NotFoundError values, not pointers, are what
// gets returned and wrapped, e.g. evaluator.go:795).
func (e NotFoundError) Is(target error) bool {
	return target == ErrNotFound
}
