package graft

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
)

// ExprError represents an error that occurred during expression parsing or evaluation.
type ExprError struct {
	Type     ExprErrorType
	Message  string
	Position Position
	Source   string
	Context  string
	Nested   error
}

// ExprErrorType categorizes expression errors.
type ExprErrorType int

// ExprErrorType constants.
const (
	// SyntaxError represents a syntax error in an expression.
	SyntaxError ExprErrorType = iota
	// TypeError represents a type mismatch error.
	TypeError
	// ReferenceError represents a reference resolution error.
	ReferenceError
	// ExprEvaluationError represents an error during evaluation.
	ExprEvaluationError
	// ExprOperatorError represents an operator-related error.
	ExprOperatorError
)

// Position tracks location in source.
type Position struct {
	Offset int    // Byte offset in source
	Line   int    // 1-based line number
	Column int    // 1-based column number
	File   string // Optional filename
}

// Error implements the error interface.
func (e *ExprError) Error() string {
	var parts []string

	// Add error type prefix
	prefix := e.typeString()
	if prefix != "" {
		parts = append(parts, ansi.Sprintf("@*R{%s}", prefix))
	}

	// Add position information
	if e.Position.Line > 0 {
		loc := fmt.Sprintf("%d:%d", e.Position.Line, e.Position.Column)
		if e.Position.File != "" {
			loc = e.Position.File + ":" + loc
		}
		parts = append(parts, ansi.Sprintf("@Y{%s}", loc))
	}

	// Add main message
	parts = append(parts, e.Message)

	// Build error message
	msg := strings.Join(parts, ": ")

	// Add source context if available
	if e.Source != "" && e.Position.Line > 0 {
		lines := strings.Split(e.Source, "\n")
		if e.Position.Line <= len(lines) {
			msg += "\n\n" + e.formatSourceContext(lines)
		}
	}

	// Add nested error
	if e.Nested != nil {
		msg += "\n  caused by: " + e.Nested.Error()
	}

	return msg
}

// Unwrap returns the nested error.
func (e *ExprError) Unwrap() error {
	return e.Nested
}

// typeString returns a string representation of the error type.
func (e *ExprError) typeString() string {
	switch e.Type {
	case SyntaxError:
		return "Syntax Error"
	case TypeError:
		return "Type Error"
	case ReferenceError:
		return "Reference Error"
	case ExprEvaluationError:
		return "Evaluation Error"
	case ExprOperatorError:
		return "Operator Error"
	default:
		return ""
	}
}

// formatSourceContext returns a formatted view of the source around the error.
func (e *ExprError) formatSourceContext(lines []string) string {
	lineIdx := e.Position.Line - 1
	if lineIdx < 0 || lineIdx >= len(lines) {
		return ""
	}

	var context strings.Builder

	// Show up to 2 lines before and after
	start := lineIdx - 2
	if start < 0 {
		start = 0
	}
	end := lineIdx + 3
	if end > len(lines) {
		end = len(lines)
	}

	// Add line numbers and content
	for i := start; i < end; i++ {
		lineNum := fmt.Sprintf("%4d | ", i+1)
		if i == lineIdx {
			// Highlight error line
			context.WriteString(ansi.Sprintf("@*W{%s}", lineNum))
			context.WriteString(lines[i])
			context.WriteString("\n")

			// Add error indicator
			spaces := strings.Repeat(" ", len(lineNum)+e.Position.Column-1)
			context.WriteString(ansi.Sprintf("@R{%s^}", spaces))

			// Add context message if provided
			if e.Context != "" {
				context.WriteString(ansi.Sprintf(" @R{%s}", e.Context))
			}
			context.WriteString("\n")
		} else {
			context.WriteString(ansi.Sprintf("@K{%s%s}\n", lineNum, lines[i]))
		}
	}

	return context.String()
}

// ExprErrorList collects multiple expression errors.
type ExprErrorList struct {
	Errors []*ExprError
}

