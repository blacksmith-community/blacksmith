package graft

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// MultiError aggregates multiple errors into a single error value.
type MultiError struct {
	Errors []error
}

// Error returns a formatted string containing all aggregated errors.
//
// Default output (GRAFT_ERROR_CODES unset) is byte-identical to spruce's
// "N error(s) detected:\n - $.path: msg\n" format; genesis parses this with
// the regex ^\s*-\s*\$\.(\S+?):, so any change here is a compatibility
// break. When GRAFT_ERROR_CODES is enabled, entries that resolve to a
// *PathError with a known ErrorCode gain a "[Ecode] " prefix on the message
// segment, after the "$.path: " prefix that regex depends on, so opted-in
// output still matches it. See docs/reference/error-codes.md.
//
// The aggregated messages themselves are emitted verbatim: only the
// leading count is color-processed, so a message quoting document
// content cannot have an "@X{...}" sequence in it silently stripped or
// turned into live ANSI.
func (e MultiError) Error() string {
	codesEnabled := errorCodesEnabled()
	s := []string{}
	for _, err := range e.Errors {
		if codesEnabled {
			var pe *PathError
			if errors.As(err, &pe) {
				if code := pe.Code(); code != "" {
					s = append(s, fmt.Sprintf(" - $.%s: [%s] %s\n", pe.Path, code, pe.Cause.Error()))
					continue
				}
			}
		}
		s = append(s, fmt.Sprintf(" - %s\n", err))
	}

	sort.Strings(s)
	// Colorize only the count. ansi.Sprintf formats first and then
	// processes @X{...} directives over the whole result, so wrapping the
	// joined body would reinterpret every byte of every aggregated
	// message: an "@r{...}" sequence quoted from a user's document is
	// deleted when color is off and injected as live ANSI when it is on.
	// Each error already resolved its own directives when it was built
	// with ansi.Errorf, so the outer pass was double-processing.
	return ansi.Sprintf("@r{%d}", len(e.Errors)) +
		fmt.Sprintf(" error(s) detected:\n%s\n", strings.Join(s, ""))
}

// Count returns the number of errors in this MultiError.
func (e *MultiError) Count() int {
	return len(e.Errors)
}

// Unwrap returns the aggregated errors for Go 1.20+ multi-error
// unwrapping, so errors.Is/errors.As traverse into each error this
// MultiError carries (e.g. errors.As(evalErr, &backendErr) reaching a
// *BackendError nested inside one of e.Errors - see
// docs/developer-guide/custom-backends.md's "Errors" section). This is
// purely additive: it does not change Error()'s output, which remains the
// byte-identical, genesis-parsed format described on that method's doc
// comment.
func (e MultiError) Unwrap() []error {
	return e.Errors
}

// Append adds an error to this MultiError, unpacking nested MultiErrors.
func (e *MultiError) Append(err error) {
	if err == nil {
		return
	}

	var mult MultiError
	if errors.As(err, &mult) {
		e.Errors = append(e.Errors, mult.Errors...)
	} else {
		e.Errors = append(e.Errors, err)
	}
}

// WarningError should produce a warning message to stderr if the context set for
// the error fits the context the error was caught in.
type WarningError struct {
	warning string
	context ErrorContext
}

// An ErrorContext is a flag or set of flags representing the contexts that
// an error should have a special meaning in.
type ErrorContext uint

// Bitwise-or these together to represent several contexts.
const (
	eContextAll          = 0
	eContextDefaultMerge = 1 << iota
)

// dontPrintWarning is read by Warn from whichever goroutine an operator
// happens to run on under parallel evaluation, so it is atomic rather
// than a plain bool - see TestSilenceWarningsIsGoroutineSafe.
var dontPrintWarning atomic.Bool

// NewWarningError returns a new WarningError object that has the given warning
// message and context(s) assigned. Assigning no context should mean that all
// contexts are active. Ansi library enabled.
func NewWarningError(context ErrorContext, warning string, args ...interface{}) (err WarningError) {
	err.warning = ansi.Sprintf(warning, args...)
	err.context = context
	return
}

