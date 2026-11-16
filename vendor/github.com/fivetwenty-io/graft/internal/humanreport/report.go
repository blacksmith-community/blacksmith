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
// (WriteReport, generateHumanDiffOutput, generateHumanDetailOutput,
// styledDotStylePath), pkg/dyff/core.go (pathToString), and
// pkg/dyff/colors.go (the BuntColorizer methods), with the location colors
// of github.com/gonvenience/ytbx v1.5.0 input.go (HumanReadableLocation),
// and modified for graft: the settings are spruce's (no header, no indent,
// dot-style paths, table layout allowed), the terminal mode and width are
// options instead of process-wide state, user text is substituted into a
// styled constant format the way bunt.Sprintf does it, and a styling error
// that makes bunt panic comes back as a *FatalError.

package humanreport

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/fivetwenty-io/graft/internal/termstyle"
	"github.com/fivetwenty-io/graft/internal/yamldiff"
)

// The colors of dyff's BuntColorizer and ytbx's location output, which are
// dyff's hex colors and bunt's named LightGreen, LightSalmon,
// LightSteelBlue, and CornflowerBlue. DimGray is gone, because it only
// colors the multiline context lines that spruce never asks for.
var (
	additionGreen      = termstyle.RGB{R: 0x58, G: 0xBF, B: 0x38}
	modificationYellow = termstyle.RGB{R: 0xC7, G: 0xC4, B: 0x3F}
	removalRed         = termstyle.RGB{R: 0xB9, G: 0x31, B: 0x1B}
	lightGreen         = termstyle.RGB{R: 0x90, G: 0xEE, B: 0x90}
	lightSalmon        = termstyle.RGB{R: 0xFF, G: 0xA0, B: 0x7A}
	lightSteelBlue     = termstyle.RGB{R: 0xB0, G: 0xC4, B: 0xDE}
	cornflowerBlue     = termstyle.RGB{R: 0x64, G: 0x95, B: 0xED}
)

// Options says how Write renders a report.
type Options struct {
	// Color turns styling on.
	Color bool
	// TrueColor selects 24-bit colors when Color is on. Without it, colors
	// fall back to the 16 basic terminal colors.
	TrueColor bool
	// Width is the terminal width that decides between the table and the
	// stacked layout.
	Width int
}

// FatalError is a render failure that makes spruce panic: a malformed
// escape sequence in styled text, a scalar tag too short to name a type,
// or a detail that lacks a node it needs.
type FatalError struct {
	// Path names the diff being rendered when the failure happened.
	Path string
	// Err is the underlying failure.
	Err error
}

