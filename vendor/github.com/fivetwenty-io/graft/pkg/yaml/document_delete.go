package yaml

import (
	"bytes"
	"errors"
	"slices"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// The reasons Delete refuses with, besides those every edit shares.
const (
	reasonRoot       = "the path names the document's root"
	reasonOnlyEntry  = "the entry is the only one in its collection"
	reasonFlowEntry  = "the entry is in a flow collection"
	reasonDashLine   = "the key shares its line with a sequence dash"
	reasonSharedLine = "the entry does not start its own line"
	reasonMergeKey   = "the entry is a merge key"
)

// The errors entryLines returns. Each one's text is the reason Delete
// refuses with.
var (
	errDashLine   = errors.New(reasonDashLine)
	errSharedLine = errors.New(reasonSharedLine)
	errTab        = errors.New(reasonTab)
)

// Delete removes the mapping entry or sequence item at path, with its
// own comment lines, and changes no other byte.
//
// The lines Delete removes run from the entry's key, or from the dash of
// a sequence item, through the last line of its value. They take in the
// comment lines directly above the key or dash at its own indentation,
// every line under it that is indented further, and, when the entry
// carries a foot comment, the comment lines at its indentation directly
// after it. Blank lines before and after the entry stay, and so do the
// comments that belong to the next entry. When the input has no final
// line break and its last line goes, the line break that ends the line
// before it stays.
//
// Delete refuses, with ErrUnsupportedEdit, the root, the only entry of
// a collection, an entry of a flow collection, a << merge key, a key
// that shares its line with a sequence dash, and any other entry that
// does not start its own line. It refuses an entry whose value is an
// alias, one reached through an alias, and one that holds a node an
// alias outside it refers to or that sits in an anchored node an alias
// refers to. It also refuses an entry with a tab on any line it would
// remove, and any entry of an input that holds a carriage return or
// starts with a byte order mark. A path that names nothing is
// ErrPathNotFound.
//
// Delete parses the edited text again, and refuses unless the new tree
// is the old one with exactly that entry taken out, every other node
// keeps its comments and its place in the text, and every byte outside
// the removed lines is unchanged. A refused Delete leaves the bytes as
// they were.
func (d *Document) Delete(path string) error {
	const op = "delete"
	loc, err := d.resolve(op, path)
	if err != nil {
		return err
	}
	if reason := d.deleteRefusal(loc); reason != "" {
		return loc.refusal(op, path, reason)
	}
	first, last, err := d.entryLines(loc)
	if err != nil {
		return loc.refusal(op, path, err.Error())
	}
	if reason := d.tabRefusal(first, last); reason != "" {
		return loc.refusal(op, path, reason)
	}
	cut := d.lineSpan(first, last)
	next := make([]byte, 0, len(d.src)-(cut.end-cut.start))
	next = append(next, d.src[:cut.start]...)
	next = append(next, d.src[cut.end:]...)
	return d.commitDelete(path, next, loc, first, last)
}

// deleteRefusal returns the reason Delete can't remove the entry at loc
// without reading its text, or "". It adds to pathRefusal the shapes
// Delete never cuts: the root, an entry of a flow collection, the only
// entry of its collection, which would leave a key with no value or an
// item with no dash, a merge key, and an anchor that an alias outside
// the entry still needs.
func (d *Document) deleteRefusal(loc located) string {
	if loc.key != nil && loc.key.Kind == yamlnode.ScalarNode && loc.key.Tag == yamlnode.TagMerge {
		// A merge key's value is often an alias, so this comes first.
		return reasonMergeKey
	}
	if reason := d.pathRefusal(loc); reason != "" {
		return reason
	}
	p := loc.parent
	switch {
	case p.Kind == yamlnode.DocumentNode:
		return reasonRoot
	case p.Style&yamlnode.StyleFlow != 0:
		return reasonFlowEntry
	case len(p.Content) <= entryWidth(p):
		return reasonOnlyEntry
	case anchorInUse(d.docs[0], loc):
		return reasonAnchored
	}
	return ""
}

// entryWidth returns how many nodes of a collection's Content one entry
// takes: a key and a value in a mapping, and one item in a sequence.
func entryWidth(collection *yamlnode.Node) int {
	if collection.Kind == yamlnode.MappingNode {
		return 2
	}
	return 1
}

// entryNodes returns the nodes the entry at loc is written as: its key
// and value in a mapping, or its item in a sequence.
func entryNodes(loc located) []*yamlnode.Node {
	if loc.key != nil {
		return []*yamlnode.Node{loc.key, loc.written()}
	}
	return []*yamlnode.Node{loc.written()}
}

// anchorInUse reports whether an alias outside the entry at loc refers
// to a node the entry holds, which would leave the alias with nothing
// to refer to, or to the collection that holds the entry or one around
// it, whose every alias would lose the entry too.
func anchorInUse(root *yamlnode.Node, loc located) bool {
	entry := entryNodes(loc)
	used := make(map[*yamlnode.Node]bool)
	var collect func(n *yamlnode.Node)
	collect = func(n *yamlnode.Node) {
		switch {
		case slices.Contains(entry, n):
		case n.Kind == yamlnode.AliasNode:
			if n.Alias != nil {
				used[n.Alias] = true
			}
		default:
			for _, c := range n.Content {
				collect(c)
			}
		}
	}
	collect(root)
	var holds func(n *yamlnode.Node) bool
	holds = func(n *yamlnode.Node) bool {
		if used[n] {
			return true
		}
		return n.Kind != yamlnode.AliasNode && slices.ContainsFunc(n.Content, holds)
	}
	return slices.ContainsFunc(entry, holds) || slices.ContainsFunc(pathTo(root, loc.parent), func(n *yamlnode.Node) bool { return used[n] })
}

// pathTo returns the nodes from root down to target, both included, as
// the tree holds them without following aliases, or nil when target is
// not in root's tree.
func pathTo(root, target *yamlnode.Node) []*yamlnode.Node {
	if root == target {
		return []*yamlnode.Node{root}
	}
	if root.Kind == yamlnode.AliasNode {
		return nil
	}
	for _, c := range root.Content {
		if p := pathTo(c, target); p != nil {
			return append([]*yamlnode.Node{root}, p...)
		}
	}
	return nil
}

// entryLines returns the 1-based first and last line of the entry loc
// names. first is the key's line, or the dash's line for a sequence
// item, moved up over the comment lines directly above it at the same
// indent. last is the line before the next line, comment or not, whose
// indent is at most the entry's, with blank lines just before that line
// left out. That line is looked for only after the last line any node
// of the entry sits on, so a block sequence written at its key's indent
// stays whole. When the entry's key or item carries a foot comment, last
// then moves down over the comment lines directly after it at the
// entry's indent.
//
// The line of the key or dash must hold no tab, read the same to the
// parser as it is written, and hold nothing before the key or dash but
// spaces.
func (d *Document) entryLines(loc located) (first, last int, err error) {
	head, indent, err := d.entryHead(loc)
	if err != nil {
		return 0, 0, err
	}
	first = head
	for first > 1 && commentAt(d.lineText(first-1), indent) {
		first--
	}
	last = head
	for _, n := range entryNodes(loc) {
		last = max(last, lowestLine(n))
	}
	for line := last + 1; line <= len(d.lines) && d.lines[line-1] < len(d.src); line++ {
		text := withoutBreak(d.lineText(line))
		if len(bytes.Trim(text, " \t")) == 0 {
			continue
		}
		if leadingSpaces(text) <= indent {
			break
		}
		last = line
	}
	foot := loc.written().FootComment
	if loc.key != nil {
		foot = loc.key.FootComment
	}
	if foot != "" {
		for last < len(d.lines) && commentAt(d.lineText(last+1), indent) {
			last++
		}
	}
	return first, last, nil
}

// entryHead returns the line the entry at loc starts on, which is its
// key's line or its item's dash's line, and the entry's indent, which is
// the number of spaces before the key or dash. An item's dash is the
// last one goccy's lexer reads before the item, and only spaces, line
// breaks, and comments may sit between the two.
func (d *Document) entryHead(loc located) (line, indent int, err error) {
	if loc.key != nil {
		line, column := loc.key.Line, loc.key.Column
		if err := d.startsLine(line, column, errDashLine); err != nil {
			return 0, 0, err
		}
		return line, column - 1, nil
	}
	item := loc.written()
	if d.tabRefusal(item.Line, item.Line) != "" {
		return 0, 0, errTab
	}
	read, ok := d.exactLine(item.Line)
	if !ok {
		return 0, 0, errInexact
	}
	dash := dashBefore(read, item.Line, item.Column)
	if dash == nil {
		return 0, 0, errInexact
	}
	line, column := dash.Position.Line, dash.Position.Column
	if err := d.startsLine(line, column, errSharedLine); err != nil {
		return 0, 0, err
	}
	at, _ := d.offset(line, column)
	to, ok := d.offset(item.Line, item.Column)
	if !ok || to <= at || d.src[at] != '-' || !onlySpaceAndComments(d.src[at+1:to]) {
		return 0, 0, errInexact
	}
	return line, column - 1, nil
}

// startsLine checks that what sits at column on line line starts that
// line, with only spaces before it, and that the line holds no tab and
// reads to the parser as it is written. A sequence dash before it gives
// afterDash, and anything else before it errSharedLine.
func (d *Document) startsLine(line, column int, afterDash error) error {
	if d.tabRefusal(line, line) != "" {
		return errTab
	}
	if _, ok := d.exactLine(line); !ok {
		return errInexact
	}
	at, ok := d.offset(line, column)
	if !ok {
		return errInexact
	}
	before := bytes.TrimLeft(d.src[d.lines[line-1]:at], " ")
	switch {
	case len(before) == 0:
		return nil
	case before[0] == '-':
		return afterDash
	}
	return errSharedLine
}

// dashBefore returns the last sequence entry token goccy's lexer places
// in text before line and column, or nil.
func dashBefore(text []byte, line, column int) (found *token.Token) {
	defer func() {
		if recover() != nil {
			found = nil
		}
	}()
	for _, tk := range lexer.Tokenize(string(text)) {
		p := tk.Position
		if p == nil {
			continue
		}
		if p.Line > line || (p.Line == line && p.Column >= column) {
			break
		}
		if tk.Type == token.SequenceEntryType {
			found = tk
		}
	}
	return found
}

// onlySpaceAndComments reports whether b, the text after a sequence
// dash, holds nothing but spaces, line breaks, and comments, so that the
// dash is the one of the item that b runs up to. A comment must follow a
// space or a line break.
func onlySpaceAndComments(b []byte) bool {
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == ' ' || b[i] == '\n':
		case b[i] == '#' && i > 0:
			for i < len(b) && b[i] != '\n' {
				i++
			}
		default:
			return false
		}
	}
	return true
}

// lowestLine returns the last line any node of n's tree sits on, not
// following aliases.
func lowestLine(n *yamlnode.Node) int {
	last := max(n.Line, n.TokenLine)
	if n.Kind == yamlnode.AliasNode {
		return last
	}
	for _, c := range n.Content {
		last = max(last, lowestLine(c))
	}
	return last
}

// commentAt reports whether line holds a comment that starts after
// exactly indent spaces.
func commentAt(line []byte, indent int) bool {
	return len(line) > indent && leadingSpaces(line) == indent && line[indent] == '#'
}

// lineSpan returns the span of lines first to last of d.src, with the
// line break that ends the last of them.
func (d *Document) lineSpan(first, last int) span {
	return span{lineStart(d.src, d.lines, first), lineStart(d.src, d.lines, last+1)}
}
