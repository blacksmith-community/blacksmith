package yamlcodec

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// This file ports the scalar half of spruce's YAML emitter so merge
// output writes every string the way spruce does. spruce marshals
// through a 2016 fork of go-yaml v2 (github.com/geofffranks/yaml), and
// the port follows that fork's encode.go (stringv, resolve,
// isBase60Float) and emitterc.go (yaml_emitter_analyze_scalar,
// yaml_emitter_select_scalar_style, yaml_emitter_check_simple_key, and
// the plain, single-quoted, double-quoted and literal writers). Names
// and control flow stay close to the original so the two can be read
// side by side.

const (
	// scalarBestIndent and scalarBestWidth are libyaml's defaults,
	// which go-yaml v2 never overrides.
	scalarBestIndent = 2
	scalarBestWidth  = 80
	// simpleKeyMaxLength is the longest key libyaml writes as a simple
	// "key: value" key; anything longer, or any multi-line key, is
	// written in the explicit "? key" form.
	simpleKeyMaxLength = 128
)

type scalarStyle int

const (
	plainScalarStyle scalarStyle = iota
	singleQuotedScalarStyle
	doubleQuotedScalarStyle
	literalScalarStyle
)

// scalarAnalysis holds yaml_emitter_analyze_scalar's verdict on which
// styles can represent a value.
type scalarAnalysis struct {
	multiline           bool
	blockPlainAllowed   bool
	singleQuotedAllowed bool
	blockAllowed        bool
}

// The byte-class helpers below mirror libyaml's yamlprivateh.go. at
// reads past the end of a value as NUL, which is what the C original
// sees at a string's terminator, so a helper never panics on a short
// tail.
func at(b []byte, i int) byte {
	if i < len(b) {
		return b[i]
	}
	return 0
}

func isPrintable(b []byte, i int) bool {
	c := at(b, i)
	switch {
	case c == 0x0A, c >= 0x20 && c <= 0x7E, c > 0xC2 && c < 0xED, c == 0xEE:
		return true
	case c == 0xC2:
		return at(b, i+1) >= 0xA0
	case c == 0xED:
		return at(b, i+1) < 0xA0
	case c == 0xEF:
		return isPrintableEF(at(b, i+1), at(b, i+2))
	}
	return false
}

// isPrintableEF reports whether a character that starts with byte 0xEF
// and continues with c1 and c2 is printable. Only the byte order mark
// (EF BB BF) and the noncharacters U+FFFE and U+FFFF (EF BF BE and
// EF BF BF) are not.
func isPrintableEF(c1, c2 byte) bool {
	if c1 == 0xBB && c2 == 0xBF {
		return false
	}
	return c1 != 0xBF || (c2 != 0xBE && c2 != 0xBF)
}

// isBOM checks the start of the value, not position i, exactly as
// libyaml's is_bom does; a value that opens with a byte order mark
// therefore has every character escaped in double quotes.
func isBOM(b []byte) bool {
	return at(b, 0) == 0xEF && at(b, 1) == 0xBB && at(b, 2) == 0xBF
}

func isSpace(b []byte, i int) bool { return at(b, i) == ' ' }

func isBlank(b []byte, i int) bool { c := at(b, i); return c == ' ' || c == '\t' }

func isBreak(b []byte, i int) bool {
	c := at(b, i)
	return c == '\r' || c == '\n' ||
		(c == 0xC2 && at(b, i+1) == 0x85) ||
		(c == 0xE2 && at(b, i+1) == 0x80 && (at(b, i+2) == 0xA8 || at(b, i+2) == 0xA9))
}

func isBlankz(b []byte, i int) bool { return isBlank(b, i) || isBreak(b, i) || at(b, i) == 0 }

// utf8Width is libyaml's width: the length of the UTF-8 sequence that
// starts with lead byte c.
func utf8Width(c byte) int {
	switch {
	case c&0x80 == 0x00:
		return 1
	case c&0xE0 == 0xC0:
		return 2
	case c&0xF0 == 0xE0:
		return 3
	case c&0xF8 == 0xF0:
		return 4
	}
	return 0
}