// SilenceWarnings when called with true will make it so that warnings will not
// print when Warn is called. Calling it with false will make warnings visible
// again. Warnings will print by default.
func SilenceWarnings(should bool) {
	dontPrintWarning.Store(should)
}

// Error will return the configured warning message as a string.
func (e WarningError) Error() string {
	return e.warning
}

// HasContext returns true if the WarningError was configured with the given context (or all).
// False otherwise.
func (e WarningError) HasContext(context ErrorContext) bool {
	return e.context == 0 || (context&e.context > 0)
}

// Warn prints the configured warning to stderr.
func (e WarningError) Warn() {
	if !dontPrintWarning.Load() {
		log.PrintStdErrf("%s", ansi.Sprintf("@Y{warning:} %s\n", e.warning))
	}
}

// Enhanced error types for library use

// GraftError is the base error type for all graft operations.
// The name is intentionally verbose to avoid confusion with the error interface.
//
//nolint:revive // GraftError name is intentional to avoid shadowing the error interface
type GraftError struct {
	Type    ErrorType
	Message string
	Path    string
	Cause   error
}

func (e *GraftError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %s", e.Type, e.Path, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *GraftError) Unwrap() error {
	return e.Cause
}

// ErrorType represents different categories of errors.
type ErrorType string

const (
	// ParseError indicates a YAML/JSON parsing error.
	ParseError ErrorType = "parse_error"

	// MergeError indicates an error during document merging.
	MergeError ErrorType = "merge_error"

	// EvaluationError indicates an error during operator evaluation.
	EvaluationError ErrorType = "evaluation_error"

	// OperatorError indicates an error with a specific operator.
	OperatorError ErrorType = "operator_error"

	// ConfigurationError indicates an invalid configuration.
	ConfigurationError ErrorType = "configuration_error"

	// ValidationError indicates invalid input or state.
	ValidationError ErrorType = "validation_error"

	// ExternalError indicates an error from external services (Vault, AWS).
	ExternalError ErrorType = "external_error"
)

// NewParseError creates a new parse error.
func NewParseError(message string, cause error) *GraftError {
	return &GraftError{
		Type:    ParseError,
		Message: message,
		Cause:   cause,
	}
}

// NewMergeError creates a new merge error.
func NewMergeError(message string, cause error) *GraftError {
	return &GraftError{
		Type:    MergeError,
		Message: message,
		Cause:   cause,
	}
}

// NewEvaluationError creates a new evaluation error with path context.
func NewEvaluationError(path, message string, cause error) *GraftError {
	return &GraftError{
		Type:    EvaluationError,
		Message: message,
		Path:    path,
		Cause:   cause,
	}
}

// NewOperatorError creates a new operator error.
func NewOperatorError(operator, message string, cause error) *GraftError {
	return &GraftError{
		Type:    OperatorError,
		Message: fmt.Sprintf("operator '%s': %s", operator, message),
		Cause:   cause,
	}
}

// NewConfigurationError creates a new configuration error.
func NewConfigurationError(message string) *GraftError {
	return &GraftError{
		Type:    ConfigurationError,
		Message: message,
	}
}

// NewValidationError creates a new validation error.
func NewValidationError(message string) *GraftError {
	return &GraftError{
		Type:    ValidationError,
		Message: message,
	}
}

// NewValidationErrorWithCause creates a validation error like
// NewValidationError, but attaches cause to the Unwrap chain so
// errors.Is/errors.As can see through it. Error() is unaffected: it never
// references Cause, so this is a purely additive change for call sites
// that were previously discarding the underlying error (see
// tree.ErrNotFound, tree.ErrTypeMismatch, tree.ErrInvalidPath and their
// pkg/graft re-exports below).
func NewValidationErrorWithCause(message string, cause error) *GraftError {
	return &GraftError{
		Type:    ValidationError,
		Message: message,
		Cause:   cause,
	}
}

