package yaml

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// errNotWritten is what a Set check returns when the edited text holds
// a node other than the one Set meant to write.
var errNotWritten = errors.New("the edited text does not hold the value Set wrote")

// Set replaces the scalar at path with value, changing no other byte.
// value is a string, a bool, any Go integer or float, or nil.
//
// The Go types Set accepts are string, bool, int, int8, int16, int32,
// int64, uint, uint8, uint16, uint32, uint64, float32, and float64, and
// an untyped nil. Any other type, a named type over one of these
// included, refuses with ErrUnsupportedEdit.
//
// A string keeps the slot's double quotes, and keeps its single quotes
// when it holds no line break and no character that needs an escape. In
// a plain slot it takes the style MarshalYAML gives it, so a string
// such as yes or 1.0 comes out quoted, since written plain it would
// read back as another type. A string that MarshalYAML writes plain
// but the parser reads as something else, such as "<<", is
// double-quoted. A string with a line break is written in double
// quotes on one line, and inside a flow collection a string that holds
// a flow indicator is double-quoted. Any other value is written plain,
// as MarshalYAML writes it, so a quoted slot loses its quotes.
//
// A literal (|) or folded (>) block scalar takes only a string, which
// replaces its body. The header line and its comment stay as written,
// and the body keeps its indentation, or takes the header line's own
// indentation plus 2 when it had no text. The value must fit the
// header's chomping: clip takes an empty value or one that ends in
// exactly one line break, strip a value with no final line break, and
// keep a value with at least one, each extra one of which is written as
// a blank line. A value whose first line with text starts with a space
// or a tab, or that holds a carriage return or U+0085, fits no header,
// and in a folded scalar no line may start with a space or a tab. A
// folded body writes each line break between two lines of text as two,
// and never folds a long line. Set refuses a header with an indentation
// indicator.
//
// Set only replaces a value that Editable accepts, and otherwise
// returns the error Editable returns. A path that names nothing is
// ErrPathNotFound, since Set never adds a key. Set parses the edited
// text again, and refuses with ErrUnsupportedEdit unless the new value
// decodes back to value and nothing else in the document has changed.
// That includes where a comment belongs, so Set refuses to quote a
// plain value that a comment line directly follows, since the parser
// gives that comment to the document rather than to the value's key once
// the value is quoted. Setting a value to what it already holds
// succeeds and changes nothing. A refused Set leaves the bytes as they
// were.
func (d *Document) Set(path string, value any) error {
	const op = "set"
	tag, ok := setTag(value)
	if !ok {
		reason := fmt.Sprintf("Set does not write a value of Go type %s", reflect.TypeOf(value))
		return &DocumentError{Op: op, Path: path, Reason: reason, err: ErrUnsupportedEdit}
	}
	loc, sl, err := d.setTarget(op, path)
	if err != nil {
		return err
	}
	if isBlockScalar(loc.node) {
		return d.setBlock(path, loc, sl, value)
	}
	sp := sl.span
	w := renderSet(loc, value, tag)
	next := make([]byte, 0, len(d.src)-(sp.end-sp.start)+len(w.text))
	next = append(next, d.src[:sp.start]...)
	next = append(next, w.text...)
	next = append(next, d.src[sp.end:]...)
	wrote := span{sp.start, sp.start + len(w.text)}
	return d.commit(path, next, loc, sp, func(n *yamlnode.Node) error {
		return w.check(next, wrote, n, value)
	})
}

// setTag returns the tag a value of v's Go type is written with, and
// false for a type Set does not write.
func setTag(v any) (string, bool) {
	switch v.(type) {
	case nil:
		return yamlnode.TagNull, true
	case string:
		return yamlnode.TagStr, true
	case bool:
		return yamlnode.TagBool, true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return yamlnode.TagInt, true
	case float32, float64:
		return yamlnode.TagFloat, true
	}
	return "", false
}

// setText is the text Set writes and the node it means that text to
// parse to.
type setText struct {
	text  string
	value string
	tag   string
	style yamlnode.Style
}

