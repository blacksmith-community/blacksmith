// Copyright © 2019 The Homeport Team
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
// Ported from github.com/homeport/dyff v1.12.0 (core_identifier.go) and
// modified for graft: identifiers work on yamlnode trees.

package yamldiff

import (
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// listItemIdentifier tells list entries apart.
type listItemIdentifier interface {
	// Name returns a unique name for the entry, or an error when the
	// entry lacks the fields the identifier needs.
	Name(mapping *yamlnode.Node) (string, error)

	// FindNodeByName returns the entry with the given name, or an error
	// when there is none or an entry lacks the needed fields.
	FindNodeByName(sequence *yamlnode.Node, name string) (*yamlnode.Node, error)

	// String describes the identifier.
	String() string
}

// singleField identifies list entries by one field, such as "name" or
// "id".
type singleField struct {
	IdentifierFieldName string
}

var _ listItemIdentifier = (*singleField)(nil)

// FindNodeByName returns the entry whose identifier field equals name.
func (sf *singleField) FindNodeByName(sequence *yamlnode.Node, name string) (*yamlnode.Node, error) {
	return findByName(sf, sequence, name)
}

// Name returns the entry's identifier field, following an alias value.
func (sf *singleField) Name(mapping *yamlnode.Node) (string, error) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k, v := mapping.Content[i], mapping.Content[i+1]
		if k.Value == sf.IdentifierFieldName {
			return yamlnode.FollowAlias(v).Value, nil
		}
	}

	return "", fmt.Errorf("no key %q found in map", sf.IdentifierFieldName)
}

// String returns the identifier field name.
func (sf *singleField) String() string {
	return sf.IdentifierFieldName
}

// k8sItemIdentifier identifies Kubernetes resources by api version,
// kind, namespace, and name.
type k8sItemIdentifier struct{}

var k8sItem listItemIdentifier = (*k8sItemIdentifier)(nil)

// FindNodeByName returns the resource with the given joined name.
func (i *k8sItemIdentifier) FindNodeByName(sequence *yamlnode.Node, name string) (*yamlnode.Node, error) {
	return findByName(i, sequence, name)
}

// Name joins apiVersion, kind, an optional metadata.namespace, and
// metadata.name with slashes.
func (i *k8sItemIdentifier) Name(node *yamlnode.Node) (string, error) {
	if node.Kind != yamlnode.MappingNode {
		return "", fmt.Errorf("provided node is not a mapping node")
	}

	var elem []string

	apiVersion, err := grab(node, "apiVersion")
	if err != nil {
		return "", err
	}
	elem = append(elem, apiVersion.Value)

	kind, err := grab(node, "kind")
	if err != nil {
		return "", err
	}
	elem = append(elem, kind.Value)

	// The namespace is optional and left out when it is not set.
	namespace, err := grab(node, "metadata.namespace")
	if err == nil {
		elem = append(elem, namespace.Value)
	}

	name, err := grab(node, "metadata.name")
	if err != nil {
		return "", err
	}
	elem = append(elem, name.Value)

	return strings.Join(elem, "/"), nil
}

// String returns "resource".
func (i *k8sItemIdentifier) String() string {
	return "resource"
}

func findByName(id listItemIdentifier, sequence *yamlnode.Node, name string) (*yamlnode.Node, error) {
	for _, mapping := range sequence.Content {
		nameOfNode, err := id.Name(mapping)
		if err != nil {
			return nil, err
		}

		if nameOfNode == name {
			return mapping, nil
		}
	}

	return nil, fmt.Errorf("failed to find mapping entry with name %q", name)
}