// NewExternalError creates a new external service error.
func NewExternalError(service, message string, cause error) *GraftError {
	return &GraftError{
		Type:    ExternalError,
		Message: fmt.Sprintf("%s: %s", service, message),
		Cause:   cause,
	}
}

// IsGraftError checks if an error is a GraftError.
func IsGraftError(err error) bool {
	var graftErr *GraftError
	return errors.As(err, &graftErr)
}

// Sentinel errors for errors.Is comparisons against Document getter
// failures. These are the same values as tree.ErrInvalidPath,
// tree.ErrTypeMismatch, and tree.ErrNotFound (re-exported here so library
// callers never need to import pkg/graft/tree directly); the canonical
// definitions and their Is() hookups onto SyntaxError/TypeMismatchError/
// NotFoundError live in that package to avoid an import cycle (see the
// comment above tree.ErrInvalidPath).
//
// errors.Is(err, ErrNotFound), errors.Is(err, ErrTypeMismatch), and
// errors.Is(err, ErrInvalidPath) work against errors returned by Document's
// Get/GetString/GetInt/GetInt64/GetFloat64/GetBool/GetSlice/GetMap/
// GetStringSlice/GetMapStringString/Set/Delete, and against tree package
// errors directly. None of the Error() strings produced by those methods
// change as a result: Cause is invisible to Error().
var (
	// ErrNotFound indicates a path does not exist in the document.
	ErrNotFound = tree.ErrNotFound

	// ErrTypeMismatch indicates a path resolved to a value of a different
	// type than the caller requested.
	ErrTypeMismatch = tree.ErrTypeMismatch

	// ErrInvalidPath indicates a path string could not be parsed.
	ErrInvalidPath = tree.ErrInvalidPath
)

// hiddenCauseError preserves an existing, contractually pinned Error()
// string exactly while attaching cause to the Unwrap chain. Used at call
// sites (document.go's GetInt64/GetFloat64/GetStringSlice/
// GetMapStringString) that previously returned a bare fmt.Errorf with no
// wrapping: adding "%w" to those format strings would append the cause's
// own Error() text to the message, which the genesis compatibility
// contract forbids. This type carries the cause invisibly instead.
type hiddenCauseError struct {
	msg   string
	cause error
}

func (e *hiddenCauseError) Error() string { return e.msg }
func (e *hiddenCauseError) Unwrap() error { return e.cause }

// withHiddenCause wraps msg, an already-fully-formatted error message,
// with cause for errors.Is/errors.As, without altering the visible
// message text.
func withHiddenCause(msg string, cause error) error {
	return &hiddenCauseError{msg: msg, cause: cause}
}

// Error codes
//
// Codes are opt-in, stable, machine-readable classifications layered on top
// of the existing error model. They never change any Error() string; they
// are surfaced either programmatically (ClassifyError, GraftError.Code(),
// PathError.Code()) or, for per-path merge/evaluation errors, in CLI
// stderr when GRAFT_ERROR_CODES is enabled (see MultiError.Error()).
//
// Every code below has at least one triggering path in real graft code —
// see docs/reference/error-codes.md for the authoritative table and
// pkg/graft/errors_test.go for the corresponding tests. Codes are grouped
// by the same E1xx/E2xx/E3xx/E4xx/E9xx ranges graft's error taxonomy has
// always used informally (parse, evaluation, merge, backend, system).

// ErrorCode identifies the stable, machine-readable classification of a
// graft error. The zero value "" means "unclassified": a real error that
// exists but has no assigned code, not an error condition itself.
type ErrorCode string

