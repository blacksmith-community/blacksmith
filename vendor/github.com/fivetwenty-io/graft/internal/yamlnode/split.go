package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// chunk is one YAML document's slice of the source. goccy numbers the
// chunk's lines from 1, so the line of anything in it is textLine-1 more
// than goccy says.
type chunk struct {
	text      string
	startLine int  // 1-based line where the document starts
	textLine  int  // 1-based line where text starts, before startLine when comments carry over
	index     int  // the document's position in the stream
	marker    bool // a "---" follows the chunk, or it ends with "..."
	afterEnd  bool // a "..." ended the document before this one

	// endComments holds the comments that sit on the "..." lines that
	// end the document before this one. yaml.v3 gives them to this
	// document as head comments, ahead of any comment above its "---".
	endComments []string

	// startErr is the error yaml.v3 gives before the chunk's document
	// starts, and nil for most chunks. Content that follows a "..." with
	// no "---" before it fails, and so do the lines after a "..." when a
	// directive among them meets another "..." or the end of the input.
	// See endWatch and closeDirectives.
	startErr error

	// tail marks the comments after the last document's "...", which are
	// no document but can still be that document's foot comment.
	tail bool

	// unterminated marks the stream's last chunk when Parse added the
	// stream's final line break.
	unterminated bool
}

// splitDocuments cuts a YAML stream into one chunk per document the way
// yaml.v3 counts documents. goccy's parser merges or drops documents
// around empty "---" sections, but its lexer reports every "---" and "..."
// marker with a correct line, so we cut at those lines.
//
// A section that starts with "---" is always a document, even when it is
// empty. A section that does not (the text before the first "---", or
// the text after a "...") is a document only when it holds a token other
// than a comment or a directive. Directive lines (%YAML, %TAG) travel with
// the "---" section that follows them, and so do the comments of a
// section that is not a document, because yaml.v3 gives them to the next
// document's first node. Comments after the last document's "..." come
// back as a tail chunk.
func splitDocuments(src string) []chunk {
	return splitTokens(src, yamlprep.Tokenize(src))
}

// splitTokens is splitDocuments for a stream the caller has already
// tokenized.
func splitTokens(src string, toks token.Tokens) []chunk {
	s := &splitter{segStart: 1, textStart: 1, directiveLines: map[int]bool{}}
	s.lines = strings.SplitAfter(src, "\n")
	if n := len(s.lines); n > 0 && s.lines[n-1] == "" {
		s.lines = s.lines[:n-1]
	}
	for _, t := range toks {
		if t.Type == token.DirectiveType {
			s.directiveLines[t.Position.Line] = true
		}
	}
	for _, t := range toks {
		s.token(t)
	}
	s.closeDirectives(len(s.lines))
	if s.emit(len(s.lines), false) {
		return s.chunks
	}
	if s.afterEnd && s.textStart <= len(s.lines) {
		s.chunks = append(s.chunks, chunk{
			text:     strings.Join(s.lines[s.textStart-1:], ""),
			textLine: s.textStart, startLine: s.textStart,
			index: len(s.chunks), afterEnd: true, tail: true,
			startErr: s.startErr,
		})
	} else if s.startErr != nil {
		// Only the "..." lines that end the stream are left, and Parse
		// has to fail on them.
		s.chunks = append(s.chunks, chunk{index: len(s.chunks), afterEnd: true, tail: true, startErr: s.startErr})
	}
	return s.chunks
}

type splitter struct {
	lines            []string
	directiveLines   map[int]bool
	chunks           []chunk
	segStart         int
	textStart        int // first line of the chunk text, before segStart when comments carry over
	explicit         bool
	hasContent       bool
	pendingDirective int // first directive line waiting for its "---"

	// endedByDots records that a "..." ended the last document, and
	// afterEnd that the section being read follows that "...".
	endedByDots bool
	afterEnd    bool

	// endLine is the line of the last "...", and endComments holds the
	// comments that sit on the "..." lines since the last chunk, which
	// the next chunk takes as comment lines of its own.
	endLine     int
	endComments []string

	// watch finds the first token of the section being read when that
	// section follows a "..." and has no "---".
	watch endWatch

	// endDirective records that a directive came in the section being
	// read, which follows a "..." and has no "---". startErr holds the
	// error closeDirectives found for such a section, until the next
	// chunk carries it.
	endDirective bool
	startErr     error
}

func (s *splitter) token(t *token.Token) {
	line := t.Position.Line
	s.watch.token(t)
	switch {
	case t.Type == token.DocumentHeaderType:
		s.header(line)
	case t.Type == token.DocumentEndType:
		s.documentEnd(line)
	case t.Type == token.DirectiveType:
		if s.pendingDirective == 0 {
			s.pendingDirective = line
		}
		if s.afterEnd && !s.explicit {
			s.endDirective = true
		}
	case t.Type == token.CommentType && line == s.endLine && s.endedByDots:
		s.endComments = append(s.endComments, "#"+t.Value)
	case t.Type == token.CommentType || s.directiveLines[line]:
		// Comments and directive arguments never make a document.
	default:
		s.hasContent = true
	}
}

