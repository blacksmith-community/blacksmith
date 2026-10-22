// Package certpatch holds the pieces of patch-in-place rotation that do not
// touch a VM.
package certpatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// NormalisedHash returns the lowercase hex SHA-256 of b after it has been
// normalised, so a template's line endings or trailing whitespace cannot
// hide a match. Normalising removes every carriage return, strips trailing
// spaces and tabs from each line, drops trailing newlines, and appends
// exactly one newline. The shell function on the VM applies the same rule.
func NormalisedHash(b []byte) string {
	text := bytes.ReplaceAll(b, []byte{'\r'}, nil)
	lines := bytes.Split(text, []byte{'\n'})

	for i, line := range lines {
		lines[i] = bytes.TrimRight(line, " \t")
	}

	joined := bytes.TrimRight(bytes.Join(lines, []byte{'\n'}), "\n")
	joined = append(joined, '\n')

	sum := sha256.Sum256(joined)

	return hex.EncodeToString(sum[:])
}
