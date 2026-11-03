package yamlprep

import (
	"slices"
	"strings"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

// goccy's scanner counts a tab inside a double-quoted scalar twice when
// a character other than a blank follows it on its line and the closing
// quote does not come right after it. The scanner then runs one
// character ahead for each such tab, so it skips the character after the
// closing quote, such as the line break, the "," or "]" in a flow
// collection, or the ":" after a key, and either fails the document or
// reads the rest of the line wrong, at the wrong columns. libyaml reads
// "a: \"p<TAB>q\"" as the string "p<TAB>q".

// mayHoldQuotedTab reports whether src has a tab after a double quote,
// which every double-quoted scalar holding a tab needs.
func mayHoldQuotedTab(src string) bool {
	q := strings.IndexByte(src, '"')
	return q >= 0 && strings.LastIndexByte(src, '\t') > q
}

// unusedRune returns a rune of the Unicode private use area that src
// does not hold.
func unusedRune(src string) (rune, bool) {
	for r := rune(0xE000); r <= 0xF8FF; r++ {
		if !strings.ContainsRune(src, r) {
			return r, true
		}
	}
	return 0, false
}

// doubledQuotedTabs returns the offsets in src, in order, of each tab
// inside a double-quoted scalar that goccy counts twice. scan holds the
// tokens goccy gives src with each tab made a space, which it reads
// right, and idx indexes that text. Each double-quoted scalar of scan is
// walked in src from its opening quote to its closing one. A scalar with
// no closing quote keeps its tabs, and a token whose place holds no
// opening quote in src marks nothing.
func doubledQuotedTabs(src string, scan token.Tokens, idx byteIndex) []int {
	var tabs []int
	for _, tk := range scan {
		if tk.Type != token.DoubleQuoteType || tk.Position == nil {
			continue
		}
		// goccy can place a scalar at the blank before its opening quote,
		// as it does after a tag, so the quote is the first character at
		// or after that place that is no blank.
		start, ok := idx.offset(tk.Position.Line, tk.Position.Column)
		for ok && start < len(src) && (src[start] == ' ' || src[start] == '\t') {
			start++
		}
		if !ok || start >= len(src) || src[start] != '"' {
			continue
		}
		tabs = append(tabs, quotedTabs(src, start)...)
	}
	slices.Sort(tabs)
	return slices.Compact(tabs)
}

// tokenizeQuotedTabs returns the tokens goccy gives text with a marker
// in place of each tab at an offset in tabs, which are in order, and
// each double-quoted scalar given its tabs back in its value and source
// text. The marker is a character text does not hold, which the scanner
// counts once and keeps in the value as it keeps the tab. It reports
// false when no such character is left, or when the marker reaches a
// token other than a double-quoted scalar.
func tokenizeQuotedTabs(text string, tabs []int) (token.Tokens, bool) {
	marker, ok := unusedRune(text)
	if !ok {
		return nil, false
	}
	toks := lexer.Tokenize(markAt(text, tabs, marker))
	for _, tk := range toks {
		if tk.Type != token.DoubleQuoteType &&
			(strings.ContainsRune(tk.Value, marker) || strings.ContainsRune(tk.Origin, marker)) {
			return nil, false
		}
	}
	m := string(marker)
	for _, tk := range toks {
		if tk.Type == token.DoubleQuoteType {
			tk.Value = strings.ReplaceAll(tk.Value, m, "\t")
			tk.Origin = strings.ReplaceAll(tk.Origin, m, "\t")
		}
	}
	return toks, true
}

// markAt returns src with marker in place of the byte at each offset in
// at, which are in order and each hold a tab.
func markAt(src string, at []int, marker rune) string {
	var b strings.Builder
	b.Grow(len(src) + 2*len(at))
	last := 0
	for _, i := range at {
		b.WriteString(src[last:i])
		b.WriteRune(marker)
		last = i + 1
	}
	b.WriteString(src[last:])
	return b.String()
}

// quotedTabs returns the offsets of the tabs goccy counts twice in the
// double-quoted scalar whose opening quote is at src[start], in order,
// or none when the scalar has no closing quote. Those are the tabs that
// neither start a line of the scalar nor end one, so a character other
// than a blank follows them on their line. An escape is skipped as
// goccy skips it, so an escaped quote never closes the scalar and an
// escaped tab is no tab to mark.
func quotedTabs(src string, start int) []int {
	var tabs []int
	lineStart := false
	for i := start + 1; i < len(src); i++ {
		switch c := src[i]; c {
		case '"':
			return tabs
		case '\\':
			i += escapeLength(src, i)
			lineStart = src[i] == '\n' || src[i] == '\r'
		case '\n', '\r':
			lineStart = true
		case ' ':
		case '\t':
			if !lineStart && lineHasTextAfter(src, i+1) {
				tabs = append(tabs, i)
			}
		default:
			lineStart = false
		}
	}
	return nil
}

// escapeLength returns how many bytes after the backslash at src[i] the
// escape it starts runs on, as goccy's scanner reads it: the "\r\n"
// after an escaped line break, the digits of a \x, \u, or \U escape, or
// else the one character after the backslash.
func escapeLength(src string, i int) int {
	rest := len(src) - i - 1
	if rest <= 0 {
		return 0
	}
	n := 1
	switch src[i+1] {
	case '\r':
		if rest >= 2 && src[i+2] == '\n' {
			n = 2
		}
	case 'x':
		n = 3
	case 'u':
		n = 5
	case 'U':
		n = 9
	}
	return min(n, rest)
}

// lineHasTextAfter reports whether src holds a character other than a
// space or a tab from offset i to the end of its line.
func lineHasTextAfter(src string, i int) bool {
	for ; i < len(src); i++ {
		switch src[i] {
		case ' ', '\t':
		case '\n', '\r':
			return false
		default:
			return true
		}
	}
	return false
}
