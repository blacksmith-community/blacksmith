// Package ansi provides ANSI escape code utilities for terminal output.
package ansi

import (
	"fmt"
	"os"
	"strings"
)

var colorEnabled = true

// Color enables or disables color output.
func Color(enabled bool) {
	colorEnabled = enabled
}

// IsColorEnabled returns whether color output is enabled.
func IsColorEnabled() bool {
	return colorEnabled
}

// ANSI escape codes.
const (
	ResetCode     = "\033[0m"
	BoldCode      = "\033[1m"
	DimCode       = "\033[2m"
	UnderlineCode = "\033[4m"
	ReverseCode   = "\033[7m"
	RedFg         = "\033[31m"
	GreenFg       = "\033[32m"
	YellowFg      = "\033[33m"
	BlueFg        = "\033[34m"
	MagentaFg     = "\033[35m"
	CyanFg        = "\033[36m"
	WhiteFg       = "\033[37m"
)

// Red wraps the string in red ANSI color codes.
func Red(s string) string {
	if !colorEnabled {
		return s
	}
	return RedFg + s + ResetCode
}

// Green wraps the string in green ANSI color codes.
func Green(s string) string {
	if !colorEnabled {
		return s
	}
	return GreenFg + s + ResetCode
}

// Yellow wraps the string in yellow ANSI color codes.
func Yellow(s string) string {
	if !colorEnabled {
		return s
	}
	return YellowFg + s + ResetCode
}

// Blue wraps the string in blue ANSI color codes.
func Blue(s string) string {
	if !colorEnabled {
		return s
	}
	return BlueFg + s + ResetCode
}

// Cyan wraps the string in cyan ANSI color codes.
func Cyan(s string) string {
	if !colorEnabled {
		return s
	}
	return CyanFg + s + ResetCode
}

// Bold wraps the string in bold ANSI codes.
func Bold(s string) string {
	if !colorEnabled {
		return s
	}
	return BoldCode + s + ResetCode
}

// Dim wraps the string in dim ANSI codes.
func Dim(s string) string {
	if !colorEnabled {
		return s
	}
	return DimCode + s + ResetCode
}

// Style is a set of SGR (Select Graphic Rendition) attributes, expressed
// as the parameter list of a CSI SGR sequence (for example "1;35" for
// bold magenta), with no leading "\033[" and no trailing "m". The zero
// value is the empty Style, which renders as no styling at all.
type Style string

// Apply wraps text in this Style's escape code and a trailing reset,
// unconditionally. Unlike every other helper in this package, which
// early-returns plain text when colorEnabled is false, Apply always
// renders its escape codes: it has no opinion on whether color output
// belongs on the stream it is writing to. Callers own that decision -
// resolve it once (see ResolveColor) and only call Apply when the
// answer is yes, the way debugStyler in cmd/graft does. An empty Style
// returns text unchanged.
func (s Style) Apply(text string) string {
	if s == "" {
		return text
	}
	return "\033[" + string(s) + "m" + text + ResetCode
}

// stTerminator is the second byte of the two-byte String Terminator
// (ESC \), which closes an OSC, DCS, SOS, PM, or APC sequence.
const stTerminator = '\\'