// Error returns the message of the underlying failure, led by the diff path
// when one is known.
func (e *FatalError) Error() string {
	if e.Path == "" {
		return e.Err.Error()
	}

	return e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying failure.
func (e *FatalError) Unwrap() error { return e.Err }

// mode returns the terminal mode the options select.
func (o Options) mode() termstyle.Mode {
	return termstyle.Mode{Color: o.Color, TrueColor: o.TrueColor}
}

// Write renders r to w the way dyff v1.12.0's HumanReport does with
// spruce's settings. Output is buffered and flushed on return, so when a
// detail fails to render, everything before that detail, its path
// included, is written and the error comes back.
func Write(w io.Writer, r yamldiff.Report, opts Options) (err error) {
	writer := bufio.NewWriter(w)
	defer func() {
		if flushErr := writer.Flush(); err == nil {
			err = flushErr
		}
	}()

	// Only show the document index if there is more than one document to show.
	showPathRoot := len(r.From.Documents) > 1

	rep := &reporter{mode: opts.mode(), width: opts.Width}
	for _, diff := range r.Diffs {
		if err := rep.writeDiff(writer, diff, showPathRoot); err != nil {
			return err
		}
	}

	// Finish with one last newline so that we do not end next to the prompt.
	_, _ = writer.WriteString("\n")
	return nil
}

// LocationStyler returns a yamldiff.LocationStyler that colors a location
// the way ytbx's HumanReadableLocation does: a file in bold, stdin as bold
// italic "stdin", a URI underlined in CornflowerBlue, and anything else
// unchanged. Like bunt.Sprintf, it styles a constant format and then
// substitutes the location, so the location is never parsed as styled text.
func LocationStyler(opts Options) yamldiff.LocationStyler {
	mode := opts.mode()
	return func(kind yamldiff.LocationKind, location string) string {
		switch kind {
		case yamldiff.LocationStdin:
			return constantStyle(mode, "stdin", termstyle.Bold(), termstyle.Italic())

		case yamldiff.LocationFile:
			return fmt.Sprintf(constantStyle(mode, "%s", termstyle.Bold()), location)

		case yamldiff.LocationURI:
			return fmt.Sprintf(constantStyle(mode, "%s", termstyle.Underline(), termstyle.Foreground(cornflowerBlue)), location)

		default:
			return location
		}
	}
}

// constantStyle styles a constant format string. A constant format holds
// no escape sequence, so styling it cannot fail, and the unstyled format
// is only a formality.
func constantStyle(mode termstyle.Mode, format string, opts ...termstyle.Option) string {
	styled, err := mode.Style(format, opts...)
	if err != nil {
		return format
	}

	return styled
}

// reporter renders one report. It holds the terminal settings and the
// first styling error, which turns the rest of the render into a no-op
// the way bunt's panic ends dyff's.
type reporter struct {
	mode  termstyle.Mode
	width int
	err   error

	// indent is dyff's HumanReport.Indent, which spruce leaves at zero.
	indent int
}

// writeDiff writes one diff: a blank line, its path, and its details laid
// out side by side or stacked.
func (r *reporter) writeDiff(output io.StringWriter, diff yamldiff.Diff, showPathRoot bool) error {
	_, _ = output.WriteString("\n")
	_, _ = output.WriteString(r.pathToString(diff.Path, showPathRoot))
	_, _ = output.WriteString("\n")

	blocks := make([]string, len(diff.Details))
	for i, detail := range diff.Details {
		generatedOutput, err := r.detailOutput(detail)
		if err != nil {
			return withDiffPath(err, diff.Path, showPathRoot)
		}

		blocks[i] = generatedOutput
	}

	// For the use case in which only a path-less diff is supposed to be
	// printed, omit the indent, since there is only one element to show.
	indent := r.indent
	if isRootPath(diff.Path) {
		indent = 0
	}

	writeTextBlocks(output, indent, r.width, blocks...)
	return nil
}

// withDiffPath names the diff path in a *FatalError that does not carry
// one yet, and returns any other error as is. Like the report, it adds the
// description of the document root when showPathRoot is set.
func withDiffPath(err error, path *yamldiff.Path, showPathRoot bool) error {
	var fatal *FatalError
	if errors.As(err, &fatal) && fatal.Path == "" {
		fatal.Path = plainPathLabel(path, showPathRoot)
	}

	return err
}

// plainPathLabel names a path the way the report prints it, but without
// styling. It uses "(file level)" for a nil path and "(root level)" where
// the diff has no path elements, and with showPathRoot it appends the
// document description in parentheses. Text holding a control byte, such
// as an escape from a key or a file name, is quoted so that it cannot
// reach a terminal as a sequence.
func plainPathLabel(path *yamldiff.Path, showPathRoot bool) string {
	if path == nil {
		return "(file level)"
	}

	label := path.ToDotStyle()
	if isRootPath(path) {
		label = "(root level)"
	}

	label = quoteIfControl(label)
	if showPathRoot {
		label += "  (" + quoteIfControl(path.RootDescription()) + ")"
	}

	return label
}

// isRootPath reports whether path names the root of a document, which
// the report labels "(root level)". A nil path is the file as a whole.
func isRootPath(path *yamldiff.Path) bool {
	return path != nil && len(path.PathElements) == 0
}

// quoteIfControl quotes text that holds a control byte and returns any
// other text as is.
func quoteIfControl(text string) string {
	if strings.ContainsFunc(text, unicode.IsControl) {
		return strconv.Quote(text)
	}

	return text
}

// detailOutput dispatches to the renderer for the kind of change.
func (r *reporter) detailOutput(detail yamldiff.Detail) (string, error) {
	switch detail.Kind {
	case yamldiff.ADDITION:
		return r.additionOutput(detail)

	case yamldiff.REMOVAL:
		return r.removalOutput(detail)

	case yamldiff.MODIFICATION:
		return r.modificationOutput(detail)

	case yamldiff.ORDERCHANGE:
		return r.orderchangeOutput(detail)
	}

	return "", fmt.Errorf("unsupported detail type %c", detail.Kind)
}

// pathToString renders a path in dyff's dot style, followed by the root
// description when the from-file holds more than one document.
func (r *reporter) pathToString(path *yamldiff.Path, showPathRoot bool) string {
	result := r.styledDotStylePath(path)

	if path != nil && showPathRoot {
		result += "  " + fmt.Sprintf(constantStyle(r.mode, "(%s)", termstyle.Foreground(lightSteelBlue)), path.RootDescription())
	}

	return result
}

// styledDotStylePath renders each map key in bold and each named list
// entry in bold italic, joined by plain dots.
func (r *reporter) styledDotStylePath(path *yamldiff.Path) string {
	if path == nil {
		return constantStyle(r.mode, "(file level)", termstyle.Bold())
	}

	if isRootPath(path) {
		return constantStyle(r.mode, "(root level)", termstyle.Bold())
	}

	bold := constantStyle(r.mode, "%s", termstyle.Bold())
	boldItalic := constantStyle(r.mode, "%s", termstyle.Bold(), termstyle.Italic())
	boldIndex := constantStyle(r.mode, "%d", termstyle.Bold())

	sections := []string{}
	for _, element := range path.PathElements {
		switch {
		case element.Key == "" && element.Name != "":
			sections = append(sections, fmt.Sprintf(bold, element.Name))

		case element.Key != "" && element.Name != "":
			sections = append(sections, fmt.Sprintf(boldItalic, element.Name))

		case element.Idx >= 0:
			sections = append(sections, fmt.Sprintf(boldIndex, element.Idx))
		}
	}

	return strings.Join(sections, ".")
}

// style applies the options to text and keeps the first error as a
// *FatalError, since bunt panics where termstyle returns an error.
func (r *reporter) style(text string, opts ...termstyle.Option) string {
	styled, err := r.mode.Style(text, opts...)
	if err != nil {
		if r.err == nil {
			r.err = &FatalError{Err: err}
		}

		return ""
	}

	return styled
}

// render formats only when there are arguments, as dyff's render does, so
// a format without arguments keeps any percent signs.
func render(format string, a ...any) string {
	if len(a) == 0 {
		return format
	}

	return fmt.Sprintf(format, a...)
}

// colored colors text line by line.
func (r *reporter) colored(color termstyle.RGB, text string) string {
	return r.style(text, termstyle.EachLine(), termstyle.Foreground(color))
}

// coloredf formats and then colors line by line.
func (r *reporter) coloredf(color termstyle.RGB, format string, a ...any) string {
	return r.colored(color, render(format, a...))
}

func (r *reporter) green(text string) string { return r.colored(additionGreen, text) }

func (r *reporter) greenf(format string, a ...any) string {
	return r.coloredf(additionGreen, format, a...)
}

func (r *reporter) red(text string) string { return r.colored(removalRed, text) }

func (r *reporter) redf(format string, a ...any) string {
	return r.coloredf(removalRed, format, a...)
}

func (r *reporter) yellowf(format string, a ...any) string {
	return r.coloredf(modificationYellow, format, a...)
}

func (r *reporter) lightGreen(text string) string { return r.colored(lightGreen, text) }

func (r *reporter) lightRed(text string) string { return r.colored(lightSalmon, text) }

func (r *reporter) bold(text string) string {
	return r.style(text, termstyle.EachLine(), termstyle.Bold())
}

func (r *reporter) italic(text string) string {
	return r.style(text, termstyle.EachLine(), termstyle.Italic())
}

func (r *reporter) boldGreen(text string) string { return r.bold(r.green(text)) }

func (r *reporter) boldRed(text string) string { return r.bold(r.red(text)) }

// fatalIfStyling wraps a styling error from neat's rendering in a
// *FatalError, because bunt panics on it, and returns other errors as is.
func fatalIfStyling(err error) error {
	var sgr *termstyle.SGRError
	if errors.As(err, &sgr) {
		return &FatalError{Err: err}
	}

	return err
}
