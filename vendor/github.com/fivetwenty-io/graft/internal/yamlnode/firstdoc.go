package yamlnode

import (
	"bytes"
	"unicode/utf8"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// FirstDocument returns the first document of src, raw YAML bytes, for
// callers that parse with goccy and read only the first document, as
// graft's merge does. goccy parses every document in a stream, so
// without the cut a syntax error or deep nesting in a later document
// would fail a merge that never reads it. FirstDocument fails a first
// document that nests deeper than yaml.v3 allows, with yaml.v3's message
// and line.
//
// The result has LF line breaks. goccy folds a quoted scalar correctly
// only across LF breaks, so FirstDocument turns every CRLF and lone CR
// into an LF, as Parse does. Each line and column in the result is
// still the one src has, because a CRLF, a lone CR, and an LF are each
// one line break.
//
// The directives before the first document are checked as spruce's
// YAML library checks them, and fail as it fails them. See startRun.
// They then become blank lines in the result, since goccy accepts no
// more than one directive before a "---" and refuses some versions
// spruce accepts, and the merge reads no tag a %TAG directive declares.
// So the result is a copy when src holds such a directive, a CR, or a
// U+0085, and otherwise it is src itself or a prefix of it.
//
// The result ends where the line holding the "---" that ends the first
// document starts, where the line after a "..." that ends it starts, or
// where a directive line that ends it starts. spruce reads a directive
// after content as the end of the document, and goccy refuses one. What
// follows a "..." or a directive has to be directives and then a "---",
// as spruce reads the stream, so FirstDocument fails anything else, and
// any directive spruce fails there, rather than drop it. A "..." at
// column 1 that a character other than a blank or a line break follows
// is a plain scalar to yaml.v3, and a document end to goccy, which would
// drop what follows. FirstDocument fails it, with yaml.v3's message and
// line where yaml.v3 fails it too. See falseEndError. A "..." marker
// before any content fails as spruce fails it.
//
// The marker is the token the depth probe confirms in two prefixes, or
// in the whole input. When nothing ends the first document, or when the
// cut cannot be proved, FirstDocument returns src as it stands. See
// documentEnd for the proof. goccy then parses every document in src,
// so FirstDocument fails nesting past maxDepth in any of them, not only
// in the first.
//
// It tokenizes the way probeDepth does. Input that cannot nest past
// maxDepth is tokenized once, whole, and other input in prefixes that
// double until the first document ends or the depth scan trips. Input
// with no line that could end the first document, which is most input,
// gets only the depth probe. See hasLaterMarker.
func FirstDocument(src []byte) ([]byte, error) {
	src = normalizeLineBreaks(src)
	run, err := startRun(src, 0, true)
	if err != nil {
		return nil, err
	}
	if len(run.lines) > 0 {
		src = blankLines(src, run.lines)
	}
	first, err := cutFirstDocument(src)
	if err != nil {
		return nil, err
	}
	return first, nil
}

// startRun reads src, the lines of a stream from line n, counted from 0,
// where a document has to start, and returns the directives it finds
// there, or the error spruce's YAML library gives. first reports
// whether that is the first document of the stream.
//
// Blank lines and comments are skipped, and each directive is checked.
// See directiveRun. A "---" starts the document. After a directive,
// anything else fails with "did not find expected <document start>", on
// the line of what came instead, or past the last line at the end of
// the input. A "..." before any directive fails the first document with
// "did not find expected node content", on its line, since libyaml
// reads the start of that document as implicit there and finds no node.
// Other content starts the first document. A later document has to
// start with a "---", so content fails it, and a "..." with nothing
// but a comment after it is skipped.
func startRun(src []byte, n int, first bool) (*directiveRun, error) {
	run := &directiveRun{}
	for ; len(src) > 0; n++ {
		var line []byte
		line, src, _ = cutLine(src)
		switch {
		case isBlankOrComment(line):
		case line[0] == '%':
			if err := run.check(line, n); err != nil {
				return nil, err
			}
		case isMarkerLine(line) && line[0] == '-':
			return run, nil
		case len(run.lines) > 0:
			return nil, documentStartError(n + 1)
		case isMarkerLine(line) && first:
			return nil, &ParseError{Line: n, SourceLine: n + 1, Message: "did not find expected node content"}
		case isMarkerLine(line) && isBlankOrComment(line[3:]):
		case first:
			return run, nil
		default:
			return nil, documentStartError(n + 1)
		}
	}
	if len(run.lines) > 0 {
		// libyaml ends the last line at the end of the input, even when
		// no line break ends it, so the end lies on the line after it.
		return nil, documentStartError(n + 1)
	}
	return run, nil
}

// isBlankOrComment reports whether line holds nothing but blanks and a
// comment.
func isBlankOrComment(line []byte) bool {
	rest := bytes.TrimLeft(line, " \t")
	return len(rest) == 0 || rest[0] == '#'
}

// blankLines returns a copy of src with the text of each line that
// lines holds, in order and counted from 0, taken out. The line breaks
// stay, so every other line keeps its number.
func blankLines(src []byte, lines []int) []byte {
	out := make([]byte, 0, len(src))
	for n := 0; len(src) > 0 && len(lines) > 0; n++ {
		line, rest, _ := cutLine(src)
		if n == lines[0] {
			lines = lines[1:]
		} else {
			out = append(out, line...)
		}
		out = append(out, src[len(line):len(src)-len(rest)]...)
		src = rest
	}
	return append(out, src...)
}

// cutFirstDocument cuts src as FirstDocument does and returns the cut with
// the line breaks src has.
func cutFirstDocument(src []byte) ([]byte, error) {
	if !hasLaterMarker(src) {
		return wholeStream(src)
	}
	cut := probeCut(src)
	if cut < 0 {
		cut = len(src)
	}
	var prev token.Tokens
	for {
		for cut < len(src) && !utf8.RuneStart(src[cut]) {
			cut++
		}
		whole := cut == len(src)
		toks := yamlprep.Tokenize(probeText(src[:cut], whole))
		if out, done, err := cutPrefix(src, prev, toks, whole); done {
			return out, err
		}
		prev, cut = toks, min(2*cut, len(src))
	}
}

// cutPrefix decides what FirstDocument returns from toks, the tokens of
// a prefix of src, and prev, the tokens of the prefix before it. whole
// reports whether the prefix is all of src. It reports false when the
// tokens decide nothing, and FirstDocument has to tokenize a longer
// prefix.
func cutPrefix(src []byte, prev, toks token.Tokens, whole bool) ([]byte, bool, error) {
	at, err := scanDepth(toks, true)
	if i := falseEndIndex(src, toks[:scanned(at, err, len(toks))]); i >= 0 {
		if whole || sameToken(prev, toks, i) {
			return nil, true, falseEndError(src, toks[i].Position.Line)
		}
		return nil, false, nil
	}
	if at < 0 || !whole && !sameToken(prev, toks, at) {
		// With no token that ended the first document in all of src,
		// the depth scan covered every token in it.
		return src, whole, nil
	}
	if err != nil {
		return nil, true, err
	}
	end := documentEnd(src, toks, at)
	if end < 0 {
		out, err := wholeStream(src)
		return out, true, err
	}
	if toks[at].Type == token.DocumentHeaderType {
		return src[:end], true, nil
	}
	// After a "..." or a directive that ends the first document, the
	// next one has to start, and documentEnd proved the marker starts
	// its line. That line is where the lines startRun reads begin, so
	// the rest of the marker line counts too.
	n := toks[at].Position.Line - 1
	if _, err := startRun(src[lineStart(src, n+1):], n, false); err != nil {
		return nil, true, err
	}
	return src[:end], true, nil
}

// scanned returns how many tokens the depth scan read, given what
// scanDepth returned for n tokens. A trip leaves out the token that
// tripped it, and the end of the first document keeps the token that
// ends it.
func scanned(at int, err error, n int) int {
	switch {
	case at < 0:
		return n
	case err != nil:
		return at
	default:
		return at + 1
	}
}

// wholeStream returns src, the whole stream FirstDocument hands goccy,
// after it fails nesting past maxDepth in any document of src. Input
// that cannot nest that deep costs one pass over its bytes.
func wholeStream(src []byte) ([]byte, error) {
	if _, err := probeDepth(src); err != nil {
		return nil, err
	}
	return src, nil
}

// documentEnd returns the offset in src where the first document ends,
// given toks[at], the marker that ends it. That is the start of the
// marker's line for a "---" or a directive, and the start of the next
// line for a "...", or len(src) when the "..." line is the last. It
// returns -1 when it cannot prove the marker starts its line.
//
// The tokens come from text whose line breaks are all LF, and goccy
// counts lines by LF alone, so the marker's line number counts the line
// breaks before it. lineStart counts the same breaks in src, where a
// CRLF, a lone CR, or an LF is one break, as normalizeLineBreaks makes
// it one LF. The "<<<" key rewrite only adds quotes inside a line. A cut
// stands only when that line of src starts with the marker itself,
// followed by a space, a tab, a line break, or the end of src.
//
// goccy also ends a document at a "---" inside a quoted scalar or a
// flow collection that runs past it, and then fails the whole stream.
// The cut would hide that error, or swap it for another, so an invalid
// token or an open flow collection before the marker leaves src whole.
func documentEnd(src []byte, toks token.Tokens, at int) int {
	t := toks[at]
	if t.Position.Column != 1 || !closedBefore(toks[:at]) {
		return -1
	}
	marker := []byte("---")
	switch t.Type {
	case token.DocumentEndType:
		marker = []byte("...")
	case token.DirectiveType:
		marker = []byte("%")
	default:
		// A "---" ends the first document.
	}
	off := lineStart(src, t.Position.Line)
	if off < 0 || !bytes.HasPrefix(src[off:], marker) {
		return -1
	}
	rest := src[off+len(marker):]
	if t.Type != token.DirectiveType && len(rest) > 0 && bytes.IndexByte([]byte(" \t\r\n"), rest[0]) < 0 {
		return -1
	}
	if t.Type == token.DocumentEndType {
		// The "..." line belongs to the document it ends.
		if next := lineStart(src, t.Position.Line+1); next >= 0 {
			return next
		}
		return len(src)
	}
	return off
}

// closedBefore reports whether toks hold no invalid token and leave no
// flow collection open.
func closedBefore(toks token.Tokens) bool {
	flow := 0
	for _, t := range toks {
		switch t.Type {
		case token.InvalidType:
			return false
		case token.SequenceStartType, token.MappingStartType:
			flow++
		case token.SequenceEndType, token.MappingEndType:
			flow = max(flow-1, 0)
		default:
			// Other tokens leave the flow level as it stands.
		}
	}
	return flow == 0
}

// lineStart returns the offset in src where line, numbered from 1,
// starts, or -1 when src has fewer lines. A CRLF, a lone CR, and an LF
// each end one line.
func lineStart(src []byte, line int) int {
	off := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexAny(src[off:], "\r\n")
		if i < 0 {
			return -1
		}
		off += i + 1
		if src[off-1] == '\r' && off < len(src) && src[off] == '\n' {
			off++
		}
	}
	return off
}

