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
// The MIT License (MIT)
//
// Copyright (c) 2016 Mitchell Hashimoto
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
// Ported from github.com/homeport/dyff v1.12.0 (core.go, calcNodeHash and
// basicType) and github.com/mitchellh/hashstructure/v2 v2.0.2 (FormatV2
// hashing), and modified for graft: the hash is computed straight from
// yamlnode trees, and a collection used as a mapping key is an error.

package yamldiff

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// UnhashableKeyError reports a mapping key that is itself a mapping or a
// sequence inside a simple list entry. dyff panics with "hash of
// unhashable type" there; graft returns this error instead.
type UnhashableKeyError struct {
	Line int
}

func (e *UnhashableKeyError) Error() string {
	return fmt.Sprintf("cannot compare a list entry whose mapping key at line %d is a collection", e.Line)
}

// nodeHash is dyff's calcNodeHash (core.go:1089-1111) without
// hashstructure. A top-level scalar entry hashes its tag and text, and a
// collection hashes the Go value dyff's basicType would build.
func nodeHash(n *yamlnode.Node) (uint64, error) {
	switch n.Kind {
	case yamlnode.MappingNode, yamlnode.SequenceNode:
		return basicHash(n)
	case yamlnode.ScalarNode:
		return fnv1(n.Tag + "/" + n.Value), nil
	case yamlnode.AliasNode:
		return nodeHash(yamlnode.FollowAlias(n))
	default:
		return 0, fmt.Errorf("kind %v is not supported", n.Kind)
	}
}

// basicHash returns hashstructure.Hash(basicType(n), FormatV2, nil) for
// the three shapes dyff's basicType (core.go:1051-1087) builds. A scalar
// becomes its Value string, which hashes as FNV-1 64 over its bytes. A
// sequence folds each item's hash into a running hash that starts at 0.
// A mapping becomes a Go map, so a repeated key keeps its last value, and
// the hash XORs the ordered hash of every key and value pair and then
// hashes that once more.
func basicHash(n *yamlnode.Node) (uint64, error) {
	switch n.Kind {
	case yamlnode.ScalarNode:
		return fnv1(n.Value), nil
	case yamlnode.AliasNode:
		return basicHash(yamlnode.FollowAlias(n))
	case yamlnode.SequenceNode:
		var acc uint64
		for _, item := range n.Content {
			h, err := basicHash(yamlnode.FollowAlias(item))
			if err != nil {
				return 0, err
			}
			acc = hashOrdered(acc, h)
		}
		return acc, nil
	case yamlnode.MappingNode:
		order := []string{}
		values := map[string]*yamlnode.Node{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := yamlnode.FollowAlias(n.Content[i]), yamlnode.FollowAlias(n.Content[i+1])
			if k.Kind != yamlnode.ScalarNode {
				return 0, &UnhashableKeyError{Line: k.Line}
			}
			if _, seen := values[k.Value]; !seen {
				order = append(order, k.Value)
			}
			values[k.Value] = v
		}
		var acc uint64
		for _, key := range order {
			h, err := basicHash(values[key])
			if err != nil {
				return 0, err
			}
			acc ^= hashOrdered(fnv1(key), h)
		}
		return hashFinish(acc), nil
	default:
		return 0, fmt.Errorf("kind %v is not supported", n.Kind)
	}
}

func fnv1(s string) uint64 {
	h := fnv.New64()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// hashOrdered is hashstructure's hashUpdateOrdered: FNV-1 64 over the
// little-endian bytes of a and then b.
func hashOrdered(a, b uint64) uint64 {
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], a)
	binary.LittleEndian.PutUint64(buf[8:], b)
	h := fnv.New64()
	_, _ = h.Write(buf[:])
	return h.Sum64()
}

// hashFinish is hashstructure's hashFinishUnordered: FNV-1 64 over the
// little-endian bytes of a.
func hashFinish(a uint64) uint64 {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], a)
	h := fnv.New64()
	_, _ = h.Write(buf[:])
	return h.Sum64()
}
