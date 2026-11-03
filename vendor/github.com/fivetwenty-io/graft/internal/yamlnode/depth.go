package yamlnode

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// maxDepth is yaml.v3's nesting limit, libyaml's max_flow_level and
// max_indents. Each one counts on its own, so a document may hold 10,000
// open block collections and 10,000 open flow collections inside them.
const maxDepth = 10000

// checkDepth fails a token stream that nests deeper than yaml.v3 allows,
// with yaml.v3's message and line. It has to run on the lexer's tokens
// before goccy parses anything, because goccy's parser recurses once per
// level and slows down faster than linearly. 200,000 levels take it
// seconds, and 2,000,000 take it longer than anyone waits.
//
// The scan follows libyaml's scanner. A "[" or "{" raises the flow level.
// Outside flow collections, every token first closes the block levels
// indented past its column, and a "-", a "?", or a key's ":" opens a new
// level when it stands right of the innermost one.
func checkDepth(toks token.Tokens) error {
	_, err := scanDepth(toks, false)
	return err
}

// scanDepth runs the depth scan over toks. It returns the index of the
// token that tripped it, with the depth error, or with firstDocOnly the
// index of the token that ends the first document, with no error. It
// returns -1 when the scan reaches the end of toks.
func scanDepth(toks token.Tokens, firstDocOnly bool) (int, error) {
	s := &depthScan{}
	var doc firstDocument
	for i, t := range toks {
		if firstDocOnly && doc.ends(t) {
			return i, nil
		}
		if err := s.token(t); err != nil {
			return i, err
		}
	}
	return -1, nil
}

// firstDocument finds the token that ends a stream's first document.
type firstDocument struct {
	started bool // a "---" or content has started the document
	dirLine int  // the line of the last directive
}

// ends reports whether t ends the first document. A "---" or any content
// starts it, and once it has started, a "---", a "...", or a directive
// at column 1 ends it. A directive's arguments are not content.
func (d *firstDocument) ends(t *token.Token) bool {
	switch t.Type {
	case token.CommentType:
		return false
	case token.DirectiveType:
		if d.started && t.Position.Column == 1 {
			return true
		}
		d.dirLine = t.Position.Line
		return false
	case token.DocumentHeaderType:
		if d.started {
			return true
		}
	case token.DocumentEndType:
		return d.started
	default:
		if t.Position.Line == d.dirLine {
			return false
		}
	}
	d.started = true
	return false
}

// probeDepth fails src, raw YAML bytes, when it nests deeper than
// checkDepth allows, while tokenizing as little of it as it can. goccy's
// scanner tokenizes its whole input in one call, and a megabyte of "["
// costs it about 300 MB, so the probe tokenizes prefixes of src instead.
//
// Each level of nesting needs an opener of its own. A flow level needs a
// "[" or a "{", and a block level needs a "-", a "?", or a ":". The open
// block levels stand at columns that grow with each level, so a block
// level past maxDepth also needs a line longer than maxDepth bytes.
// Input that cannot nest past maxDepth by that count costs one pass over
// its bytes. Otherwise the probe tokenizes a prefix that ends just past
// the opener that could first go too deep, and doubles the prefix until
// the depth scan trips or the prefix covers the input.
//
// The tokens at the end of a prefix can differ from the ones the whole
// input gives. A quoted scalar that runs past the cut becomes an invalid
// token, and a "-" just before the cut reads as a sequence entry even
// when the next byte makes it text. So a trip, or the end of the first
// document, counts only when the previous prefix holds the same token at
// the same index. That token then lies at least half a prefix before the
// cut, past the reach of anything the scanner looks ahead at, and the
// tokens up to it are the ones the whole input gives. A prefix that
// covers the input counts as it stands.
//
// Each prefix gets the rewrites that change what the depth scan sees,
// which are LF line breaks, quoted "<<<" keys, and quoted column-1
// scalars that start with "...", so the probe fails what checkDepth would
// fail, on the same line, and finds the same first document. It sees a
// "{{x}}" placeholder before the placeholder rewrite, so a placeholder
// counts as the two flow levels yaml.v3 reads. The bare-dash rewrite only
// adds a "~" after a "-" whose line a key follows, which never changes a
// trip.
//
// When the probe tokenizes all of src without tripping, it returns the
// text it tokenized and the tokens, so Parse can skip tokenizing the same
// text again.
func probeDepth(src []byte) (probeResult, error) {
	cut := probeCut(src)
	if cut < 0 {
		return probeResult{}, nil
	}
	var prev token.Tokens
	for {
		for cut < len(src) && !utf8.RuneStart(src[cut]) {
			cut++
		}
		whole := cut == len(src)
		text := probeText(src[:cut], whole)
		toks := yamlprep.Tokenize(text)
		at, err := scanDepth(toks, false)
		if at >= 0 && (whole || sameToken(prev, toks, at)) {
			return probeResult{}, err
		}
		if whole {
			return probeResult{text: text, toks: toks}, nil
		}
		prev, cut = toks, min(2*cut, len(src))
	}
}

// probeResult holds the text probeDepth tokenized and its tokens, when
// the probe covered the whole stream. Both are empty otherwise.
type probeResult struct {
	text string
	toks token.Tokens
}

// probeCut returns the length of the first prefix probeDepth tokenizes,
// which ends just past the opener that could first nest past maxDepth.
// It returns -1 when src holds too few openers to nest that deep.
func probeCut(src []byte) int {
	blockToo := hasLineLongerThan(src, maxDepth)
	n := 0
	for i, c := range src {
		switch c {
		case '[', '{':
		case '-', '?', ':':
			if !blockToo {
				continue
			}
		default:
			continue
		}
		n++
		if n > maxDepth {
			return i + 1
		}
	}
	return -1
}

