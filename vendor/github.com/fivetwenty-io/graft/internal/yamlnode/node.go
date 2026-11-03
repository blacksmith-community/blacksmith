// Package yamlnode is graft's YAML node model. It parses with
// goccy/go-yaml and reproduces the node trees, tags, and comment slots
// go.yaml.in/yaml/v3 produces, so the diff engine and the human report
// can run on goccy while matching spruce byte for byte.
package yamlnode

// Kind identifies what a Node holds.
type Kind uint8

// The node kinds, in yaml.v3's order.
const (
	DocumentNode Kind = iota + 1
	SequenceNode
	MappingNode
	ScalarNode
	AliasNode
)

// Style records how a node was written, with yaml.v3's meaning for each
// flag. The flags combine, so !!str 'x' is both tagged and single-quoted.
// The bits are in the order of value.StyleFlags, and the loader maps
// each one to its value flag through a table.
type Style uint8

// The style flags. StyleTagged marks an explicit tag other than a bare
// "!", including a core tag that resolution would have implied.
const (
	StyleTagged Style = 1 << iota
	StyleSingle
	StyleDouble
	StyleLiteral
	StyleFolded
	StyleFlow
)

// Node is one YAML node with yaml.v3's conventions. A document holds
// exactly one child, a mapping stores keys and values in one flat slice,
// and an alias keeps its anchor name in Value and its target in Alias.
type Node struct {
	Kind    Kind
	Tag     string  // yaml.v3's short tag: "!!str", "!!int", "!!map", "!foo"; empty for documents and aliases
	Value   string  // scalar text as yaml.v3 reports it; the anchor name for an alias
	Anchor  string  // the anchor defined on this node, without "&"
	Alias   *Node   // the anchored node an AliasNode points at
	Content []*Node // document: [root]; mapping: k0, v0, k1, v1, ...; sequence: items
	Style   Style

	HeadComment string // comment text including "#", lines joined with "\n"
	LineComment string
	FootComment string

	Line, Column int // 1-based source position; 0 for synthesized nodes

	// TokenLine and TokenColumn are where goccy's own token for the node
	// sits, which is where goccy's decoder reports an error about it.
	// They differ from Line and Column after a tag or an anchor, and for
	// a block mapping, whose token is its first ":".
	TokenLine, TokenColumn int
	// BareTag is set on a node written with the non-specific tag "!",
	// which yaml.v3 does not report, but which makes goccy read a plain
	// scalar as a string.
	BareTag bool
}

// FollowAlias returns the node an alias chain ends at, or n itself when
// n is not an alias. It mirrors dyff's followAlias.
func FollowAlias(n *Node) *Node {
	for n != nil && n.Alias != nil {
		n = n.Alias
	}
	return n
}
