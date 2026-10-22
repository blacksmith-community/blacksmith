// Package certpatch holds the pieces of patch-in-place rotation that do not
// touch a VM.
package certpatch

import (
	"crypto/sha256"
	"encoding/hex"
)

// NormalisedHash returns the lowercase hex SHA-256 of b after it has been
// normalised, so a template's line endings or trailing whitespace cannot
// hide a match. Normalising removes every carriage return, strips trailing
// spaces and tabs from each line, drops trailing newlines, and appends
// exactly one newline. The shell function on the VM applies the same rule.
func NormalisedHash(b []byte) string {
	return normalisedHash(b, nil)
}

// normalisedHash does the work in one buffer that it clears before it
// returns, because the input can be a private key. seen, when set, receives
// that buffer after it was cleared, so a test can check it.
func normalisedHash(b []byte, seen func([]byte)) string {
	out := make([]byte, 0, len(b)+1)

	defer func() {
		out = out[:cap(out)]

		clear(out)

		if seen != nil {
			seen(out)
		}
	}()

	lineStart := 0

	for _, c := range b {
		switch c {
		case '\r':
		case '\n':
			out = trimLine(out, lineStart)
			out = append(out, '\n')
			lineStart = len(out)
		default:
			out = append(out, c)
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
