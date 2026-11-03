package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"
)

// propertyNameMessage is libyaml's error for an anchor or alias name it
// cannot scan.
const propertyNameMessage = "did not find expected alphabetic or numeric character"

// PropertyNameError returns libyaml's error for the anchor or alias that
// the "&" or "*" token mark starts and the token name names, or nil when
// libyaml reads the name as goccy does. line turns the line of the
// mark, numbered from 1, into the line the caller's reader reports, and
// offset is the number of input lines before the text goccy read, which
// gives the error's SourceLine.
//
// libyaml reads a name as a run of letters, digits, "_", and "-" right
// after the indicator, and fails the scan when the run is empty or when
// something other than a blank, a line break, the end of the input, or
// one of "?:,]}%@`" follows it. goccy reads every character up to a
// blank as the name, so it takes "&a[1]" for the anchor "a[1]", where
// spruce fails the file. A run that one of those indicators ends is a
// name libyaml reads as shorter than goccy does, and that is left alone.
func PropertyNameError(mark, name *token.Token, offset int, line func(int) int) error {
	if mark == nil || name == nil {
		return nil
	}
	adjacent := name.Position.Line == mark.Position.Line && name.Position.Column == mark.Position.Column+1
	if adjacent && validPropertyName(name.Value) {
		return nil
	}
	return &ParseError{Line: line(mark.Position.Line), SourceLine: mark.Position.Line + offset, Message: propertyNameMessage}
}

// validPropertyName reports whether libyaml scans name, the text goccy
// reads as an anchor or alias name, without an error.
func validPropertyName(name string) bool {
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case isAlphaByte(c):
		case i > 0 && strings.IndexByte("?:,]}%@`", c) >= 0:
			return true
		default:
			return false
		}
	}
	return name != ""
}

// TagReader names the YAML library whose tag scanner TagError copies.
type TagReader int

const (
	// MergeTags is the yaml.v2 fork that spruce merge reads with. It
	// takes a tag with no URI characters, such as "!!" or "!<>", and it
	// reports every fault in a "%" escape as a missing escaped octet.
	MergeTags TagReader = iota
	// DiffTags is the yaml.v3 that spruce diff reads with. It fails a
	// verbatim tag or a named handle with no URI characters after it,
	// and it names the fault in a "%" escape.
	DiffTags
)

// uriOctetMessage is the error the yaml.v2 fork spruce merge reads with
// gives for every fault in a tag URI or a %TAG handle, whatever the
// fault is.
const uriOctetMessage = "did not find URI escaped octet"

// TagError returns the error reader gives for the tag token tag, or nil
// when it scans the whole tag that goccy read. line turns the tag's
// line, numbered from 1, into the line the caller's reader reports, and
// offset is the number of input lines before the text goccy read, which
// gives the error's SourceLine.
//
// libyaml reads a tag as "!<", a URI, and ">", or as a handle and then
// URI characters, and fails the scan when something other than a blank,
// a line break, or the end of the input follows it. A handle is "!" and
// a run of letters, digits, "_", and "-", with a closing "!" or without
// one. goccy reads every character up to a blank as the tag, so it took
// "!a^b" for a tag, where spruce fails the file.
func TagError(tag *token.Token, offset int, line func(int) int, reader TagReader) error {
	if tag == nil {
		return nil
	}
	if msg := scanTag([]byte(tag.Value), reader); msg != "" {
		return &ParseError{Line: line(tag.Position.Line), SourceLine: tag.Position.Line + offset, Message: msg}
	}
	return nil
}

// scanTag reads text, which starts with "!", as reader's scanner reads a
// tag, and returns the scanner's error, or "".
func scanTag(text []byte, reader TagReader) string {
	s := &propertyScan{text: text, i: 1}
	verbatim, needsURI := s.peek() == '<', true
	if verbatim {
		s.i++
	} else {
		s.alpha()
		if s.peek() == '!' {
			s.i++
		} else {
			// The "!" and the run are no handle but the start of the URI.
			needsURI = false
		}
	}
	start := s.i
	if msg := s.uri(); msg != "" {
		if reader == MergeTags {
			return uriOctetMessage
		}
		return msg
	}
	if reader == DiffTags && needsURI && s.i == start {
		return "did not find expected tag URI"
	}
	if verbatim {
		if s.peek() != '>' {
			return "did not find the expected '>'"
		}
		s.i++
	}
	if c := s.peek(); c != ' ' && c != '\t' && c != '\r' && c != '\n' && c != 0 {
		return "did not find expected whitespace or line break"
	}
	return ""
}

// propertyScan holds the text a scan of a tag or a directive line reads,
// and where it is.
type propertyScan struct {
	text []byte
	i    int
}

// uriMarks holds the characters other than letters, digits, "_", and
// "-" that libyaml reads in a tag URI.
const uriMarks = ";/?:@&=+$,.!~*'()[]"

// uri reads URI characters and returns libyaml's error when a "%" among
// them starts no escaped UTF-8 sequence, or "". An escape is "%" and two
// hex digits, and one escape stands for each byte of the sequence the
// first byte begins.
func (s *propertyScan) uri() string {
	for {
		switch c := s.peek(); {
		case c == '%':
			if msg := s.uriEscapes(); msg != "" {
				return msg
			}
		case c != 0 && (isAlphaByte(c) || strings.IndexByte(uriMarks, c) >= 0):
			s.i++
		default:
			return ""
		}
	}
}

// uriEscapes reads the escapes of one UTF-8 sequence.
func (s *propertyScan) uriEscapes() string {
	width := 0
	for k := 0; k == 0 || k < width; k++ {
		if s.peek() != '%' || s.i+2 >= len(s.text) {
			return uriOctetMessage
		}
		octet, ok := hexByte(s.text[s.i+1], s.text[s.i+2])
		switch {
		case !ok:
			return uriOctetMessage
		case k == 0:
			if width = utf8Width(octet); width == 0 {
				return "found an incorrect leading UTF-8 octet"
			}
		case octet&0xC0 != 0x80:
			return "found an incorrect trailing UTF-8 octet"
		}
		s.i += 3
	}
	return ""
}

// peek returns the byte at the scan position, or 0 at the end of the
// text, which libyaml reads as a line break.
func (s *propertyScan) peek() byte {
	if s.i < len(s.text) {
		return s.text[s.i]
	}
	return 0
}

// alpha returns the run of letters, digits, "_", and "-" at the scan
// position, which libyaml reads as a name.
func (s *propertyScan) alpha() []byte {
	start := s.i
	for isAlphaByte(s.peek()) {
		s.i++
	}
	return s.text[start:s.i]
}

// hexByte returns the byte the hex digits hi and lo spell.
func hexByte(hi, lo byte) (byte, bool) {
	h, ok1 := hexDigit(hi)
	l, ok2 := hexDigit(lo)
	return h<<4 | l, ok1 && ok2
}

// hexDigit returns the value of the hex digit c.
func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// isAlphaByte reports whether c is a letter, a digit, "_", or "-".
func isAlphaByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c == '-'
}
