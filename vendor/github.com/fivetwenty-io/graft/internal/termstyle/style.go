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
// Ported from github.com/gonvenience/bunt v1.4.3 (model.go, bunt.go,
// convenience.go) and modified for graft: Style returns the parse error
// instead of panicking, the color mode is an explicit value instead of
// process-wide state, and the blend and text-annotation features that
// graft never enables are dropped.

// Package termstyle reproduces the styled strings of bunt v1.4.3 byte for
// byte, so that graft's diff output stays identical to spruce's.
package termstyle

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Internal bit masks that mark feature states of a styled rune.
const (
	fgMask        = 0x1
	bgMask        = 0x2
	boldMask      = 0x4
	italicMask    = 0x8
	underlineMask = 0x10
)

// escapeFinder finds the SGR sequences that StripSGR removes.
var escapeFinder = regexp.MustCompile(`\x1b\[([\d;]*)m`)

// Mode says how a styled string renders.
type Mode struct {
	// Color turns styling on. With it off, Style returns the plain text.
	Color bool
	// TrueColor selects 24-bit colors. With it off, colors fall back to the
	// 16 basic terminal colors.
	TrueColor bool
}

// RGB is an 8-bit-per-channel color.
type RGB struct{ R, G, B uint8 }

// styledRune is a rune with its style settings.
//
// Bit details:
//   - 1st bit, foreground color on/off
//   - 2nd bit, background color on/off
//   - 3rd bit, bold on/off
//   - 4th bit, italic on/off
//   - 5th bit, underline on/off
//   - 9th-32nd bit, 24 bit RGB foreground color
//   - 33rd-56th bit, 24 bit RGB background color
type styledRune struct {
	symbol   rune
	settings uint64
}

// styledString is a string with style information.
type styledString []styledRune

// Option is a style to apply to a text, or a flag that changes how later
// options apply.
type Option struct {
	skipNewLine bool
	apply       func(s styledString, skipNewLine bool)
}

// SGRError reports a select-graphic-rendition escape sequence that bunt
// cannot parse, which makes bunt panic.
type SGRError struct {
	msg string
}

// Error returns the message that bunt panics with.
func (e *SGRError) Error() string { return e.msg }

// applyEach runs set on every rune, skipping newlines when asked to.
func applyEach(set func(r *styledRune)) func(styledString, bool) {
	return func(s styledString, skipNewLine bool) {
		for i := range s {
			if skipNewLine && s[i].symbol == '\n' {
				continue
			}

			set(&s[i])
		}
	}
}

// Bold applies the bold text parameter.
func Bold() Option {
	return Option{apply: applyEach(func(r *styledRune) { r.settings |= boldMask })}
}

// Italic applies the italic text parameter.
func Italic() Option {
	return Option{apply: applyEach(func(r *styledRune) { r.settings |= italicMask })}
}

// Underline applies the underline text parameter.
func Underline() Option {
	return Option{apply: applyEach(func(r *styledRune) { r.settings |= underlineMask })}
}

// Foreground sets the given color as the foreground color of the text.
func Foreground(c RGB) Option {
	return Option{apply: applyEach(func(r *styledRune) {
		// Reset the currently set foreground color.
		r.settings &= 0xFFFFFFFF000000FF

		r.settings |= fgMask
		r.settings |= uint64(c.R) << 8
		r.settings |= uint64(c.G) << 16
		r.settings |= uint64(c.B) << 24
	})}
}

// EachLine makes the options that follow it skip newline runes, so that a
// text is colored line by line and not as a block.
func EachLine() Option {
	return Option{skipNewLine: true}
}

// Style parses the escape sequences already in text, applies the options in
// argument order, and renders the result for the mode. It returns an
// *SGRError where bunt would panic on a malformed color escape.
func (m Mode) Style(text string, opts ...Option) (string, error) {
	result, err := parse(text)
	if err != nil {
		return "", err
	}

	skipNewLine := false
	for _, opt := range opts {
		if opt.skipNewLine {
			skipNewLine = true
		}

		if opt.apply != nil {
			opt.apply(result, skipNewLine)
		}
	}

	return m.render(result), nil
}

// StripSGR returns the input with all select-graphic-rendition escape
// sequences removed. It finds the first sequence, removes every copy of
// that exact sequence, and searches again, so a removal that assembles a
// new sequence removes that one too, as bunt's RemoveAllEscapeSequences
// does.
func StripSGR(s string) string {
	for loc := escapeFinder.FindStringIndex(s); loc != nil; loc = escapeFinder.FindStringIndex(s) {
		s = strings.ReplaceAll(s, s[loc[0]:loc[1]], "")
	}

	return s
}

// PlainLen returns the number of runes in s once StripSGR has removed its
// escape sequences.
func PlainLen(s string) int {
	return utf8.RuneCountInString(StripSGR(s))
}
