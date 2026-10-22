package ssh

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// minWithheldRun is the shortest run of base64 characters that withholds a
// stream. A PEM body line is 64 characters, and a stream cut inside a body
// still holds a run this long, while words, paths, and job names rarely
// do.
const minWithheldRun = 40

// withheldMarkers are the pieces of a PEM header or footer, any one of
// which withholds a stream.
//
//nolint:gochecknoglobals // a fixed list, read only
var withheldMarkers = [][]byte{[]byte("-----"), []byte("BEGIN"), []byte("PRIVATE")}

// Withheld reports whether a stream of command output may hold key or
// certificate content, so it must not reach a log line. It is true when
// the stream holds "-----", "BEGIN", or "PRIVATE" anywhere, or a run of 40
// or more base64 characters that is not all lowercase hex. The last rule
// catches a stream that was cut inside a PEM body, where neither marker
// survives, and it still lets a 64-character SHA-256 hash through. The
// rule judges the whole stream, never only the part around a marker.
func Withheld(stream []byte) bool {
	for _, marker := range withheldMarkers {
		if bytes.Contains(stream, marker) {
			return true
		}
	}

	run, hexOnly := 0, true

	for _, char := range stream {
		if !isBase64(char) {
			run, hexOnly = 0, true

			continue
		}

		run++
		hexOnly = hexOnly && isLowerHex(char)

		if run >= minWithheldRun && !hexOnly {
			return true
		}
	}

	return false
}

// ForLog is the only form in which command output reaches a log line. It
// returns the text of a stream Withheld passes, and for any other stream
// only its size, as "[output withheld, N bytes]". The text is escaped as
// escapeForLog does, so output a VM printed can never forge a log line or
// send a terminal escape sequence.
func ForLog(stream []byte) string {
	if Withheld(stream) {
		return fmt.Sprintf("[output withheld, %d bytes]", len(stream))
	}

	return escapeForLog(stream)
}

// escapeForLog writes stream with every character a log line must not
// carry raw replaced by an escape. A newline, a carriage return, and a
// tab become \n, \r, and \t, and a backslash is doubled, so an escape
// in the result is never ambiguous. Any other C0 control character and
// DEL become \xNN, a C1 control character or any other rune that is not
// printable, such as a line separator, becomes \uNNNN, and a byte that
// is not UTF-8 becomes \xNN.
func escapeForLog(stream []byte) string {
	var out strings.Builder

	out.Grow(len(stream))

	for len(stream) > 0 {
		char, size := utf8.DecodeRune(stream)

		switch {
		case char == utf8.RuneError && size == 1:
			fmt.Fprintf(&out, `\x%02x`, stream[0])
		case char == '\\':
			out.WriteString(`\\`)
		case char == '\n':
			out.WriteString(`\n`)
		case char == '\r':
			out.WriteString(`\r`)
		case char == '\t':
			out.WriteString(`\t`)
		case char < utf8.RuneSelf && !strconv.IsPrint(char):
			fmt.Fprintf(&out, `\x%02x`, char)
		case !strconv.IsPrint(char) && char <= 0xFFFF:
			fmt.Fprintf(&out, `\u%04x`, char)
		case !strconv.IsPrint(char):
			fmt.Fprintf(&out, `\U%08x`, char)
		default:
			out.Write(stream[:size])
		}

		stream = stream[size:]
	}

	return out.String()
}

// isBase64 reports whether c is in the standard base64 alphabet. The '='
// padding is left out, so "hash=<hex>" splits into two runs.
func isBase64(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
}

// isLowerHex reports whether c is a lowercase hex digit.
func isLowerHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
}
