package literal

import (
	"errors"
	"fmt"
)

// ErrNotCode is the error every refusal matches with errors.Is. An
// operator refuses when a value it would read as code, as a path, or as
// the name of something outside to read came from a literal.
var ErrNotCode = errors.New("a value from a literal is data, and graft never reads it as code or as a name")

// RefusedError is the error an operator returns when it refuses a value
// from a literal. It names the operator and what the operator would have
// read the value as, and it quotes nothing from the value.
type RefusedError struct {
	// Operator is the name of the operator that refused, such as "calc".
	Operator string
	// Role says what the operator would have read the value as, such as
	// "an expression" or "a secret path".
	Role string
}

// Refuse returns the RefusedError for operator and role.
func Refuse(operator, role string) error {
	return &RefusedError{Operator: operator, Role: role}
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("(( %s )) cannot use a value from a literal as %s, because a literal is always data", e.Operator, e.Role)
}

// IsRefusal reports whether err is, or wraps, an operator's refusal of
// a value from a literal. Every || fallback, vault default, and
// vault-try list asks it before trying another side, so a refusal is
// never hidden by what the other side would give.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrNotCode)
}

// Is reports whether target is ErrNotCode.
func (e *RefusedError) Is(target error) bool {
	return target == ErrNotCode
}

// The read hooks let the operators package count the reads of a literal
// that one operator call makes, on the evaluator that runs the call,
// without the evaluator exporting a field for it. pkg/graft sets them
// when it loads.
var (
	noteRead   func(ev any)
	countReads func(ev any) int
)

// SetReadHooks installs the functions NoteRead and Reads call. pkg/graft
// calls it once, from an init function.
func SetReadHooks(note func(ev any), count func(ev any) int) {
	noteRead = note
	countReads = count
}

// NoteRead records on ev that an operator read a value that is a
// literal, sits inside one, or holds one.
func NoteRead(ev any) {
	if noteRead != nil {
		noteRead(ev)
	}
}

// Reads returns how many reads of a literal ev has recorded. Only the
// difference between two calls means anything.
func Reads(ev any) int {
	if countReads != nil {
		return countReads(ev)
	}
	return 0
}

// StripReport is Strip, and it also reports whether v was a Value or
// held one, which is whether anything was stripped.
func StripReport(v any) (any, bool) {
	return strip(v, 0)
}
