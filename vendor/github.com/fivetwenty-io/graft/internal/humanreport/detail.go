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
// Ported from github.com/homeport/dyff v1.12.0 pkg/dyff/output_human.go
// (generateHumanDetailOutputAddition, generateHumanDetailOutputRemoval,
// generateHumanDetailOutputModification,
// generateHumanDetailOutputOrderchange, humanReadableType) and modified for
// graft: the settings are spruce's (no indent, no indent lines, no
// multiline prefixes), additions and removals share one renderer, the
// modification and order change renderers split along their branches, and
// the inputs that make dyff panic (a missing node, a tag too short to name
// a type, an unknown node kind) come back as a *FatalError.

package humanreport

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/fivetwenty-io/graft/internal/termstyle"
	"github.com/fivetwenty-io/graft/internal/yamldiff"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// missingNode is the *FatalError for a detail that lacks a node dyff
// dereferences.
func missingNode(kind yamldiff.DetailKind, side string) error {
	return &FatalError{Err: fmt.Errorf("%c detail has no %s node", kind, side)}
}

func (r *reporter) additionOutput(detail yamldiff.Detail) (string, error) {
	if detail.To == nil {
		return "", missingNode(detail.Kind, "to")
	}

	return r.blockOutput(yamldiff.ADDITION, "added", detail.To, greenish)
}

func (r *reporter) removalOutput(detail yamldiff.Detail) (string, error) {
	if detail.From == nil {
		return "", missingNode(detail.Kind, "from")
	}

	return r.blockOutput(yamldiff.REMOVAL, "removed", detail.From, reddish)
}

// blockOutput renders an added or removed node: a header that counts its
// documents, list entries, or map entries, and the node restructured and
// rendered as YAML in the palette.
func (r *reporter) blockOutput(kind yamldiff.DetailKind, verb string, node *yamlnode.Node, p palette) (string, error) {
	var output bytes.Buffer

	switch node.Kind {
	case yamlnode.DocumentNode:
		_, _ = output.WriteString(r.yellowf("%c %s %s:\n", kind, plural(len(node.Content), "document"), verb))

	case yamlnode.SequenceNode:
		_, _ = output.WriteString(r.yellowf("%c %s %s:\n", kind, plural(len(node.Content), "list entry", "list entries"), verb))

	case yamlnode.MappingNode:
		_, _ = output.WriteString(r.yellowf("%c %s %s:\n", kind, plural(len(node.Content)/2, "map entry", "map entries"), verb))

	default:
		// Scalars and aliases get no header.
	}

	restructureObject(node)
	yamlOutput, err := neatYAML(node, p, r.mode)
	if err != nil {
		return "", fatalIfStyling(err)
	}

	writeTextBlocks(&output, r.indent, r.width, yamlOutput)

	return output.String(), r.err
}

func (r *reporter) modificationOutput(detail yamldiff.Detail) (string, error) {
	if detail.From == nil {
		return "", missingNode(detail.Kind, "from")
	}

	if detail.To == nil {
		return "", missingNode(detail.Kind, "to")
	}

	fromType, err := humanReadableType(detail.From)
	if err != nil {
		return "", err
	}

	toType, err := humanReadableType(detail.To)
	if err != nil {
		return "", err
	}

	var output bytes.Buffer
	switch {
	case fromType == "string" && toType == "string":
		// delegate to special string output
		r.writeStringDiff(&output, detail.From.Value, detail.To.Value)

	case fromType == "binary" && toType == "binary":
		if err := r.writeBinaryChange(&output, detail); err != nil {
			return "", err
		}

	default:
		if err := r.writeTypeOrValueChange(&output, detail, fromType, toType); err != nil {
			return "", err
		}
	}

	return output.String(), r.err
}

// writeBinaryChange writes hex dumps of both decoded binary values.
func (r *reporter) writeBinaryChange(output *bytes.Buffer, detail yamldiff.Detail) error {
	from, err := base64.StdEncoding.DecodeString(detail.From.Value)
	if err != nil {
		return err
	}

	to, err := base64.StdEncoding.DecodeString(detail.To.Value)
	if err != nil {
		return err
	}

	_, _ = output.WriteString(r.yellowf("%c content change\n", yamldiff.MODIFICATION))
	writeTextBlocks(output, 0, r.width,
		r.red(createStringWithPrefix("- ", hex.Dump(from), r.indent)),
		r.green(createStringWithPrefix("+ ", hex.Dump(to), r.indent)),
	)

	return nil
}

