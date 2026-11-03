// Package errtext gives graft's errors two texts. Error() is plain: it
// holds no ANSI escape codes and quotes no value from a document, so a
// library caller can log it or show it in an API. The detailed text is
// what the graft CLI prints, with the values and, when the ansi toggle
// is on, the color. pkg/graft exports Detail as graft.DetailedError.
//
// An error type joins in by implementing Detailer. Errors that only wrap
// others, such as the ones fmt.Errorf and errors.Join build, need
// nothing: Detail finds each wrapped error's plain text inside the
// wrapper's text and puts the detailed text in its place.
package errtext

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
)

// Detailer is an error whose detailed text differs from its Error()
// text.
type Detailer interface {
	error
	// DetailedError returns the text the graft CLI prints for the
	// error, which may quote document values and hold color.
	DetailedError() string
}

// Detail returns err's detailed text: DetailedError() when err is a
// Detailer, and otherwise err's own text with the detailed text of each
// error it wraps put in place of that error's plain text. It returns ""
// for a nil err.
func Detail(err error) string {
	if err == nil {
		return ""
	}
	if d, ok := err.(Detailer); ok { //nolint:errorlint // only err itself is asked; its chain is walked below
		return d.DetailedError()
	}
	msg := err.Error()
	switch u := err.(type) { //nolint:errorlint // the wrapper is unwrapped one level at a time
	case interface{ Unwrap() error }:
		return substituteOne(msg, u.Unwrap())
	case interface{ Unwrap() []error }:
		return substituteMany(msg, u.Unwrap())
	}
	return msg
}

// substituteOne puts child's detailed text in place of its plain text
// inside msg. The last occurrence is the one replaced, since a wrapper
// almost always ends with the error it wraps.
func substituteOne(msg string, child error) string {
	if child == nil {
		return msg
	}
	plain := child.Error()
	detail := Detail(child)
	if plain == detail {
		return msg
	}
	i := strings.LastIndex(msg, plain)
	if i < 0 {
		return msg
	}
	return msg[:i] + detail + msg[i+len(plain):]
}

// substituteMany does what substituteOne does for each of children, in
// order, each one searched for after the text the one before it took.
func substituteMany(msg string, children []error) string {
	var b strings.Builder
	pos := 0
	for _, child := range children {
		if child == nil {
			continue
		}
		plain := child.Error()
		i := strings.Index(msg[pos:], plain)
		if i < 0 {
			continue
		}
		b.WriteString(msg[pos : pos+i])
		b.WriteString(Detail(child))
		pos += i + len(plain)
	}
	if pos == 0 {
		return msg
	}
	b.WriteString(msg[pos:])
	return b.String()
}

// detailArg stands in for an error argument when an error's detailed
// text is formatted. It is itself an error, so a %w verb still accepts
// it, and its text is the detailed text of the error it replaces.
type detailArg struct {
	text string
}

func (a detailArg) Error() string { return a.text }

// detailArgs returns args with each error whose detailed text differs
// from its plain text replaced by a detailArg, and whether any was.
func detailArgs(args []any) ([]any, bool) {
	var out []any
	for i, arg := range args {
		err, ok := arg.(error)
		if !ok || err == nil {
			continue
		}
		detail := Detail(err)
		if detail == err.Error() {
			continue
		}
		if out == nil {
			out = append([]any(nil), args...)
		}
		out[i] = detailArg{text: detail}
	}
	return out, out != nil
}

// Errorf behaves exactly like fmt.Errorf, giving the same Error() text
// and the same Unwrap chain. Its detailed text formats the same
// arguments with each error argument's detailed text in place of its
// plain text, whether the verb is %w, %s, or %v. Callers use it where an
// error is formatted with a verb other than %w, which fmt.Errorf leaves
// out of the chain that Detail walks.
func Errorf(format string, args ...any) error {
	base := fmt.Errorf(format, args...)
	dargs, changed := detailArgs(args)
	if !changed {
		return base
	}
	detail := fmt.Errorf(format, dargs...).Error()
	return withDetail(base, detail)
}

// Colorf replaces ansi.Errorf. Its detailed text is what ansi.Errorf
// gives, with "@X{...}" color markup rendered when the ansi toggle is on
// as the error is built, and with each error argument's detailed text.
// Its Error() text drops the markup in format and writes the arguments
// verbatim, so markup inside a path or name stays as written. Like
// ansi.Errorf, it wraps nothing.
func Colorf(format string, args ...any) error {
	plain := ansi.SprintfTemplate(false, format, args...)
	dargs, changed := detailArgs(args)
	if !changed {
		dargs = args
	}
	return &textError{plain: plain, detail: ansi.Sprintf(format, dargs...)}
}

// New returns an error whose Error() text is plain and whose detailed
// text is detail.
func New(plain, detail string) error {
	return &textError{plain: plain, detail: detail}
}

