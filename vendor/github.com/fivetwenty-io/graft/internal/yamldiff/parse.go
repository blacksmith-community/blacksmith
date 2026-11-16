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
// Ported from github.com/gonvenience/ytbx v1.5.0 (input.go) and modified
// for graft.

package yamldiff

import (
	"strings"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// ParseDocuments reads YAML or JSON bytes into documents the way graft
// diff does. Empty input, or input that is only "---", is one null
// document. Input whose first byte is '{' or '[' is tried as
// JSON first. It never reads TOML.
func ParseDocuments(data []byte) ([]*yamlnode.Node, error) {
	// Empty nodes are null values (YAML 1.2.2 sections 7.2, 9.1.2, and
	// 10.3.2).
	if IsEmpty(data) {
		return []*yamlnode.Node{{
			Kind: yamlnode.DocumentNode,
			Content: []*yamlnode.Node{
				{Kind: yamlnode.ScalarNode, Tag: yamlnode.TagNull},
			},
		}}, nil
	}

	switch data[0] {
	case '{', '[':
		return loadJSONDocuments(data)
	default:
		return yamlnode.Parse(data)
	}
}

// IsEmpty reports whether data is blank or only a document marker, which
// both read as one null document.
func IsEmpty(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	return trimmed == "" || trimmed == "---"
}