// header starts the section of a "---" on line, after closing the one
// before it.
func (s *splitter) header(line int) {
	textStart := line
	if s.emit(line-1, true) {
		s.endedByDots = false
	} else if !s.explicit {
		textStart = s.textStart // carry the section's comments over
	}
	s.afterEnd = s.endedByDots
	s.textStart, s.segStart, s.explicit, s.hasContent = textStart, line, true, false
	s.watch.reset(false)
	s.endDirective = false
	if s.pendingDirective > 0 {
		s.segStart, s.pendingDirective = s.pendingDirective, 0
		s.textStart = min(s.textStart, s.segStart)
	}
}

// documentEnd closes the section that a "..." on line ends and starts
// the one after it.
func (s *splitter) documentEnd(line int) {
	s.closeDirectives(line)
	if s.emit(line, true) {
		s.endedByDots = true
	}
	s.afterEnd = s.endedByDots
	s.endLine = line
	s.textStart, s.segStart, s.explicit, s.hasContent = line+1, line+1, false, false
	s.watch.reset(s.afterEnd)
}

// emit closes the section that ends at endLine and reports whether it
// was a document. It clamps endLine to the last line, so a marker line
// the lexer counts past the end of the text can never slice beyond it.
func (s *splitter) emit(endLine int, marker bool) bool {
	endLine = min(endLine, len(s.lines))
	if endLine < s.segStart || s.textStart > endLine+1 || (!s.explicit && !s.hasContent) {
		return false
	}
	var endComments []string
	if s.afterEnd {
		endComments = s.endComments
	}
	s.endComments = nil
	startErr := s.startErr
	if startErr == nil && s.watch.line > 0 {
		startErr = documentStartError(s.watch.line)
	}
	s.startErr = nil
	s.chunks = append(s.chunks, chunk{
		text:        strings.Join(s.lines[s.textStart-1:endLine], ""),
		endComments: endComments,
		startErr:    startErr,
		startLine:   s.segStart,
		textLine:    s.textStart,
		index:       len(s.chunks),
		marker:      marker,
		afterEnd:    s.afterEnd,
	})
	return true
}

// closeDirectives ends the section being read at line end, a "..." line
// or the last line, when a directive came in it after a "...". libyaml
// needs a "---" after a directive, so such a section always fails. It
// reads the section's lines as FirstDocument reads the lines after its
// first document, which gives the error spruce's YAML library gives,
// whether for a directive or for what follows it. The error waits for
// the next chunk, so that an error in an earlier document comes first.
func (s *splitter) closeDirectives(end int) {
	if !s.endDirective {
		return
	}
	s.endDirective = false
	end = min(end, len(s.lines))
	from := min(s.endLine, end)
	if _, err := startRun([]byte(strings.Join(s.lines[from:end], "")), from, false); err != nil && s.startErr == nil {
		s.startErr = err
	}
}

// lineOffset is what to add to a line goccy reports in the chunk to get
// its line in the stream.
func (c chunk) lineOffset() int {
	return c.textLine - 1
}

// endWatch finds the content yaml.v3 rejects after a "..." that ends a
// document. The document after a "..." has to start with a "---", so
// libyaml fails content that comes first with "did not find expected
// <document start>". Comments, directives, and more "..." lines may come
// between. Parse fails the chunk that holds such content. FirstDocument
// reads the lines after the first document itself. See startRun.
type endWatch struct {
	armed   bool // a "..." ended a document, and no "---" or content has come since
	line    int  // the line of the content that came first, or 0
	dirLine int  // the line of the last directive, whose arguments are not content
}

// reset forgets any content the watch found, and arms it after a "..."
// that ends a document.
func (w *endWatch) reset(armed bool) {
	w.armed, w.line = armed, 0
}

// token reads the next token. A "---" disarms the watch, and so does
// content, once the watch records its line.
func (w *endWatch) token(t *token.Token) {
	switch t.Type {
	case token.DocumentHeaderType:
		w.armed = false
	case token.DirectiveType:
		w.dirLine = t.Position.Line
	case token.CommentType, token.DocumentEndType:
		// Neither starts a document.
	default:
		if w.armed && t.Position.Line != w.dirLine {
			w.armed, w.line = false, t.Position.Line
		}
	}
}

// documentStartError is yaml.v3's error for content on line that follows
// a "..." with no "---" before it. libyaml marks it on the line before
// the content.
func documentStartError(line int) error {
	return &ParseError{Line: line - 1, SourceLine: line, Message: "did not find expected <document start>"}
}
