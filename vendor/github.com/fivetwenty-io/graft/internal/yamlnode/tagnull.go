package yamlnode

import (
	"strings"

	"github.com/goccy/go-yaml/token"
)

// goccy's parser reads the node after a tag wherever it starts, and does
// not check that the node is indented under the tag's parent. So when a
// tag, alone or with an anchor, ends a line and the next line holds a
// sibling key or item, the tag takes that key or item, and everything
// after it at that level, as its value. "a: !custom\nb: 1" reads as
// a: {b: 1}, and "- !custom\n- 2" as [[2]], where spruce reads the empty
// string under the tag. Only an anchor alone is read right, through a
// special case in the parser.
//
// emptyTagValues gives each such tag the implicit null the parser gives
// a tag at the end of the document, so the next line keeps its place. A
// node on the next line that is indented under the tag's parent is still
// the tag's value, as in "a: !custom\n  x: 1", and so is a sequence at
// the indentation of a mapping key, as in "a: !custom\n- x".
//
// In a flow collection the parser fails a custom tag, the bare tag "!",
// or a verbatim tag that has no value before a comma or the collection's
// end, as in "{k: !custom &n, w: z}". emptyTagValues gives those tags an
// implicit null too, which spruce reads as the empty string. It leaves
// the core tags in a flow collection alone, since the parser has its own
// reading of each of them there, and a tag with no blank before the
// comma or bracket, which spruce reads as part of the tag.

// emptyTagValues returns toks with an implicit null after each tag that
// has no value of its own, as the comment above sets out, together with
// an anchor, if one sits next to it. It also returns the tags among them
// that the parser reads a collection under. text is the document toks come from. The added
// tokens are not linked to the tokens around them, as the parser's own
// implicit nulls are not, so a walk of the token chain still gives the
// text as written.
func emptyTagValues(text string, toks token.Tokens) (token.Tokens, []*token.Token) {
	var runs []tagRun
	lines := lineIndex{text: text, after: map[int]int{}}
	depth := 0
	for i, tk := range toks {
		switch tk.Type {
		case token.MappingStartType, token.SequenceStartType:
			depth++
		case token.MappingEndType, token.SequenceEndType:
			depth--
		case token.DirectiveType:
			if isTagDirective(toks, i) {
				// The parser reads the token after every tag as a
				// string once a %TAG directive names "!!".
				return toks, nil
			}
		case token.TagType:
			// Every tag is found in the text, in order, so that each
			// search on a line starts after the tag before it.
			abuts := lines.tagAbuts(tk)
			r, ok := tagProperties(toks, i)
			if depth == 0 {
				ok = ok && emptyInBlock(&lines, toks, r)
			} else {
				ok = ok && emptyInFlow(toks, r, abuts)
			}
			if ok {
				runs = append(runs, r)
			}
		default:
		}
	}
	if len(runs) == 0 {
		return toks, nil
	}
	return withImplicitNulls(toks, runs)
}

// withImplicitNulls returns toks with an implicit null after each of
// runs, and the tags among them that the parser reads a collection under.
func withImplicitNulls(toks token.Tokens, runs []tagRun) (token.Tokens, []*token.Token) {
	out := make(token.Tokens, 0, len(toks)+len(runs))
	var collections []*token.Token
	prev := 0
	for _, r := range runs {
		out = append(out, toks[prev:r.end+1]...)
		out = append(out, implicitNullAfter(toks[r.start:r.end+1]))
		prev = r.end + 1
		if r.end == r.tag && prev < len(toks) {
			retypeAfterTag(toks[r.tag], toks[prev])
		}
		if tag := toks[r.tag]; collectionTags[token.ReservedTagKeyword(tag.Value)] {
			collections = append(collections, tag)
		}
	}
	return append(out, toks[prev:]...), collections
}

// tagRun is a tag and the anchor next to it, if there is one, as indexes
// into a token slice: the tag, and the first and last of the tokens.
type tagRun struct {
	tag, start, end int
}

// collectionTags holds the tags the parser reads a collection under, and
// fails or reads wrongly over an implicit null.
var collectionTags = map[token.ReservedTagKeyword]bool{
	token.MappingTag:    true,
	token.SetTag:        true,
	token.SequenceTag:   true,
	token.OrderedMapTag: true,
}

// hideCollectionTags gives each tag in tags, which tags an implicit null
// that emptyTagValues added, the non-specific tag "!" while the parser
// reads it, so the parser reads the null under it as it does under any
// other tag. It returns the function that puts each tag back, which the
// caller runs once the parse ends. The tag node keeps the token itself,
// so the parsed tree holds the tag as written.
func hideCollectionTags(tags []*token.Token) func() {
	values := make([]string, len(tags))
	for i, tk := range tags {
		values[i], tk.Value = tk.Value, "!"
	}
	return func() {
		for i, tk := range tags {
			tk.Value = values[i]
		}
	}
}