// analyzeScalar ports yaml_emitter_analyze_scalar with unicode output
// enabled, as go-yaml v2 configures its emitter.
//
//nolint:gocyclo // a line-for-line port; splitting it would hide the correspondence with libyaml
func analyzeScalar(value []byte) scalarAnalysis {
	if len(value) == 0 {
		return scalarAnalysis{blockPlainAllowed: true, singleQuotedAllowed: true}
	}

	var (
		blockIndicators, lineBreaks, specialCharacters bool

		leadingSpace, leadingBreak, trailingSpace, trailingBreak bool
		breakSpace, spaceBreak                                   bool

		previousSpace, previousBreak bool
	)

	if len(value) >= 3 && (string(value[:3]) == "---" || string(value[:3]) == "...") {
		blockIndicators = true
	}

	precededByWhitespace := true
	for i, w := 0, 0; i < len(value); i += w {
		w = utf8Width(value[i])
		if w == 0 {
			w = 1
		}
		followedByWhitespace := i+w >= len(value) || isBlank(value, i+w)

		if i == 0 {
			switch value[i] {
			case '#', ',', '[', ']', '{', '}', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
				blockIndicators = true
			case '?', ':':
				if followedByWhitespace {
					blockIndicators = true
				}
			case '-':
				if followedByWhitespace {
					blockIndicators = true
				}
			}
		} else {
			switch value[i] {
			case ':':
				if followedByWhitespace {
					blockIndicators = true
				}
			case '#':
				if precededByWhitespace {
					blockIndicators = true
				}
			}
		}

		if !isPrintable(value, i) {
			specialCharacters = true
		}
		switch {
		case isSpace(value, i):
			if i == 0 {
				leadingSpace = true
			}
			if i+w == len(value) {
				trailingSpace = true
			}
			if previousBreak {
				breakSpace = true
			}
			previousSpace, previousBreak = true, false
		case isBreak(value, i):
			lineBreaks = true
			if i == 0 {
				leadingBreak = true
			}
			if i+w == len(value) {
				trailingBreak = true
			}
			if previousSpace {
				spaceBreak = true
			}
			previousSpace, previousBreak = false, true
		default:
			previousSpace, previousBreak = false, false
		}

		precededByWhitespace = isBlankz(value, i)
	}

	a := scalarAnalysis{
		multiline:           lineBreaks,
		blockPlainAllowed:   true,
		singleQuotedAllowed: true,
		blockAllowed:        true,
	}
	if leadingSpace || leadingBreak || trailingSpace || trailingBreak {
		a.blockPlainAllowed = false
	}
	if trailingSpace {
		a.blockAllowed = false
	}
	if breakSpace {
		a.blockPlainAllowed = false
		a.singleQuotedAllowed = false
	}
	if spaceBreak || specialCharacters {
		a.blockPlainAllowed = false
		a.singleQuotedAllowed = false
		a.blockAllowed = false
	}
	if lineBreaks || blockIndicators {
		a.blockPlainAllowed = false
	}
	return a
}

// yamlV2ResolveTable ports the fork's resolve.go table, which reads a
// scalar's first byte as a hint of what the scalar can be.
var yamlV2ResolveTable = func() [256]byte {
	var t [256]byte
	t['+'], t['-'] = 'S', 'S'
	for _, c := range "0123456789" {
		t[c] = 'D'
	}
	for _, c := range "yYnNtTfFoO~" {
		t[c] = 'M'
	}
	t['.'] = '.'
	return t
}()

// yamlV2ResolvesToString reports whether the fork's resolve("", s)
// returns the !!str tag for valid UTF-8 s: false when s would read back
// as a bool, null, int, or float. It is resolvePlain, which follows the
// fork's resolve step by step, so it accepts exactly what spruce does.
func yamlV2ResolvesToString(s string) bool {
	tag, _ := resolvePlain(s)
	return tag == yamlnode.TagStr || tag == yamlnode.TagBinary
}

// yamlV2Base60Float is the fork's base60float expression; such strings
// are double-quoted on the way out for YAML 1.1 readers.
var yamlV2Base60Float = regexp.MustCompile(`^[-+]?\d[0-9_]*(?::[0-5]?\d)+(?:\.[0-9_]*)?$`)

