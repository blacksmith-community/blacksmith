package interfaces

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorCollector accumulates multiple errors during parsing.
type ErrorCollector interface {
	// AddError adds an error to the collection
	AddError(err error)

	// HasErrors returns true if any errors have been collected
	HasErrors() bool

	// Errors returns all collected errors
	Errors() []error

	// Clear removes all collected errors
	Clear()

	// ErrorsAsString returns all errors formatted as a single string
	ErrorsAsString() string
}

// SyntaxError represents a parsing syntax error.
type SyntaxError struct {
	Message    string
	Position   Position
	Source     string
	Expected   []string // What was expected
	Got        string   // What was actually found
	Suggestion string   // Optional suggestion for fix
}

func (e *SyntaxError) Error() string {
	if len(e.Expected) > 0 {
		expected := strings.Join(e.Expected, " or ")
		return fmt.Sprintf("syntax error at line %d, column %d: expected %s, got %s",
			e.Position.Line, e.Position.Column, expected, e.Got)
	}
	return fmt.Sprintf("syntax error at line %d, column %d: %s",
		e.Position.Line, e.Position.Column, e.Message)
}

// TypeError represents a type-related error.
type TypeError struct {
	Message    string
	Position   Position
	Expected   string
	Got        string
	Expression string
}

func (e *TypeError) Error() string {
	return fmt.Sprintf("type error at line %d, column %d: %s (expected %s, got %s)",
		e.Position.Line, e.Position.Column, e.Message, e.Expected, e.Got)
}

// ReferenceError represents an invalid reference error.
type ReferenceError struct {
	Message   string
	Position  Position
	Reference string
	Segment   string // Which segment is invalid
}

func (e *ReferenceError) Error() string {
	return fmt.Sprintf("reference error at line %d, column %d: %s in '%s'",
		e.Position.Line, e.Position.Column, e.Message, e.Reference)
}

// OperatorError represents an operator-related error.
type OperatorError struct {
	Message      string
	Position     Position
	OperatorName string
	Phase        OperatorPhase
	Arguments    int
	ExpectedArgs string
}

func (e *OperatorError) Error() string {
	return fmt.Sprintf("operator error at line %d, column %d: %s for operator '%s'",
		e.Position.Line, e.Position.Column, e.Message, e.OperatorName)
}

// InternalError represents an unexpected internal parser error.
type InternalError struct {
	Message string
	Context string
	Cause   error
}

func (e *InternalError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("internal parser error in %s: %s (caused by: %v)",
			e.Context, e.Message, e.Cause)
	}
	return fmt.Sprintf("internal parser error in %s: %s", e.Context, e.Message)
}

// SourceContext provides context for error reporting.
type SourceContext struct {
	Source   string
	Position Position
	Length   int
}

// GetLineContent returns the content of the line containing the error.
func (s *SourceContext) GetLineContent() string {
	lines := strings.Split(s.Source, "\n")
	if s.Position.Line <= len(lines) && s.Position.Line > 0 {
		return lines[s.Position.Line-1]
	}
	return ""
}

// GetErrorHighlight returns a string that highlights the error position.
func (s *SourceContext) GetErrorHighlight() string {
	line := s.GetLineContent()
	if line == "" {
		return ""
	}

	// Create a highlight string with spaces and carets
	highlight := strings.Repeat(" ", s.Position.Column-1)
	if s.Length > 0 {
		highlight += strings.Repeat("^", s.Length)
	} else {
		highlight += "^"
	}

	return highlight
}

// ErrorFormatter provides formatted error messages with context.
type ErrorFormatter interface {
	// FormatError formats an error with source context
	FormatError(err error, context *SourceContext) string

	// FormatMultipleErrors formats multiple errors
	FormatMultipleErrors(errors []error, source string) string
}

// DefaultErrorFormatter provides a standard error formatting implementation.
type DefaultErrorFormatter struct {
	ShowLineNumbers bool
	ShowContext     bool
	ContextLines    int // Number of context lines to show
}

// FormatError formats a single error with context information.
func (f *DefaultErrorFormatter) FormatError(err error, context *SourceContext) string {
	var result strings.Builder

	// Write the error message
	result.WriteString(err.Error())
	result.WriteString("\n")

	if f.ShowContext && context != nil {
		// Show the problematic line
		line := context.GetLineContent()
		if line != "" {
			if f.ShowLineNumbers {
				_, _ = fmt.Fprintf(&result, "%4d | %s\n", context.Position.Line, line)
				result.WriteString("     | ")
			} else {
				result.WriteString(line + "\n")
			}

			// Show the error highlight
			highlight := context.GetErrorHighlight()
			result.WriteString(highlight + "\n")
		}
	}

	return result.String()
}

