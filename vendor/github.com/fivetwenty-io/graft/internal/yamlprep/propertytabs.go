package yamlprep

import (
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/token"
)

// goccy's scanner reads a tag up to the next space or line break, and an
// anchor's name the same way, so a tab after either, which YAML counts as
// the blank that ends it, does not end it. The tag or the name takes in
// the tab and the text after it on its line. "a: !!str<TAB>x" then reads
// as a tag with no value, so a key on the next line moves under "a", and
// "a: &n<TAB>x" anchors nothing and fails the alias that names it.
// libyaml reads both as they read with a space in place of the tab.

// propertyTab is a run of blanks holding a tab that ends a tag or an
// anchor's name: its offset in the source, its text, and the index among
// the tags and anchor names of the one it ends.
type propertyTab struct {
	at, index int
	blanks    string
}

// propertyTabs returns the runs of blanks in src that start with a tab
// and end a tag or an anchor's name, in order. scan holds the tokens
// goccy gives src with each tab made a space, and idx indexes that text.
// A tag or a name ends at a tab when goccy, reading the text with
// spaces, gives it a token whose text is in src right before the tab.
// The run goes on to the next character other than a space or a tab.
func propertyTabs(src string, scan token.Tokens, idx byteIndex) []propertyTab {
	var tabs []propertyTab
	index := -1
	for i, tk := range scan {
		if !isPropertyName(scan, i) {
			continue
		}
		index++
		if tk.Position == nil || tk.Value == "" {
			continue
		}
		start, ok := propertyStart(src, idx, tk)
		end := start + len(tk.Value)
		if !ok || end >= len(src) || src[end] != '\t' || src[start:end] != tk.Value {
			continue
		}
		stop := end
		for stop < len(src) && (src[stop] == ' ' || src[stop] == '\t') {
			stop++
		}
		tabs = append(tabs, propertyTab{at: end, index: index, blanks: src[end:stop]})
	}
	return tabs
}

// propertyStart returns the offset in src of the text of tk, a tag or an
// anchor's name. goccy can place the token at the blank before it, as it
// does after a tag, so the text starts at the first character at or
// after that place that is no blank, past the "&" of an anchor. A tag
// must start with "!", and a name must follow an "&".
func propertyStart(src string, idx byteIndex, tk *token.Token) (int, bool) {
	start, ok := idx.offset(tk.Position.Line, tk.Position.Column)
	for ok && start < len(src) && (src[start] == ' ' || src[start] == '\t') {
		start++
	}
	switch {
	case !ok || start >= len(src):
		return 0, false
	case tk.Type == token.TagType:
		return start, src[start] == '!'
	case src[start] == '&':
		return start + 1, true
	default:
		return start, start > 0 && src[start-1] == '&'
	}
}

// isPropertyName reports whether toks[i] is a tag or the name of an
// anchor, which goccy gives a string token after the anchor's "&".
func isPropertyName(toks token.Tokens, i int) bool {
	tk := toks[i]
	return tk.Type == token.TagType ||
		tk.Type == token.StringType && i > 0 && toks[i-1].Type == token.AnchorType
}

// keepPropertyTabs puts the blanks of each run in tabs back into toks,
// the tokens goccy gives text, which is src with those runs made spaces,
// so that the tokens read as goccy reads a tab between two tokens
// elsewhere. A run goes back after the text of the tag or name it ends,
// into what is left of that token's source text and then into the next
// token's, over the spaces that stand for it there. goccy skips such a
// tab without counting it in a column, so each token on the run's line
// moves back one column for each tab of the run before its column. When
// the tags and names of toks are not those the runs were found among,
// toks stays as it is.
func keepPropertyTabs(toks token.Tokens, scan token.Tokens, text string, tabs []propertyTab) {
	if len(tabs) == 0 {
		return
	}
	var names []int
	for i := range toks {
		if isPropertyName(toks, i) {
			names = append(names, i)
		}
	}
	var scanned []string
	for i, tk := range scan {
		if isPropertyName(scan, i) {
			scanned = append(scanned, tk.Value)
		}
	}
	if len(names) != len(scanned) {
		return
	}
	uncounted := map[int][]int{}
	for _, tab := range tabs {
		i := names[tab.index]
		tk := toks[i]
		if tk.Value != scanned[tab.index] {
			continue
		}
		rest := tab.blanks
		if k := strings.Index(tk.Origin, tk.Value); k >= 0 && strings.Trim(tk.Origin[:k], " \t\r\n") == "" {
			tk.Origin, rest = overBlanks(tk.Origin, k+len(tk.Value), rest)
		}
		if rest != "" && i+1 < len(toks) {
			toks[i+1].Origin, _ = overBlanks(toks[i+1].Origin, 0, rest)
		}
		line := 1 + strings.Count(text[:tab.at], "\n")
		from := strings.LastIndexByte(text[:tab.at], '\n') + 1
		for k := range len(tab.blanks) {
			if tab.blanks[k] == '\t' {
				uncounted[line] = append(uncounted[line], goccyColumn(text[from:tab.at+k]))
			}
		}
	}
	uncountTabs(toks, uncounted)
}

