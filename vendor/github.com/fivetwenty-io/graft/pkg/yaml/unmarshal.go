package yaml

import (
	"reflect"

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
// These limits apply to that route:
//
//   - A named bool type, such as type Flag bool, follows goccy's YAML 1.2
//     rule, so only true and false decode into it and a plain yes is a
//     type error.
//   - A type with an UnmarshalText method receives goccy's formatted text
//     of a scalar, so 1.10 arrives as "1.1". So does a string that a
//     type's own UnmarshalYAML method decodes through its callback.
//   - A type's own UnmarshalYAML method that keeps what it decodes in an
//     unexported field, or in a local variable, gets goccy's raw values.
//     In YAML 1.1 mode, when v also holds a value of interface type, a
//     quoted yes, no, on, or off can reach that method with an internal
//     marker character in front of it.
//   - An ,inline map receives every key of its mapping, including keys
//     that name another field of the struct. When the inline map's values
//     are not strings, a string field beside it keeps goccy's formatted
//     text, so name: 012 gives "10".
//   - In a << merge key's list of maps, a later map wins over an earlier
//     one, as on the generic route, where yaml.v2 and yaml.v3 let the
//     earlier map win.
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
	data, err := toUTF8(data)
	if err != nil {
		return err
	}
	if n, ok := v.(*Node); ok {
		if n == nil {
			return typeError("Unmarshal(nil %T)", n)
		}
		return decodeNode(data, n, opts)
	}
	if err := checkTarget(v); err != nil {
		return err
	}
	return decodeFrom(textSource(data, opts), v, opts)
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
// that checkTarget accepted, by the route v's type takes.
func decodeFrom(src source, v any, opts DecodeOptions) (err error) {
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
		return decodeTyped(src, v, opts)
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
