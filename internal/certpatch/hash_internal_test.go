package certpatch

import (
	"strings"
	"testing"
)

func TestNormalisedHashClearsItsBuffer(t *testing.T) {
	t.Parallel()

	var seen []byte

	input := []byte("-----BEGIN PRIVATE KEY-----\r\nSECRETSECRET  \r\n-----END PRIVATE KEY-----\r\n")

	got := normalisedHash(input, func(buf []byte) { seen = buf })

	if got != NormalisedHash(input) {
		t.Fatal("the hash must not depend on the hook")
	}

	if len(seen) == 0 {
		t.Fatal("the hook was never called")
	}

	for _, b := range seen {
		if b != 0 {
			t.Fatalf("the working buffer still holds data: %q", strings.TrimRight(string(seen), "\x00"))
		}
	}
}

func TestNormalisedHashEmptyInput(t *testing.T) {
	t.Parallel()

	if NormalisedHash(nil) != NormalisedHash([]byte("\n\n")) {
		t.Fatal("empty input and bare newlines normalise alike")
	}
}