// renderSet returns the text Set writes for value, whose tag is tag, in
// the slot loc names.
func renderSet(loc located, value any, tag string) setText {
	s, isString := value.(string)
	if !isString {
		// MarshalYAML writes every type setTag accepts on one line, and
		// the check refuses any text that does not read back as value.
		out, _ := yamlcodec.MarshalYAML(value)
		text := strings.TrimSuffix(string(out), "\n")
		if tag == yamlnode.TagFloat {
			text = withDecimalPoint(text)
		}
		return setText{text: text, value: text, tag: tag}
	}
	prefer := yamlcodec.InlinePlain
	switch {
	case loc.node.Style&yamlnode.StyleDouble != 0:
		prefer = yamlcodec.InlineDouble
	case loc.node.Style&yamlnode.StyleSingle != 0:
		prefer = yamlcodec.InlineSingle
	}
	text := yamlcodec.InlineScalar(s, prefer)
	style := quotedStyle(text)
	if loc.parent.Style&yamlnode.StyleFlow != 0 && style == 0 && strings.ContainsAny(text, ",[]{}") {
		// A flow indicator ends a plain scalar in a flow collection.
		text = yamlcodec.InlineScalar(s, yamlcodec.InlineDouble)
		style = yamlnode.StyleDouble
	}
	return setText{text: text, value: s, tag: tag, style: style}
}

// withDecimalPoint returns the float text f with ".0" put before an
// exponent whose mantissa has no decimal point, so that 1e+06 becomes
// 1.0e+06. Unmarshal and Node.Decode read a plain 1e+06 as a string
// when they decode into an interface, and read 1.0e+06 as a float64.
func withDecimalPoint(f string) string {
	e := strings.IndexAny(f, "eE")
	if e < 0 || strings.IndexByte(f[:e], '.') >= 0 {
		return f
	}
	return f[:e] + ".0" + f[e:]
}

// quotedStyle returns the style of the one-line scalar text: double or
// single quotes by its first byte, and zero for plain text.
func quotedStyle(text string) yamlnode.Style {
	switch {
	case strings.HasPrefix(text, `"`):
		return yamlnode.StyleDouble
	case strings.HasPrefix(text, `'`):
		return yamlnode.StyleSingle
	}
	return 0
}

// check reports whether n, a node of the tree parsed from next, is the
// node Set meant to write for value at wrote. Its Value, Tag, and Style
// must be the ones w names, and its text must be exactly the bytes at
// wrote. The public Node for it must decode into an interface as the
// kind of Go value value is, so a reader that doesn't know the type
// sees the one Set was given. It must also decode into value's Go type
// as a value equal to value, or as a NaN when value is a NaN.
func (w setText) check(next []byte, wrote span, n *yamlnode.Node, value any) error {
	if n.Kind != yamlnode.ScalarNode || n.Value != w.value || n.Tag != w.tag || n.Style != w.style || n.BareTag {
		return errNotWritten
	}
	edited := &Document{src: next, lines: newLineIndex(next)}
	if at, err := edited.scalarSpan(n); err != nil || at != wrote {
		return errNotWritten
	}
	pn := publicNode(next, n)
	var generic any
	if err := pn.Decode(&generic); err != nil {
		return err
	}
	if kindClass(generic) != kindClass(value) {
		return errNotWritten
	}
	if value == nil {
		return nil
	}
	got := reflect.New(reflect.TypeOf(value))
	if err := pn.Decode(got.Interface()); err != nil {
		return err
	}
	if isNaN(value) {
		if !isNaN(got.Elem().Interface()) {
			return errNotWritten
		}
		return nil
	}
	if !reflect.DeepEqual(got.Elem().Interface(), value) {
		return errNotWritten
	}
	return nil
}

// kindClass groups a decoded value by the kind of scalar it is: nil, a
// string, a bool, an integer of any size or sign, or a float.
func kindClass(v any) string {
	if v == nil {
		return "null"
	}
	switch reflect.TypeOf(v).Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	default:
		return reflect.TypeOf(v).String()
	}
}

// isNaN reports whether v is a float32 or float64 NaN.
func isNaN(v any) bool {
	switch f := v.(type) {
	case float32:
		return math.IsNaN(float64(f))
	case float64:
		return math.IsNaN(f)
	}
	return false
}