// hasLaterMarker reports whether a line of src that comes after its
// first content or marker line starts with a "---", a "...", or the "%"
// of a directive. A token that ends the first document needs content or
// a "---" before it, and documentEnd needs its line to start with the
// marker, so without such a line FirstDocument would return src whole
// and need not tokenize it. A line that isFalseEndLine accepts makes the
// answer true wherever it stands, so FirstDocument can fail it. Lines
// of spaces and tabs, comment lines, and directive lines before the
// first content are not content. Taking a line for content when goccy does not only makes the
// answer true more often, which costs a tokenize and never a wrong cut.
func hasLaterMarker(src []byte) bool {
	started := false
	for len(src) > 0 {
		end := bytes.IndexAny(src, "\r\n")
		if end < 0 {
			end = len(src)
		}
		line := src[:end]
		switch {
		case isFalseEndLine(line):
			return true
		case isMarkerLine(line):
			if started {
				return true
			}
			started = true
		case started && len(line) > 0 && line[0] == '%':
			return true
		case !started && isPreamble(line):
		default:
			started = true
		}
		src = src[min(end+1, len(src)):]
	}
	return false
}

// isFalseEndLine reports whether line starts with a "..." that a
// character other than a blank or a "." follows, which goccy can read as
// a document end where yaml.v3 reads a plain scalar. See isFalseEnd.
func isFalseEndLine(line []byte) bool {
	return len(line) > 3 && bytes.HasPrefix(line, []byte("...")) && bytes.IndexByte([]byte(" \t."), line[3]) < 0
}

// isMarkerLine reports whether line starts with a "---" or a "..."
// followed by a space, a tab, or nothing.
func isMarkerLine(line []byte) bool {
	if !bytes.HasPrefix(line, []byte("---")) && !bytes.HasPrefix(line, []byte("...")) {
		return false
	}
	return len(line) == 3 || line[3] == ' ' || line[3] == '\t'
}

// isPreamble reports whether line, before any content, is blank, a
// comment, or a directive.
func isPreamble(line []byte) bool {
	rest := bytes.TrimLeft(line, " \t")
	return len(rest) == 0 || rest[0] == '#' || line[0] == '%'
}
