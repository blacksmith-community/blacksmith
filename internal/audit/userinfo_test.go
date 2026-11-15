package audit_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"blacksmith/internal/audit"
)

const urlLeak = "s3cret-in-url-9a2e"

// urlPasswordCases are the values the broker and the page must both
// treat alike. The page's test reads the same file.
type urlPasswordCases struct {
	Leak  string `json:"leak"`
	Cases []struct {
		Value string `json:"value"`
		Want  string `json:"want"`
	} `json:"cases"`
}

// A value holding userinfo with a password keeps its username and host
// and loses its password, whatever the key, so a pasted URL never stores
// a credential. That holds with or without a scheme, and for a password
// that holds a raw /, ?, #, or @. A value without a password, a username
// alone, a host and port, and a plain value with an @ are left alone.
func TestSanitizeChangeStripsAURLPassword(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/url-passwords.json")
	if err != nil {
		t.Fatal(err)
	}

	var vectors urlPasswordCases

	err = json.Unmarshal(raw, &vectors)
	if err != nil {
		t.Fatal(err)
	}

	if vectors.Leak != urlLeak || len(vectors.Cases) < 20 {
		t.Fatalf("the shared cases read %d cases for marker %q", len(vectors.Cases), vectors.Leak)
	}

	for _, vector := range vectors.Cases {
		rec := audit.SanitizeChange(audit.ChangeRecord{Scope: audit.ScopeEndpoints, Key: keyAPIURL, Old: vector.Value, New: vector.Value})
		if rec.Old != vector.Want || rec.New != vector.Want {
			t.Errorf("SanitizeChange(%q) = old %q new %q, want %q", vector.Value, rec.Old, rec.New, vector.Want)
		}

		if strings.Contains(rec.Old+rec.New, urlLeak) {
			t.Errorf("SanitizeChange(%q) kept the password", vector.Value)
		}
	}
}
