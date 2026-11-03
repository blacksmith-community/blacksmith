package value

// StyleFlags records how a YAML scalar or collection was written. Any of
// the flags may combine.
type StyleFlags uint8

// The style flags.
const (
	Tagged StyleFlags = 1 << iota
	SingleQuoted
	DoubleQuoted
	Literal
	Folded
	Flow
)

// Meta carries the presentation details yq keeps around a value. The jq
// phases never set it, and every primitive that copies a value carries it
// through. Nothing in it points at a parent, so a shared child can sit
// under more than one container.
type Meta struct {
	// Tag is the explicit tag as written, and empty when it was implied.
	Tag   string
	Style StyleFlags
	// Text is a scalar's text as written. The YAML loader sets it for
	// every scalar, so round trips keep spellings such as 0x10, 1_000,
	// and .inf.
	Text string
	// Anchor is the anchor this node defines, and AliasOf is the anchor
	// an alias node refers to.
	Anchor, AliasOf string

	HeadComment, LineComment, FootComment, LeadingContent string

	Line, Column        int
	DocIndex, FileIndex int
	Filename            string
}

// Path is a sequence of components, each a string, a number, or a slice
// object with the keys start and end.
type Path []*Value

// IsSliceComponent reports whether c is a slice path component, which is
// an object holding exactly the keys start and end, either of which may be
// null, and returns the two bounds.
func IsSliceComponent(c *Value) (start, end *Value, ok bool) {
	if c == nil || c.kind != KindObject || c.Len() != 2 {
		return nil, nil, false
	}
	start, hasStart := c.Get("start")
	end, hasEnd := c.Get("end")
	if !hasStart || !hasEnd {
		return nil, nil, false
	}
	return start, end, true
}