const (
	// CodeParseError means YAML/JSON parsing or (( if/for/while/case ))
	// control-flow expansion failed before evaluation could begin.
	// Triggered by GraftError{Type: ParseError}, e.g. Engine.ParseYAML,
	// Engine.ParseJSON, or control-flow expansion (engine.go).
	CodeParseError ErrorCode = "E100"

	// CodePathSyntaxError means a graft reference/path expression (e.g.
	// inside (( grab )) or (( sort by )) ) could not be parsed as a
	// cursor. Triggered by tree.SyntaxError.
	CodePathSyntaxError ErrorCode = "E101"

	// CodeEvaluationError means a generic operator-evaluation failure that
	// does not fall into a more specific category below. Triggered by
	// GraftError{Type: EvaluationError}.
	CodeEvaluationError ErrorCode = "E200"

	// CodeReferenceNotFound means a reference expression resolved to a
	// path that does not exist in the document, e.g.
	// (( grab missing.path )). Triggered by tree.NotFoundError.
	CodeReferenceNotFound ErrorCode = "E201"

	// CodeTypeMismatch means a path was expected to hold one kind of value
	// (map, list, scalar) but held another. Triggered by
	// tree.TypeMismatchError, raised both while resolving references
	// during operator evaluation and while merging/sorting.
	CodeTypeMismatch ErrorCode = "E202"

	// CodeCircularReference means the operator data-flow graph has a cycle
	// (e.g. (( grab a )) / (( grab b )) referencing each other). Triggered
	// by the data-flow cycle detector (evaluator.go
	// kahnSort/CheckForCycles).
	CodeCircularReference ErrorCode = "E203"

	// CodeParamRequired means an (( param "..." )) placeholder was never
	// overridden by a later document. Triggered by ParamOperator.
	CodeParamRequired ErrorCode = "E204"

	// CodeUnknownOperator means "(( someop ... ))" referenced an operator
	// name that is not registered.
	CodeUnknownOperator ErrorCode = "E205"

	// CodeArgumentCount means an operator was called with the wrong number
	// of arguments, or a required argument was missing/nil. Covers the
	// "requires exactly/at least/one or two N argument(s)", "too few
	// arguments supplied to (( ... ))", "no arguments specified to
	// (( ... ))", and "<operator> operator expects N argument(s)"
	// message families used across pkg/graft/operators.
	CodeArgumentCount ErrorCode = "E206"

	// CodeDivisionByZero means (( a / b )) or (( a % b )) with a zero (or
	// null) divisor.
	CodeDivisionByZero ErrorCode = "E207"

	// CodeUnsupportedTarget means "(( op@target ... ))" was used on an
	// operator that does not support @target selection.
	CodeUnsupportedTarget ErrorCode = "E210"

	// CodeMergeError means documents could not be merged for a reason
	// other than a type mismatch. Triggered by
	// GraftError{Type: MergeError}.
	CodeMergeError ErrorCode = "E300"

	// CodeValidationError means a structural/path operation on the library
	// Document or MergeBuilder API was invalid (empty path, path segment
	// not found, array index out of bounds, navigating through a
	// non-container value, etc). Triggered by
	// GraftError{Type: ValidationError}.
	CodeValidationError ErrorCode = "E301"

	// CodeExternalError means a generic external-service integration
	// failure raised via the library's NewExternalError constructor. No
	// graft-internal call site currently constructs this; it exists for
	// library consumers implementing custom operators/backends.
	CodeExternalError ErrorCode = "E400"

	// CodeSecretNotFound means a Vault secret path or field did not exist.
	// Triggered by the vault operator's "secret <key> not found"
	// normalization of internal/backends/vault.ErrNotFound.
	CodeSecretNotFound ErrorCode = "E403"

	// CodeConfigurationError means an engine/library configuration value
	// was invalid (e.g. negative concurrency). Triggered by
	// GraftError{Type: ConfigurationError}.
	CodeConfigurationError ErrorCode = "E900"

	// CodeFileNotFound means a file referenced by (( file )) does not
	// exist. (( load )) does not trigger this: it checks os.Stat first and
	// returns a generic "not a file or usable URI" message for a missing
	// path rather than propagating fs.ErrNotExist. CLI merge input files
	// don't trigger it either: their read errors are flattened into
	// ansi-formatted strings before they ever reach ClassifyError,
	// destroying the fs.ErrNotExist chain (cmd/graft/main.go readFile).
	CodeFileNotFound ErrorCode = "E901"

	// CodePermissionDenied means a file referenced by (( file )) could not
	// be read due to permissions. Same (( load ))/CLI-input caveats as
	// CodeFileNotFound apply.
	CodePermissionDenied ErrorCode = "E902"
)

