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
// Ported from github.com/gonvenience/bunt v1.4.3 (parse.go, colors.go) and
// modified for graft: malformed color escapes return an *SGRError instead
// of reading out of range, and the 8-bit palette is a plain RGB table.

package termstyle

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	escapeSeqRegExp = regexp.MustCompile(`\x1b\[(\d+(;\d+)*)m`)
	modeSettingsRE  = regexp.MustCompile(`\x1b\[\?.+[lh]`)
	unknownCheckRE  = regexp.MustCompile(`\x1b\]11;\?.`)
)

// sgrColors maps the foreground codes 30-37 and 90-97 to the colors bunt
// parses them as. The background codes 40-47 and 100-107 use the same
// colors, ten codes higher.
var sgrColors = map[uint8]RGB{
	30: {1, 1, 1},
	31: {222, 56, 43},
	32: {57, 181, 74},
	33: {255, 199, 6},
	34: {0, 111, 184},
	35: {118, 38, 113},
	36: {44, 181, 233},
	37: {204, 204, 204},
	90: {128, 128, 128},
	91: {255, 0, 0},
	92: {0, 255, 0},
	93: {255, 255, 0},
	94: {0, 0, 255},
	95: {255, 0, 255},
	96: {0, 255, 255},
	97: {255, 255, 255},
}

// colorPalette8bit is the 256-color palette that 38;5;n and 48;5;n select
// from.
var colorPalette8bit = func() [256]RGB {
	var palette [256]RGB

	// Standard colors.
	palette[0] = RGB{0, 0, 0}
	palette[1] = RGB{170, 0, 0}
	palette[2] = RGB{0, 170, 0}
	palette[3] = RGB{229, 229, 16}
	palette[4] = RGB{0, 0, 170}
	palette[5] = RGB{170, 0, 170}
	palette[6] = RGB{0, 170, 170}
	palette[7] = RGB{229, 229, 229}

	// High-intensity colors.
	palette[8] = RGB{85, 85, 85}
	palette[9] = RGB{255, 85, 85}
	palette[10] = RGB{85, 255, 85}
	palette[11] = RGB{255, 255, 85}
	palette[12] = RGB{85, 85, 255}
	palette[13] = RGB{255, 85, 255}
	palette[14] = RGB{85, 255, 255}
	palette[15] = RGB{255, 255, 255}

	// The 216 colors of the 6x6x6 cube.
	for b := 0; b <= 5; b++ {
		for g := 0; g <= 5; g++ {
			for r := 0; r <= 5; r++ {
				palette[16+36*r+6*g+b] = RGB{uint8(r * 51), uint8(g * 51), uint8(b * 51)}
			}
		}
	}

	// The 24 grayscale shades.
	for i := 232; i < 256; i++ {
		value := uint8(float32(i-232) * (255.0 / 23.0))
		palette[i] = RGB{value, value, value}
	}

	return palette
}()

// parse turns a string that may contain select-graphic-rendition escape
// sequences into runes with settings, as bunt's ParseString does.
func parse(input string) (styledString, error) {
	var (
		pointer int
		current uint64
		err     error
		result  styledString
	)

	// The escape sequence without any parameter is equivalent to the reset
	// escape sequence.
	input = strings.ReplaceAll(input, "\x1b[m", "\x1b[0m")

	// Ignore the "set cursor key to application" sequence.
	input = strings.ReplaceAll(input, "\x1b[?1h", "")

	// Ignore keypad mode settings.
	input = strings.ReplaceAll(input, "\x1b=", "")
	input = strings.ReplaceAll(input, "\x1b>", "")

	// Ignore "clear line from cursor right".
	input = strings.ReplaceAll(input, "\x1b[K", "")

	// Ignore known mode settings.
	input = modeSettingsRE.ReplaceAllString(input, "")

	// Ignore this unknown sequence, which seems to be a conditional check.
	input = unknownCheckRE.ReplaceAllString(input, "")

	apply := func(str string, mask uint64) {
		for _, r := range str {
			result = append(result, styledRune{r, mask})
		}
	}

	for _, submatch := range escapeSeqRegExp.FindAllStringSubmatchIndex(input, -1) {
		fullMatchStart, fullMatchEnd := submatch[0], submatch[1]
		settingsStart, settingsEnd := submatch[2], submatch[3]

		apply(input[pointer:fullMatchStart], current)

		current, err = parseSGR(input[settingsStart:settingsEnd])
		if err != nil {
			return nil, err
		}

		pointer = fullMatchEnd
	}

	// Flush the remaining input into the result.
	apply(input[pointer:], current)

	return result, nil
}

