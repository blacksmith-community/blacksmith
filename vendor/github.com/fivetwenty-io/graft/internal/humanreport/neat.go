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
// Ported from github.com/gonvenience/neat v1.3.20 (output.go colorize,
// colorizef, prefixAdd; output_yaml.go neatYAMLofNode, createAnchorDefinition,
// needsQuotes) and from github.com/homeport/dyff v1.12.0 (pkg/dyff/colors.go
// YAMLInRedishColors and YAMLInGreenishColors, pkg/dyff/output_human.go
// yamlString), and modified for graft: it renders yamlnode nodes only, the
// palettes hold RGB values and the terminal mode is a parameter, indent lines
// and the document start marker are never used because dyff's report turns
// both off, and the Go-value paths that dyff never reaches are gone. A
// terminal styling error comes back as the error instead of a panic.

package humanreport

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fivetwenty-io/graft/internal/termstyle"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// The color names neat's schema uses. A palette that lacks a name leaves
// that text uncolored, which is how the comment, anchor, and binary colors
// stay plain in both of dyff's palettes.
const (
	colorAnchor        = "anchorColor"
	colorBinary        = "binaryColor"
	colorBool          = "boolColor"
	colorComment       = "commentColor"
	colorDash          = "dashColor"
	colorFloat         = "floatColor"
	colorIndentLine    = "indentLineColor"
	colorInt           = "intColor"
	colorKey           = "keyColor"
	colorMultiLineText = "multiLineTextColor"
	colorNull          = "nullColor"
	colorScalarDefault = "scalarDefaultColor"
	emptyStructures    = "emptyStructures"
)

const (
	emptyList   = "[]"
	emptyObject = "{}"
)

var numberRegEx = regexp.MustCompile(`^(-|\+)?[0-9.e+]+$`)

// palette maps neat's color names to foreground colors. A nil palette means
// no color schema.
type palette map[string]termstyle.RGB

// reddish is dyff's palette for removed blocks. The RGB values are the
// bunt colors FireBrick, LightCoral, DarkSalmon, Salmon, and LightSalmon,
// and the indent color {0.2, 0, 0} rounded the way go-colorful's RGB255 does.
var reddish = palette{
	colorKey:           {R: 178, G: 34, B: 34},
	colorIndentLine:    {R: 51, G: 0, B: 0},
	colorScalarDefault: {R: 240, G: 128, B: 128},
	colorBool:          {R: 240, G: 128, B: 128},
	colorFloat:         {R: 240, G: 128, B: 128},
	colorInt:           {R: 240, G: 128, B: 128},
	colorMultiLineText: {R: 233, G: 150, B: 122},
	colorNull:          {R: 250, G: 128, B: 114},
	emptyStructures:    {R: 255, G: 160, B: 122},
	colorDash:          {R: 178, G: 34, B: 34},
}

// greenish is dyff's palette for added blocks. The RGB values are the bunt
// colors Green, LimeGreen, OliveDrab, Olive, and DarkOliveGreen, and the
// indent color {0, 0.2, 0} rounded the way go-colorful's RGB255 does.
var greenish = palette{
	colorKey:           {R: 0, G: 128, B: 0},
	colorIndentLine:    {R: 0, G: 51, B: 0},
	colorScalarDefault: {R: 50, G: 205, B: 50},
	colorBool:          {R: 50, G: 205, B: 50},
	colorFloat:         {R: 50, G: 205, B: 50},
	colorInt:           {R: 50, G: 205, B: 50},
	colorMultiLineText: {R: 107, G: 142, B: 35},
	colorNull:          {R: 128, G: 128, B: 0},
	emptyStructures:    {R: 85, G: 107, B: 47},
	colorDash:          {R: 0, G: 128, B: 0},
}

// neatYAML renders n as neat does for dyff, with bold keys and the given
// palette. A terminal styling error comes back as the error.
func neatYAML(n *yamlnode.Node, p palette, m termstyle.Mode) (string, error) {
	if n == nil {
		return "", nil
	}

	w := &neatWriter{pal: p, mode: m}
	w.node("", false, n)
	if w.err != nil {
		return "", w.err
	}

	return w.out.String(), nil
}

// yamlString renders a changed value the way dyff's yamlString does, which
// is neat without a palette. It returns "<nil>" for nil and !!null.
func yamlString(n *yamlnode.Node, m termstyle.Mode) (string, error) {
	if n == nil || n.Tag == yamlnode.TagNull {
		return "<nil>", nil
	}

	return neatYAML(n, nil, m)
}

// neatWriter holds the output and the first styling error of one render.
type neatWriter struct {
	out  strings.Builder
	pal  palette
	mode termstyle.Mode
	err  error
}

// colorize returns text in the named color, or unchanged when the palette
// does not name it.
func (w *neatWriter) colorize(colorName, text string) string {
	value, ok := w.pal[colorName]
	if !ok {
		return text
	}

	return w.style(text, termstyle.Foreground(value))
}

// colorizef formats and then colorizes.
func (w *neatWriter) colorizef(colorName, format string, a ...any) string {
	return w.colorize(colorName, fmt.Sprintf(format, a...))
}

// style applies the options to text and keeps the first error.
func (w *neatWriter) style(text string, opts ...termstyle.Option) string {
	styled, err := w.mode.Style(text, opts...)
	if err != nil {
		if w.err == nil {
			w.err = err
		}

		return ""
	}

	return styled
}

// prefixAdd is the indentation of one level.
func (w *neatWriter) prefixAdd() string {
	return w.colorize(colorIndentLine, "  ")
}