// StripEscapes removes ANSI/ECMA-48 escape sequences from s, leaving
// every other byte untouched: CSI (SGR color/style codes, cursor
// movement, and other final bytes 0x40-0x7E), OSC (terminal titles, OSC
// 8 hyperlinks, terminated by BEL or the String Terminator ESC \), DCS/
// SOS/PM/APC (the same string-terminated shape as OSC but ST-only, no
// BEL), the remaining two-byte Fe escapes not already claimed by one of
// those introducers, and 3-byte charset-select sequences (ESC,
// intermediate 0x20-0x2F, final 0x30-0x7E).
//
// Every ESC byte (0x1b) that does not begin one of those recognized,
// properly terminated sequences is DELETED rather than left in place:
// the bytes that follow it (a failed introducer's own bytes, an
// unterminated sequence's payload, or just plain text) are kept as-is
// and rescanned as ordinary text, so only the ESC itself disappears.
// This is a deliberate contract change from an earlier version that left
// an unrecognized ESC untouched. That earlier behavior could be made to
// manufacture a live, terminal-honored escape sequence that was never
// present as a complete sequence in the input: "ESC ESC \" has its
// second "ESC \" recognized and deleted as a two-byte Fe escape, letting
// the first, unrecognized ESC survive and land directly against
// whatever OSC- or CSI-shaped text happened to follow it in the source
// document, forming a brand-new sequence in the output. It also let a
// bare, unterminated introducer such as "ESC ]" reach the terminal
// unchanged, where it would swallow every byte up to the next BEL or ST
// as a title string. Deleting every unrecognized ESC unconditionally
// closes both holes: StripEscapes now guarantees zero ESC bytes in its
// output for any input, and the guarantee is idempotent (stripping
// already-stripped output is a no-op) since a second pass over text
// containing no ESC bytes has nothing left to remove.
//
// Deleting only the ESC byte, and never the text after it, is what
// keeps the result safe to hand to a terminal even when several
// independently stripped fragments are later concatenated: text such as
// "]0;pwned" or "Pattacker" has no terminal meaning without a genuine
// ESC byte immediately before it, and concatenation cannot manufacture
// that missing ESC byte out of surrounding bytes that were never 0x1b to
// begin with - a single byte value cannot be synthesized by joining two
// byte sequences that do not already contain it. Dropping the leftover
// payload as well (rather than keeping it as inert text) was considered
// and rejected: it would not close any additional hole, since the
// payload alone is already inert, and it would delete legitimate
// document content that merely happens to follow a stray ESC byte.
//
// C1 single-byte forms (e.g. 0x9B for CSI) are not recognized: over
// valid UTF-8 those byte values only occur as continuation bytes, never
// as a stand-alone escape introducer. It has no dependency on
// colorEnabled: call it whenever the origin of s is unknown, such as an
// error message that may carry raw bytes from a user-supplied document
// value (an operator argument, for example) alongside any color codes
// already baked in.
func StripEscapes(s string) string {
	if strings.IndexByte(s, '\033') == -1 {
		return s
	}

	var out strings.Builder
	out.Grow(len(s))

	i := 0
	for i < len(s) {
		if end, ok := consumeEscape(s, i); ok {
			i = end
			continue
		}
		if s[i] == '\033' {
			// Not a recognized, terminated escape: drop the ESC byte
			// itself so no live escape byte reaches the terminal, and
			// resume scanning at the next byte (see the doc comment
			// above for why the surviving text is safe to keep).
			i++
			continue
		}
		out.WriteByte(s[i])
		i++
	}

	return out.String()
}

// consumeEscape recognizes one complete, terminated escape sequence
// starting at s[i] and classifies it by the byte following ESC: "["
// begins a CSI sequence, "]" an OSC sequence, "P"/"X"/"^"/"_" a DCS/SOS/
// PM/APC sequence, an intermediate byte (0x20-0x2F) a 3-byte
// charset-select sequence, and any remaining Fe byte (0x40-0x5F) a plain
// two-byte escape. It returns the index past the sequence and true on
// success. It returns i and false when s[i] is not ESC, ESC is the last
// byte of s, or the sequence it introduces is never terminated - the
// caller then drops s[i] when it is ESC (StripEscapes never lets an
// unrecognized ESC byte survive), or keeps it unchanged otherwise.
func consumeEscape(s string, i int) (int, bool) {
	if s[i] != '\033' || i+1 >= len(s) {
		return i, false
	}

	next := s[i+1]
	if next == '[' {
		return scanCSI(s, i+2)
	}
	if next == ']' {
		return scanStringTerminated(s, i+2, true)
	}
	if isStringTerminatedIntroducer(next) {
		return scanStringTerminated(s, i+2, false)
	}
	if end, ok := scanCharsetSelect(s, i); ok {
		return end, true
	}
	if isFeByte(next) {
		return i + 2, true
	}

	return i, false
}

// isStringTerminatedIntroducer reports whether b introduces a DCS, SOS,
// PM, or APC sequence - the four escape classes that share OSC's
// string-terminated shape but, unlike OSC, accept only the two-byte
// String Terminator (never BEL) to close.
func isStringTerminatedIntroducer(b byte) bool {
	switch b {
	case 'P', 'X', '^', '_':
		return true
	default:
		return false
	}
}

