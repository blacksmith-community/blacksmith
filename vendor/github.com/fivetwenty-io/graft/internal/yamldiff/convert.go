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
// Ported from github.com/gonvenience/ytbx v1.5.0 (input.go, convert.go)
// and modified for graft: JSON values become yamlnode trees
// through yamlnode.FromValue, and JSON keeps ytbx's default of decoding
// numbers as float64 with keys in sorted order.

package yamldiff

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// loadJSONDocuments reads data as a stream of JSON values and returns one
// document per value.
func loadJSONDocuments(data []byte) ([]*yamlnode.Node, error) {
	values := []*yamlnode.Node{}

	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var value interface{}
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		node, err := yamlnode.FromValue(value)
		if err != nil {
			return nil, err
		}

		values = append(values, node)
	}

	return values, nil
}
