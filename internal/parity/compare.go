package parity

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// CompareYAML fails the test for every difference between two YAML
// documents, each reported with its path. See YAMLDifferences for what
// counts as a difference.
func CompareYAML(t *testing.T, want, got []byte) {
	t.Helper()

	for _, difference := range YAMLDifferences(want, got) {
		t.Error(difference)
	}
}

// YAMLDifferences compares two YAML documents and returns one line for
// each difference, or nothing when they match. Two checks both have to
// hold.
//
// The first compares the trees graft reads. Every node must match on kind,
// resolved tag, and value. Tags resolve as yaml.v3 resolves them, so a
// plain 3 is !!int, a plain 3.0 is !!float, a plain 2026-01-02 is
// !!timestamp, and any quoted scalar is !!str. Mapping pairs match by key
// and not by position, so indentation and key order do not count, while
// sequence order does. An alias is followed to the node its anchor names
// and compared as that node, so a document that spells a value with an
// alias equals one that writes the value out. A << merge key is not
// expanded here. It is compared as written, as a key tagged !!merge, and
// the second check covers what it merges.
//
// The second decodes both documents into any under YAML 1.1, where plain
// yes is true, and requires the results to be reflect.DeepEqual. That
// check sees merged values and the Go types the broker will see.
//
// Paths follow Dump, so a key with a dot in it reads ["a.b"] and cannot be
// mistaken for a nested key.
func YAMLDifferences(want, got []byte) []string {
	var wantTree, gotTree yaml.Node

	err := yaml.Unmarshal(want, &wantTree)
	if err != nil {
		return []string{"want: cannot read the document: " + err.Error()}
	}

	err = yaml.Unmarshal(got, &gotTree)
	if err != nil {
		return []string{"got: cannot read the document: " + err.Error()}
	}

	var differences []string

	compareNodes(&differences, "$", &wantTree, &gotTree)

	var wantValue, gotValue any

	err = yaml.Unmarshal(want, &wantValue)
	if err != nil {
		return append(differences, "want: cannot decode the document: "+err.Error())
	}

	err = yaml.Unmarshal(got, &gotValue)
	if err != nil {
		return append(differences, "got: cannot decode the document: "+err.Error())
	}

	if !reflect.DeepEqual(wantValue, gotValue) {
		differences = append(differences, decodeDifferences(wantValue, gotValue)...)
	}

	return differences
}

// decodeDifferences lists the leaves where two decoded values differ, using
// Dump so that the paths and types read as they do in the goldens.
func decodeDifferences(want, got any) []string {
	wantLines := dumpLines(want)
	gotLines := dumpLines(got)

	var differences []string

	for path, line := range wantLines {
		other, ok := gotLines[path]

		switch {
		case !ok:
			differences = append(differences, "decode "+path+": missing, want "+line)
		case other != line:
			differences = append(differences, "decode "+path+": want "+line+", got "+other)
		}
	}

	for path, line := range gotLines {
		if _, ok := wantLines[path]; !ok {
			differences = append(differences, "decode "+path+": extra, got "+line)
		}
	}

	if len(differences) == 0 {
		differences = append(differences, "decode $: values differ")
	}

	sort.Strings(differences)

	return differences
}

// dumpLines maps each leaf path of Dump's output to the type and value.
func dumpLines(value any) map[string]string {
	lines := map[string]string{}

	for _, line := range strings.Split(strings.TrimSuffix(Dump(value), "\n"), "\n") {
		path, rest, _ := strings.Cut(line, "\t")
		lines[path] = rest
	}

	return lines
}

