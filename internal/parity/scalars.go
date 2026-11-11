package parity

import (
	"fmt"
	"strconv"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	yamlv4 "go.yaml.in/yaml/v4"
)

// Scalars records how YAML text spells and resolves each of its scalars,
// which Dump cannot show once a library has decoded the text. A line holds
// the scalar's path, its style (plain, single, double, literal, or folded),
// the tag a YAML 1.2 reader gives it, the Go type a YAML 1.1 reader gives
// it, and its text. A quoted scalar is always a string, and a plain one is
// whatever its text resolves to, so `a: 2026-01-02` and `a: "2026-01-02"`
// record differently, as do `yes` and `"yes"`, `0755` and `"0755"`, and `~`
// and `"~"`. The tag comes from yaml/v4, which tags a plain `-0` as a float
// to keep negative zero, where YAML 1.2's core schema and graft's Node.Tag
// say int. Mapping keys are recorded too, under the path of the value
// they hold with "#key" added. The text must hold at most one document.
func Scalars(text string) string {
	var root yamlv4.Node

	err := yamlv4.Unmarshal([]byte(text), &root)
	if err != nil {
		return "ERROR: " + err.Error() + "\n"
	}

	var builder strings.Builder

	scalarsOf(&builder, "$", &root)

	return builder.String()
}

func scalarsOf(builder *strings.Builder, path string, node *yamlv4.Node) {
	switch node.Kind {
	case yamlv4.DocumentNode:
		for _, child := range node.Content {
			scalarsOf(builder, path, child)
		}
	case yamlv4.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			label := "[" + strconv.Quote(key.Value) + "]"

			scalarsOf(builder, path+label+"#key", key)
			scalarsOf(builder, path+label, value)
		}
	case yamlv4.SequenceNode:
		for index, child := range node.Content {
			scalarsOf(builder, fmt.Sprintf("%s[%d]", path, index), child)
		}
	case yamlv4.AliasNode:
		writeScalarLine(builder, path, "alias", "-", "-", node.Value)
	case yamlv4.ScalarNode:
		writeScalarLine(builder, path, styleName(node.Style), node.Tag, yaml11Type(node), node.Value)
	}
}

func styleName(style yamlv4.Style) string {
	switch {
	case style&yamlv4.DoubleQuotedStyle != 0:
		return "double"
	case style&yamlv4.SingleQuotedStyle != 0:
		return "single"
	case style&yamlv4.LiteralStyle != 0:
		return "literal"
	case style&yamlv4.FoldedStyle != 0:
		return "folded"
	default:
		return "plain"
	}
}

// yaml11Type is the Go type a YAML 1.1 reader makes of the scalar.
func yaml11Type(node *yamlv4.Node) string {
	if node.Style != 0 && node.Style&yamlv4.TaggedStyle == 0 {
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

// LegacyRoundTrip marshals value with yaml/v4 and reads the text back with
// yaml.v2, the way jobs reached credentials templates before graft carried
// them. Those jobs used yaml.v3 to marshal. That library is no longer a
// dependency, and v4's Marshal uses v3's defaults, so v4 stands in for it.
// It exists for comparison in tests only, and is here so this file is the
// one place that imports those libraries.
func LegacyRoundTrip(value interface{}) (map[interface{}]interface{}, error) {
	text, err := yamlv4.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshaling with yaml/v4: %w", err)
	}

	var decoded map[interface{}]interface{}

	err = yamlv2.Unmarshal(text, &decoded)
	if err != nil {
		return nil, fmt.Errorf("unmarshaling with yaml.v2: %w", err)
	}

	return decoded, nil
}
