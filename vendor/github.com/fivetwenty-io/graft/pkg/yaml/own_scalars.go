package yaml

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	goyaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// ownScalar is a scalar that goccy hands to a type which decodes itself,
// as a time.Time, a time.Duration, or through UnmarshalText.
type ownScalar struct {
	kind ownKind
	typ  reflect.Type
	node ast.Node
}

// ownScalars are the scalars asWritten found at such places, in the
// order they appear in the document.
type ownScalars []ownScalar

// timestampLayouts are the layouts goccy tries on a string it puts into
// a time.Time, which are also the ones yaml.v3 tries.
var timestampLayouts = []string{
	"2006-1-2T15:4:5.999999999Z07:00",
	"2006-1-2t15:4:5.999999999Z07:00",
	"2006-1-2 15:4:5.999999999",
	"2006-1-2",
}

// badTime returns a type error for the first time.Time scalar that goccy
// reads as a string no layout parses, or nil. goccy leaves the zero time
// for such a scalar and reports nothing, where yaml.v2 and yaml.v3
// refuse it.
func (s ownScalars) badTime() error {
	for _, o := range s {
		if o.kind != ownTime {
			continue
		}
		text, ok := o.generic().(string)
		if ok && !parsesAsTime(text) {
			return o.typeError(fmt.Sprintf("cannot unmarshal a string into Go value of type %s", o.typ))
		}
	}
	return nil
}

// parsesAsTime reports whether a timestamp layout parses text.
func parsesAsTime(text string) bool {
	for _, layout := range timestampLayouts {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}

// locate returns err as a type error that names only a Go type when err
// is a plain error from a method or a parse that goccy ran on a value,
// and err otherwise. Such an error can repeat the input, as net.IP's
// does, so its text is never kept. When err is a time.Duration's or an
// UnmarshalText method's refusal, the error is on the line of the scalar
// it concerns, which locate finds by reading each candidate again on its
// own and keeping the one whose refusal err holds. When candidates on
// different lines match, or none does, the error has Line 0 and names
// the candidates' type, or target when they differ or there are none.
func (s ownScalars) locate(err error, target reflect.Type) error {
	if !methodError(err) {
		return err
	}
	var found *ownScalar
	for i := range s {
		refusal := s[i].refusal()
		if refusal == nil || !strings.Contains(err.Error(), refusal.Error()) {
			continue
		}
		if found == nil {
			found = &s[i]
			continue
		}
		if !samePosition(found, &s[i]) {
			if found.typ != s[i].typ {
				return unplaced(target)
			}
			return unplaced(found.typ)
		}
	}
	if found == nil {
		return unplaced(target)
	}
	return found.typeError(ownMessage(found.typ))
}

// methodError reports whether err is a plain error rather than one goccy
// or the codec builds, whose messages translate already keeps free of
// the input.
func methodError(err error) bool {
	var ge goyaml.Error
	var de *DecodeError
	var tooDeep *yamlcodec.MaxRecursionError
	if errors.As(err, &ge) || errors.As(err, &de) || errors.As(err, &tooDeep) ||
		errors.Is(err, goyaml.ErrExceededMaxDepth) {
		return false
	}
	return !codecMessage(err.Error())
}

// unplaced returns a type error with no position for a value of type t.
func unplaced(t reflect.Type) *DecodeError {
	return &DecodeError{Kind: ErrorKindType, Msg: ownMessage(t)}
}

// ownMessage is the message for a refusal by a value of type t. It names
// only the type, since the refusal's own text can quote the input.
func ownMessage(t reflect.Type) string {
	return fmt.Sprintf("cannot unmarshal into Go value of type %s", t)
}

// refusal returns the error goccy gets when it decodes the scalar into a
// value of its type, or nil, by the same steps goccy takes. For
// UnmarshalText that runs the method on a new value of the type.
func (o *ownScalar) refusal() error {
	switch o.kind {
	case ownDuration:
		if text, ok := o.generic().(string); ok {
			_, err := time.ParseDuration(text)
			return err
		}
	case ownText:
		text, ok := o.text()
		if u, isText := reflect.New(o.typ).Interface().(encoding.TextUnmarshaler); ok && isText {
			return u.UnmarshalText([]byte(text))
		}
	default:
	}
	return nil
}

// generic returns the value goccy reads the scalar as, before it
// converts that value to the type, or nil when it cannot read it.
func (o *ownScalar) generic() any {
	var got any
	if yamlcodec.DecodeBody(o.node, &got) != nil {
		return nil
	}
	return got
}

// text returns the string goccy reads the scalar as when it decodes it
// into a string, and false when it cannot.
func (o *ownScalar) text() (string, bool) {
	var got string
	err := yamlcodec.DecodeBody(o.node, &got)
	return got, err == nil
}

// line returns the line and column of the scalar's token, or zeros.
func (o *ownScalar) line() (line, column int) {
	if tk := o.node.GetToken(); tk != nil && tk.Position != nil {
		return tk.Position.Line, tk.Position.Column
	}
	return 0, 0
}

// samePosition reports whether two scalars start at the same place.
func samePosition(a, b *ownScalar) bool {
	al, ac := a.line()
	bl, bc := b.line()
	return al == bl && ac == bc
}

// typeError returns an ErrorKindType DecodeError at the scalar.
func (o *ownScalar) typeError(msg string) *DecodeError {
	line, column := o.line()
	return &DecodeError{Kind: ErrorKindType, Line: line, Column: column, Msg: msg}
}