// tagProperties returns the tag at toks[i] and the anchor on its line
// right before or after it, if there is one. It reports false for a tag
// that sits next to a second tag or between two anchors, which the
// parser fails anyway, and for a !!merge tag.
func tagProperties(toks token.Tokens, i int) (tagRun, bool) {
	tag := toks[i]
	if tag.Position == nil || tag.Value == string(token.MergeTag) {
		return tagRun{}, false
	}
	r := tagRun{tag: i, start: i, end: i}
	before, after := anchorAt(toks, i-2, tag), anchorAt(toks, i+1, tag)
	if before && after {
		return tagRun{}, false
	}
	if before {
		r.start = i - 2
	}
	if after {
		r.end = i + 2
	}
	if tagAt(toks, r.start-1, tag) || tagAt(toks, r.end+1, tag) {
		return tagRun{}, false
	}
	return r, true
}

// anchorAt reports whether toks[j] and toks[j+1] are an anchor and its
// name, both on the line of tag.
func anchorAt(toks token.Tokens, j int, tag *token.Token) bool {
	return j >= 0 && j+1 < len(toks) && toks[j].Type == token.AnchorType &&
		sameLine(toks[j], tag) && sameLine(toks[j+1], tag)
}

// tagAt reports whether toks[j] is a tag on the line of tag.
func tagAt(toks token.Tokens, j int, tag *token.Token) bool {
	return j >= 0 && j < len(toks) && toks[j].Type == token.TagType && sameLine(toks[j], tag)
}

// emptyInBlock reports whether the tag and anchor r, outside any flow
// collection, end their line with no value, before a node that is not
// indented under their parent.
func emptyInBlock(lines *lineIndex, toks token.Tokens, r tagRun) bool {
	tag := toks[r.tag]
	next := nextNonCommentAt(toks, r.end+1)
	if next >= 0 && (toks[next].Position == nil || toks[next].Position.Line <= tag.Position.Line) {
		return false
	}
	p := prevNonCommentAt(toks, r.start-1)
	if p < 0 {
		return false
	}
	col, ok := parentColumn(lines, toks, p)
	if !ok {
		return false
	}
	if next < 0 {
		return true
	}
	n := toks[next]
	if lines.tabBefore(n) || n.Position.Column > col {
		return false
	}
	// A block sequence may sit at its key's indentation.
	return toks[p].Type != token.MappingValueType || n.Type != token.SequenceEntryType || n.Position.Column != col
}

// emptyInFlow reports whether the tag and anchor r, inside a flow
// collection, come right before a comma or the collection's end, and
// the tag is one that the parser has no reading of its own for. abuts
// reports whether the tag has a character other than a blank right
// after it.
func emptyInFlow(toks token.Tokens, r tagRun, abuts bool) bool {
	tag := toks[r.tag].Value
	if strings.HasPrefix(tag, "!!") && (scalarCoreTags[tag] || collectionTags[token.ReservedTagKeyword(tag)]) {
		return false
	}
	next := nextNonCommentAt(toks, r.end+1)
	if next < 0 || r.end == r.tag && abuts {
		// spruce reads a comma or a bracket right after a tag as part of
		// the tag's name.
		return false
	}
	switch toks[next].Type {
	case token.CollectEntryType, token.MappingEndType, token.SequenceEndType:
		return true
	default:
		return false
	}
}

// tagAbuts reports whether a character other than a blank follows tag
// on its line, as a comma or a bracket that the scanner split off does.
// The parser places the tokens after a tag on a line one column earlier
// than the text has them, one more column for each tag, so this reads
// the text for the first place the tag's text starts a token, from the
// tag's column or the end of the tag found before it on the line,
// whichever is later. It reports true when there is no such place. The
// caller asks about the tags in the order they come.
func (x *lineIndex) tagAbuts(tag *token.Token) bool {
	r := []rune(x.line(tag.Position.Line))
	v := []rune(tag.Value)
	for i := max(tag.Position.Column-1, x.after[tag.Position.Line]); i+len(v) <= len(r); i++ {
		if string(r[i:i+len(v)]) != tag.Value || i > 0 && !strings.ContainsRune(" \t[{,", r[i-1]) {
			continue
		}
		j := i + len(v)
		x.after[tag.Position.Line] = j
		return j < len(r) && r[j] != ' ' && r[j] != '\t'
	}
	return true
}