// hasLineLongerThan reports whether a line of src holds more than n
// bytes. Only LF ends a line here, so a CR line break makes a line look
// longer, never shorter.
func hasLineLongerThan(src []byte, n int) bool {
	for len(src) > n {
		i := bytes.IndexByte(src, '\n')
		if i < 0 || i > n {
			return true
		}
		src = src[i+1:]
	}
	return false
}

// probeText returns the text the probe tokenizes for a prefix of the
// input. Its line breaks become LF, its "<<<" keys are quoted, and so
// are the column-1 scalars that start with "..." and a non-blank, and
// when the prefix is the whole input, it ends in a line break, as the
// text Parse tokenizes does. goccy places an unquoted "<<<" key right of
// where it starts, which the depth scan would read as a level, and it
// reads an unquoted "...x" as the end of the first document.
func probeText(prefix []byte, whole bool) string {
	text := yamlprep.QuoteEndMarkerScalars(yamlprep.QuoteInjectKeys(normalizeLineBreaks(prefix)))
	if whole && len(text) > 0 && text[len(text)-1] != '\n' {
		return string(text) + "\n"
	}
	return string(text)
}

// sameToken reports whether prev and toks hold tokens of one type at
// one position at index i.
func sameToken(prev, toks token.Tokens, i int) bool {
	if i >= len(prev) {
		return false
	}
	a, b := prev[i], toks[i]
	return a.Type == b.Type && a.Position.Line == b.Position.Line && a.Position.Column == b.Position.Column
}

type depthScan struct {
	flow    int
	indents []int // the columns of the open block collections
	keyLine int   // the last line where a key could have started outside flow collections
	dirLine int   // the line of the last directive
}

func (s *depthScan) token(t *token.Token) error {
	line := t.Position.Line
	switch t.Type {
	case token.CommentType:
		return nil
	case token.DirectiveType:
		s.dirLine, s.indents = line, s.indents[:0]
		return nil
	case token.DocumentHeaderType, token.DocumentEndType:
		s.indents = s.indents[:0]
		return nil
	default:
		if line == s.dirLine {
			return nil // a directive's arguments
		}
	}
	if s.flow == 0 {
		s.unroll(t.Position.Column)
	}
	switch t.Type {
	case token.SequenceStartType, token.MappingStartType:
		return s.openFlow(line)
	case token.SequenceEndType, token.MappingEndType:
		s.flow = max(s.flow-1, 0)
	case token.SequenceEntryType, token.MappingKeyType:
		return s.roll(t.Position.Column, line)
	case token.MappingValueType:
		return s.roll(keyColumn(t), line)
	case token.CollectEntryType, token.LiteralType, token.FoldedType:
		// These never start a key.
	default:
		if s.flow == 0 && !isBlockBody(t) {
			s.keyLine = line
		}
	}
	return nil
}

// openFlow enters a flow collection. libyaml reports the error on the
// line of the bracket that went too deep.
func (s *depthScan) openFlow(line int) error {
	if s.flow == 0 {
		s.keyLine = line
	}
	s.flow++
	if s.flow > maxDepth {
		return depthError(line)
	}
	return nil
}

func (s *depthScan) unroll(col int) {
	for n := len(s.indents); n > 0 && s.indents[n-1] > col; n-- {
		s.indents = s.indents[:n-1]
	}
}

// roll opens a block collection at col when col stands right of the
// innermost one. libyaml reports the error on the line of the last token
// that could have started a key, and when that is the first line, on the
// line it is reading.
func (s *depthScan) roll(col, line int) error {
	if s.flow > 0 {
		return nil
	}
	if n := len(s.indents); n > 0 && s.indents[n-1] >= col {
		return nil
	}
	s.indents = append(s.indents, col)
	if len(s.indents) <= maxDepth {
		return nil
	}
	if s.keyLine > 1 {
		return depthError(s.keyLine)
	}
	return depthError(line)
}

// keyColumn returns the column where the key of the ":" token t starts,
// which is the first token on its line after any "-", "?", or earlier
// ":". Stopping at an earlier ":" keeps the walk linear on a line that
// repeats "a: a: a:".
func keyColumn(t *token.Token) int {
	start := t
	for p := t.Prev; p != nil && p.Position.Line == t.Position.Line; p = p.Prev {
		if p.Type == token.SequenceEntryType || p.Type == token.MappingKeyType || p.Type == token.MappingValueType {
			break
		}
		start = p
	}
	return start.Position.Column
}

// isBlockBody reports whether t is the body of a literal or folded
// scalar, which libyaml never treats as a possible key.
func isBlockBody(t *token.Token) bool {
	return t.Prev != nil && (t.Prev.Type == token.LiteralType || t.Prev.Type == token.FoldedType)
}

// depthMessage is yaml.v3's message for nesting past maxDepth.
var depthMessage = fmt.Sprintf("exceeded max depth of %d", maxDepth)

// depthError is yaml.v3's depth error. libyaml numbers lines from 0 and
// gives a line only when it is not the first.
func depthError(line int) error {
	reported := line
	if reported <= 1 {
		reported = 0
	}
	return &ParseError{Line: reported, SourceLine: line, Message: depthMessage}
}

// IsDepthError reports whether err is the error for nesting deeper than
// yaml.v3 allows, which FirstDocument and Parse give before anything
// parses.
func IsDepthError(err error) bool {
	var pe *ParseError
	return errors.As(err, &pe) && pe.Message == depthMessage
}