func isBase60Float(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	if (c != '+' && c != '-' && (c < '0' || c > '9')) || strings.IndexByte(s, ':') < 0 {
		return false
	}
	return yamlV2Base60Float.MatchString(s)
}

// requestedScalarStyle is the style the fork's stringv asks the emitter
// for: double quotes for a string that would read back as another type,
// a literal block for a multi-line string, and plain otherwise.
func requestedScalarStyle(s string) scalarStyle {
	if !yamlV2ResolvesToString(s) || isBase60Float(s) {
		return doubleQuotedScalarStyle
	}
	if strings.Contains(s, "\n") {
		return literalScalarStyle
	}
	return plainScalarStyle
}

// selectScalarStyle ports yaml_emitter_select_scalar_style for an
// untagged scalar in block context.
func selectScalarStyle(value []byte, requested scalarStyle, a scalarAnalysis, simpleKey bool) scalarStyle {
	style := requested
	if simpleKey && a.multiline {
		style = doubleQuotedScalarStyle
	}
	if style == plainScalarStyle {
		if !a.blockPlainAllowed {
			style = singleQuotedScalarStyle
		}
		if len(value) == 0 && simpleKey {
			style = singleQuotedScalarStyle
		}
	}
	if style == singleQuotedScalarStyle && !a.singleQuotedAllowed {
		style = doubleQuotedScalarStyle
	}
	if style == literalScalarStyle && (!a.blockAllowed || simpleKey) {
		style = doubleQuotedScalarStyle
	}
	return style
}

// isSimpleKey ports yaml_emitter_check_simple_key for a scalar key.
func isSimpleKey(value []byte, a scalarAnalysis) bool {
	return !a.multiline && len(value) <= simpleKeyMaxLength
}

// scalarWriter is the slice of libyaml's emitter state the scalar
// writers touch: the output, the current column (counted in
// characters), the block indent continuation lines return to, and the
// whitespace and indention flags.
type scalarWriter struct {
	buf        []byte
	column     int
	indent     int
	whitespace bool
	indention  bool
}

// put, putBreak, write, writeAll, and writeBreak port libyaml's PUT,
// PUT_BREAK, WRITE, and WRITE_BREAK macros, counting columns in
// characters.
func (w *scalarWriter) put(c byte) {
	w.buf = append(w.buf, c)
	w.column++
}

func (w *scalarWriter) putBreak() {
	w.buf = append(w.buf, '\n')
	w.column = 0
}

func (w *scalarWriter) write(s []byte, i *int) {
	n := utf8Width(s[*i])
	if n == 0 || *i+n > len(s) {
		n = 1
	}
	w.buf = append(w.buf, s[*i:*i+n]...)
	w.column++
	*i += n
}

func (w *scalarWriter) writeAll(s string) {
	b := []byte(s)
	for i := 0; i < len(b); {
		w.write(b, &i)
	}
}

func (w *scalarWriter) writeBreak(s []byte, i *int) {
	if s[*i] == '\n' {
		w.putBreak()
		*i++
		return
	}
	w.write(s, i)
	w.column = 0
}

// writeIndent ports yaml_emitter_write_indent.
func (w *scalarWriter) writeIndent() {
	indent := w.indent
	if indent < 0 {
		indent = 0
	}
	if !w.indention || w.column > indent || (w.column == indent && !w.whitespace) {
		w.putBreak()
	}
	for w.column < indent {
		w.put(' ')
	}
	w.whitespace = true
	w.indention = true
}

// writeIndicator ports yaml_emitter_write_indicator. Its is_whitespace
// argument is false for every indicator a scalar writer emits, so the
// flag is always cleared.
func (w *scalarWriter) writeIndicator(indicator string, needWhitespace, isIndention bool) {
	if needWhitespace && !w.whitespace {
		w.put(' ')
	}
	w.writeAll(indicator)
	w.whitespace = false
	w.indention = w.indention && isIndention
}