// CodedError is implemented by errors that carry a stable ErrorCode. Use
// errors.As(err, &coded) to find the nearest CodedError in an error chain;
// ClassifyError does this plus a set of type/message-based fallbacks for
// errors that were never explicitly tagged.
type CodedError interface {
	error
	Code() ErrorCode
}

// Code implements CodedError for GraftError, mapping each ErrorType to its
// corresponding code. Returns "" for an ErrorType that has no assigned
// code (there are none today, but the switch stays explicit rather than a
// blanket default so a future ErrorType is a visible gap, not a silent
// misclassification).
func (e *GraftError) Code() ErrorCode {
	switch e.Type {
	case ParseError:
		return CodeParseError
	case MergeError:
		return CodeMergeError
	case EvaluationError:
		return CodeEvaluationError
	case OperatorError:
		// OperatorError's constructor (NewOperatorError) wraps an
		// arbitrary caller-supplied message ("operator '%s': %s") and is
		// only used by example/documentation code today
		// (pkg/graft/examples.go), not by any real operator failure path.
		// Its message is not reliably an argument-count problem — mapping
		// it to CodeArgumentCount would misclassify whatever future code
		// actually starts constructing it. Left unclassified until a real
		// call site exists to say what it should be.
		return ""
	case ConfigurationError:
		return CodeConfigurationError
	case ValidationError:
		return CodeValidationError
	case ExternalError:
		return CodeExternalError
	default:
		return ""
	}
}

// codedError tags an existing error with an ErrorCode without altering its
// Error() string, for call sites where the semantic category is known
// precisely and message-pattern matching would be fragile or ambiguous
// (e.g. (( param )) failures, whose message is arbitrary user text).
type codedError struct {
	err  error
	code ErrorCode
}

func (e *codedError) Error() string   { return e.err.Error() }
func (e *codedError) Unwrap() error   { return e.err }
func (e *codedError) Code() ErrorCode { return e.code }

// WithCode tags err with code, preserving err's Error() string and Unwrap
// chain exactly. Returns nil if err is nil (mirrors fmt.Errorf/errors.New
// callers that check "if err != nil" before wrapping).
func WithCode(err error, code ErrorCode) error {
	if err == nil {
		return nil
	}
	return &codedError{err: err, code: code}
}

// PathError associates an error with the "$."-style dotted document path
// where it occurred (e.g. Path "database.host" for an error at
// $.database.host). It replaces the ad hoc fmt.Errorf("$.%s: %w", ...)
// previously used at this call site (pkg/graft/interfaces.go Opcall.Run);
// Error() produces byte-identical output ("$.<path>: <cause>") to that
// former format, so wrapping every per-operator error in PathError instead
// of a bare fmt.Errorf does not change default CLI output.
type PathError struct {
	// Path is the dotted document path, without a leading "$." (e.g.
	// "database.host", or the literal "<generated>" for operator calls
	// with no associated tree location).
	Path string
	// Cause is the underlying error returned by the operator/component
	// that ran at Path.
	Cause error
}

// Error returns "$.<path>: <cause>", matching the pre-PathError format
// exactly.
func (e *PathError) Error() string {
	return fmt.Sprintf("$.%s: %s", e.Path, e.Cause.Error())
}

// Unwrap returns Cause, so errors.Is/errors.As see through PathError to
// classify or compare the underlying error.
func (e *PathError) Unwrap() error {
	return e.Cause
}

// Code classifies Cause via ClassifyError. Returns "" if Cause is
// unclassified.
func (e *PathError) Code() ErrorCode {
	return ClassifyError(e.Cause)
}

