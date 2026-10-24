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

// A job template often puts a blank line before <%= p(...) %> and more
// after it, so a rendered file holds the PEM with whitespace around it.
// That file still holds the variable's value and nothing else.
func TestNormalisedHashIgnoresSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	pemText := "-----BEGIN CERTIFICATE-----\nMIIB\nabcd\n-----END CERTIFICATE-----\n"
	want := certpatch.NormalisedHash([]byte(pemText))

	for _, padded := range []string{
		"\n" + pemText + "\n\n",
		" \t\r\n\n" + pemText + "\r\n \t\n",
		"\n\n" + strings.TrimSuffix(pemText, "\n"),
		"  " + pemText,
	} {
		if got := certpatch.NormalisedHash([]byte(padded)); got != want {
			t.Errorf("hash of %q = %s, want the hash of the bare PEM", padded, got)
		}
	}

	// Anything that is not ASCII space, tab, CR, or LF still counts, and
	// so does whitespace inside the PEM.
	for _, other := range []string{
		"x\n" + pemText,
		"\v" + pemText,
		"\f\n" + pemText,
		pemText + "x\n",
		"-----BEGIN CERTIFICATE-----\n  MIIB\nabcd\n-----END CERTIFICATE-----\n",
	} {
		if certpatch.NormalisedHash([]byte(other)) == want {
			t.Errorf("hash of %q matches the bare PEM, want it to differ", other)
		}
	}
}

// The patched file keeps the whitespace around the old PEM, so the bytes
// on disk are prefix, new PEM, and suffix. The journal records the hash
// of the new PEM, and that must be the hash of the bytes written.
func TestNormalisedHashOfWrittenBytes(t *testing.T) {
	t.Parallel()

	newPEM := "-----BEGIN CERTIFICATE-----\nMIIC\nwxyz\n-----END CERTIFICATE-----\n"
	written := "\n" + strings.TrimSpace(newPEM) + "\n\n\n"

	if certpatch.NormalisedHash([]byte(written)) != certpatch.NormalisedHash([]byte(newPEM)) {
		t.Error("the written bytes do not hash to the new PEM's hash that the journal records")
	}
}