// writePlain ports yaml_emitter_write_plain_scalar.
func (w *scalarWriter) writePlain(value []byte, allowBreaks bool) {
	if !w.whitespace {
		w.put(' ')
	}
	spaces, breaks := false, false
	for i := 0; i < len(value); {
		switch {
		case isSpace(value, i):
			if allowBreaks && !spaces && w.column > scalarBestWidth && !isSpace(value, i+1) {
				w.writeIndent()
				i++
			} else {
				w.write(value, &i)
			}
			spaces = true
		case isBreak(value, i):
			if !breaks && value[i] == '\n' {
				w.putBreak()
			}
			w.writeBreak(value, &i)
			w.indention = true
			breaks = true
		default:
			if breaks {
				w.writeIndent()
			}
			w.write(value, &i)
			w.indention = false
			spaces, breaks = false, false
		}
	}
	w.whitespace = false
	w.indention = false
}

// writeSingleQuoted ports yaml_emitter_write_single_quoted_scalar.
func (w *scalarWriter) writeSingleQuoted(value []byte, allowBreaks bool) {
	w.writeIndicator("'", true, false)
	spaces, breaks := false, false
	for i := 0; i < len(value); {
		switch {
		case isSpace(value, i):
			if allowBreaks && !spaces && w.column > scalarBestWidth && i > 0 && i < len(value)-1 && !isSpace(value, i+1) {
				w.writeIndent()
				i++
			} else {
				w.write(value, &i)
			}
			spaces = true
		case isBreak(value, i):
			if !breaks && value[i] == '\n' {
				w.putBreak()
			}
			w.writeBreak(value, &i)
			w.indention = true
			breaks = true
		default:
			if breaks {
				w.writeIndent()
			}
			if value[i] == '\'' {
				w.put('\'')
			}
			w.write(value, &i)
			w.indention = false
			spaces, breaks = false, false
		}
	}
	w.writeIndicator("'", false, false)
	w.whitespace = false
	w.indention = false
}

// writeDoubleQuoted ports yaml_emitter_write_double_quoted_scalar,
// including its escapes: \0 \a \b \t \n \v \f \r \e \" \\ \N \_ \L \P,
// then \x, \u, or \U with uppercase hex for any other character it
// cannot print.
//
//nolint:gocyclo // a line-for-line port; splitting it would hide the correspondence with libyaml
func (w *scalarWriter) writeDoubleQuoted(value []byte, allowBreaks bool) {
	spaces := false
	w.writeIndicator(`"`, true, false)
	bom := isBOM(value)
	for i := 0; i < len(value); {
		switch {
		case !isPrintable(value, i) || bom || isBreak(value, i) || value[i] == '"' || value[i] == '\\':
			n := utf8Width(value[i])
			if n == 0 || i+n > len(value) {
				n = 1
			}
			var v rune
			switch n {
			case 1:
				v = rune(value[i] & 0x7F)
			case 2:
				v = rune(value[i] & 0x1F)
			case 3:
				v = rune(value[i] & 0x0F)
			case 4:
				v = rune(value[i] & 0x07)
			}
			for k := 1; k < n; k++ {
				v = (v << 6) + (rune(value[i+k]) & 0x3F)
			}
			i += n

			w.put('\\')
			switch v {
			case 0x00:
				w.put('0')
			case 0x07:
				w.put('a')
			case 0x08:
				w.put('b')
			case 0x09:
				w.put('t')
			case 0x0A:
				w.put('n')
			case 0x0B:
				w.put('v')
			case 0x0C:
				w.put('f')
			case 0x0D:
				w.put('r')
			case 0x1B:
				w.put('e')
			case 0x22:
				w.put('"')
			case 0x5C:
				w.put('\\')
			case 0x85:
				w.put('N')
			case 0xA0:
				w.put('_')
			case 0x2028:
				w.put('L')
			case 0x2029:
				w.put('P')
			default:
				digits := 8
				switch {
				case v <= 0xFF:
					w.put('x')
					digits = 2
				case v <= 0xFFFF:
					w.put('u')
					digits = 4
				default:
					w.put('U')
				}
				for k := (digits - 1) * 4; k >= 0; k -= 4 {
					d := byte((v >> uint(k)) & 0x0F)
					if d < 10 {
						w.put(d + '0')
					} else {
						w.put(d + 'A' - 10)
					}
				}
			}
			spaces = false
		case isSpace(value, i):
			if allowBreaks && !spaces && w.column > scalarBestWidth && i > 0 && i < len(value)-1 {
				w.writeIndent()
				if isSpace(value, i+1) {
					w.put('\\')
				}
				i++
			} else {
				w.write(value, &i)
			}
			spaces = true
		default:
			w.write(value, &i)
			spaces = false
		}
	}
	w.writeIndicator(`"`, false, false)
	w.whitespace = false
	w.indention = false
}

