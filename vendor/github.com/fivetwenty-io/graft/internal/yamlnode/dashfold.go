package yamlnode

import (
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/token"
)

// goccy's scanner reads a continuation line of a plain scalar that starts
// with "-", such as the second line of "a: v" followed by "  - x", as the
// start of a folded block scalar. From that line to the end of the scalar
// it keeps a comment as text, keeps the spaces and tabs at the end of each
// line, keeps the line break before a deeper line, and takes a ": " as
// text. libyaml, and so spruce, reads the line as more of the plain scalar,
// so a comment ends it, the blanks at the ends of each line go, every line
// break between two lines of text folds to a space, and a ": " fails the
// document.

// dashFoldErrors are the messages of the errors readDashContinuations
// gives a scalar that libyaml refuses. Neither quotes the input.
const (
	dashFoldMappingValue     = "mapping values are not allowed in this context"
	dashFoldTextAfterComment = "a plain scalar cannot continue after a comment"
)

// readDashContinuations gives every plain scalar token in toks that holds a
// continuation line starting with "-" the value libyaml reads for it, which
// is a string. When libyaml refuses the scalar, because a line of it holds
// a mapping value or text follows a comment in it, the token becomes an
// invalid one at the place libyaml stops, so the parse fails there.
func readDashContinuations(toks token.Tokens) {
	for i, tk := range toks {
		if !isPlainScalar(tk) || !hasDashContinuation(tk.Origin) || !isPlainValue(toks, i) {
			continue
		}
		v, fail := dashFoldValue(tk.Origin)
		if fail.msg != "" {
			line := originStart(toks, i)
			pos := *tk.Position
			pos.Line, pos.Column = line+fail.line, fail.column
			tk.Type, tk.CharacterType, tk.Error, tk.Position = token.InvalidType, token.CharacterTypeInvalid, fail.msg, &pos
			continue
		}
		// The fold drops only blanks and a comment at the end, so a value
		// that holds anything else goccy's does not comes from an Origin
		// this fold does not read as goccy does, and goccy's value stands.
		if !strings.HasPrefix(stripBlanks(tk.Value), stripBlanks(v)) {
			continue
		}
		s := token.String(v, tk.Origin, tk.Position)
		tk.Type, tk.CharacterType, tk.Indicator, tk.Value = s.Type, s.CharacterType, s.Indicator, s.Value
	}
}

// hasDashContinuation reports whether a line of origin after its first
// starts with "-" once its indentation is set aside.
func hasDashContinuation(origin string) bool {
	_, rest, ok := strings.Cut(origin, "\n")
	for ok {
		var line string
		line, rest, ok = strings.Cut(rest, "\n")
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "-") {
			return true
		}
	}
	return false
}

// dashFoldFailure says where libyaml stops in a scalar it refuses, as the
// index of the line in the scalar's Origin and the 1-based column there,
// and why. msg is "" when libyaml reads the scalar.
type dashFoldFailure struct {
	line, column int
	msg          string
}

// dashFoldValue returns the value libyaml reads for the plain scalar whose
// source text, with the blanks and line breaks around it, is origin, as
// plainValue does, except that a comment ends the scalar. It returns a
// failure when a line holds a mapping value, or when a line after the
// comment holds text that is not another comment.
func dashFoldValue(origin string) (string, dashFoldFailure) {
	var b strings.Builder
	empty, started, ended := 0, false, false
	for n, raw := range strings.Split(origin, "\n") {
		lead := len(raw) - len(strings.TrimLeft(raw, " \t"))
		line := strings.Trim(raw, " \t\r")
		if ended {
			if line != "" && line[0] != '#' {
				return "", dashFoldFailure{n, column(raw, lead), dashFoldTextAfterComment}
			}
			continue
		}
		if c := commentAt(line); c >= 0 {
			line, ended = strings.TrimRight(line[:c], " \t"), true
		}
		if c := mappingValueAt(line); c >= 0 {
			return "", dashFoldFailure{n, column(raw, lead+c), dashFoldMappingValue}
		}
		switch {
		case line == "":
			empty++
			continue
		case !started:
			started = true
		case empty == 0:
			b.WriteByte(' ')
		default:
			b.WriteString(strings.Repeat("\n", empty))
		}
		empty = 0
		b.WriteString(line)
	}
	return b.String(), dashFoldFailure{}
}

// commentAt returns the index in line, which has no blanks at its ends, of
// the "#" that starts a comment, which is one at the start of the line or
// after a space or a tab, or -1.
func commentAt(line string) int {
	for j := 0; j < len(line); j++ {
		if line[j] == '#' && (j == 0 || line[j-1] == ' ' || line[j-1] == '\t') {
			return j
		}
	}
	return -1
}

// mappingValueAt returns the index in line, which has no blanks at its
// ends, of a ":" that a blank or the end of the line follows, which ends a
// plain scalar in a block collection, or -1.
func mappingValueAt(line string) int {
	for j := 0; j < len(line); j++ {
		if line[j] == ':' && (j == len(line)-1 || line[j+1] == ' ' || line[j+1] == '\t') {
			return j
		}
	}
	return -1
}

// column returns the 1-based column of the byte at index at of line.
func column(line string, at int) int {
	return utf8.RuneCountInString(line[:at]) + 1
}

// originStart returns the line on which the Origin of toks[i] starts,
// which is where the token before it ends. The first token's Origin
// starts on line 1.
func originStart(toks token.Tokens, i int) int {
	if i == 0 || toks[i-1].Position == nil {
		return 1
	}
	prev := toks[i-1]
	return prev.Position.Line + strings.Count(prev.Origin, "\n")
}

// stripBlanks returns s without its spaces, tabs, and line breaks.
func stripBlanks(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}