// isFeByte reports whether b falls in the Fe range (0x40-0x5F): the
// second byte of a plain two-byte escape not already claimed by one of
// the CSI/OSC/DCS/SOS/PM/APC introducers above.
func isFeByte(b byte) bool {
	return b >= 0x40 && b <= 0x5F
}

// scanCharsetSelect recognizes a 3-byte charset-select sequence starting
// at s[i] (the ESC byte): an intermediate byte (0x20-0x2F) followed by a
// final byte (0x30-0x7E), such as "ESC ( B" (select ASCII into G0). It
// returns the index past the final byte and true on success, or i and
// false if the string ends too soon or the bytes are out of range.
func scanCharsetSelect(s string, i int) (int, bool) {
	if i+2 >= len(s) {
		return i, false
	}
	intermediate, final := s[i+1], s[i+2]
	if intermediate < 0x20 || intermediate > 0x2F {
		return i, false
	}
	if final < 0x30 || final > 0x7E {
		return i, false
	}
	return i + 3, true
}

// scanCSI consumes a CSI sequence's parameter bytes (0x30-0x3F),
// intermediate bytes (0x20-0x2F), and single final byte (0x40-0x7E),
// where start is the index right after "ESC [". It returns the index
// past the final byte and true on success, or start and false if an
// invalid byte or end of string is reached first.
func scanCSI(s string, start int) (int, bool) {
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 0x30 && c <= 0x3F, c >= 0x20 && c <= 0x2F:
			continue
		case c >= 0x40 && c <= 0x7E:
			return i + 1, true
		default:
			return start, false
		}
	}
	return start, false
}

// scanStringTerminated consumes an OSC/DCS/SOS/PM/APC sequence's
// payload, where start is the index right after its introducer ("ESC ]",
// "ESC P", "ESC X", "ESC ^", or "ESC _"). OSC (allowBEL true) accepts
// either BEL (0x07) or the two-byte String Terminator (ESC \) to close;
// the others (allowBEL false) accept only the String Terminator. It
// returns the index past the terminator and true on success, or start
// and false if no terminator is found before end of string.
func scanStringTerminated(s string, start int, allowBEL bool) (int, bool) {
	for i := start; i < len(s); i++ {
		switch {
		case allowBEL && s[i] == '\a':
			return i + 1, true
		case s[i] == '\033' && i+1 < len(s) && s[i+1] == stTerminator:
			return i + 2, true
		}
	}
	return start, false
}

// Sprintf formats with color codes like @R{red text} @c{cyan text}.
func Sprintf(format string, args ...interface{}) string {
	return SprintfColor(colorEnabled, format, args...)
}

// SprintfColor behaves like Sprintf but takes the color flag explicitly
// instead of reading the package-wide toggle, so callers can render with or
// without color without touching shared state.
func SprintfColor(enabled bool, format string, args ...interface{}) string {
	result := fmt.Sprintf(format, args...)
	return processColorCodes(enabled, result)
}

// SprintfTemplate processes color markup in format only, then formats args
// into the result, so the args appear verbatim and are never read as markup.
// The color flag is explicit, and the package-wide toggle is not consulted.
func SprintfTemplate(enabled bool, format string, args ...interface{}) string {
	return fmt.Sprintf(processColorCodes(enabled, format), args...)
}

// Errorf creates a formatted error with color codes.
func Errorf(format string, args ...interface{}) error {
	msg := Sprintf(format, args...)
	return fmt.Errorf("%s", msg)
}