// parentColumn returns the column a node must be indented past to be
// the value of the "-" or ":" at toks[p]: the column of the "-", or the
// column the key before the ":" starts at, with its properties. It
// reports false for any other token, and for a key it cannot place.
func parentColumn(lines *lineIndex, toks token.Tokens, p int) (int, bool) {
	tk := toks[p]
	if tk.Position == nil || lines.tabBefore(tk) {
		return 0, false
	}
	switch tk.Type {
	case token.SequenceEntryType:
		return tk.Position.Column, true
	case token.MappingValueType:
	default:
		return 0, false
	}
	k := p - 1
	if k < 0 || !sameLine(toks[k], tk) {
		// A ":" that starts its line follows an explicit key.
		return tk.Position.Column, true
	}
	switch toks[k].Type {
	case token.MappingEndType, token.SequenceEndType, token.CommentType:
		return 0, false
	default:
	}
	for k > 0 && sameLine(toks[k-1], tk) {
		if t := toks[k-1].Type; t == token.TagType || t == token.AliasType {
			k--
			continue
		}
		if k >= 2 && toks[k-2].Type == token.AnchorType && sameLine(toks[k-2], tk) {
			k -= 2
			continue
		}
		break
	}
	return toks[k].Position.Column, true
}

// implicitNullAfter returns an implicit null placed one column after the
// last of run, a tag and the anchor next to it, as the parser places the
// null it gives a tag at the end of the document.
func implicitNullAfter(run token.Tokens) *token.Token {
	last := run[0]
	for _, tk := range run[1:] {
		if tk.Position.Column > last.Position.Column {
			last = tk
		}
	}
	pos := *last.Position
	pos.Column++
	tk := token.New("null", " null", &pos)
	tk.Type = token.ImplicitNullType
	return tk
}

// retypeAfterTag gives tk, the token right after tag, the type its text
// has when tag is not one of goccy's reserved tags. goccy's scanner gives
// a plain scalar right after such a tag the string type, so that the tag
// owns the text, wherever the scalar sits. Once emptyTagValues gives the
// tag a null of its own, the scalar on a later line is no longer the
// tag's, and without its own type a null key such as ~ or Null kept its
// spelling as a string key. A quoted scalar keeps its own type in the
// scanner, so only a plain one, which holds no blank, changes here.
func retypeAfterTag(tag, tk *token.Token) {
	if _, reserved := token.ReservedTagKeywordMap[token.ReservedTagKeyword(tag.Value)]; reserved {
		return
	}
	if tk.Type != token.StringType || strings.ContainsAny(tk.Value, " \t\r\n") {
		return
	}
	plain := token.New(tk.Value, tk.Origin, tk.Position)
	tk.Type, tk.CharacterType, tk.Indicator = plain.Type, plain.CharacterType, plain.Indicator
}

// isTagDirective reports whether the directive at toks[i] is a %TAG
// directive for the "!!" handle.
func isTagDirective(toks token.Tokens, i int) bool {
	return i+2 < len(toks) && toks[i+1].Value == tagDirectiveName && toks[i+2].Value == "!!"
}

// tagDirectiveName is the name of the %TAG directive.
const tagDirectiveName = "TAG"

// prevNonCommentAt returns the index of the last token in toks at or
// before from that is not a comment, or -1 when there is none.
func prevNonCommentAt(toks token.Tokens, from int) int {
	for i := from; i >= 0; i-- {
		if toks[i].Type != token.CommentType {
			return i
		}
	}
	return -1
}

// sameLine reports whether a and b start on the same line.
func sameLine(a, b *token.Token) bool {
	return a.Position != nil && b.Position != nil && a.Position.Line == b.Position.Line
}

// lineIndex finds the lines of text, splitting it the first time a line
// is asked for. after holds, for each line, the index of the rune after
// the last tag that tagAbuts found on it.
type lineIndex struct {
	text  string
	lines []string
	after map[int]int
}

// tabBefore reports whether a tab sits on tk's line before tk, where the
// parser's columns stop matching the indentation the text shows.
func (x *lineIndex) tabBefore(tk *token.Token) bool {
	r := []rune(x.line(tk.Position.Line))
	end := tk.Position.Column - 1
	if end > len(r) {
		end = len(r)
	}
	return end > 0 && strings.ContainsRune(string(r[:end]), '\t')
}

// line returns line n of the text, counted from 1, or "" when there is
// no such line.
func (x *lineIndex) line(n int) string {
	if x.lines == nil {
		x.lines = strings.Split(x.text, "\n")
	}
	if n < 1 || n > len(x.lines) {
		return ""
	}
	return x.lines[n-1]
}
