package graft

import (
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/logfilter"
	"github.com/fivetwenty-io/graft/internal/loopbind"
)

// loopBindingKey is the reserved top-level key that control-flow
// expansion stores loop bindings under.
const loopBindingKey = loopbind.Key

//nolint:gochecknoinits // the trace filter must be in place before any merge logs a line
func init() {
	logfilter.Hook = traceWithoutLoopNames
}

// bindingTreePath matches a trace line's mention of the binding tree
// itself as a document path, such as "$.__graft_loop.l1a2b3c4d5e6f_1",
// and bindingKeyInList the binding key in a printed list of keys.
var (
	bindingTreePath  = regexp.MustCompile(`\$\.` + loopBindingKey + `\b`)
	bindingKeyInList = regexp.MustCompile(`(\[| )` + loopBindingKey + `( |\])`)
)

// traceWithoutLoopNames is the hook the log package consults. A trace message
// about the binding tree itself, such as the merge that adds it, is
// left out, since the merged output never holds it. A list of keys
// leaves out the binding key, and an expression in a loop body has the
// binding prefix taken out, so it reads as the user wrote it. Any other
// message that still names the tree is left out too.
func traceWithoutLoopNames(message string) (string, bool) {
	if !strings.Contains(message, loopBindingKey) {
		return message, true
	}
	if bindingTreePath.MatchString(message) {
		return "", false
	}
	message = withoutLoopPrefix(message)
	message = bindingKeyInList.ReplaceAllStringFunc(message, func(m string) string {
		switch {
		case m[0] == '[' && m[len(m)-1] == ']':
			return "[]"
		case m[0] == '[':
			return "["
		case m[len(m)-1] == ']':
			return "]"
		}
		return " "
	})
	if strings.Contains(message, loopBindingKey) {
		return "", false
	}
	return message, true
}

// withoutLoopPrefix returns text with every loop binding prefix taken
// out, so a path reads as the user wrote it in the loop body.
func withoutLoopPrefix(text string) string {
	return loopbind.WithoutPrefix(text)
}

// withoutLoopNames returns err with the loop binding prefix taken out of
// every message in it, when the tree holds loop bindings. Every phase of
// both evaluator modes returns its error through runPhase, which calls
// this once, so an error from an operator in a loop body and a cycle
// through one name the paths and the operator source the user wrote.
// The scrub only removes the prefix and never adds text.
//
// The error keeps its type at the top, so a MultiError stays a
// MultiError, a *PathError a *PathError, and a *CycleError a
// *CycleError. Each error in a MultiError is scrubbed on its own, so a
// caller that walks the list sees clean messages too. Any other error
// whose text holds the prefix is wrapped in a loopNamesError, which
// keeps it on the Unwrap chain, so errors.Is and errors.As match the
// same sentinels and types as before.
func (ev *Evaluator) withoutLoopNames(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := ev.Tree[loopBindingKey]; !ok {
		return err
	}
	_, scrubbed := scrubLoopNames(err)
	return scrubbed
}

// scrubLoopNames is withoutLoopNames for one error. It reports whether
// it changed anything, so that callers never compare errors, which can
// hold types that == panics on.
func scrubLoopNames(err error) (bool, error) {
	switch e := err.(type) { //nolint:errorlint // the shape at the top is kept, and the rest is wrapped
	case MultiError:
		items, changed := scrubLoopNameList(e.Errors)
		if !changed {
			return false, err
		}
		return true, MultiError{Errors: items}
	case *MultiError:
		items, changed := scrubLoopNameList(e.Errors)
		if !changed {
			return false, err
		}
		return true, &MultiError{Errors: items}
	case *PathError:
		if scrubbed, changed := scrubPathError(e); changed {
			return true, scrubbed
		}
		return false, err
	case *CycleError:
		if scrubbed, changed := scrubCycleError(e); changed {
			return true, scrubbed
		}
		return false, err
	}
	plain, detail := err.Error(), errtext.Detail(err)
	newPlain, newDetail := withoutLoopPrefix(plain), withoutLoopPrefix(detail)
	if newPlain == plain && newDetail == detail {
		return false, err
	}
	return true, &loopNamesError{cause: err, plain: newPlain, detail: newDetail}
}

// scrubLoopNameList scrubs each error in errs, returning a new list
// when any of them changed.
func scrubLoopNameList(errs []error) ([]error, bool) {
	out := make([]error, len(errs))
	changed := false
	for i, item := range errs {
		var itemChanged bool
		itemChanged, out[i] = scrubLoopNames(item)
		changed = changed || itemChanged
	}
	if !changed {
		return errs, false
	}
	return out, true
}

// scrubPathError returns a copy of e whose path and cause are scrubbed.
func scrubPathError(e *PathError) (*PathError, bool) {
	path := withoutLoopPrefix(e.Path)
	cause, causeChanged := e.Cause, false
	if cause != nil {
		causeChanged, cause = scrubLoopNames(cause)
	}
	if path == e.Path && !causeChanged {
		return e, false
	}
	return &PathError{Path: path, Cause: cause}, true
}

