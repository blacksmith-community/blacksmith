// Copyright © 2018 The Homeport Team
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.
//
// Ported from github.com/gonvenience/ytbx v1.5.0 (list_functions.go,
// map_functions.go, restructure.go, getting.go, path.go) and
// github.com/homeport/dyff v1.12.0 (core.go), and modified for graft:
// the helpers work on yamlnode trees, and grab is narrowed to what the
// identifiers need.

package yamldiff

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// ListKeys returns the keys of a mapping node in document order.
func ListKeys(mapping *yamlnode.Node) []string {
	keys := make([]string, 0, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		keys = append(keys, mapping.Content[i].Value)
	}

	return keys
}

// ValueByKey returns the value of the first entry of a mapping node whose
// key is key.
func ValueByKey(mapping *yamlnode.Node, key string) (*yamlnode.Node, bool) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], true
		}
	}

	return nil, false
}

// NamedListIdentifier returns the key that every entry of the sequence
// carries and that names it, which is "name", "key", or "id" checked in
// that order, or an empty string when there is none.
func NamedListIdentifier(sequence *yamlnode.Node) string {
	counters := map[string]int{}
	for _, mapping := range sequence.Content {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			counters[mapping.Content[i].Value]++
		}
	}

	listLength := len(sequence.Content)
	for _, identifier := range []string{"name", "key", "id"} {
		if count, ok := counters[identifier]; ok && count == listLength {
			return identifier
		}
	}

	return ""
}

// MaxDepth returns the largest number of path elements on any leaf below
// n, where a leaf is a scalar or an alias. A named-list entry counts as
// one element and its identifier key is skipped, so containers with no
// leaf, such as empty mappings, add nothing and a node with no leaf has
// depth 0.
func MaxDepth(n *yamlnode.Node) int {
	deepest := 0
	traverseLeaves(0, n, func(depth int) {
		if depth > deepest {
			deepest = depth
		}
	})

	return deepest
}

// traverseLeaves calls leaf with the depth of every leaf below n, which
// sits at the given depth.
func traverseLeaves(depth int, n *yamlnode.Node, leaf func(depth int)) {
	switch n.Kind {
	case yamlnode.DocumentNode:
		if len(n.Content) > 0 {
			traverseLeaves(depth, n.Content[0], leaf)
		}

	case yamlnode.SequenceNode:
		if identifier := NamedListIdentifier(n); identifier != "" {
			for _, entry := range n.Content {
				traverseNamedEntry(depth+1, entry, identifier, leaf)
			}
			return
		}

		for _, entry := range n.Content {
			traverseLeaves(depth+1, entry, leaf)
		}

	case yamlnode.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			traverseLeaves(depth+1, n.Content[i+1], leaf)
		}

	default:
		leaf(depth)
	}
}

// traverseNamedEntry walks the fields of a named-list entry, which sits at
// depth, and skips the identifier field.
func traverseNamedEntry(depth int, entry *yamlnode.Node, identifier string, leaf func(depth int)) {
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if entry.Content[i].Value == identifier {
			continue
		}

		traverseLeaves(depth+1, entry.Content[i+1], leaf)
	}
}

// grab returns the node at a dot-separated path below n. At a mapping it
// looks the segment up by key. A scalar or an alias ends the walk and is
// returned as it is, which is how ytbx treats a path that runs past a
// scalar. At a sequence the segment has to name an entry through the
// sequence's named-list identifier.
func grab(n *yamlnode.Node, path string) (*yamlnode.Node, error) {
	pointer := n
	for _, segment := range strings.Split(path, ".") {
		switch pointer.Kind {
		case yamlnode.MappingNode:
			value, ok := ValueByKey(pointer, segment)
			if !ok {
				return nil, fmt.Errorf("no key called %s in map, available keys: %s",
					segment, strings.Join(ListKeys(pointer), ", "))
			}
			pointer = value

		case yamlnode.SequenceNode:
			entry, ok := namedListEntry(pointer, segment)
			if !ok {
				return nil, fmt.Errorf("there is no entry %s=%s in the list",
					NamedListIdentifier(pointer), segment)
			}
			pointer = entry

		default:
			return pointer, nil
		}
	}

	return pointer, nil
}

// namedListEntry returns the entry of the sequence whose identifier field
// has the value name.
func namedListEntry(sequence *yamlnode.Node, name string) (*yamlnode.Node, bool) {
	identifier := NamedListIdentifier(sequence)
	for _, entry := range sequence.Content {
		for i := 0; i+1 < len(entry.Content); i += 2 {
			if entry.Content[i].Value == identifier && entry.Content[i+1].Value == name {
				return entry, true
			}
		}
	}

	return nil, false
}