// writeBlockScalarHints ports yaml_emitter_write_block_scalar_hints:
// an indent hint when the value opens with a space or a break, then the
// chomping indicator.
func (w *scalarWriter) writeBlockScalarHints(value []byte) {
	if isSpace(value, 0) || isBreak(value, 0) {
		w.writeIndicator(string(rune('0'+scalarBestIndent)), false, false)
	}
	chomp := ""
	if len(value) == 0 {
		chomp = "-"
	} else {
		i := len(value) - 1
		for i > 0 && value[i]&0xC0 == 0x80 {
			i--
		}
		switch {
		case !isBreak(value, i):
			chomp = "-"
		case i == 0:
			chomp = "+"
		default:
			i--
			for i > 0 && value[i]&0xC0 == 0x80 {
				i--
			}
			if isBreak(value, i) {
				chomp = "+"
			}
		}
	}
	if chomp != "" {
		w.writeIndicator(chomp, false, false)
	}
}

// writeLiteral ports yaml_emitter_write_literal_scalar.
func (w *scalarWriter) writeLiteral(value []byte) {
	w.writeIndicator("|", true, false)
	w.writeBlockScalarHints(value)
	w.putBreak()
	w.indention = true
	w.whitespace = true
	breaks := true
	for i := 0; i < len(value); {
		if isBreak(value, i) {
			w.writeBreak(value, &i)
			w.indention = true
			breaks = true
			continue
		}
		if breaks {
			w.writeIndent()
		}
		w.write(value, &i)
		w.indention = false
		breaks = false
	}
}

// writeScalar ports yaml_emitter_process_scalar: it writes value in
// style, folding long lines unless the scalar is a simple key.
func (w *scalarWriter) writeScalar(value []byte, style scalarStyle, simpleKey bool) {
	switch style {
	case plainScalarStyle:
		w.writePlain(value, !simpleKey)
	case singleQuotedScalarStyle:
		w.writeSingleQuoted(value, !simpleKey)
	case doubleQuotedScalarStyle:
		w.writeDoubleQuoted(value, !simpleKey)
	case literalScalarStyle:
		w.writeLiteral(value)
	}
}

// renderValueScalarAs returns spruce's text for string s written as a
// block value or sequence item, in the requested style. column is where
// the scalar starts, just after the "key: " or "- " that introduces it;
// indent is the block indent its continuation lines use (two past the
// enclosing mapping or sequence); indention says whether only
// indentation and indicators precede it on its line, as after "- ". The
// result never includes the separating space.
func renderValueScalarAs(s string, requested scalarStyle, column, indent int, indention bool) string {
	value := []byte(s)
	a := analyzeScalar(value)
	style := selectScalarStyle(value, requested, a, false)
	w := scalarWriter{column: column, indent: indent, whitespace: true, indention: indention}
	w.writeScalar(value, style, false)
	return string(w.buf)
}

// renderSimpleKeyAs returns spruce's text for string s written as a
// simple mapping key, or ok=false when spruce writes s in the explicit
// "? key" form instead.
func renderSimpleKeyAs(s string, requested scalarStyle, column int) (text string, ok bool) {
	value := []byte(s)
	a := analyzeScalar(value)
	if !isSimpleKey(value, a) {
		return "", false
	}
	style := selectScalarStyle(value, requested, a, true)
	w := scalarWriter{column: column, indent: column + scalarBestIndent, whitespace: true, indention: true}
	w.writeScalar(value, style, true)
	return string(w.buf), true
}

