package certpatch_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"blacksmith/internal/certpatch"
)

func TestNormalisedHashRules(t *testing.T) {
	t.Parallel()

	same := []string{"a\r\nb  \n\n\n", "a\nb\n", "a\nb"}

	want := certpatch.NormalisedHash([]byte(same[0]))
	for _, input := range same {
		if got := certpatch.NormalisedHash([]byte(input)); got != want {
			t.Errorf("hash of %q = %s, want %s", input, got, want)
		}
	}

	if certpatch.NormalisedHash([]byte("a \tb\n")) == certpatch.NormalisedHash([]byte("a b\n")) {
		t.Error("whitespace inside a line must change the hash")
	}

	sum := sha256.Sum256([]byte("a\nb\n"))
	if want != hex.EncodeToString(sum[:]) {
		t.Errorf("hash = %s, want the SHA-256 of the normalised text", want)
	}

	pemText := "-----BEGIN CERTIFICATE-----\nMIIB\nabcd\n-----END CERTIFICATE-----\n"
	crlf := strings.ReplaceAll(pemText, "\n", "\r\n")

	if certpatch.NormalisedHash([]byte(crlf)) != certpatch.NormalisedHash([]byte(strings.TrimSuffix(pemText, "\n"))) {
		t.Error("CRLF endings and a missing trailing newline must hash the same")
	}
}
