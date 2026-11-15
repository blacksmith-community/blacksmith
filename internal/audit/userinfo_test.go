package audit_test

import (
	"strings"
	"testing"

	"blacksmith/internal/audit"
)

const urlLeak = "s3cret-in-url-9a2e"

// A URL with a password in its userinfo keeps its username and host and
// loses its password, whatever the key, so a pasted URL never stores a
// credential. A URL without a password is left alone.
func TestSanitizeChangeStripsAURLPassword(t *testing.T) {
	t.Parallel()

	//nolint:gosec // the URLs are fixtures, and "(changed)" stands where a password was
	for value, want := range map[string]string{
		"https://admin:" + urlLeak + "@api.sys.example.test":            "https://admin:(changed)@api.sys.example.test",
		"https://admin:" + urlLeak + "@api.sys.example.test:443/v3?x=1": "https://admin:(changed)@api.sys.example.test:443/v3?x=1",
		"HTTPS://Admin:" + urlLeak + "@API.example.test":                "HTTPS://Admin:(changed)@API.example.test",
		"https://admin:p@" + urlLeak + "@api.example.test/path":         "https://admin:(changed)@api.example.test/path",
		"https://:" + urlLeak + "@api.example.test":                     "https://:(changed)@api.example.test",
		"see https://admin:" + urlLeak + "@a.example.test and more":     "see https://admin:(changed)@a.example.test and more",
		"https://admin@api.example.test":                                "https://admin@api.example.test",
		"https://api.example.test:8443/a@b":                             "https://api.example.test:8443/a@b",
		"not a url":                                                     "not a url",
	} {
		rec := audit.SanitizeChange(audit.ChangeRecord{Scope: audit.ScopeEndpoints, Key: "api_url", Old: value, New: value})
		if rec.Old != want || rec.New != want {
			t.Errorf("SanitizeChange(%q) = old %q new %q, want %q", value, rec.Old, rec.New, want)
		}

		if strings.Contains(rec.Old+rec.New, urlLeak) {
			t.Errorf("SanitizeChange(%q) kept the password", value)
		}
	}
}