// processColorCodes replaces @X{text} patterns with ANSI codes.
func processColorCodes(enabled bool, s string) string {
	if !enabled {
		// Strip color codes when disabled
		return stripColorCodes(s)
	}

	// Process color codes one at a time to handle nested braces correctly
	result := s

	// Define color code mappings
	colorPrefixes := map[string]string{
		"@R{": RedFg,     // Red
		"@r{": RedFg,     // red
		"@G{": GreenFg,   // Green
		"@g{": GreenFg,   // green
		"@Y{": YellowFg,  // Yellow
		"@y{": YellowFg,  // yellow
		"@B{": BlueFg,    // Blue
		"@b{": BlueFg,    // blue
		"@M{": MagentaFg, // Magenta
		"@m{": MagentaFg, // magenta
		"@C{": CyanFg,    // Cyan
		"@c{": CyanFg,    // cyan
		"@W{": WhiteFg,   // White
		"@w{": WhiteFg,   // white
		"@*{": BoldCode,  // Bold
		"@~{": DimCode,   // Dim
	}

	// Process each color code type
	for prefix, ansiCode := range colorPrefixes {
		result = processColorPrefix(result, prefix, ansiCode)
	}

	return result
}

// processColorPrefix handles a single color prefix, properly matching braces.
func processColorPrefix(s, prefix, ansiCode string) string {
	var result strings.Builder
	i := 0

	for i < len(s) {
		// Look for the prefix
		if i+len(prefix) <= len(s) && s[i:i+len(prefix)] == prefix {
			// Find the matching closing brace
			start := i + len(prefix)
			depth := 1
			end := start

			for end < len(s) && depth > 0 {
				switch s[end] {
				case '{':
					depth++
				case '}':
					depth--
				}
				if depth > 0 {
					end++
				}
			}

			if depth == 0 {
				// Found matching brace
				content := s[start:end]
				// Recursively process content in case there are nested color codes
				result.WriteString(ansiCode)
				result.WriteString(content)
				result.WriteString(ResetCode)
				i = end + 1
			} else {
				// No matching brace, write the prefix as-is
				result.WriteString(prefix)
				i += len(prefix)
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}

	return result.String()
}

// stripColorCodes removes @X{...} patterns, leaving just the content.
func stripColorCodes(s string) string {
	colorPrefixes := []string{"@R{", "@r{", "@G{", "@g{", "@Y{", "@y{", "@B{", "@b{", "@M{", "@m{", "@C{", "@c{", "@W{", "@w{", "@*{", "@~{"}

	result := s
	for _, prefix := range colorPrefixes {
		result = stripColorPrefix(result, prefix)
	}

	return result
}

// stripColorPrefix removes a single color prefix pattern, properly matching braces.
func stripColorPrefix(s, prefix string) string {
	var result strings.Builder
	i := 0

	for i < len(s) {
		// Look for the prefix
		if i+len(prefix) <= len(s) && s[i:i+len(prefix)] == prefix {
			// Find the matching closing brace
			start := i + len(prefix)
			depth := 1
			end := start

			for end < len(s) && depth > 0 {
				switch s[end] {
				case '{':
					depth++
				case '}':
					depth--
				}
				if depth > 0 {
					end++
				}
			}

			if depth == 0 {
				// Found matching brace - write just the content (no color codes)
				content := s[start:end]
				result.WriteString(content)
				i = end + 1
			} else {
				// No matching brace, write the prefix as-is
				result.WriteString(prefix)
				i += len(prefix)
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}

	return result.String()
}

//nolint:gochecknoinits // Environment-based color detection must happen at startup
func init() {
	// Auto-detect if terminal supports colors
	if !EnvAllowsColor() {
		colorEnabled = false
	}
}

// EnvAllowsColor reports whether the NO_COLOR and TERM environment
// variables permit color output: false when NO_COLOR is set to any
// non-empty value (https://no-color.org), or when TERM is "dumb"; true
// otherwise. It does not consider whether output is a terminal - see
// ResolveColor for that.
func EnvAllowsColor() bool {
	return os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// ResolveColor decides whether color output should be enabled, given an
// optional explicit override and whether the relevant output stream is a
// terminal. Precedence, highest first:
//
//  1. explicit - non-nil when the caller passed an explicit flag such as
//     --color/--no-color; its value wins outright.
//  2. environment - EnvAllowsColor() (NO_COLOR/TERM) must permit color.
//  3. isTTY - the output stream must be a terminal.
//
// Both 2 and 3 must hold for color to be enabled when explicit is nil.
func ResolveColor(explicit *bool, isTTY bool) bool {
	if explicit != nil {
		return *explicit
	}
	return EnvAllowsColor() && isTTY
}