// anchorDefinition is the " &anchor" text of a node, or nothing.
func (w *neatWriter) anchorDefinition(n *yamlnode.Node) string {
	if n.Anchor != "" {
		return " " + w.colorizef(colorAnchor, "&%s", n.Anchor)
	}

	return ""
}

// node renders one node, dispatching on its kind.
func (w *neatWriter) node(prefix string, skipIndentOnFirstLine bool, n *yamlnode.Node) {
	if w.err != nil {
		return
	}

	switch n.Kind {
	case yamlnode.DocumentNode:
		w.document(prefix, n)
	case yamlnode.SequenceNode:
		w.sequence(prefix, skipIndentOnFirstLine, n)
	case yamlnode.MappingNode:
		w.mapping(prefix, skipIndentOnFirstLine, n)
	case yamlnode.ScalarNode:
		w.scalar(prefix, n)
	case yamlnode.AliasNode:
		if target := yamlnode.FollowAlias(n); target != n {
			w.node(prefix, skipIndentOnFirstLine, target)
		}
	}
}

func (w *neatWriter) document(prefix string, n *yamlnode.Node) {
	for _, content := range n.Content {
		w.node(prefix, false, content)
	}

	if n.FootComment != "" {
		w.out.WriteString(w.colorize(colorComment, n.FootComment) + "\n")
	}
}

func (w *neatWriter) sequence(prefix string, skipIndentOnFirstLine bool, n *yamlnode.Node) {
	for i, entry := range n.Content {
		if i > 0 || !skipIndentOnFirstLine {
			w.out.WriteString(prefix)
		}

		w.out.WriteString(w.colorize(colorDash, "-") + " ")
		w.node(prefix+w.prefixAdd(), true, entry)
	}
}

func (w *neatWriter) mapping(prefix string, skipIndentOnFirstLine bool, n *yamlnode.Node) {
	for i := 0; i < len(n.Content); i += 2 {
		if !skipIndentOnFirstLine || i > 0 {
			w.out.WriteString(prefix)
		}

		key := n.Content[i]
		if key.HeadComment != "" {
			w.out.WriteString(w.colorize(colorComment, key.HeadComment) + "\n")
		}

		w.out.WriteString(w.style(w.colorizef(colorKey, "%s:", key.Value), termstyle.Bold()))
		w.mapValue(prefix, n.Content[i+1])

		if key.FootComment != "" {
			w.out.WriteString(w.colorize(colorComment, key.FootComment) + "\n")
		}

		if w.err != nil {
			return
		}
	}
}

// mapValue renders what follows a mapping key.
func (w *neatWriter) mapValue(prefix string, value *yamlnode.Node) {
	switch value.Kind {
	case yamlnode.MappingNode:
		w.nestedValue(value, emptyObject, prefix+w.prefixAdd())
	case yamlnode.SequenceNode:
		w.nestedValue(value, emptyList, prefix)
	case yamlnode.ScalarNode:
		w.out.WriteString(w.anchorDefinition(value) + " ")
		w.node(prefix+w.prefixAdd(), false, value)
	case yamlnode.AliasNode:
		fmt.Fprintf(&w.out, " %s\n", w.colorizef(colorAnchor, "*%s", value.Value))
	case yamlnode.DocumentNode:
		// A document never sits under a key, and neat prints nothing for one.
	}
}

// nestedValue renders a mapping or sequence value, or its empty marker.
func (w *neatWriter) nestedValue(value *yamlnode.Node, empty, childPrefix string) {
	if len(value.Content) == 0 {
		w.out.WriteString(w.anchorDefinition(value) + " " + w.colorize(emptyStructures, empty) + "\n")
		return
	}

	w.out.WriteString(w.anchorDefinition(value) + "\n")
	w.node(childPrefix, false, value)
}

func (w *neatWriter) scalar(prefix string, n *yamlnode.Node) {
	colorName := colorScalarDefault
	switch n.Tag {
	case yamlnode.TagBinary:
		colorName = colorBinary
	case yamlnode.TagFloat:
		colorName = colorFloat
	case yamlnode.TagInt:
		colorName = colorInt
	case yamlnode.TagBool:
		colorName = colorBool
	case yamlnode.TagNull:
		colorName = colorNull
	}

	lines := strings.Split(n.Value, "\n")
	switch len(lines) {
	case 1:
		if needsQuotes(n) {
			w.out.WriteString(w.colorizef(colorName, "%q", n.Value))
		} else {
			w.out.WriteString(w.colorize(colorName, n.Value))
		}
	default:
		colorName = colorMultiLineText
		w.out.WriteString(w.colorize(colorName, "|") + "\n")
		for i, line := range lines {
			w.out.WriteString(prefix + w.colorize(colorName, line))
			if i != len(lines)-1 {
				w.out.WriteString("\n")
			}
		}
	}

	if n.LineComment != "" {
		w.out.WriteString(" " + w.colorize(colorComment, n.LineComment))
	}

	w.out.WriteString("\n")

	if n.FootComment != "" {
		w.out.WriteString(w.colorize(colorComment, n.FootComment) + "\n")
	}
}

// needsQuotes reports whether neat prints a string scalar in quotes.
func needsQuotes(n *yamlnode.Node) bool {
	if n.Tag != yamlnode.TagStr {
		return false
	}

	for _, chk := range []string{"true", "false", "null", ".nan", ".inf", "-.inf", "+.inf"} {
		if n.Value == chk {
			return true
		}
	}

	if strings.HasPrefix(n.Value, "-") {
		return true
	}

	if numberRegEx.MatchString(n.Value) {
		return true
	}

	return strings.ContainsAny(n.Value, " *&:,")
}