// Wrap is New with cause on the Unwrap chain, so errors.Is and errors.As
// see it. Neither text includes cause's text unless the caller put it
// there.
func Wrap(cause error, plain, detail string) error {
	return &textError{plain: plain, detail: detail, cause: cause}
}

// WithPlain returns detail with its Error() text replaced by plain. The
// detailed text is Detail(detail). Callers pass the error they built
// before, which quotes values, as detail, and the same message without
// the values as plain.
//
// detail is kept off the Unwrap chain, since its text and the text of
// the errors it wraps quote values. errors.Is still sees every error in
// it, and errors.As sees the ones Exposable allows, so the sentinels and
// graft's own error types match as they did before.
func WithPlain(detail error, plain string) error {
	if detail == nil {
		return nil
	}
	return &plainError{plain: plain, detail: detail}
}

// textError is an error with fixed plain and detailed texts.
type textError struct {
	plain  string
	detail string
	cause  error
}

func (e *textError) Error() string         { return e.plain }
func (e *textError) DetailedError() string { return e.detail }
func (e *textError) Unwrap() error         { return e.cause }

// plainError is the error WithPlain builds.
type plainError struct {
	plain  string
	detail error
}

func (e *plainError) Error() string         { return e.plain }
func (e *plainError) DetailedError() string { return Detail(e.detail) }

// Is reports whether any error in detail matches target.
func (e *plainError) Is(target error) bool { return errors.Is(e.detail, target) }

// As finds the first error in detail that Exposable allows and that
// matches target.
func (e *plainError) As(target any) bool { return AsHidden(e.detail, target) }

// graftModule is the import path every graft package starts with.
const graftModule = "github.com/fivetwenty-io/graft"

// Exposable reports whether errors.As may hand err to a caller from
// inside an error that keeps it off the Unwrap chain. Only graft's own
// error types qualify, because their Error() text is plain and quotes no
// value. The types this package builds detailed text with, and every
// type from another module, do not: their text is what the wrapper
// hides.
func Exposable(err error) bool {
	switch err.(type) { //nolint:errorlint // the type of this one error, not its chain
	case *textError, *detailedOne, *detailedMulti:
		return false
	}
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	path := t.PkgPath()
	return path == graftModule || strings.HasPrefix(path, graftModule+"/")
}

// AsHidden is errors.As over hidden, an error kept off the Unwrap chain,
// with only the errors Exposable allows eligible to match. It walks
// hidden's whole tree, including the errors inside a type it does not
// allow, in the order errors.As would.
func AsHidden(hidden error, target any) bool {
	return AsHiddenWhere(hidden, target, Exposable)
}

// AsHiddenWhere is AsHidden with allow in place of Exposable. An error
// that allow rejects is never handed to the caller, either directly or
// through an As method of an error it does allow.
func AsHiddenWhere(hidden error, target any, allow func(error) bool) bool {
	val := reflect.ValueOf(target)
	if !val.IsValid() || val.Kind() != reflect.Pointer || val.IsNil() {
		return false
	}
	return asHidden(hidden, target, val, val.Type().Elem(), allow)
}

func asHidden(err error, target any, val reflect.Value, want reflect.Type, allow func(error) bool) bool {
	for err != nil {
		if allow(err) {
			if reflect.TypeOf(err).AssignableTo(want) {
				val.Elem().Set(reflect.ValueOf(err))
				return true
			}
			if x, ok := err.(interface{ As(any) bool }); ok && x.As(target) {
				if found, isErr := val.Elem().Interface().(error); !isErr || found == nil || allow(found) {
					return true
				}
				val.Elem().Set(reflect.Zero(want))
			}
		}
		switch u := err.(type) { //nolint:errorlint // the tree is walked one level at a time
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, child := range u.Unwrap() {
				if asHidden(child, target, val, want, allow) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// withDetail pairs base, an error from fmt.Errorf, with its detailed
// text, keeping base's text and the shape of its Unwrap chain.
func withDetail(base error, detail string) error {
	if multi, ok := base.(interface{ Unwrap() []error }); ok {
		return &detailedMulti{base: base, detail: detail, wrapped: multi.Unwrap()}
	}
	return &detailedOne{base: base, detail: detail}
}

// detailedOne is an fmt.Errorf error with no %w or one, plus its
// detailed text.
type detailedOne struct {
	base   error
	detail string
}

func (e *detailedOne) Error() string         { return e.base.Error() }
func (e *detailedOne) DetailedError() string { return e.detail }
func (e *detailedOne) Unwrap() error         { return errors.Unwrap(e.base) }

// detailedMulti is an fmt.Errorf error with two or more %w verbs, plus
// its detailed text.
type detailedMulti struct {
	base    error
	detail  string
	wrapped []error
}

func (e *detailedMulti) Error() string         { return e.base.Error() }
func (e *detailedMulti) DetailedError() string { return e.detail }
func (e *detailedMulti) Unwrap() []error       { return e.wrapped }