// FormatMultipleErrors formats multiple errors with source context.
func (f *DefaultErrorFormatter) FormatMultipleErrors(errs []error, source string) string {
	var result strings.Builder

	_, _ = fmt.Fprintf(&result, "Found %d error(s):\n\n", len(errs))

	for i, err := range errs {
		_, _ = fmt.Fprintf(&result, "Error %d:\n", i+1)

		// Try to extract position information for context
		if posErr, ok := err.(interface{ Position() Position }); ok {
			context := &SourceContext{
				Source:   source,
				Position: posErr.Position(),
			}
			result.WriteString(f.FormatError(err, context))
		} else {
			result.WriteString(f.FormatError(err, nil))
		}

		if i < len(errs)-1 {
			result.WriteString("\n")
		}
	}

	return result.String()
}

// Error factory functions for common error types

// NewSyntaxError creates a new syntax error.
func NewSyntaxError(msg string, pos Position, source string) *SyntaxError {
	return &SyntaxError{
		Message:  msg,
		Position: pos,
		Source:   source,
	}
}

// NewExpectedTokenError creates an error for unexpected tokens.
func NewExpectedTokenError(expected []string, got string, pos Position, source string) *SyntaxError {
	return &SyntaxError{
		Message:  "unexpected token",
		Position: pos,
		Source:   source,
		Expected: expected,
		Got:      got,
	}
}

// NewUnclosedStringError creates an error for unclosed string literals.
func NewUnclosedStringError(pos Position, source string) *SyntaxError {
	return &SyntaxError{
		Message:    "unclosed string literal",
		Position:   pos,
		Source:     source,
		Suggestion: "add closing quote",
	}
}

// NewInvalidReferenceError creates an error for invalid references.
func NewInvalidReferenceError(ref, segment string, pos Position) *ReferenceError {
	return &ReferenceError{
		Message:   fmt.Sprintf("invalid reference segment '%s'", segment),
		Position:  pos,
		Reference: ref,
		Segment:   segment,
	}
}

// NewOperatorNotFoundError creates an error for unknown operators.
func NewOperatorNotFoundError(name string, pos Position) *OperatorError {
	return &OperatorError{
		Message:      fmt.Sprintf("unknown operator '%s'", name),
		Position:     pos,
		OperatorName: name,
	}
}

// NewWrongPhaseError creates an error for operators used in wrong phases.
func NewWrongPhaseError(name string, phase OperatorPhase, pos Position) *OperatorError {
	return &OperatorError{
		Message:      fmt.Sprintf("operator '%s' not available in this phase", name),
		Position:     pos,
		OperatorName: name,
		Phase:        phase,
	}
}

// NewArgumentCountError creates an error for wrong argument counts.
func NewArgumentCountError(name, expected string, got int, pos Position) *OperatorError {
	return &OperatorError{
		Message:      fmt.Sprintf("wrong number of arguments: expected %s, got %d", expected, got),
		Position:     pos,
		OperatorName: name,
		Arguments:    got,
		ExpectedArgs: expected,
	}
}

// Recovery strategies for error handling

// Tokenizer interface for error recovery - abstracts token stream operations.
type Tokenizer interface {
	NextToken() *Token
	HasMore() bool
}

// ErrorRecovery defines strategies for recovering from parse errors.
type ErrorRecovery interface {
	// CanRecover determines if recovery is possible for this error
	CanRecover(err error) bool

	// Recover attempts to recover from the error and continue parsing
	// Returns true if recovery was successful
	Recover(err error, tokenizer Tokenizer) bool
}

// PanicRecovery recovers by advancing to the next operator boundary.
type PanicRecovery struct{}

// CanRecover determines if panic recovery is possible.
func (r *PanicRecovery) CanRecover(err error) bool {
	// Can recover from most syntax errors but not internal errors
	var internalErr *InternalError
	return !errors.As(err, &internalErr)
}

// Recover attempts to recover by skipping to the next operator boundary.
func (r *PanicRecovery) Recover(err error, tokenizer Tokenizer) bool {
	// Skip tokens until we find an operator boundary or EOF
	for tokenizer.HasMore() {
		token := tokenizer.NextToken()
		if token.Type == TokenOperatorEnd || token.Type == TokenEOF {
			return true
		}
	}
	return false
}
