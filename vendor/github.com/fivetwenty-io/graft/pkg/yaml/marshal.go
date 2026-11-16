package yaml

import (
	"fmt"
	"reflect"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/pkg/sortkeys"
)

// Marshal encodes v as YAML, with every map's keys in spruce's order and
// two spaces of indentation, as graft merge writes a document.
//
// Marshal first walks v into plain maps, lists, and scalars, and graft's
// own encoder writes those, so a struct, a typed map, and a typed slice
// come out in the same style as a map[string]any does. The walk follows
// the yaml tag options a field names, which are a rename, "-",
// ,omitempty, ,inline, and ,flow, and a field with no yaml tag takes its
// key from its json tag and then from its name in lower case. Struct
// fields keep their declared order, and a type's own MarshalYAML method
// or MarshalText method supplies its value. A ,flow field is written on
// one line, with a string plain only when that is safe there and in
// double quotes otherwise.
//
// A string is written in the style spruce picks for it, so one that would
// read back as another type is quoted and a multi-line one is a literal
// block, even when it starts with an indicator such as "-", as a PEM
// certificate does. A multi-line string that a block cannot hold, such as
// one with a space at the end of a line or a U+2028 at the start of one,
// is double-quoted instead. A single-line string that starts with "-",
// "?", or ":" and a character other than a space, such as "-a", is
// double-quoted, and one with a space or nothing after the indicator, such
// as "- a" or "-", is single-quoted. A string that a YAML 1.1 reader
// resolves as a timestamp, such as 2026-01-02, is double-quoted. A key
// that spruce would write as a "? key" is double-quoted on one line
// instead, because goccy's parser misreads some of those, and so are
// strings that goccy's parser misreads in other places. A key whose
// double-quoted form would be longer than 1024 bytes still takes the
// "? key" form. A float that holds a whole
// number is plain integer digits when it fits in an int64, so 3.0 is 3
// and 1e6 is 1000000. A whole float outside that range, and any other
// float, has a "." (1e20 is 1.0e+20) so that a YAML 1.1 reader reads it
// as a float, and NaN and the infinities are .nan, .inf, and -.inf. A
// map key that is an integer, a bool, or a float is written as that
// scalar without quotes, and keys are ordered by the key order applied
// to their text, so 9 comes before 10 in spruce order.
//
// Unmarshal reads this output back to an equal value, with a whole float
// inside the int64 range coming back as an int when the target is any.
//
// Marshal refuses a v that holds a Node anywhere, including as a map key,
// because writing a Node tree back is not supported, a v that holds a
// cycle, a map key that is a collection, and a type whose MarshalYAML
// method returns YAML text. Its errors name a Go type and a field path
// and never quote a value from v. A value the encoder cannot write, such
// as a channel, is an error and not a panic, but a panic from a type's
// own MarshalYAML method is not recovered.
func Marshal(v any) ([]byte, error) {
	return MarshalWith(v, EncodeOptions{})
}

// MarshalWith is Marshal with the options given. It returns an error
// and no output for a KeyOrder other than the zero value, which means
// sortkeys.Spruce, and sortkeys.Lexical, or for an Indent outside 0 and
// 2 through 9.
func MarshalWith(v any, opts EncodeOptions) ([]byte, error) {
	return marshalWith(v, opts, false)
}

// marshalWith is MarshalWith, and with documentStart it puts a "---" line
// before the document in the same buffer.
func marshalWith(v any, opts EncodeOptions, documentStart bool) (out []byte, err error) {
	mode := opts.KeyOrder
	switch mode {
	case "":
		mode = sortkeys.Spruce
	case sortkeys.Spruce, sortkeys.Lexical:
	default:
		return nil, &encodeError{msg: fmt.Sprintf("MarshalWith does not support key order %q; use the zero value, sortkeys.Spruce, or sortkeys.Lexical", string(mode))}
	}

	if opts.Indent != 0 && (opts.Indent < 2 || opts.Indent > 9) {
		return nil, &encodeError{msg: fmt.Sprintf("MarshalWith does not support indent %d; use 0, which means 2, or 2 through 9", opts.Indent)}
	}

	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if !panicFromEncoder() {
			panic(r)
		}
		out, err = nil, encoderPanicError(v)
	}()
	less := sortkeys.Less(mode)
	var generic any
	if v != nil {
		var werr error
		generic, werr = newNormalizer(less).value(reflect.ValueOf(v), reflect.TypeOf(v).String())
		if werr != nil {
			return nil, werr
		}
	}
	out, err = yamlcodec.MarshalYAMLTypedLayout(generic, less, yamlcodec.Layout{Indent: opts.Indent, IndentSequence: opts.IndentSequence, DocumentStart: documentStart})
	if err != nil {
		return nil, &encodeError{msg: "cannot encode Go value of type " + typeName(v)}
	}
	return out, nil
}

// encodeError is the error Marshal returns. Its text names a Go type or a
// field path and never quotes a value from the input.
type encodeError struct {
	msg string
}

func (e *encodeError) Error() string { return "yaml: " + e.msg }

// encoderPanicError is the error an encode returns in place of a panic in
// the encoder, which names only the Go type of v.
func encoderPanicError(v any) error {
	return &encodeError{msg: "cannot encode Go value of type " + typeName(v)}
}

func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	return reflect.TypeOf(v).String()
}

// nodeType is Node, which Marshal refuses wherever it finds one.
var nodeType = reflect.TypeFor[Node]()
