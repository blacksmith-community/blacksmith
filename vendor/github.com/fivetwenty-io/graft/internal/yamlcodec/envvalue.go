package yamlcodec

import (
	"bytes"
	"errors"
	"strings"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

// ErrExternalValue is the error DecodeExternal gives for a value that
// does not decode. Its text names no part of the value, which may be a
// secret, and it wraps nothing that could.
var ErrExternalValue = errors.New("the value is not valid YAML")

// DecodeExternal decodes data, a value graft reads from outside the
// merge documents, such as an environment variable or a value from a
// secret store, the way a merge reads a document. Only the first
// document counts, nesting past the depth limit fails before anything
// tokenizes it, an alias inside its own anchor's collection fails, and
// aliases that would expand past the merge's node cap fail rather than
// expand. Plain yes, no, on, and off words become booleans, as they do
// in a merge input.
//
// Every failure comes back as ErrExternalValue, so no error quotes the
// value. A nil value with a nil error means data holds no document, or
// a null one.
func DecodeExternal(data []byte) (any, error) {
	if leadsWithTab(data) {
		return nil, ErrExternalValue
	}
	first, err := FirstMergeDocument(data)
	if err != nil {
		return nil, ErrExternalValue
	}
	if tabAfterIndicator(first) {
		return nil, ErrExternalValue
	}
	v, err := DecodeGeneric(first, DefaultYAMLCompat(), false)
	if err != nil {
		return nil, ErrExternalValue
	}
	return v, nil
}

// DecodeEnvValue gives the value spruce gives for text, the value of an
// environment variable that is set. spruce decodes the text as YAML and
// uses the result unless it is a string or the decode fails, and then
// it uses the text itself, with its quotes and spaces. So 8080 becomes
// an integer, yes a boolean, and a: b a map, while hello, "q", and an
// unclosed [ stay the text they were.
func DecodeEnvValue(text string) any {
	v, err := DecodeExternal([]byte(text))
	if err != nil {
		return text
	}
	if _, isString := v.(string); isString {
		return text
	}
	return v
}

// leadsWithTab reports whether the first line of data that holds
// anything starts, after any spaces, with a tab. libyaml, which spruce
// decodes with, cannot start a token at such a tab outside a flow, so
// spruce keeps the text, while goccy reads past it. A tab later in a
// line, such as the one in "a:\tyes", is fine in both.
func leadsWithTab(data []byte) bool {
	for _, c := range data {
		switch c {
		case ' ', '\n', '\r':
			continue
		case '\t':
			return true
		default:
			return false
		}
	}
	return false
}

// tabAfterIndicator reports whether a block indicator that opens a line
// in data, a "-", "?", or ":", has a tab after it, with spaces between
// or not. A previous indicator on the line, as in "- -", does not stop
// an indicator from opening it. libyaml refuses such a tab, so spruce
// keeps the text, while goccy reads past it. The lexer finds the
// indicators, so a "-" inside a block scalar or a quoted string does
// not count, and a tab after a ":" that follows a key, as in "a:\t1",
// is fine in both tools.
func tabAfterIndicator(data []byte) bool {
	if !bytes.Contains(data, []byte{'\t'}) {
		return false
	}
	lines := bytes.Split(data, []byte{'\n'})
	for _, tk := range lexer.Tokenize(string(data)) {
		switch tk.Type {
		case token.SequenceEntryType, token.MappingKeyType, token.MappingValueType:
		default:
			continue
		}
		line, col := tk.Position.Line-1, tk.Position.Column-1
		if line < 0 || line >= len(lines) || col < 0 || col >= len(lines[line]) {
			continue
		}
		text := lines[line]
		if !strings.ContainsRune("-?:", rune(text[col])) {
			continue
		}
		// A dash is an indicator wherever the lexer finds one, even
		// inside a flow collection. A key or value indicator counts
		// only when it opens its line.
		if tk.Type != token.SequenceEntryType && !opensLine(text[:col]) {
			continue
		}
		rest := text[col+1:]
		for len(rest) > 0 && rest[0] == ' ' {
			rest = rest[1:]
		}
		if len(rest) > 0 && rest[0] == '\t' {
			return true
		}
	}
	return false
}

// opensLine reports whether before, the text ahead of an indicator on
// its line, holds nothing but spaces and other indicators.
func opensLine(before []byte) bool {
	for _, c := range before {
		switch c {
		case ' ', '-', '?', ':':
		default:
			return false
		}
	}
	return true
}
