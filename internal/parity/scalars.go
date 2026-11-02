package parity

import (
	"fmt"
	"strconv"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	yamlv3 "gopkg.in/yaml.v3"
)

// Scalars records how YAML text spells and resolves each of its scalars,
// which Dump cannot show once a library has decoded the text. A line holds
// the scalar's path, its style (plain, single, double, literal, or folded),
// the tag a YAML 1.2 reader gives it, the Go type a YAML 1.1 reader gives
// it, and its text. A quoted scalar is always a string, and a plain one is
// whatever its text resolves to, so `a: 2026-01-02` and `a: "2026-01-02"`
// record differently, as do `yes` and `"yes"`, `0755` and `"0755"`, and `~`
// and `"~"`. Mapping keys are recorded too, under the path of the value
// they hold with "#key" added. The text must hold at most one document.
func Scalars(text string) string {
	var root yamlv3.Node

	err := yamlv3.Unmarshal([]byte(text), &root)
	if err != nil {
		return "ERROR: " + err.Error() + "\n"
	}

	var builder strings.Builder

	scalarsOf(&builder, "$", &root)

	return builder.String()
}

func scalarsOf(builder *strings.Builder, path string, node *yamlv3.Node) {
	switch node.Kind {
	case yamlv3.DocumentNode:
		for _, child := range node.Content {
			scalarsOf(builder, path, child)
		}
	case yamlv3.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			label := "[" + strconv.Quote(key.Value) + "]"

			scalarsOf(builder, path+label+"#key", key)
			scalarsOf(builder, path+label, value)
		}
	case yamlv3.SequenceNode:
		for index, child := range node.Content {
			scalarsOf(builder, fmt.Sprintf("%s[%d]", path, index), child)
		}
	case yamlv3.AliasNode:
		writeScalarLine(builder, path, "alias", "-", "-", node.Value)
	case yamlv3.ScalarNode:
		writeScalarLine(builder, path, styleName(node.Style), node.Tag, yaml11Type(node), node.Value)
	}
}

func styleName(style yamlv3.Style) string {
	switch {
	case style&yamlv3.DoubleQuotedStyle != 0:
		return "double"
	case style&yamlv3.SingleQuotedStyle != 0:
		return "single"
	case style&yamlv3.LiteralStyle != 0:
		return "literal"
	case style&yamlv3.FoldedStyle != 0:
		return "folded"
	default:
		return "plain"
	}
}

// yaml11Type is the Go type a YAML 1.1 reader makes of the scalar.
func yaml11Type(node *yamlv3.Node) string {
	if node.Style != 0 && node.Style&yamlv3.TaggedStyle == 0 {
		return "string"
	}

	var resolved interface{}

	err := yamlv2.Unmarshal([]byte(node.Value), &resolved)
	if err != nil {
		return "string"
	}

	if node.Value == "" {
		return "null"
	}

	return fmt.Sprintf("%T", resolved)
}

func writeScalarLine(builder *strings.Builder, path, style, tag, resolved, text string) {
	fmt.Fprintf(builder, "%s\t%s\t%s\t%s\t%q\n", path, style, tag, resolved, text)
}
