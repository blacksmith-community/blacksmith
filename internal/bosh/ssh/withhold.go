package ssh

import (
	"bytes"
	"fmt"
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
// only its size, as "[output withheld, N bytes]".
func ForLog(stream []byte) string {
	if Withheld(stream) {
		return fmt.Sprintf("[output withheld, %d bytes]", len(stream))
	}

	return string(stream)
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
