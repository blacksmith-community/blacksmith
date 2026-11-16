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
// Ported from github.com/gonvenience/ytbx v1.5.0 (path.go, input.go) and
// modified for graft: paths and input files carry yamlnode trees.

// Package yamldiff compares YAML documents the way homeport/dyff v1.12.0
// does with graft's settings, and loads diff inputs the way
// gonvenience/ytbx v1.5.0 does. Its Report feeds graft's human report and
// the histdiff change lists.
package yamldiff

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// InputFile is one diff input and the documents read from it.
type InputFile struct {
	Location  string
	Documents []*yamlnode.Node
	Names     []string
}

// PathElement is one part of a Path. It addresses an entry in a map by
// name, an entry of a named list by identifier key and name, or an entry
// of a list by index.
type PathElement struct {
	Idx  int
	Key  string
	Name string

	// id is the identifier that named a named-list entry.
	id listItemIdentifier
}

// Step returns the node e names in n, following an alias, the way the
// comparison found it. from says n is on the from side, where a map
// entry is matched by its key as written, and not through an alias. It
// reports false when n holds no such node, or more than one.
func (e PathElement) Step(n *yamlnode.Node, from bool) (*yamlnode.Node, bool) {
	return e.StepIndexed(n, from, nil)
}

// StepIndexed is Step, finding a map entry through ix when ix is not
// nil, so that many steps under one map don't each scan it.
func (e PathElement) StepIndexed(n *yamlnode.Node, from bool, ix *KeyIndex) (*yamlnode.Node, bool) {
	n = yamlnode.FollowAlias(n)
	if n == nil {
		return nil, false
	}
	var found *yamlnode.Node
	switch {
	case e.Key != "":
		found = e.namedEntry(n)
	case e.Idx >= 0:
		if n.Kind == yamlnode.SequenceNode && e.Idx < len(n.Content) {
			found = n.Content[e.Idx]
		}
	default:
		found = e.mapValue(n, from, ix)
	}
	if found == nil {
		return nil, false
	}
	return yamlnode.FollowAlias(found), true
}

// namedEntry returns the one entry of the list n that e names, or nil
// when n is not a list or holds no such entry, or more than one.
func (e PathElement) namedEntry(n *yamlnode.Node) *yamlnode.Node {
	if e.id == nil || n.Kind != yamlnode.SequenceNode {
		return nil
	}
	var found *yamlnode.Node
	for _, entry := range n.Content {
		name, err := e.id.Name(entry)
		if err != nil || name != e.Name {
			continue
		}
		if found != nil {
			return nil
		}
		found = entry
	}
	return found
}

// mapValue returns the value of the one key of the map n that e names,
// or nil when n is not a map or holds no such key, or more than one. A
// key on the from side is matched as written, and not through an alias.
func (e PathElement) mapValue(n *yamlnode.Node, from bool, ix *KeyIndex) *yamlnode.Node {
	if n.Kind != yamlnode.MappingNode {
		return nil
	}
	if ix != nil {
		value, count := ix.Value(n, e.Name, from)
		if count != 1 {
			return nil
		}
		return value
	}
	var found *yamlnode.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !from {
			k = yamlnode.FollowAlias(k)
		}
		if k.Value != e.Name {
			continue
		}
		if found != nil {
			return nil
		}
		found = n.Content[i+1]
	}
	return found
}

// Path points to a section of a document by naming each step from the
// root.
type Path struct {
	Root         *InputFile
	DocumentIdx  int
	PathElements []PathElement
}

// ToGoPatchStyle returns the path in go-patch style, such as
// "/a/name=web/0".
func (p *Path) ToGoPatchStyle() string {
	if len(p.PathElements) == 0 {
		return "/"
	}

	sections := []string{""}
	for _, element := range p.PathElements {
		switch {
		case element.Name != "" && element.Key == "":
			sections = append(sections, element.Name)

		case element.Name != "" && element.Key != "":
			sections = append(sections, fmt.Sprintf("%s=%s", element.Key, element.Name))

		default:
			sections = append(sections, strconv.Itoa(element.Idx))
		}
	}

	return strings.Join(sections, "/")
}

// ToDotStyle returns the path in dot style, such as "a.web.0". Keys are
// not escaped, and an element with neither a name nor an index is
// skipped.
func (p *Path) ToDotStyle() string {
	sections := []string{}
	for _, element := range p.PathElements {
		switch {
		case element.Name != "":
			sections = append(sections, element.Name)

		case element.Idx >= 0:
			sections = append(sections, strconv.Itoa(element.Idx))
		}
	}

	return strings.Join(sections, ".")
}

// RootDescription names the document a path starts in, using the file's
// document name when it has one and a one-based number otherwise.
func (p *Path) RootDescription() string {
	if p.Root != nil && p.DocumentIdx >= 0 && p.DocumentIdx < len(p.Root.Names) {
		return p.Root.Names[p.DocumentIdx]
	}

	return fmt.Sprintf("document #%d", p.DocumentIdx+1)
}

// NewPathWithPathElement returns a copy of path with element appended.
// The copy never shares its element slice with path.
func NewPathWithPathElement(path Path, element PathElement) Path {
	elements := make([]PathElement, len(path.PathElements), len(path.PathElements)+1)
	copy(elements, path.PathElements)

	return Path{
		Root:         path.Root,
		DocumentIdx:  path.DocumentIdx,
		PathElements: append(elements, element),
	}
}

// NewPathWithNamedElement returns path extended by a map entry.
func NewPathWithNamedElement(path Path, name interface{}) Path {
	return NewPathWithPathElement(path, PathElement{
		Idx:  -1,
		Name: fmt.Sprintf("%v", name),
	})
}

// NewPathWithNamedListElement returns path extended by a named-list entry.
func NewPathWithNamedListElement(path Path, identifier, name interface{}) Path {
	id, _ := identifier.(listItemIdentifier)
	return NewPathWithPathElement(path, PathElement{
		Idx:  -1,
		Key:  fmt.Sprintf("%v", identifier),
		Name: fmt.Sprintf("%v", name),
		id:   id,
	})
}

// NewPathWithIndexedListElement returns path extended by a list entry.
func NewPathWithIndexedListElement(path Path, idx int) Path {
	return NewPathWithPathElement(path, PathElement{Idx: idx})
}