// scrubCycleError returns a copy of e whose node paths and operator
// sources are scrubbed. The sources then show each operator as the user
// wrote it in the loop body.
func scrubCycleError(e *CycleError) (*CycleError, bool) {
	nodes := make([]CycleNode, len(e.Nodes))
	changed := false
	for i, n := range e.Nodes {
		nodes[i] = n
		nodes[i].Path = withoutLoopPrefix(n.Path)
		nodes[i].Expr = withoutLoopPrefix(n.Expr)
		changed = changed || nodes[i].Path != n.Path || nodes[i].Expr != n.Expr
	}
	if !changed {
		return e, false
	}
	return &CycleError{Inputs: e.Inputs, Nodes: nodes}, true
}

// loopNamesError is an error whose plain and detailed texts had the loop
// binding prefix taken out. cause stays on the Unwrap chain, so
// errors.Is sees every sentinel it matched before. errors.As hands out
// a scrubbed copy of whatever typed error it finds in cause, so its
// fields and its own text read as the user wrote them too.
type loopNamesError struct {
	cause  error
	plain  string
	detail string
}

func (e *loopNamesError) Error() string         { return e.plain }
func (e *loopNamesError) DetailedError() string { return e.detail }
func (e *loopNamesError) Unwrap() error         { return e.cause }

// As finds the target's type in cause and hands out a scrubbed copy of
// what it found. A *CycleError has its nodes scrubbed. Any other error
// that is a struct, or a pointer to one, is copied and has its exported
// fields scrubbed: a string loses the prefix, a []string path loses the
// three segments the prefix splits into, and an error or a list of
// errors is scrubbed the way scrubLoopNames does it. The original
// error is never changed. Unexported fields are copied as they are, so
// a type that keeps its text in one shows the prefix in that field,
// though the loopNamesError's own text never does. For an interface
// target As reports false, and errors.As goes on down cause.
func (e *loopNamesError) As(target any) bool {
	slot := reflect.ValueOf(target).Elem()
	if slot.Kind() == reflect.Interface {
		return false
	}
	if !errors.As(e.cause, target) {
		return false
	}
	if cycle, ok := slot.Interface().(*CycleError); ok {
		scrubbed, _ := scrubCycleError(cycle)
		slot.Set(reflect.ValueOf(scrubbed))
		return true
	}
	slot.Set(scrubbedCopy(slot))
	return true
}

// errorType is the reflect type of the error interface.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// scrubbedCopy returns a copy of v, a struct or a pointer to one, whose
// exported fields are scrubbed. Any other value comes back as it is.
func scrubbedCopy(v reflect.Value) reflect.Value {
	switch {
	case v.Kind() == reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		scrubFields(out)
		return out
	case v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().Kind() == reflect.Struct:
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(v.Elem())
		scrubFields(out.Elem())
		return out
	}
	return v
}

// scrubFields takes the loop binding prefix out of the exported fields
// of s, a settable struct, replacing each slice rather than changing
// the one it shares with the original.
func scrubFields(s reflect.Value) {
	for i := range s.NumField() {
		f := s.Field(i)
		if !f.CanSet() {
			continue
		}
		switch {
		case f.Kind() == reflect.String:
			f.SetString(withoutLoopPrefix(f.String()))
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
			f.Set(withoutLoopSegments(f))
		case f.Type() == errorType && !f.IsNil():
			if cause, ok := f.Interface().(error); ok {
				_, scrubbed := scrubLoopNames(cause)
				f.Set(reflect.ValueOf(&scrubbed).Elem())
			}
		case f.Kind() == reflect.Slice && f.Type().Elem() == errorType:
			if list, ok := f.Interface().([]error); ok {
				if items, changed := scrubLoopNameList(list); changed {
					f.Set(reflect.ValueOf(items))
				}
			}
		}
	}
}

// loopSegment matches the loop id segment of a binding path split at
// its dots, and loopIteration the iteration segment after it.
var (
	loopSegment   = regexp.MustCompile(`^l[0-9a-f]+_\d+$`)
	loopIteration = regexp.MustCompile(`^\d+$`)
)

// withoutLoopSegments returns a copy of path, a slice of strings, with
// every "__graft_loop", loop id, iteration run of segments taken out and
// the prefix taken out of each remaining segment. A path with nothing to
// take out comes back as it is.
func withoutLoopSegments(path reflect.Value) reflect.Value {
	segments := make([]string, 0, path.Len())
	changed := false
	for i := 0; i < path.Len(); i++ {
		seg := path.Index(i).String()
		if seg == loopBindingKey && i+2 < path.Len() &&
			loopSegment.MatchString(path.Index(i+1).String()) &&
			loopIteration.MatchString(path.Index(i+2).String()) {
			i += 2
			changed = true
			continue
		}
		plain := withoutLoopPrefix(seg)
		changed = changed || plain != seg
		segments = append(segments, plain)
	}
	if !changed {
		return path
	}
	out := reflect.MakeSlice(path.Type(), len(segments), len(segments))
	for i, seg := range segments {
		out.Index(i).SetString(seg)
	}
	return out
}
