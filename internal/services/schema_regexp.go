package services

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Cloud Controller compiles each patternProperties key with Ruby's
// Regexp.new, and a key Onigmo can't compile refuses the catalog. Go has
// no Onigmo, so checkRubyRegexp accepts only a subset of Ruby's syntax
// that Onigmo is known to compile, and refuses the rest, including some
// patterns Ruby would compile. The subset is literals, the escapes \d,
// \D, \w, \W, \s, \S, \b, \B, \A, \z, \Z, \n, \t, \r, \f, \v, \uXXXX, and
// \xHH below 0x80, escaped punctuation, ., ^, and $, groups (, (?:, (?=,
// and (?!, alternation, the quantifiers *, +, ?, {n}, {n,}, and {n,m} up
// to 100000, each optionally lazy, backreferences \1 to \9 to a group
// already closed, and bracket classes of literals, escapes, and ranges.

// maxRubyRepeat is Onigmo's largest repeat count.
const maxRubyRepeat = 100000

// The hex digits a \u and a \x escape take.
const (
	unicodeEscapeDigits = 4
	hexEscapeDigits     = 2
)

type rubyRegexpParser struct {
	pattern string
	at      int
	groups  int
	closed  map[int]bool
}

// checkRubyRegexp refuses pattern unless it is in the subset above.
func checkRubyRegexp(pattern string) error {
	parser := &rubyRegexpParser{pattern: pattern, closed: map[int]bool{}}

	ok := parser.alternation(0) && parser.at == len(pattern)
	if !ok {
		return gemFault(faultUnmodelled, "the patternProperties key %q is outside the Ruby regular expressions the broker checks", pattern)
	}

	return nil
}

func (parser *rubyRegexpParser) peek() byte {
	if parser.at < len(parser.pattern) {
		return parser.pattern[parser.at]
	}

	return 0
}

func (parser *rubyRegexpParser) done() bool {
	return parser.at >= len(parser.pattern)
}

// alternation parses sequences separated by |, up to a ) or the end.
func (parser *rubyRegexpParser) alternation(depth int) bool {
	if depth > maxGemDepth {
		return false
	}

	for {
		if !parser.sequence(depth) {
			return false
		}

		if parser.peek() != '|' || parser.done() {
			return true
		}

		parser.at++
	}
}

// sequence parses atoms, each optionally quantified.
func (parser *rubyRegexpParser) sequence(depth int) bool {
	for !parser.done() {
		char := parser.peek()
		if char == '|' || char == ')' {
			return true
		}

		quantifiable, ok := parser.atom(depth)
		if !ok {
			return false
		}

		if parser.quantifierNext() {
			if !quantifiable || !parser.quantifier() {
				return false
			}

			if parser.quantifierNext() {
				return false
			}
		}
	}

	return true
}

func (parser *rubyRegexpParser) quantifierNext() bool {
	if parser.done() {
		return false
	}

	switch parser.peek() {
	case '*', '+', '?', '{':
		return true
	}

	return false
}

// quantifier parses one quantifier and an optional lazy ?.
func (parser *rubyRegexpParser) quantifier() bool {
	if parser.peek() == '{' {
		if !parser.interval() {
			return false
		}
	} else {
		parser.at++
	}

	if !parser.done() && parser.peek() == '?' {
		parser.at++
	}

	return true
}

// interval parses {n}, {n,}, or {n,m}.
func (parser *rubyRegexpParser) interval() bool {
	end := strings.IndexByte(parser.pattern[parser.at:], '}')
	if end < 0 {
		return false
	}

	body := parser.pattern[parser.at+1 : parser.at+end]
	low, high, ranged := strings.Cut(body, ",")

	lowCount, ok := repeatCount(low)
	if !ok {
		return false
	}

	if ranged && high != "" {
		highCount, ok := repeatCount(high)
		if !ok || highCount < lowCount {
			return false
		}
	}

	parser.at += end + 1

	return true
}

func repeatCount(text string) (int, bool) {
	if text == "" || len(text) > 6 {
		return 0, false
	}

	for index := range len(text) {
		if !isASCIIDigit(text[index]) {
			return 0, false
		}
	}

	count, err := strconv.Atoi(text)

	return count, err == nil && count <= maxRubyRepeat
}

// atom parses one atom and reports whether a quantifier may follow it.
func (parser *rubyRegexpParser) atom(depth int) (bool, bool) {
	char := parser.peek()

	switch char {
	case '(':
		return parser.group(depth)
	case '[':
		return true, parser.class()
	case '\\':
		return parser.escape()
	case '^', '$':
		parser.at++

		return false, true
	case '.':
		parser.at++

		return true, true
	case ']', '}', '{', '*', '+', '?':
		return false, false
	}

	r, size := utf8.DecodeRuneInString(parser.pattern[parser.at:])
	if r == utf8.RuneError || r == 0 {
		return false, false
	}

	parser.at += size

	return true, true
}