// parseSGR turns the parameters of one escape sequence into settings. The
// codes combine bitwise into a zero-initialized setting, so unknown codes
// do nothing and two colors in one sequence OR together.
func parseSGR(escapeSeq string) (uint64, error) {
	parts := strings.Split(escapeSeq, ";")
	values := make([]uint8, 0, len(parts))

	for _, x := range parts {
		// The regular expression only matches digits, so omitting the error
		// is safe. A value that overflows Atoi arrives as MaxInt64 and
		// wraps to 255, and any other value wraps modulo 256, as in bunt.
		value, _ := strconv.Atoi(x)
		values = append(values, uint8(value&0xFF))
	}

	result := uint64(0)

	for i := 0; i < len(values); i++ {
		switch values[i] {
		case 1:
			result |= boldMask

		case 3:
			result |= italicMask

		case 4:
			result |= underlineMask

		case 38, 48:
			mask, skip, err := parseExtendedColor(values, i, values[i] == 38)
			if err != nil {
				return 0, err
			}

			result |= mask
			i += skip

		default:
			result |= basicColorMask(values[i])
		}
	}

	return result, nil
}

// basicColorMask returns the settings for the codes 30-37, 90-97, 40-47,
// and 100-107, and zero for every other code.
func basicColorMask(code uint8) uint64 {
	if c, ok := sgrColors[code]; ok {
		return fgRGBMask(uint64(c.R), uint64(c.G), uint64(c.B))
	}

	if c, ok := sgrColors[code-10]; ok {
		return bgRGBMask(uint64(c.R), uint64(c.G), uint64(c.B))
	}

	return 0
}

// parseExtendedColor handles the codes 38 and 48 at values[i]. It keeps
// bunt's conditions, and returns an *SGRError both for the forms bunt
// rejects and for the reads that make bunt panic with an index error. skip
// is the number of parameters the color selection consumed after the code.
func parseExtendedColor(values []uint8, i int, foreground bool) (mask uint64, skip int, err error) {
	// The error is built only where it is returned, because formatting the
	// whole parameter list for every valid selection would make a long
	// sequence quadratic.
	fail := func() *SGRError { return extendedColorError(values, foreground) }
	maskFor := bgRGBMask
	if foreground {
		maskFor = fgRGBMask
	}

	if len(values) > 4 {
		if i+1 >= len(values) {
			return 0, 0, fail()
		}

		if values[i+1] == 2 {
			if i+4 >= len(values) {
				return 0, 0, fail()
			}

			return maskFor(uint64(values[i+2]), uint64(values[i+3]), uint64(values[i+4])), 4, nil
		}
	}

	if len(values) > 2 {
		if i+1 >= len(values) {
			return 0, 0, fail()
		}

		if values[i+1] == 5 {
			if i+2 >= len(values) {
				return 0, 0, fail()
			}

			c := colorPalette8bit[values[i+2]]

			return maskFor(uint64(c.R), uint64(c.G), uint64(c.B)), 2, nil
		}
	}

	return 0, 0, fail()
}

// extendedColorError returns the error bunt panics with for a malformed
// color selection over the parsed values.
func extendedColorError(values []uint8, foreground bool) *SGRError {
	kind := "background"
	if foreground {
		kind = "foreground"
	}

	return &SGRError{msg: fmt.Sprintf("unsupported %s color selection '%v'", kind, values)}
}

func fgRGBMask(r, g, b uint64) uint64 {
	return fgMask | r<<8 | g<<16 | b<<24
}

func bgRGBMask(r, g, b uint64) uint64 {
	return bgMask | r<<32 | g<<40 | b<<48
}
