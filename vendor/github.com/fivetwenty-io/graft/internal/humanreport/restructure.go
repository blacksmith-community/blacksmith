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
// Ported from github.com/gonvenience/ytbx v1.5.0 restructure.go and
// modified for graft: it works on yamlnode nodes, maxDepth is
// yamldiff.MaxDepth, and the DisableRemainingKeySort switch is gone.

package humanreport

import (
	"sort"

	"github.com/fivetwenty-io/graft/internal/yamldiff"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// knownKeyOrders lists the key orders that people expect to read, and the
// first entry that shares the most keys with a mapping decides its order.
var knownKeyOrders = [][]string{
	{"name", "director_uuid", "releases", "instance_groups", "networks", "resource_pools", "compilation"},
	{"name", "url", "version", "sha1"},

	// Concourse (https://concourse-ci.org/pipelines.html, https://concourse-ci.org/steps.html, https://concourse-ci.org/resources.html).
	{"jobs", "resources", "resource_types"},
	{"name", "type", "source"},
	{"get"},
	{"put"},
	{"task"},

	// SUSE SCF role manifest.
	{"releases", "instance_groups", "configuration", "variables"},
	{"auth", "templates"},

	// Kubernetes object.
	{"apiVersion", "kind", "metadata", "spec", "status"},

	// Universal default #1, where name should always be first.
	{"name"},

	// Universal default #2, where key should always be first.
	{"key"},

	// Universal default #3, where id should always be first.
	{"id"},
}

func lookupMap(list []string) map[string]int {
	result := make(map[string]int, len(list))
	for idx, entry := range list {
		result[entry] = idx
	}

	return result
}

// lookupMapOfContentList maps each key to the index of its last occurrence,
// which is the quirk that makes a repeated key take its last pair.
func lookupMapOfContentList(list []*yamlnode.Node) map[string]int {
	lookup := make(map[string]int, len(list))
	for i := 0; i < len(list); i += 2 {
		lookup[list[i].Value] = i
	}

	return lookup
}

func countCommonKeys(keys []string, list []string) (counter int) {
	lookup := lookupMap(keys)
	for _, key := range list {
		if _, ok := lookup[key]; ok {
			counter++
		}
	}

	return counter
}

func commonKeys(setA []string, setB []string) []string {
	result, lookup := []string{}, lookupMap(setB)
	for _, entry := range setA {
		if _, ok := lookup[entry]; ok {
			result = append(result, entry)
		}
	}

	return result
}

// reorderKeyValuePairsInMappingNodeContent rebuilds the content of a mapping
// with the given keys first and the remaining keys after them, sorted with
// the unstable sort.Slice so that large mappings come out as they do in
// spruce.
func reorderKeyValuePairsInMappingNodeContent(mappingNode *yamlnode.Node, keys []string) {
	remainingKeys, keysLookup := []string{}, lookupMap(keys)
	for i := 0; i < len(mappingNode.Content); i += 2 {
		key := mappingNode.Content[i].Value
		if _, ok := keysLookup[key]; !ok {
			remainingKeys = append(remainingKeys, key)
		}
	}

	// Sort long and possibly hard to read structures to the end.
	sort.Slice(remainingKeys, func(i, j int) bool {
		valI, _ := yamldiff.ValueByKey(mappingNode, remainingKeys[i])
		valJ, _ := yamldiff.ValueByKey(mappingNode, remainingKeys[j])

		return yamldiff.MaxDepth(valI) < yamldiff.MaxDepth(valJ)
	})

	contentLookup := lookupMapOfContentList(mappingNode.Content)
	ordered := append(keys, remainingKeys...)
	content := make([]*yamlnode.Node, 0, 2*len(ordered))
	for _, key := range ordered {
		idx := contentLookup[key]
		content = append(content,
			mappingNode.Content[idx],
			mappingNode.Content[idx+1],
		)
	}

	mappingNode.Content = content
}

func getSuitableReorderFunction(keys []string) func(*yamlnode.Node) {
	topCandidateIdx, topCandidateHits := -1, -1
	for idx, candidate := range knownKeyOrders {
		if count := countCommonKeys(keys, candidate); count > 0 && count > topCandidateHits {
			topCandidateIdx = idx
			topCandidateHits = count
		}
	}

	if topCandidateIdx >= 0 {
		return func(input *yamlnode.Node) {
			reorderKeyValuePairsInMappingNodeContent(
				input,
				commonKeys(knownKeyOrders[topCandidateIdx], keys),
			)
		}
	}

	return nil
}

// restructureObject traverses down any sub elements such as list entries or
// map values, and on mapping nodes it rearranges the keys to meet a known
// human order, in place so that every alias to the node sees the new order.
func restructureObject(node *yamlnode.Node) {
	switch node.Kind {
	case yamlnode.DocumentNode:
		if len(node.Content) > 0 {
			restructureObject(node.Content[0])
		}

	case yamlnode.MappingNode:
		keys := yamldiff.ListKeys(node)
		if fn := getSuitableReorderFunction(keys); fn != nil {
			fn(node)
		}

		for i := 0; i+1 < len(node.Content); i += 2 {
			restructureObject(node.Content[i+1])
		}

	case yamlnode.SequenceNode:
		for i := range node.Content {
			restructureObject(node.Content[i])
		}

	default:
	}
}