// group parses (, (?:, (?=, or (?! with its alternation and ).
func (parser *rubyRegexpParser) group(depth int) (bool, bool) {
	parser.at++
	capturing := true
	quantifiable := true
	number := 0

	if strings.HasPrefix(parser.pattern[parser.at:], "?") {
		switch {
		case strings.HasPrefix(parser.pattern[parser.at:], "?:"):
		case strings.HasPrefix(parser.pattern[parser.at:], "?="), strings.HasPrefix(parser.pattern[parser.at:], "?!"):
			quantifiable = false
		default:
			return false, false
		}

		parser.at += 2
		capturing = false
	}

	if capturing {
		parser.groups++
		number = parser.groups
	}

	if !parser.alternation(depth+1) || parser.peek() != ')' || parser.done() {
		return false, false
	}

	parser.at++

	if capturing {
		parser.closed[number] = true
	}

	return quantifiable, true
}

// escape parses an escape outside a class.
func (parser *rubyRegexpParser) escape() (bool, bool) {
	if parser.at+1 >= len(parser.pattern) {
		return false, false
	}

	char := parser.pattern[parser.at+1]

	switch {
	case strings.IndexByte("AzZbB", char) >= 0:
		parser.at += 2

		return false, true
	case char >= '1' && char <= '9':
		parser.at += 2
		if !parser.closed[int(char-'0')] || (!parser.done() && isASCIIDigit(parser.peek())) {
			return false, false
		}

		return true, true
	}

	return true, parser.classEscape(true)
}

// classEscape parses an escape that is one character or one shorthand
// class, and reports whether it was one character in single.
func (parser *rubyRegexpParser) classEscape(shorthandAllowed bool) bool {
	if parser.at+1 >= len(parser.pattern) {
		return false
	}

	char := parser.pattern[parser.at+1]

	switch {
	case strings.IndexByte("dDwWsS", char) >= 0:
		parser.at += 2

		return shorthandAllowed
	case strings.IndexByte("ntrfv", char) >= 0:
		parser.at += 2

		return true
	case char == 'u':
		return parser.hexEscape(unicodeEscapeDigits)
	case char == 'x':
		return parser.hexEscape(hexEscapeDigits)
	case char < 0x80 && !isASCIILetter(char) && !isASCIIDigit(char) && char > ' ':
		parser.at += 2

		return true
	}

	return false
}

// hexEscape parses \u with four hex digits, which may not name a
// surrogate, or \x with two, which must be below 0x80.
func (parser *rubyRegexpParser) hexEscape(width int) bool {
	start := parser.at + len(`\u`)
	if start+width > len(parser.pattern) {
		return false
	}

	value, err := strconv.ParseUint(parser.pattern[start:start+width], 16, 32)
	if err != nil || strings.ContainsAny(parser.pattern[start:start+width], "+-_") {
		return false
	}

	if (width == unicodeEscapeDigits && value >= 0xD800 && value <= 0xDFFF) || (width == hexEscapeDigits && value >= 0x80) {
		return false
	}

	parser.at = start + width

	return true
}

// class parses a bracket class of literals, single-character escapes,
// shorthand classes, and ranges between single characters.
//
//nolint:cyclop // one branch per kind of class member Onigmo reads
func (parser *rubyRegexpParser) class() bool {
	parser.at++

	if parser.peek() == '^' && !parser.done() {
		parser.at++
	}

	members := 0

	for {
		if parser.done() {
			return false
		}

		char := parser.peek()

		switch {
		case char == ']':
			parser.at++

			return members > 0
		case char == '[' || strings.HasPrefix(parser.pattern[parser.at:], "&&"):
			return false
		case char == '-':
			parser.at++

			if members > 0 && parser.peek() != ']' {
				return false
			}
		default:
			low, single, ok := parser.classMember()
			if !ok {
				return false
			}

			if parser.peek() == '-' && parser.at+1 < len(parser.pattern) && parser.pattern[parser.at+1] != ']' {
				parser.at++

				high, highSingle, ok := parser.classMember()
				if !ok || !single || !highSingle || high < low {
					return false
				}
			}
		}

		members++
	}
}

// classMember parses one member and returns its character, when it is
// a single character.
func (parser *rubyRegexpParser) classMember() (rune, bool, bool) {
	if parser.peek() == '\\' {
		start := parser.at
		if !parser.classEscape(true) {
			return 0, false, false
		}

		text := parser.pattern[start:parser.at]

		switch {
		case len(text) == len(`\d`) && strings.IndexByte("dDwWsS", text[1]) >= 0:
			return 0, false, true
		case len(text) == len(`\n`) && strings.IndexByte("ntrfv", text[1]) >= 0:
			return rune(map[byte]byte{'n': '\n', 't': '\t', 'r': '\r', 'f': '\f', 'v': '\v'}[text[1]]), true, true
		case len(text) > len(`\x`):
			value, err := strconv.ParseUint(text[len(`\x`):], 16, 32)
			if err != nil || value > unicode.MaxRune {
				return 0, false, false
			}

			return rune(value), true, true
		default:
			return rune(text[1]), true, true
		}
	}

	char, size := utf8.DecodeRuneInString(parser.pattern[parser.at:])
	if char == utf8.RuneError || char == 0 {
		return 0, false, false
	}

	parser.at += size

	return char, true, true
}