// uncountTabs moves each token back one column for each column in
// uncounted, by line, that comes before its own. An anchor's name moves
// as its "&" does, so that the two stay side by side where goccy places
// the "&" at a blank before it, as it does after a tag.
func uncountTabs(toks token.Tokens, uncounted map[int][]int) {
	if len(uncounted) == 0 {
		return
	}
	moved := map[*token.Position]bool{}
	n := 0
	for i, tk := range toks {
		if tk.Position == nil || moved[tk.Position] {
			continue
		}
		moved[tk.Position] = true
		if !isPropertyName(toks, i) || tk.Type == token.TagType {
			n = 0
			for _, col := range uncounted[tk.Position.Line] {
				if col < tk.Position.Column {
					n++
				}
			}
		}
		tk.Position.Column = max(tk.Position.Column-n, 1)
	}
}

// GoccyColumn returns the column goccy gives a token that starts right
// after prefix, the start of its line. It counts a tab outside a quoted
// scalar as no column, as goccy counts none between two tokens.
func GoccyColumn(prefix string) int {
	return goccyColumn(prefix)
}

// goccyColumn returns the column goccy gives the character after prefix,
// the start of its line. goccy counts each character but a tab outside
// a quoted scalar.
func goccyColumn(prefix string) int {
	col := 1
	var q lineQuotes
	for i := 0; i < len(prefix); {
		r, size := utf8.DecodeRuneInString(prefix[i:])
		i += size
		if q.quote == 0 && r == '\t' {
			q.prev = r
			continue
		}
		if skip := q.step(r, prefix[i:]); skip > 0 {
			i += skip
			col++
		}
		col++
		q.prev = r
	}
	return col
}

// lineQuotes follows a walk along a line through its quoted scalars.
// quote is the quote the walk is inside, or 0, and prev is the character
// before the current one, or 0 at the start of the line. flowMaps counts
// the flow mappings open on the line, keyed reports that a ":" has ended
// a key since the last token, and delim reports that prev is such a ":".
type lineQuotes struct {
	quote, prev  rune
	flowMaps     int
	keyed, delim bool
}

// step moves the walk over r, which rest follows on the line, and
// returns the length of the character after r when r and that character
// stand for one character of a quoted scalar, an escape or a doubled
// single quote, or 0. A quote opens a quoted scalar after a blank, the
// start of the line, "[", "{", ",", or a ":" that ends a key.
func (q *lineQuotes) step(r rune, rest string) int {
	delim := q.delim
	q.delim = false
	switch {
	case q.quote == 0 && q.opens(r, delim):
		q.quote = r
	case q.quote == '"' && r == '\\' && rest != "":
		_, size := utf8.DecodeRuneInString(rest)
		return size
	case q.quote == '"' && r == '"':
		q.quote, q.keyed = 0, false
	case q.quote == '\'' && r == '\'':
		if strings.HasPrefix(rest, "'") {
			return 1
		}
		q.quote, q.keyed = 0, false
	case q.quote == 0:
		q.delim = q.flow(r, strings.HasPrefix(rest, "/"))
	}
	return 0
}

// opens reports whether r, outside quotes, opens a quoted scalar. delim
// reports that the character before r is a ":" that ends a key.
func (q *lineQuotes) opens(r rune, delim bool) bool {
	return (r == '"' || r == '\'') && (delim || strings.ContainsRune("\x00 \t[{,", q.prev))
}

// flow follows goccy's scanner over r, outside quotes, through the flow
// mappings of the line, and reports whether r is a ":" that ends a key
// with no blank after it. In a flow mapping, or anything inside one,
// goccy reads a ":" as ending a key unless one has ended since the last
// token or a "/" follows it, as slash does. A quote right after it then
// opens a quoted scalar, as in {"k":"v"} and {k:"v"}. Outside a flow
// mapping a ":" ends a key only before a blank, which opens a quote too.
func (q *lineQuotes) flow(r rune, slash bool) bool {
	switch r {
	case '{':
		q.flowMaps++
		q.keyed = false
	case '}':
		q.flowMaps = max(q.flowMaps-1, 0)
		q.keyed = false
	case '[', ']', ',':
		q.keyed = false
	case ':':
		if q.flowMaps > 0 && !q.keyed && !slash {
			q.keyed = true
			return true
		}
	}
	return false
}

// overBlanks writes the start of blanks over the spaces of origin from
// offset at on, for as long as both last, and returns origin and the
// rest of blanks.
func overBlanks(origin string, at int, blanks string) (string, string) {
	n := 0
	for n < len(blanks) && at+n < len(origin) && origin[at+n] == ' ' {
		n++
	}
	if n == 0 {
		return origin, blanks
	}
	return origin[:at] + blanks[:n] + origin[at+n:], blanks[n:]
}

// mayHoldPropertyTab reports whether src has a tab right after a run of
// characters other than blanks and line breaks that holds a "!" or a
// "&", which every tag or anchor with a tab after it needs.
func mayHoldPropertyTab(src string) bool {
	for i := 0; i < len(src); i++ {
		if src[i] != '\t' {
			continue
		}
		j := i
		for j > 0 && !isBlankOrBreak(src[j-1]) {
			j--
		}
		if strings.ContainsAny(src[j:i], "!&") {
			return true
		}
	}
	return false
}

// isBlankOrBreak reports whether c is a space, a tab, or a line break.
func isBlankOrBreak(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// spaceAt returns src with each run in tabs made spaces.
func spaceAt(src string, tabs []propertyTab) string {
	if len(tabs) == 0 {
		return src
	}
	b := []byte(src)
	for _, tab := range tabs {
		for k := range len(tab.blanks) {
			b[tab.at+k] = ' '
		}
	}
	return string(b)
}
