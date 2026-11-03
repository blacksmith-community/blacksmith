package yaml

import (
	"bytes"
	"sync"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

// readText is the document a tree was read from. Unmarshal's own parser
// reads some scalars differently from the tree, which follows yaml.v3,
// such as the trailing spaces of a block scalar, which it drops.
// So Decode parses the document again, once for the whole tree, to learn
// how Unmarshal read each scalar.
type readText struct {
	doc     []byte
	once    sync.Once
	scalars map[[2]int][]*parsedScalar
}

// parsedScalar is how Unmarshal's parser read a scalar, with its token's
// type, value, and source text, its tag as the text wrote it, such as
// !<tag:yaml.org,2002:str>, which the parser does not take for !!str,
// with the tag token's source text, and whether it is a block scalar,
// whose type is the type of its header and whose text is that header,
// such as "|-" or ">2". explicit says the scalar is a mapping key the
// text marked with "?", which the decoder reads differently, so an
// explicit ~ key decodes into a string as "" rather than "null". A
// collection records only its tag and whether it is an explicit key,
// with collection set.
type parsedScalar struct {
	typ        token.Type
	value      string
	origin     string
	tag        string
	tagOrigin  string
	block      bool
	collection bool
	explicit   bool
}

// parsed returns how Unmarshal's parser read n, or nil when n is not as
// Unmarshal read it, or the parse holds nothing where n's token sat. For
// a collection, only the tag is recorded.
func (n *Node) parsed() *parsedScalar {
	if !n.read || n.text == nil || n.tokenLine <= 0 || n.Value != n.readValue || n.Tag != n.readTag {
		return nil
	}
	block := n.style&(yamlnode.StyleLiteral|yamlnode.StyleFolded) != 0
	return n.text.scalar(n.tokenLine, n.tokenColumn, n.Value, block, n.Kind != ScalarNode)
}

// scalar returns the scalar whose token sits at line and col in the
// parse of the document, parsing it on first use. A block scalar is
// taken only when block is set, and then whatever it holds, since goccy's
// parser reads the last spaces of a block scalar differently. Any other
// scalar is taken when it holds value, or is the null of an empty value
// and value is empty. The parser can put two tokens at one place, such
// as an explicit key and an empty value after it, so a scalar that holds
// something else is never taken. With collection set, it returns the
// collection recorded at the place.
func (r *readText) scalar(line, col int, value string, block, collection bool) *parsedScalar {
	r.once.Do(r.parse)
	for _, s := range r.scalars[[2]int{line, col}] {
		switch {
		case s.collection != collection, s.block != block:
		case collection, block, s.value == value,
			value == "" && s.typ == token.ImplicitNullType:
			return s
		}
	}
	return nil
}

// parse parses the document as Unmarshal parses it and records each
// scalar by the place of its token. The tree comes from the document
// with a final line break, which the reader adds when it has none, and
// goccy's parser can read the last line of a document differently
// without one, placing a token elsewhere, or reading a lone tag as no
// document at all. So a document with no final line break is parsed with
// one, and the tree decodes as that text does. The one exception is a
// block scalar that the document's last line ends, whose value the added
// line break can change, so it takes the value Unmarshal reads for it
// from the document as it is. A document the parser refuses records
// nothing, so Decode falls back on the tree.
func (r *readText) parse() {
	r.scalars = make(map[[2]int][]*parsedScalar)
	prepared, _ := yamlprep.Prepare(r.doc)
	text := prepared
	if !bytes.HasSuffix(prepared, []byte("\n")) {
		text = append(prepared[:len(prepared):len(prepared)], '\n')
	}
	read, ok := scalarsOf(text)
	if !ok {
		return
	}
	if len(text) != len(prepared) {
		readLastBlockAsIs(read, prepared)
	}
	for _, s := range read {
		r.scalars[s.pos] = append(r.scalars[s.pos], s.scalar)
	}
}

// readLastBlockAsIs gives the last scalar of read, the scalars of doc
// with a line break added, when it is a block scalar, the value the
// parser reads for it from doc itself. A line break after the last line
// of a block changes its value when the block keeps its final line
// breaks, or when that line holds only blanks but more of them than the
// block's indentation. The value stays when the parser refuses doc or
// places its last block scalar elsewhere.
func readLastBlockAsIs(read []placedScalar, doc []byte) {
	last := len(read) - 1
	if last < 0 || !read[last].scalar.block {
		return
	}
	asIs, ok := scalarsOf(doc)
	if !ok || len(asIs) == 0 {
		return
	}
	if s := asIs[len(asIs)-1]; s.pos == read[last].pos && s.scalar.block {
		read[last].scalar.value = s.scalar.value
	}
}

// placedScalar is a scalar of a parse and the place of its token.
type placedScalar struct {
	pos    [2]int
	scalar *parsedScalar
}

// scalarsOf parses doc and returns its first document's scalars in the
// order the syntax tree holds them, and false when the parser refuses
// doc.
func scalarsOf(doc []byte) ([]placedScalar, bool) {
	file, err := yamlnode.ParseBytes(doc, 0)
	if err != nil {
		return nil, false
	}
	var out []placedScalar
	if body := yamlnode.FirstBody(file); body != nil {
		ast.Walk(scalarVisitor{out: &out}, body)
	}
	return out, true
}

// scalarVisitor records the scalars of a syntax tree in out. It skips
// the names of anchors and aliases, which are no values, and walks past
// a tag to the value under it, which is a scalar's own when tag is set,
// and past the "?" of an explicit key to the key, when explicit is set.
type scalarVisitor struct {
	out       *[]placedScalar
	tag       string
	tagOrigin string
	explicit  bool
}

// add records s, with the visitor's tag, its text, and whether s is an
// explicit key, as the scalar whose token sits at pos.
func (v scalarVisitor) add(pos *token.Position, s *parsedScalar) {
	s.tag, s.tagOrigin, s.explicit = v.tag, v.tagOrigin, v.explicit
	*v.out = append(*v.out, placedScalar{pos: [2]int{pos.Line, pos.Column}, scalar: s})
}

// Visit records n when it is a scalar, and walks on below it.
func (v scalarVisitor) Visit(n ast.Node) ast.Visitor {
	switch x := n.(type) {
	case *ast.AnchorNode:
		if x.Value != nil {
			ast.Walk(v, x.Value)
		}
		return nil
	case *ast.AliasNode:
		return nil
	case *ast.MappingKeyNode:
		if x.Value != nil {
			ast.Walk(scalarVisitor{out: v.out, explicit: true}, x.Value)
		}
		return nil
	case *ast.TagNode:
		v.visitTag(x)
		return nil
	case *ast.LiteralNode:
		if x.Start != nil && x.Start.Position != nil && x.Value != nil {
			v.add(x.Start.Position, &parsedScalar{typ: x.Start.Type, value: x.Value.Value, origin: x.Start.Value, block: true})
		}
		return nil
	case ast.ScalarNode:
		if tk := x.GetToken(); tk != nil && tk.Position != nil {
			v.add(tk.Position, &parsedScalar{typ: tk.Type, value: tk.Value, origin: tk.Origin})
		}
		return nil
	}
	return scalarVisitor{out: v.out}
}

// visitTag walks the value under the tag x, as its tag. A collection
// records the tag at the place of its first token, and its children walk
// with no tag.
func (v scalarVisitor) visitTag(x *ast.TagNode) {
	if x.Value == nil {
		return
	}
	inner := scalarVisitor{out: v.out, explicit: v.explicit}
	if x.Start != nil {
		inner.tag, inner.tagOrigin = x.Start.Value, x.Start.Origin
	}
	if tk := x.Value.GetToken(); tk != nil && tk.Position != nil && isCollection(x.Value) {
		inner.add(tk.Position, &parsedScalar{collection: true})
		inner.tag, inner.tagOrigin, inner.explicit = "", "", false
	}
	ast.Walk(inner, x.Value)
}

// isCollection reports whether n is a mapping or a sequence.
func isCollection(n ast.Node) bool {
	switch n.(type) {
	case *ast.MappingNode, *ast.MappingValueNode, *ast.SequenceNode:
		return true
	}
	return false
}