// renderComplexKeyAs returns spruce's text for string s written as an
// explicit key in a block mapping whose keys sit at column keyColumn:
// the "? " indicator, the key scalar, a line break where libyaml writes
// one, and the ":" that introduces the value. The value follows the
// colon, separated by a single space.
func renderComplexKeyAs(s string, requested scalarStyle, keyColumn int) string {
	value := []byte(s)
	a := analyzeScalar(value)
	style := selectScalarStyle(value, requested, a, false)
	w := scalarWriter{column: keyColumn, indent: keyColumn, whitespace: true, indention: true}
	w.writeIndicator("?", true, true)
	w.indent = keyColumn + scalarBestIndent
	w.writeScalar(value, style, false)
	w.indent = keyColumn
	w.writeIndent()
	w.writeIndicator(":", true, true)
	return string(w.buf)
}

// yaml11Timestamp is the timestamp expression of the YAML 1.1 type
// repository, which Psych and other 1.1 readers resolve a plain scalar
// against.
var yaml11Timestamp = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[Tt]|[ \t]+)[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]*)?(?:[ \t]*(?:Z|[-+][0-9]{1,2}(?::[0-9]{2})?))?)$`)

// readsAsTimestamp reports whether a YAML 1.1 reader resolves the plain
// scalar s as a timestamp.
func readsAsTimestamp(s string) bool {
	return yaml11Timestamp.MatchString(s)
}

// timestampStyle is the style requested for s when typed is set, which
// asks for double quotes in four cases. One is a string a YAML 1.1
// reader resolves as a timestamp. Another is a string that ends in "<<",
// which goccy's parser reads as a merge key when it ends a key. A string
// with "<<" elsewhere, such as "cat <<EOF", stays plain. A string with a
// line break and leading whitespace is the third, since spruce writes it
// as a literal block with an indentation indicator that goccy's parser
// refuses. The last is a string that starts with "-", "?", or ":", which
// goccy's parser misreads as an indicator in some places. A multi-line
// value that starts that way is a literal block, as it is in spruce, and
// goccy reads the block back, but a multi-line key keeps its quotes
// because goccy misreads some explicit literal keys. A multi-line value
// with U+2028 or U+2029 at the start of a line is double-quoted too,
// since goccy refuses the block that holds it. Otherwise the style is
// the fork's own choice.
func timestampStyle(s string, typed, isKey bool) scalarStyle {
	if typed && (readsAsTimestamp(s) || strings.HasSuffix(s, "<<") || leadsWithSpaceBeforeBreak(s) || startsWithIndicator(s, isKey) || (!isKey && linesStartWithSeparator(s))) {
		return doubleQuotedScalarStyle
	}
	return requestedScalarStyle(s)
}

// startsWithIndicator reports whether s starts with "-", "?", or ":" and
// a character other than a space, which libyaml writes plain. A
// multi-line value is exempt, because it is written as a block.
func startsWithIndicator(s string, isKey bool) bool {
	return len(s) > 1 && (isKey || !strings.Contains(s, "\n")) && strings.ContainsRune("-?:", rune(s[0])) && s[1] != ' '
}

// linesStartWithSeparator reports whether the multi-line s has U+2028 or
// U+2029 where a line starts, which is at its start or after a line
// feed or another separator.
func linesStartWithSeparator(s string) bool {
	if !strings.Contains(s, "\n") {
		return false
	}
	for i, r := range s {
		if (r == '\u2028' || r == '\u2029') && (i == 0 || s[i-1] == '\n' || strings.HasSuffix(s[:i], "\u2028") || strings.HasSuffix(s[:i], "\u2029")) {
			return true
		}
	}
	return false
}

// leadsWithSpaceBeforeBreak reports whether s holds a line break and
// starts with a space, a tab, or a line break.
func leadsWithSpaceBeforeBreak(s string) bool {
	return strings.ContainsAny(s, "\r\n") && strings.ContainsAny(s[:1], " \t\r\n")
}

// maxQuotedKeyLength is the longest key YAML lets a double-quoted
// implicit key be.
const maxQuotedKeyLength = 1024

// fitsQuotedKey reports whether k written double-quoted fits on one line
// as an implicit key.
func fitsQuotedKey(k string) bool {
	return len(strconv.Quote(k)) <= maxQuotedKeyLength
}