// Add adds an error to the list.
func (el *ExprErrorList) Add(err *ExprError) {
	el.Errors = append(el.Errors, err)
}

// Error implements the error interface.
func (el *ExprErrorList) Error() string {
	if len(el.Errors) == 0 {
		return "no errors"
	}

	msgs := make([]string, 0, len(el.Errors)+1)
	msgs = append(msgs, fmt.Sprintf("Found %d errors:", len(el.Errors)))

	for i, err := range el.Errors {
		msgs = append(msgs, fmt.Sprintf("\n[%d] %s", i+1, err.Error()))
	}

	return strings.Join(msgs, "\n")
}

// HasErrors returns true if there are any errors.
func (el *ExprErrorList) HasErrors() bool {
	return len(el.Errors) > 0
}

// NewSyntaxError creates a new syntax error.
func NewSyntaxError(msg string, pos Position) *ExprError {
	return &ExprError{
		Type:     SyntaxError,
		Message:  msg,
		Position: pos,
	}
}

// NewTypeError creates a new type error.
func NewTypeError(msg string, pos Position) *ExprError {
	return &ExprError{
		Type:     TypeError,
		Message:  msg,
		Position: pos,
	}
}

// NewReferenceError creates a new reference error.
func NewReferenceError(msg string, pos Position) *ExprError {
	return &ExprError{
		Type:     ReferenceError,
		Message:  msg,
		Position: pos,
	}
}

// NewExprEvaluationError creates a new evaluation error.
func NewExprEvaluationError(msg string, pos Position) *ExprError {
	return &ExprError{
		Type:     ExprEvaluationError,
		Message:  msg,
		Position: pos,
	}
}

// NewExprOperatorError creates a new operator error.
func NewExprOperatorError(msg string, pos Position) *ExprError {
	return &ExprError{
		Type:     ExprOperatorError,
		Message:  msg,
		Position: pos,
	}
}

// WithSource adds source code context to an error.
func (e *ExprError) WithSource(source string) *ExprError {
	e.Source = source
	return e
}

// WithContext adds a context message to an error.
func (e *ExprError) WithContext(context string) *ExprError {
	e.Context = context
	return e
}

// WithNested wraps another error.
func (e *ExprError) WithNested(err error) *ExprError {
	e.Nested = err
	return e
}

// WrapError converts a regular error to an ExprError.
func WrapError(err error, errType ExprErrorType, pos Position) *ExprError {
	if err == nil {
		return nil
	}

	// If it's already an ExprError, preserve it
	var exprErr *ExprError
	if errors.As(err, &exprErr) {
		return exprErr
	}

	return &ExprError{
		Type:     errType,
		Message:  err.Error(),
		Position: pos,
		Nested:   err,
	}
}

// ErrorRecoveryContext helps the parser recover from errors.
type ErrorRecoveryContext struct {
	Errors      *ExprErrorList
	MaxErrors   int
	StopOnFirst bool
}

// NewErrorRecoveryContext creates a new error recovery context.
func NewErrorRecoveryContext(maxErrors int) *ErrorRecoveryContext {
	return &ErrorRecoveryContext{
		Errors:    &ExprErrorList{},
		MaxErrors: maxErrors,
	}
}

// RecordError records an error and returns whether parsing should continue.
func (erc *ErrorRecoveryContext) RecordError(err *ExprError) bool {
	erc.Errors.Add(err)

	if erc.StopOnFirst {
		return false
	}

	return len(erc.Errors.Errors) < erc.MaxErrors
}

// HasErrors returns true if any errors were recorded.
func (erc *ErrorRecoveryContext) HasErrors() bool {
	return erc.Errors.HasErrors()
}

// GetError returns the collected errors as a single error.
func (erc *ErrorRecoveryContext) GetError() error {
	if !erc.HasErrors() {
		return nil
	}

	if len(erc.Errors.Errors) == 1 {
		return erc.Errors.Errors[0]
	}

	return erc.Errors
}
