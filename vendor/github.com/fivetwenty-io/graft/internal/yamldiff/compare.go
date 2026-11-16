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
// Ported from github.com/homeport/dyff v1.12.0 (core.go) and modified for
// graft: the engine compares yamlnode trees, the compare options are gone
// in favor of graft's fixed settings (a guess threshold of 3, Kubernetes
// detection on, order changes reported, and rename detection and string
// formatting off), list hashing goes through nodeHash and returns an
// error where dyff panics, and the larger functions are split into
// helpers that keep every decision in dyff's order.

package yamldiff

import (
	"errors"
	"fmt"
	"sort"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// nonStandardIdentifierGuessCountThreshold is how many distinct values a
// guessed identifier field must exceed before it names list entries.
const nonStandardIdentifierGuessCountThreshold = 3

// listItemIdentifierCandidates are the fields that name list entries,
// checked in this order. "manager" is dyff's Kubernetes extra candidate.
var listItemIdentifierCandidates = []string{"name", "key", "id", "manager"}

// CompareInputFiles compares two inputs and reports their differences in
// dyff's order. When every non-empty document on both sides is a
// Kubernetes resource, documents are matched by resource name, and the
// report's inputs keep only those documents and carry their names.
// Otherwise documents are compared by position, and both inputs must hold
// the same number of documents. PairDocuments decides which documents
// meet, so every caller that compares documents pairs them this way.
func CompareInputFiles(from, to InputFile) (Report, error) {
	pairing, err := PairDocuments(from, to)
	if err != nil {
		return Report{}, err
	}

	groups, err := pairing.Compare()
	if err != nil {
		if pairing.Kubernetes {
			err = fmt.Errorf("comparing Kubernetes resources: %w", err)
		}
		return Report{}, err
	}

	var result []Diff
	for _, group := range groups {
		result = append(result, group.Diffs...)
	}

	return Report{From: pairing.From, To: pairing.To, Diffs: append(result, pairing.OrderChange()...)}, nil
}

// kubernetesDocuments returns the non-empty documents of input, the
// Kubernetes resource names it could find for them, each document's
// position in input, and whether every one of those documents has a
// name.
func kubernetesDocuments(input InputFile) (docs []*yamlnode.Node, names []string, positions []int, allNamed bool) {
	for position, entry := range input.Documents {
		if isEmptyDocument(entry) {
			continue
		}
		docs = append(docs, entry)
		positions = append(positions, position)
		if len(entry.Content) == 0 {
			continue
		}
		if name, err := k8sItem.Name(entry.Content[0]); err == nil {
			names = append(names, name)
		}
	}

	return docs, names, positions, len(names) == len(docs)
}

// DocumentPair names two documents that are compared with each other, or
// one document that has no partner. A position is the document's index
// in the Documents of the input given to PairDocuments, and is -1 on the
// side that has no document.
type DocumentPair struct {
	FromPosition int
	ToPosition   int

	from, to *yamlnode.Node
}

// DocumentDiffs holds the diffs found for one DocumentPair. A document
// without a partner has a single diff that adds or removes the whole
// document.
type DocumentDiffs struct {
	Pair  DocumentPair
	Diffs []Diff
}

// DocumentPairing records which documents of two inputs meet. When every
// non-empty document on both sides is a Kubernetes resource, documents
// meet when they share a resource name, From and To keep only those
// documents and carry their names, and Kubernetes is true. Otherwise
// documents meet by position.
type DocumentPairing struct {
	From, To   InputFile
	Kubernetes bool

	pairs, removals, additions []indexedPair
	fromNames, toNames         []string
}

// indexedPair is a matched document pair together with the position of
// its from document in the (possibly filtered) from input.
type indexedPair struct {
	pair DocumentPair
	idx  int
}

// ErrDocumentCount is the refusal for inputs that are compared by position
// and hold different numbers of documents.
var ErrDocumentCount = errors.New("comparing YAMLs with a different number of documents is currently not supported")

// PairDocuments decides which documents of from and to are compared with
// each other. It fails when the inputs are compared by position and hold
// different numbers of documents, or when a Kubernetes resource cannot be
// named.
func PairDocuments(from, to InputFile) (DocumentPairing, error) {
	fromDocs, fromNames, fromPositions, fromNamed := kubernetesDocuments(from)
	toDocs, toNames, toPositions, toNamed := kubernetesDocuments(to)
	if fromNamed && toNamed {
		from.Documents, from.Names = fromDocs, fromNames
		to.Documents, to.Names = toDocs, toNames

		pairing := DocumentPairing{From: from, To: to, Kubernetes: true}
		if err := pairing.matchByName(fromPositions, toPositions); err != nil {
			return DocumentPairing{}, fmt.Errorf("comparing Kubernetes resources: %w", err)
		}
		return pairing, nil
	}

	if len(from.Documents) != len(to.Documents) {
		return DocumentPairing{}, ErrDocumentCount
	}

	pairing := DocumentPairing{From: from, To: to}
	for idx := range from.Documents {
		pairing.pairs = append(pairing.pairs, indexedPair{
			pair: DocumentPair{FromPosition: idx, ToPosition: idx, from: from.Documents[idx], to: to.Documents[idx]},
			idx:  idx,
		})
	}

	return pairing, nil
}

// Compare compares every pair, in document order, and then reports each
// document only From has as a removal and each document only To has as an
// addition. A change in the order of Kubernetes resources is left to
// OrderChange.
func (p *DocumentPairing) Compare() ([]DocumentDiffs, error) {
	result := make([]DocumentDiffs, 0, len(p.pairs)+len(p.removals)+len(p.additions))

	for _, entry := range p.pairs {
		diffs, err := objects(Path{Root: &p.From, DocumentIdx: entry.idx}, entry.pair.from, entry.pair.to)
		if err != nil {
			return nil, err
		}
		result = append(result, DocumentDiffs{Pair: entry.pair, Diffs: diffs})
	}

	for _, entry := range p.removals {
		result = append(result, DocumentDiffs{
			Pair:  entry.pair,
			Diffs: documentChanges(&p.From, REMOVAL, []indexedDocument{{node: entry.pair.from, idx: entry.idx}}),
		})
	}

	for _, entry := range p.additions {
		result = append(result, DocumentDiffs{
			Pair:  entry.pair,
			Diffs: documentChanges(&p.To, ADDITION, []indexedDocument{{node: entry.pair.to, idx: entry.idx}}),
		})
	}

	return result, nil
}

// OrderChange returns one order change without a path when the inputs are
// Kubernetes resources and their names appear in a different order.
func (p *DocumentPairing) OrderChange() []Diff {
	return documentOrderChange(p.fromNames, p.toNames)
}

// modification returns one MODIFICATION diff at path.
func modification(path Path, from, to *yamlnode.Node) []Diff {
	return []Diff{{
		Path:    &path,
		Details: []Detail{{Kind: MODIFICATION, From: from, To: to}},
	}}
}

func objects(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	switch {
	case from == nil && to == nil:
		return []Diff{}, nil

	case from == nil || to == nil:
		return modification(path, from, to), nil

	case from.Kind != to.Kind || from.Tag != to.Tag:
		return modification(path, from, to), nil
	}

	return nonNilSameKindNodes(path, from, to)
}

func nonNilSameKindNodes(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	switch from.Kind {
	case yamlnode.DocumentNode:
		return objects(path, documentRoot(from), documentRoot(to))

	case yamlnode.MappingNode:
		return mappingNodes(path, from, to)

	case yamlnode.SequenceNode:
		return sequenceNodes(path, from, to)

	case yamlnode.ScalarNode:
		return scalarNodes(path, from, to)

	case yamlnode.AliasNode:
		return objects(path, from.Alias, to.Alias)

	default:
		return nil, fmt.Errorf("failed to compare objects due to unsupported kind %v", from.Kind)
	}
}

// documentRoot returns the one child of a document node, or nil when the
// document has none.
func documentRoot(document *yamlnode.Node) *yamlnode.Node {
	if len(document.Content) == 0 {
		return nil
	}

	return document.Content[0]
}

func scalarNodes(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	switch from.Tag {
	case yamlnode.TagStr:
		return nodeValues(path, from, to), nil

	case yamlnode.TagNull:
		// Every way of writing a null value is the same null.
		return nil, nil

	case yamlnode.TagBool:
		return boolValues(path, from, to)

	default:
		if from.Value != to.Value {
			return modification(path, from, to), nil
		}
		return nil, nil
	}
}

// indexedDocument is a document's root node and its position in its
// input.
type indexedDocument struct {
	node *yamlnode.Node
	idx  int
}

// documentLookup maps each document's Kubernetes resource name to its
// root and position, the last document winning a repeated name, and
// returns the names in document order.
func documentLookup(input InputFile) (map[string]indexedDocument, []string, error) {
	lookup := make(map[string]indexedDocument)
	var names []string

	for i, document := range input.Documents {
		node := documentRoot(document)
		if node == nil {
			return nil, nil, fmt.Errorf("document #%d has no content", i+1)
		}

		name, err := k8sItem.Name(node)
		if err != nil {
			return nil, nil, err
		}

		names = append(names, name)
		lookup[name] = indexedDocument{idx: i, node: node}
	}

	return lookup, names, nil
}

// matchByName pairs Kubernetes resources by name. Matching documents come
// in the order of From's names, then each document only From has becomes
// a removal and each document only To has becomes an addition.
// fromPositions and toPositions give each kept document's position in the
// input it came from.
func (p *DocumentPairing) matchByName(fromPositions, toPositions []int) error {
	fromLookup, fromNames, err := documentLookup(p.From)
	if err != nil {
		return err
	}

	toLookup, toNames, err := documentLookup(p.To)
	if err != nil {
		return err
	}

	p.fromNames, p.toNames = fromNames, toNames

	for _, name := range fromNames {
		fromItem := fromLookup[name]
		toItem, ok := toLookup[name]
		if !ok {
			p.removals = append(p.removals, indexedPair{
				pair: DocumentPair{FromPosition: fromPositions[fromItem.idx], ToPosition: -1, from: fromItem.node},
				idx:  fromItem.idx,
			})
			continue
		}

		p.pairs = append(p.pairs, indexedPair{
			pair: DocumentPair{
				FromPosition: fromPositions[fromItem.idx],
				ToPosition:   toPositions[toItem.idx],
				from:         yamlnode.FollowAlias(fromItem.node),
				to:           yamlnode.FollowAlias(toItem.node),
			},
			idx: fromItem.idx,
		})
	}

	for _, name := range toNames {
		if _, ok := fromLookup[name]; !ok {
			toItem := toLookup[name]
			p.additions = append(p.additions, indexedPair{
				pair: DocumentPair{FromPosition: -1, ToPosition: toPositions[toItem.idx], to: toItem.node},
				idx:  toItem.idx,
			})
		}
	}

	return nil
}

// documentChanges returns one diff per whole document added to or
// removed from root, each holding a fresh document node that wraps the
// document's root.
func documentChanges(root *InputFile, kind DetailKind, docs []indexedDocument) []Diff {
	result := make([]Diff, 0, len(docs))
	for _, d := range docs {
		document := &yamlnode.Node{Kind: yamlnode.DocumentNode, Content: []*yamlnode.Node{d.node}}
		detail := Detail{Kind: kind}
		if kind == ADDITION {
			detail.To = document
		} else {
			detail.From = document
		}

		result = append(result, Diff{
			Path:    &Path{Root: root, DocumentIdx: d.idx},
			Details: []Detail{detail},
		})
	}

	return result
}

// documentOrderChange returns one order change without a path when both
// name lists have the same length and differ at some position.
func documentOrderChange(fromNames, toNames []string) []Diff {
	if len(fromNames) != len(toNames) {
		return nil
	}

	for i := range fromNames {
		if fromNames[i] != toNames[i] {
			return []Diff{{
				Details: []Detail{{
					Kind: ORDERCHANGE,
					From: AsSequenceNode(fromNames...),
					To:   AsSequenceNode(toNames...),
				}},
			}}
		}
	}

	return nil
}

// mappingNodes compares two mappings key by key. Keys only from has are
// gathered into one removal fragment and keys only to has into one
// addition fragment, which take the parent's kind and tag and reuse the
// original key and value nodes. That diff comes before the diffs of the
// shared keys.
func mappingNodes(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	result := make([]Diff, 0)
	removals := []*yamlnode.Node{}
	additions := []*yamlnode.Node{}

	fromTo, toPaired := pairKeys(from, to)
	for i := 0; i+1 < len(from.Content); i += 2 {
		key, fromItem := from.Content[i], from.Content[i+1]
		toItem, ok := fromTo[i/2], fromTo[i/2] != nil
		if !ok {
			removals = append(removals, key, fromItem)
			continue
		}

		diffs, err := objects(
			NewPathWithNamedElement(path, key.Value),
			yamlnode.FollowAlias(fromItem),
			yamlnode.FollowAlias(toItem),
		)
		if err != nil {
			return nil, err
		}
		result = append(result, diffs...)
	}

	for i := 0; i+1 < len(to.Content); i += 2 {
		key, toItem := to.Content[i], to.Content[i+1]
		if !toPaired[i/2] {
			additions = append(additions, key, toItem)
		}
	}

	diff := Diff{Path: &path, Details: []Detail{}}

	if len(removals) > 0 {
		diff.Details = append(diff.Details, Detail{
			Kind: REMOVAL,
			From: &yamlnode.Node{Kind: from.Kind, Tag: from.Tag, Content: removals},
		})
	}

	if len(additions) > 0 {
		diff.Details = append(diff.Details, Detail{
			Kind: ADDITION,
			To:   &yamlnode.Node{Kind: to.Kind, Tag: to.Tag, Content: additions},
		})
	}

	if len(diff.Details) > 0 {
		result = append([]Diff{diff}, result...)
	}

	return result, nil
}

// sequenceNodes compares two lists, naming their entries by a known
// identifier field, then by a guessed one, then by Kubernetes resource
// name, and comparing them as simple lists when none of those applies.
func sequenceNodes(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	if len(from.Content) == 0 && len(to.Content) == 0 {
		return []Diff{}, nil
	}

	if identifier, ok := getIdentifierFromNamedLists(from, to); ok {
		return namedEntryLists(path, identifier, from, to)
	}

	if identifier := getNonStandardIdentifierFromNamedLists(from, to); identifier != nil {
		return namedEntryLists(path, identifier, from, to)
	}

	if getIdentifierFromKubernetesEntityList(from, to) {
		return namedEntryLists(path, k8sItem, from, to)
	}

	return simpleLists(path, from, to)
}

// hashedNodes is a list of nodes, their nodeHash values, and their
// positions in the list they came from.
type hashedNodes struct {
	nodes   []*yamlnode.Node
	hashes  []uint64
	indexes []int
}

func (h *hashedNodes) add(node *yamlnode.Node, hash uint64, index int) {
	h.nodes = append(h.nodes, node)
	h.hashes = append(h.hashes, hash)
	h.indexes = append(h.indexes, index)
}

// entries returns the nodes with their positions, without the hashes.
func (h *hashedNodes) entries() listEntries {
	return listEntries{nodes: h.nodes, indexes: h.indexes}
}

// listEntries is a group of list entries that one side added or removed,
// together with the position each has in its own list.
type listEntries struct {
	nodes   []*yamlnode.Node
	indexes []int
}

func (l *listEntries) add(node *yamlnode.Node, index int) {
	l.nodes = append(l.nodes, node)
	l.indexes = append(l.indexes, index)
}

// contains reports whether a node with the given hash is in the list,
// which is dyff's hasEntry.
func (h *hashedNodes) contains(hash uint64) bool {
	for _, entry := range h.hashes {
		if entry == hash {
			return true
		}
	}

	return false
}

// hashList returns the nodeHash of every entry in the sequence and maps
// each hash to the positions of the entries that have it.
func hashList(sequence *yamlnode.Node) ([]uint64, map[uint64][]int, error) {
	hashes := make([]uint64, len(sequence.Content))
	lookup := make(map[uint64][]int, len(sequence.Content))
	for idx, entry := range sequence.Content {
		hash, err := nodeHash(entry)
		if err != nil {
			return nil, nil, err
		}
		hashes[idx] = hash
		lookup[hash] = append(lookup[hash], idx)
	}

	return hashes, lookup, nil
}

// simpleLists compares two lists entry by entry through their hashes.
// Two one-entry lists compare their entries directly. Otherwise an entry
// missing on the other side is a removal or an addition, an entry that
// appears more often on one side adds the surplus copies once, and the
// entries both sides share are checked for an order change.
func simpleLists(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	if len(from.Content) == 1 && len(to.Content) == 1 {
		return objects(
			NewPathWithIndexedListElement(path, 0),
			yamlnode.FollowAlias(from.Content[0]),
			yamlnode.FollowAlias(to.Content[0]),
		)
	}

	fromHashes, fromLookup, err := hashList(from)
	if err != nil {
		return nil, err
	}

	toHashes, toLookup, err := hashList(to)
	if err != nil {
		return nil, err
	}

	removals, fromCommon := listSurplus(from.Content, fromHashes, fromLookup, toLookup)
	additions, toCommon := listSurplus(to.Content, toHashes, toLookup, fromLookup)

	orderChanges := findOrderChangesInSimpleList(fromCommon, toCommon)

	return packChangesAndAddToResult([]Diff{}, path, orderChanges, additions.entries(), removals.entries()), nil
}

// listSurplus walks the entries of one list. An entry whose hash the other
// list lacks is surplus, and an entry that appears more often in this
// list than in the other adds the difference in copies, once per distinct
// entry. Each surplus copy records the position of an unmatched entry
// with its hash, so the k-th copy takes the k-th position after the ones
// the other list matches. It also returns the entries both lists share,
// in order.
func listSurplus(entries []*yamlnode.Node, hashes []uint64, own, other map[uint64][]int) (surplus, common hashedNodes) {
	for idx, entry := range entries {
		hash := hashes[idx]
		_, ok := other[hash]
		if ok {
			common.add(entry, hash, idx)
		}

		switch {
		case !ok:
			surplus.add(entry, hash, idx)

		case len(own[hash]) > len(other[hash]):
			if !surplus.contains(hash) {
				for _, unmatched := range own[hash][len(other[hash]):] {
					surplus.add(entry, hash, unmatched)
				}
			}
		}
	}

	return surplus, common
}

// namedEntryLists compares two lists whose entries the identifier names.
// Entries with the same name are compared, an entry only one side has is
// a removal or an addition, and the shared names are checked for an
// order change.
func namedEntryLists(path Path, identifier listItemIdentifier, from, to *yamlnode.Node) ([]Diff, error) {
	var removals, additions listEntries
	var result []Diff

	fromNames := make([]string, 0, len(from.Content))
	toNames := make([]string, 0, len(from.Content))

	for fromIdx, fromEntry := range from.Content {
		name, err := identifier.Name(fromEntry)
		if err != nil {
			return nil, fmt.Errorf("failed to identify name: %w", err)
		}

		toEntry, err := identifier.FindNodeByName(to, name)
		if err != nil {
			removals.add(fromEntry, fromIdx)
			continue
		}

		diffs, err := objects(
			NewPathWithNamedListElement(path, identifier, name),
			yamlnode.FollowAlias(fromEntry),
			yamlnode.FollowAlias(toEntry),
		)
		if err != nil {
			return nil, err
		}
		result = append(result, diffs...)
		fromNames = append(fromNames, name)
	}

	for toIdx, toEntry := range to.Content {
		name, err := identifier.Name(toEntry)
		if err != nil {
			return nil, fmt.Errorf("failed to identify name: %w", err)
		}

		if _, err := identifier.FindNodeByName(from, name); err != nil {
			additions.add(toEntry, toIdx)
			continue
		}
		toNames = append(toNames, name)
	}

	orderChanges := findOrderChangesInNamedEntryLists(fromNames, toNames)

	return packChangesAndAddToResult(result, path, orderChanges, additions, removals), nil
}

// nodeValues reports a string change.
func nodeValues(path Path, from, to *yamlnode.Node) []Diff {
	if from.Value != to.Value {
		return modification(path, from, to)
	}

	return nil
}

// boolValues reports a change between two booleans, comparing what they
// mean rather than how they are written.
func boolValues(path Path, from, to *yamlnode.Node) ([]Diff, error) {
	boolFrom, err := toBool(from.Value)
	if err != nil {
		return nil, err
	}

	boolTo, err := toBool(to.Value)
	if err != nil {
		return nil, err
	}

	if boolFrom != boolTo {
		return modification(path, from, to), nil
	}

	return []Diff{}, nil
}

// trueValues and falseValues are the YAML 1.1 boolean literals listed at
// https://yaml.org/type/bool.html. They are the set dyff accepted, and
// toBool reaches them only for nodes tagged !!bool. That differs on purpose
// from the YAML 1.2 core forms in internal/yamlnode, which decide what is
// tagged !!bool in the first place, and from the yes, no, on, and off table
// in pkg/graft that serves merge, so the three tables must not be merged.
var (
	trueValues  = [...]string{"y", "Y", "yes", "Yes", "YES", "true", "True", "TRUE", "on", "On", "ON"}
	falseValues = [...]string{"n", "N", "no", "No", "NO", "false", "False", "FALSE", "off", "Off", "OFF"}
)

func toBool(input string) (bool, error) {
	for _, t := range trueValues {
		if input == t {
			return true, nil
		}
	}

	for _, f := range falseValues {
		if input == f {
			return false, nil
		}
	}

	return false, fmt.Errorf("not a valid boolean value: '%s'", input)
}

// findOrderChangesInSimpleList reports an order change when both lists of
// shared entries have the same length and differ at some position.
func findOrderChangesInSimpleList(fromCommon, toCommon hashedNodes) []Detail {
	if len(fromCommon.hashes) == len(toCommon.hashes) {
		for idx := range fromCommon.hashes {
			if fromCommon.hashes[idx] != toCommon.hashes[idx] {
				return []Detail{{
					Kind: ORDERCHANGE,
					From: &yamlnode.Node{Kind: yamlnode.SequenceNode, Content: fromCommon.nodes},
					To:   &yamlnode.Node{Kind: yamlnode.SequenceNode, Content: toCommon.nodes},
				}}
			}
		}
	}

	return []Detail{}
}

// AsSequenceNode returns a sequence node that holds each string as a
// string scalar. The sequence itself has no tag.
func AsSequenceNode(list ...string) *yamlnode.Node {
	result := make([]*yamlnode.Node, len(list))
	for i, entry := range list {
		result[i] = &yamlnode.Node{
			Kind:  yamlnode.ScalarNode,
			Tag:   yamlnode.TagStr,
			Value: entry,
		}
	}

	return &yamlnode.Node{
		Kind:    yamlnode.SequenceNode,
		Content: result,
	}
}

// findOrderChangesInNamedEntryLists reports an order change when some
// shared name sits at a different position in to than in from.
func findOrderChangesInNamedEntryLists(fromNames, toNames []string) []Detail {
	orderchanges := make([]Detail, 0)

	idxLookupMap := make(map[string]int, len(toNames))
	for idx, name := range toNames {
		idxLookupMap[name] = idx
	}

	for idx, name := range fromNames {
		if idxLookupMap[name] != idx {
			orderchanges = append(orderchanges, Detail{
				Kind: ORDERCHANGE,
				From: AsSequenceNode(fromNames...),
				To:   AsSequenceNode(toNames...),
			})
			break
		}
	}

	return orderchanges
}

// packChangesAndAddToResult puts one diff at path in front of list. It
// holds the order changes, then one removal fragment, then one addition
// fragment, and it is left out when there is none of them. Each fragment
// detail carries the list positions of its entries.
func packChangesAndAddToResult(list []Diff, path Path, orderchanges []Detail, additions, removals listEntries) []Diff {
	diff := Diff{Path: &path, Details: []Detail{}}

	if len(orderchanges) > 0 {
		diff.Details = append(diff.Details, orderchanges...)
	}

	if len(removals.nodes) > 0 {
		diff.Details = append(diff.Details, Detail{
			Kind:    REMOVAL,
			From:    &yamlnode.Node{Kind: yamlnode.SequenceNode, Tag: yamlnode.TagSeq, Content: removals.nodes},
			Indexes: removals.indexes,
		})
	}

	if len(additions.nodes) > 0 {
		diff.Details = append(diff.Details, Detail{
			Kind:    ADDITION,
			To:      &yamlnode.Node{Kind: yamlnode.SequenceNode, Tag: yamlnode.TagSeq, Content: additions.nodes},
			Indexes: additions.indexes,
		})
	}

	if len(diff.Details) > 0 {
		list = append([]Diff{diff}, list...)
	}

	return list
}

// keyID is a mapping key's text and YAML type.
type keyID struct{ tag, text string }

// pairKeys pairs the keys of the mappings from and to. It returns, for
// each key of from, the value of the key it pairs with in to (nil for
// none), and for each key of to, whether a key of from pairs with it.
// Aliases are followed on keys and values.
//
// A first pass pairs keys that have the same text and the same YAML
// type, taking the first such key of to. A second pass looks at the keys
// left over on each side and pairs a key by text alone when exactly one
// leftover key on each side has that text, so the string "1" of a JSON
// file pairs with the int 1 of a YAML file. Any other key is unpaired.
func pairKeys(from, to *yamlnode.Node) ([]*yamlnode.Node, []bool) {
	first := make(map[keyID]int)
	for j := 0; j+1 < len(to.Content); j += 2 {
		id := idOfKey(to.Content[j])
		if _, seen := first[id]; !seen {
			first[id] = j
		}
	}
	paired := make([]bool, len(to.Content)/2)
	values := make([]*yamlnode.Node, len(from.Content)/2)
	fromIDs := make(map[keyID]bool)
	var leftFrom []int
	for i := 0; i+1 < len(from.Content); i += 2 {
		id := idOfKey(from.Content[i])
		if j, ok := first[id]; ok {
			values[i/2] = yamlnode.FollowAlias(to.Content[j+1])
			paired[j/2] = true
			fromIDs[id] = true
			continue
		}
		leftFrom = append(leftFrom, i)
	}
	// A repeat of a paired key in to is paired too, and is not an addition.
	for j := 0; j+1 < len(to.Content); j += 2 {
		if fromIDs[idOfKey(to.Content[j])] {
			paired[j/2] = true
		}
	}
	if len(leftFrom) == 0 {
		return values, paired
	}
	fromText := make(map[string][]int)
	for _, i := range leftFrom {
		text := yamlnode.FollowAlias(from.Content[i]).Value
		fromText[text] = append(fromText[text], i)
	}
	toText := make(map[string][]int)
	for j := 0; j+1 < len(to.Content); j += 2 {
		if !paired[j/2] {
			text := yamlnode.FollowAlias(to.Content[j]).Value
			toText[text] = append(toText[text], j)
		}
	}
	for text, is := range fromText {
		js := toText[text]
		if len(is) == 1 && len(js) == 1 {
			values[is[0]/2] = yamlnode.FollowAlias(to.Content[js[0]+1])
			paired[js[0]/2] = true
		}
	}
	return values, paired
}

func idOfKey(k *yamlnode.Node) keyID {
	k = yamlnode.FollowAlias(k)
	return keyID{tag: keyTag(k), text: k.Value}
}

// keyTag returns the YAML type of a mapping key, so that matching can
// tell a key from the string spelled the same way. A key with no tag is
// a string.
func keyTag(k *yamlnode.Node) string {
	if tag := yamlnode.ShortTag(k.Tag); tag != "" && tag != "!" {
		return tag
	}
	return yamlnode.TagStr
}

// isCandidate reports whether node is a scalar that names one of the
// identifier candidates.
func isCandidate(node *yamlnode.Node) bool {
	if node.Kind != yamlnode.ScalarNode {
		return false
	}

	for _, entry := range listItemIdentifierCandidates {
		if node.Value == entry {
			return true
		}
	}

	return false
}

// candidateValues maps each candidate field found in the mapping entries
// of the sequence to the set of distinct values it has.
func candidateValues(sequence *yamlnode.Node) map[string]map[string]struct{} {
	result := map[string]map[string]struct{}{}
	for _, entry := range sequence.Content {
		if entry.Kind != yamlnode.MappingNode {
			continue
		}

		for i := 0; i+1 < len(entry.Content); i += 2 {
			k, v := yamlnode.FollowAlias(entry.Content[i]), yamlnode.FollowAlias(entry.Content[i+1])
			if !isCandidate(k) {
				continue
			}

			if _, found := result[k.Value]; !found {
				result[k.Value] = map[string]struct{}{}
			}
			result[k.Value][v.Value] = struct{}{}
		}
	}

	return result
}

// getIdentifierFromNamedLists returns the first candidate field that has
// a distinct value in every entry of both lists.
func getIdentifierFromNamedLists(listA, listB *yamlnode.Node) (listItemIdentifier, bool) {
	counterA := candidateValues(listA)
	counterB := candidateValues(listB)

	for _, identifier := range listItemIdentifierCandidates {
		if countA, okA := counterA[identifier]; okA && len(countA) == len(listA.Content) {
			if countB, okB := counterB[identifier]; okB && len(countB) == len(listB.Content) {
				return &singleField{identifier}, true
			}
		}
	}

	return nil, false
}

// stringFieldCounts maps each string key whose value is a string scalar
// to the number of distinct values it has across the entries of the
// list. A list with an entry that is not a mapping has no such keys.
func stringFieldCounts(list *yamlnode.Node) map[string]int {
	tmp := map[string]map[string]struct{}{}
	for _, entry := range list.Content {
		if entry.Kind != yamlnode.MappingNode {
			return map[string]int{}
		}

		for i := 0; i+1 < len(entry.Content); i += 2 {
			k, v := yamlnode.FollowAlias(entry.Content[i]), yamlnode.FollowAlias(entry.Content[i+1])
			if k.Kind == yamlnode.ScalarNode && k.Tag == yamlnode.TagStr &&
				v.Kind == yamlnode.ScalarNode && v.Tag == yamlnode.TagStr {
				if _, ok := tmp[k.Value]; !ok {
					tmp[k.Value] = map[string]struct{}{}
				}
				tmp[k.Value][v.Value] = struct{}{}
			}
		}
	}

	result := map[string]int{}
	for key, value := range tmp {
		result[key] = len(value)
	}

	return result
}

// getNonStandardIdentifierFromNamedLists guesses an identifier field: the
// first key, in sorted order, that has a distinct string value in every
// entry of both lists and more distinct values than the guess threshold.
func getNonStandardIdentifierFromNamedLists(listA, listB *yamlnode.Node) listItemIdentifier {
	listALength := len(listA.Content)
	listBLength := len(listB.Content)
	counterA := stringFieldCounts(listA)
	counterB := stringFieldCounts(listB)

	keysA := make([]string, 0, len(counterA))
	for keyA := range counterA {
		keysA = append(keysA, keyA)
	}
	sort.Strings(keysA)

	for _, keyA := range keysA {
		countA := counterA[keyA]
		if countB, ok := counterB[keyA]; ok {
			if countA == listALength && countB == listBLength && countA > nonStandardIdentifierGuessCountThreshold {
				return &singleField{keyA}
			}
		}
	}

	return nil
}

// getIdentifierFromKubernetesEntityList reports whether every entry of
// both lists is a mapping with a Kubernetes resource name.
func getIdentifierFromKubernetesEntityList(listA, listB *yamlnode.Node) bool {
	return allHaveKubernetesName(listA) && allHaveKubernetesName(listB)
}

func allHaveKubernetesName(sequence *yamlnode.Node) bool {
	var numWithMetadata int
	for _, entry := range sequence.Content {
		if entry.Kind != yamlnode.MappingNode {
			continue
		}
		if _, err := k8sItem.Name(entry); err == nil {
			numWithMetadata++
		}
	}

	return numWithMetadata == len(sequence.Content)
}

// isEmptyDocument reports whether node is a document that holds only a
// null scalar.
func isEmptyDocument(node *yamlnode.Node) bool {
	if node.Kind != yamlnode.DocumentNode || len(node.Content) != 1 {
		return false
	}

	return node.Content[0].Kind == yamlnode.ScalarNode && node.Content[0].Tag == yamlnode.TagNull
}
