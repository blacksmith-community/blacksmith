// Package certpatch holds the pieces of patch-in-place rotation that do not
// touch a VM.
package certpatch

import (
	"crypto/sha256"
	"encoding/hex"
)

// NormalisedHash returns the lowercase hex SHA-256 of b after it has been
// normalised, so a template's line endings or the whitespace it puts
// around a value cannot hide a match. Normalising removes every carriage
// return, drops the newlines before the first other byte, strips
// trailing spaces and tabs from each line, drops trailing newlines, and
// appends exactly one newline. A file that holds a PEM with blank lines
// before it and whitespace after it therefore hashes the same as the
// bare PEM, but a space or tab before the BEGIN marker still counts,
// because a PEM reader wants the marker at the start of its line. The
// shell function on the VM applies the same rule.
func NormalisedHash(b []byte) string {
	return normalisedHash(b, nil)
}

// normalisedHash does the work in one buffer that it clears before it
// returns, because the input can be a private key. seen, when set, receives
// that buffer after it was cleared, so a test can check it.
func normalisedHash(input []byte, seen func([]byte)) string {
	out := make([]byte, 0, len(input)+1)

	defer func() {
		out = out[:cap(out)]

		clear(out)

		if seen != nil {
			seen(out)
		}
	}()

	lineStart := 0
	started := false

	for _, char := range input {
		switch {
		case char == '\r':
		case !started && char == '\n':
		case char == '\n':
			out = trimLine(out, lineStart)
			out = append(out, '\n')
			lineStart = len(out)
		default:
			started = true

			out = append(out, char)
		}
	}

	out = trimLine(out, lineStart)

	for len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}

	out = append(out, '\n')

	sum := sha256.Sum256(out)

	return hex.EncodeToString(sum[:])
}

// trimLine drops trailing spaces and tabs from the line that starts at
// lineStart.
func trimLine(out []byte, lineStart int) []byte {
	for len(out) > lineStart && (out[len(out)-1] == ' ' || out[len(out)-1] == '\t') {
		out = out[:len(out)-1]
	}

	return out
}

// RawHash returns the lowercase hex SHA-256 of b exactly as it is. The
// normalised hash finds the files that hold a certificate, and the raw
// hash then pins the exact bytes discovery read and a replacement
// writes, so a change of whitespace alone is still seen. raw_hash in the
// VM's scripts computes the same.
func RawHash(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}
