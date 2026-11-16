package yaml

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// Unmarshal decodes the first YAML document in data into v, reading
// yes, no, on, and off as booleans the way graft's merge engine does.
//
// v must be a non-nil pointer. A *Node receives the document's tree, as
// the Node type describes, and its Decode method decodes any part of it
// as Unmarshal decodes text. A *any, *map[string]any, or *[]any
// receives maps as map[string]any, sequences as []any, integers as int,
// and floats as float64. Empty input, or input that holds only comments
// or a null, leaves v unchanged. Input that starts with a UTF-16 byte
// order mark, in either byte order, is converted to UTF-8 first. Every
// error is a *DecodeError.
//
// Any other v, such as a pointer to a struct, a typed map, or a typed
// slice, is decoded by goccy/go-yaml, which honors the yaml tag options
// ,omitempty, ,inline, and ,flow. A struct field with no yaml tag takes
// its key from its json tag, and then from its name in lower case. A
// plain yes, no, on, or off decodes into a bool or *bool field as a
// boolean, while a quoted one is refused with an ErrorKindType error, as
// yaml.v2 refuses it. A string, including a map key of a string type,
// holds a scalar as written, so 1.10 stays "1.10" and 0x1F stays "0x1F",
// as in yaml.v2 and yaml.v3. A key that reads as a number, a boolean, or
// a null still matches a struct field by its text. A value of type any,
// map[string]any, or []any anywhere inside v holds exactly what a
// top-level decode of the same text gives, and so do the keys of such a
// map. A failed decode can leave v partly filled, with those values
// already converted.
//
// A type that is an Unmarshaler has its method called with the Node that
// holds its value, wherever it sits in v, as Node.Decode calls it. A
// target that holds one is read into a Node first and decoded from there,
// which gives the same values and errors that decoding the text gives,
// and every other target is decoded from the text directly.
//
// These limits apply to every typed target, whichever route it takes:
//
//   - A named bool type, such as type Flag bool, follows goccy's YAML 1.2
//     rule, so only true and false decode into it and a plain yes is a
//     type error.
//   - A type with an UnmarshalText method receives goccy's formatted text
//     of a scalar, so 1.10 arrives as "1.1". So does a string that an
//     UnmarshalYAML method decodes through the callback it takes.
//   - An UnmarshalYAML method that takes a callback and keeps what it
//     decodes in an unexported field, or in a local variable, gets goccy's
//     raw values.
//     In YAML 1.1 mode, when v also holds a value of interface type, a
//     quoted yes, no, on, or off can reach that method with an internal
//     marker character in front of it.
//   - An ,inline map receives every key of its mapping, including keys
//     that name another field of the struct, though not the key of a
//     field whose type is an Unmarshaler. When the inline map's values
//     are not strings, a string field beside it keeps goccy's formatted
//     text, so name: 012 gives "10".
//   - A time.Time accepts any string goccy's timestamp layouts parse, so
//     a quoted "2001-12-14" decodes, where yaml.v2 and yaml.v3 refuse a
//     quoted date that is not RFC 3339.
//   - A value of interface type that v already held is converted as a
//     decoded generic value is, even where the document sets nothing.
func Unmarshal(data []byte, v any) error {
	return UnmarshalWith(data, v, DecodeOptions{})
}

// UnmarshalWith decodes the first YAML document in data into v with opts.
// A mapping that repeats a key is an error, as it is for Unmarshal, unless
// opts.OnDuplicateKey is set, in which case the last value wins and the
// callback receives each later occurrence.
func UnmarshalWith(data []byte, v any, opts DecodeOptions) error {
	return unmarshalAt(data, v, opts, 0)
}

// unmarshalAt is UnmarshalWith for a document that starts lineOffset
// lines into the stream it came from, so that its errors, the duplicate
// keys it reports, and the lines of its Nodes all count from the start
// of that stream.
func unmarshalAt(data []byte, v any, opts DecodeOptions, lineOffset int) error {
	data, err := toUTF8(data)
	if err != nil {
		return err
	}
	if n, ok := v.(*Node); ok {
		if n == nil {
			return typeError("Unmarshal(nil %T)", n)
		}
		_, err := decodeNode(data, n, opts, lineOffset, false)
		return err
	}
	if err := checkTarget(v); err != nil {
		return err
	}
	if reachesUnmarshaler(reflect.TypeOf(v).Elem()) {
		// Each method needs the Node that holds its value, so the
		// document goes through a Node first. Every other target keeps
		// the direct route, which costs less. The Nodes already count
		// their lines from the start of the stream, and so do the
		// errors their decode gives. A repeated key that OnDuplicateKey
		// folds keeps the place of its last repeat in the Nodes the
		// methods receive. The decode reads the pairs the text wrote,
		// so it keeps the entries the direct route keeps.
		var root Node
		written, err := decodeNode(data, &root, opts, lineOffset, true)
		if err != nil {
			return err
		}
		return root.decodeHooked(v, written)
	}
	err = decodeFrom(textSource(data, shiftDuplicates(opts, lineOffset)), v, opts, nil)
	return shiftError(err, lineOffset)
}

