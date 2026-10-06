// Package redact hides secrets in text that an error or a log line shows.
package redact

import (
	"encoding/base64"
	"net/url"
	"strings"
)

// unparsableURL stands in for a URL that cannot be parsed, because its text
// may hold a password that cannot be located.
const unparsableURL = "the configured token URL"

// URL returns raw with any password replaced, so that neither an error nor a
// log line repeats it.
func URL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return unparsableURL
	}

	return parsed.Redacted()
}

// Hidden is what replaces a secret in text.
const Hidden = "[REDACTED]"

// Bearer returns text with the bearer token, and its full "Bearer <token>"
// form, replaced by Hidden. A server that echoes the request headers repeats
// the whole Authorization value, so the longer form goes first and no
// "Bearer " is left dangling. An empty token is skipped, because it would
// match between every character.
func Bearer(text, token string) string {
	if token == "" {
		return text
	}

	text = strings.ReplaceAll(text, "Bearer "+token, Hidden)

	return strings.ReplaceAll(text, token, Hidden)
}

// Basic returns text with the HTTP Basic credentials replaced by Hidden. A
// server that echoes the request headers repeats the whole Authorization
// value, which holds the base64 of username:password, so Basic hides that
// value, its base64 payload, and the raw password. The longest form goes
// first, so no "Basic " is left dangling. A needle that is empty is skipped,
// because it would match between every character.
func Basic(text, username, password string) string {
	if username == "" && password == "" {
		return text
	}

	payload := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

	for _, needle := range []string{"Basic " + payload, payload, password} {
		if needle != "" {
			text = strings.ReplaceAll(text, needle, Hidden)
		}
	}

	return text
}