// PartialEvaluationError wraps an evaluation failure with the
// partially-evaluated document tree that existed at the moment of
// failure: every operator that already succeeded holds its resolved
// value, and every operator that failed (or was never reached, e.g. a
// dependent of a failed operator) still carries its original,
// unevaluated "(( ... ))" text. Engine.Evaluate returns this instead of
// a bare error whenever evaluation fails, so a caller that wants a
// partial result - graft merge --defer-on-error/--adaptive; see
// cmd/graft's adaptive-merge loop - can retrieve Tree via errors.As.
//
// This is purely additive for every other caller: Error() delegates
// verbatim to Err's own message (preserving the genesis-compat-contract
// "N error(s) detected:\n - $.path: msg" byte format that MultiError.
// Error() produces - see that method's doc comment), and Unwrap()
// exposes Err for further errors.Is/errors.As traversal (e.g.
// mergeBuilderImpl's isMergerError, which does errors.As(err,
// &multiErr) and does not need to change), so a caller that only checks
// "err != nil", or unwraps to a *MultiError/*PathError/other error type
// as it already does today, sees no behavior change at all.
type PartialEvaluationError struct {
	// Err is the original evaluation error (typically a MultiError of
	// *PathError values, one per failed operator - see RunOps).
	Err error
	// Tree is the partially-evaluated document as of the failure. Never
	// nil (Engine.Evaluate always supplies one, via NewDocument, which
	// is itself nil-safe).
	Tree Document
}

// Error returns Err's message verbatim.
func (e *PartialEvaluationError) Error() string {
	return e.Err.Error()
}

// Unwrap exposes Err for errors.Is/errors.As.
func (e *PartialEvaluationError) Unwrap() error {
	return e.Err
}

// ClassifyError returns the ErrorCode for err, or "" if err does not match
// any known code. It checks, in order: an explicit CodedError anywhere in
// err's Unwrap chain (GraftError.Code(), or a WithCode tag); well-known
// tree package error types; well-known stdlib filesystem sentinel errors;
// and finally a small set of message patterns for operator errors that are
// constructed as plain errors across many call sites in
// pkg/graft/operators (argument-count checks, "unknown operator: ...",
// division by zero, and the data-flow cycle detector), where tagging every
// individual call site would be far more invasive than matching their
// existing, stable, non-user-controlled message text.
func ClassifyError(err error) ErrorCode {
	if err == nil {
		return ""
	}

	var coded CodedError
	if errors.As(err, &coded) {
		if code := coded.Code(); code != "" {
			return code
		}
	}

	var notFoundErr tree.NotFoundError
	if errors.As(err, &notFoundErr) {
		return CodeReferenceNotFound
	}

	var typeMismatchErr tree.TypeMismatchError
	if errors.As(err, &typeMismatchErr) {
		return CodeTypeMismatch
	}

	var syntaxErr tree.SyntaxError
	if errors.As(err, &syntaxErr) {
		return CodePathSyntaxError
	}

	if errors.Is(err, fs.ErrNotExist) {
		return CodeFileNotFound
	}
	if errors.Is(err, fs.ErrPermission) {
		return CodePermissionDenied
	}

	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "unknown operator: "):
		return CodeUnknownOperator
	case strings.Contains(msg, "cycle detected"):
		return CodeCircularReference
	case strings.Contains(msg, "division by zero"), strings.Contains(msg, "division by null"):
		return CodeDivisionByZero
	case strings.Contains(msg, "requires exactly"),
		strings.Contains(msg, "requires at least"),
		strings.Contains(msg, "requires one or two"),
		strings.Contains(msg, "too few arguments"),
		strings.Contains(msg, "no arguments specified to"),
		strings.Contains(msg, "operator expects"):
		return CodeArgumentCount
	}

	return ""
}

// errorCodesEnabled reports whether GRAFT_ERROR_CODES opts the current
// process into ErrorCode-annotated CLI stderr output (see
// MultiError.Error()). Unset, empty, or any value other than the
// recognized truthy forms below leaves default (byte-identical) output in
// effect.
func errorCodesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GRAFT_ERROR_CODES"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// GetErrorType returns the error type if it's a GraftError, empty string otherwise.
func GetErrorType(err error) ErrorType {
	var graftErr *GraftError
	if errors.As(err, &graftErr) {
		return graftErr.Type
	}
	return ""
}