func compareNodes(differences *[]string, path string, want, got *yaml.Node) {
	want, got = resolveAlias(want), resolveAlias(got)

	if want.Kind != got.Kind {
		*differences = append(*differences, fmt.Sprintf("tree %s: kind %s, want %s", path, kindName(got.Kind), kindName(want.Kind)))

		return
	}

	switch want.Kind { //nolint:exhaustive // an alias never survives resolveAlias
	case yaml.DocumentNode:
		compareContent(differences, path, want, got)
	case yaml.SequenceNode:
		compareTag(differences, path, want, got)

		if len(want.Content) != len(got.Content) {
			*differences = append(*differences, fmt.Sprintf("tree %s: %d items, want %d", path, len(got.Content), len(want.Content)))
		}

		for index := range min(len(want.Content), len(got.Content)) {
			compareNodes(differences, fmt.Sprintf("%s[%d]", path, index), want.Content[index], got.Content[index])
		}
	case yaml.MappingNode:
		compareTag(differences, path, want, got)
		compareMappings(differences, path, want, got)
	case yaml.ScalarNode:
		compareTag(differences, path, want, got)

		if want.Value != got.Value {
			*differences = append(*differences, fmt.Sprintf("tree %s: value %q, want %q", path, got.Value, want.Value))
		}
	}
}

func compareContent(differences *[]string, path string, want, got *yaml.Node) {
	if len(want.Content) != len(got.Content) {
		*differences = append(*differences, fmt.Sprintf("tree %s: %d documents, want %d", path, len(got.Content), len(want.Content)))

		return
	}

	for index := range want.Content {
		compareNodes(differences, path, want.Content[index], got.Content[index])
	}
}

func compareTag(differences *[]string, path string, want, got *yaml.Node) {
	if want.Tag != got.Tag {
		*differences = append(*differences, fmt.Sprintf("tree %s: tag %s, want %s", path, got.Tag, want.Tag))
	}
}

// compareMappings pairs entries by the key's tag and text, so that order
// does not matter. A key repeated in one mapping is refused when the
// document is read, so the pairing is unambiguous.
func compareMappings(differences *[]string, path string, want, got *yaml.Node) {
	gotPairs := map[string]*yaml.Node{}
	gotKeys := map[string]*yaml.Node{}

	for index := 0; index+1 < len(got.Content); index += 2 {
		key := resolveAlias(got.Content[index])
		identity := key.Tag + "\x00" + key.Value
		gotPairs[identity] = got.Content[index+1]
		gotKeys[identity] = key
	}

	seen := map[string]bool{}

	for index := 0; index+1 < len(want.Content); index += 2 {
		key := resolveAlias(want.Content[index])
		identity := key.Tag + "\x00" + key.Value
		seen[identity] = true
		label := keyPath(key)

		other, ok := gotPairs[identity]
		if !ok {
			*differences = append(*differences, fmt.Sprintf("tree %s%s: missing, want %s", path, label, describe(want.Content[index+1])))

			continue
		}

		compareNodes(differences, path+label, want.Content[index+1], other)
	}

	extras := []string{}

	for identity, key := range gotKeys {
		if !seen[identity] {
			extras = append(extras, fmt.Sprintf("tree %s%s: extra, got %s", path, keyPath(key), describe(gotPairs[identity])))
		}
	}

	sort.Strings(extras)

	*differences = append(*differences, extras...)
}

// keyPath spells a key the way keyLabel does for a string key. A key that
// is not a string carries its tag, since 1 and "1" are different keys.
func keyPath(key *yaml.Node) string {
	text := key.Value

	if key.Tag != "!!str" {
		return fmt.Sprintf("[%q](%s)", text, key.Tag)
	}

	if text != "" && strings.IndexFunc(text, func(r rune) bool { return !isPlainKeyRune(r) }) < 0 {
		return "." + text
	}

	return "[" + strconv.Quote(text) + "]"
}

func describe(node *yaml.Node) string {
	node = resolveAlias(node)

	if node.Kind == yaml.ScalarNode {
		return fmt.Sprintf("%s %q", node.Tag, node.Value)
	}

	return kindName(node.Kind)
}

// resolveAlias follows an alias to the node its anchor names. Graft refuses
// an alias inside its own anchor, so the chain ends.
func resolveAlias(node *yaml.Node) *yaml.Node {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}

	return node
}

func kindName(kind yaml.Kind) string {
	switch kind { //nolint:exhaustive // any other kind is reported by number
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	}

	return fmt.Sprintf("kind(%d)", kind)
}