// writeTypeOrValueChange writes a type change or a plain value change
// with both values rendered as YAML.
func (r *reporter) writeTypeOrValueChange(output *bytes.Buffer, detail yamldiff.Detail, fromType, toType string) error {
	if fromType != toType {
		_, _ = output.WriteString(r.yellowf("%c type change from %s to %s\n",
			yamldiff.MODIFICATION,
			r.italic(fromType),
			r.italic(toType),
		))
	} else {
		_, _ = output.WriteString(r.yellowf("%c value change\n",
			yamldiff.MODIFICATION,
		))
	}

	from, err := yamlString(detail.From, r.mode)
	if err != nil {
		return fatalIfStyling(err)
	}

	to, err := yamlString(detail.To, r.mode)
	if err != nil {
		return fatalIfStyling(err)
	}

	_, _ = output.WriteString(r.red(createStringWithPrefix("- ", strings.TrimRight(from, "\n"), r.indent)))
	_, _ = output.WriteString(r.green(createStringWithPrefix("+ ", strings.TrimRight(to, "\n"), r.indent)))

	return nil
}

func (r *reporter) orderchangeOutput(detail yamldiff.Detail) (string, error) {
	if detail.From == nil {
		return "", missingNode(detail.Kind, "from")
	}

	var output bytes.Buffer
	_, _ = output.WriteString(r.yellowf("%c order changed\n", yamldiff.ORDERCHANGE))

	switch detail.From.Kind {
	case yamlnode.SequenceNode:
		if detail.To == nil {
			return "", missingNode(detail.Kind, "to")
		}

		if err := r.writeOrderchangeLists(&output, detail.From, detail.To); err != nil {
			return "", err
		}

	default:
		// dyff only lists the entries of a sequence.
	}

	return output.String(), r.err
}

// writeOrderchangeLists writes both orders on one line each when the
// longer one fits in half the width, and as a two-column table otherwise.
func (r *reporter) writeOrderchangeLists(output *bytes.Buffer, fromNode, toNode *yamlnode.Node) error {
	from, err := asStringList(fromNode, r.mode)
	if err != nil {
		return err
	}

	to, err := asStringList(toNode, r.mode)
	if err != nil {
		return err
	}

	const singleLineSeparator = ", "

	threshold := r.width / 2
	fromSingleLineLength := stringArrayLen(from) + ((len(from) - 1) * termstyle.PlainLen(singleLineSeparator))
	toSingleLineLength := stringArrayLen(to) + ((len(to) - 1) * termstyle.PlainLen(singleLineSeparator))
	if estimatedLength := max(fromSingleLineLength, toSingleLineLength); estimatedLength < threshold {
		_, _ = output.WriteString(r.redf(strings.Repeat(" ", r.indent)+"- %s\n", strings.Join(from, singleLineSeparator)))
		_, _ = output.WriteString(r.greenf(strings.Repeat(" ", r.indent)+"+ %s\n", strings.Join(to, singleLineSeparator)))
	} else {
		_, _ = output.WriteString(createTableStyleString(" ", 2,
			r.red(strings.Join(from, "\n")),
			r.green(strings.Join(to, "\n"))))
	}

	return nil
}

// asStringList returns the values of a sequence's entries, rendering an
// entry as YAML when its value is empty.
func asStringList(sequenceNode *yamlnode.Node, mode termstyle.Mode) ([]string, error) {
	result := make([]string, len(sequenceNode.Content))
	for i, entry := range sequenceNode.Content {
		result[i] = entry.Value
		if entry.Value == "" {
			s, err := yamlString(entry, mode)
			if err != nil {
				return result, fatalIfStyling(err)
			}

			result[i] = s
		}
	}

	return result, nil
}

// humanReadableType names a node's type the way dyff's report does. Where
// dyff panics, on a nil node, an unknown kind, or a scalar tag shorter
// than two characters, it returns a *FatalError.
func humanReadableType(node *yamlnode.Node) (string, error) {
	if node == nil {
		return "", &FatalError{Err: fmt.Errorf("cannot name the type of a missing node")}
	}

	switch node.Kind {
	case yamlnode.DocumentNode:
		return "document", nil

	case yamlnode.MappingNode:
		return "map", nil

	case yamlnode.SequenceNode:
		return "list", nil

	case yamlnode.ScalarNode:
		switch node.Tag {
		case yamlnode.TagStr:
			return "string", nil

		case yamlnode.TagNull:
			return "<nil>", nil

		default:
			if len(node.Tag) < 2 {
				return "", &FatalError{Err: fmt.Errorf("tag %q is too short to name a type", node.Tag)}
			}

			// use the YAML tag name without the exclamation marks
			return node.Tag[2:], nil
		}

	case yamlnode.AliasNode:
		return humanReadableType(node.Alias)
	}

	return "", &FatalError{Err: fmt.Errorf("unknown and therefore unsupported kind %v", node.Kind)}
}