// shiftError moves the line of err on by lineOffset lines when err is a
// *DecodeError that has a place. It moves on the place of a first key that
// the message names as well. It leaves any other error, and nil, as it is.
func shiftError(err error, lineOffset int) error {
	var de *DecodeError
	if lineOffset == 0 || !errors.As(err, &de) || de.Line <= 0 {
		return err
	}
	out := *de
	out.Line += lineOffset
	out.Msg = shiftMessagePlace(out.Msg, lineOffset)
	return &out
}

// shiftMessagePlace moves on the line of the place that ends msg, as in
// "mapping key already defined at [2:1]", which is where goccy names the
// first of two repeated keys. Any other msg comes back as it is.
func shiftMessagePlace(msg string, lineOffset int) string {
	const open = " at ["
	start := strings.LastIndex(msg, open)
	if start < 0 || !strings.HasSuffix(msg, "]") {
		return msg
	}
	lineText, column, ok := strings.Cut(msg[start+len(open):len(msg)-1], ":")
	if !ok {
		return msg
	}
	line, err := strconv.Atoi(lineText)
	if _, colErr := strconv.Atoi(column); err != nil || colErr != nil || line <= 0 {
		return msg
	}
	return msg[:start] + open + strconv.Itoa(line+lineOffset) + ":" + column + "]"
}

// shiftDuplicates returns opts with an OnDuplicateKey callback that moves
// each report's lines lineOffset lines on before the caller's callback
// receives it.
func shiftDuplicates(opts DecodeOptions, lineOffset int) DecodeOptions {
	report := opts.OnDuplicateKey
	if lineOffset == 0 || report == nil {
		return opts
	}
	opts.OnDuplicateKey = func(d DuplicateKey) {
		d.Line = atLine(d.Line, lineOffset)
		d.FirstLine = atLine(d.FirstLine, lineOffset)
		report(d)
	}
	return opts
}

// atLine returns line moved lineOffset lines on, and leaves a line below
// 1, which marks no place, as it is.
func atLine(line, lineOffset int) int {
	if line <= 0 {
		return line
	}
	return line + lineOffset
}

// source hands the decode routes the root of the document they decode,
// ready for goccy's decoder, or nil when there is no document. tag says
// whether each quoted YAML 1.1 boolean-lookalike scalar must carry the
// marker that keeps it a string, as yamlcodec.ParseFirstBody describes.
// Every error it returns is a *DecodeError. Unmarshal's source parses
// text, and Node.Decode's builds the tree from a Node.
type source func(tag bool) (ast.Node, error)

// textSource returns the source for data, which parses the first
// document of data the way a merge does. Each call parses data again, so
// each route gets a tree of its own. With opts.OnDuplicateKey set, the
// parse accepts a repeated key and reports each one as it parses.
func textSource(data []byte, opts DecodeOptions) source {
	return func(tag bool) (ast.Node, error) {
		doc, err := yamlcodec.FirstMergeDocument(data)
		if err != nil {
			return nil, translate(err)
		}
		var body ast.Node
		if opts.OnDuplicateKey != nil {
			body, err = yamlcodec.ParseMergeBodyDuplicates(doc, tag, func(d yamlcodec.DuplicateKey) {
				opts.OnDuplicateKey(DuplicateKey(d))
			})
		} else {
			body, err = yamlcodec.ParseMergeBody(doc, tag)
		}
		if err != nil {
			return nil, translate(err)
		}
		return body, nil
	}
}

// decodeFrom decodes the document src returns into v, a non-nil pointer
// that checkTarget accepted, by the route v's type takes. owners is set
// when src records the Node each token came from, so the typed route
// calls each Unmarshaler in v, as decodeTyped describes.
func decodeFrom(src source, v any, opts DecodeOptions, owners *tokenOwners) (err error) {
	// The typed route recovers a decoder panic itself, so the fixer still
	// runs on what goccy filled. This catches one anywhere else.
	defer func() {
		if r := recover(); r != nil {
			if !panicFromDecoder() {
				panic(r)
			}
			err = decoderPanicError(reflect.TypeOf(v))
		}
	}()
	switch p := v.(type) {
	case *any:
		return decodeInto(src, opts, func(got any) error {
			*p = got
			return nil
		})
	case *map[string]any:
		return decodeInto(src, opts, func(got any) error {
			m, ok := got.(map[string]any)
			if !ok {
				return mismatch(got, *p)
			}
			*p = m
			return nil
		})
	case *[]any:
		return decodeInto(src, opts, func(got any) error {
			s, ok := got.([]any)
			if !ok {
				return mismatch(got, *p)
			}
			*p = s
			return nil
		})
	default:
		return decodeTyped(src, v, opts, owners)
	}
}

// decodeInto runs the generic route and hands a non-nil result to set,
// so a document that holds nothing leaves the target unchanged.
func decodeInto(src source, opts DecodeOptions, set func(any) error) error {
	got, err := decodeGeneric(src, opts)
	if err != nil {
		return err
	}
	if got == nil {
		return nil
	}
	return set(got)
}

// mismatch reports a decoded root whose type does not fit the target,
// naming both Go types and quoting no value.
func mismatch(got, target any) error {
	return typeError("cannot unmarshal %T into Go value of type %T", got, target)
}
