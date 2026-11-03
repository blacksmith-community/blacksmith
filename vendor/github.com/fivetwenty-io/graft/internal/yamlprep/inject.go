// Package yamlprep rewrites YAML bytes before goccy/go-yaml parses them.
// It holds the workarounds graft needs so that goccy reads the same
// documents spruce reads, and every parser entry point in graft applies
// them in the same order through Prepare.
package yamlprep

import (
	"bytes"
	"regexp"
)

// injectKeyStandaloneRe matches <<<: as a standalone map key.
var injectKeyStandaloneRe = regexp.MustCompile(`(?m)^(\s*(?:- )?)<<<:`)

// injectKeyDottedRe matches <<<: at the end of a dotted path key (e.g., host.web1.<<<:).
var injectKeyDottedRe = regexp.MustCompile(`(?m)^(\s*(?:- )?)(\S+\.<<<):`)

// QuoteInjectKeys pre-processes YAML bytes to quote the graft-specific
// <<<: inject key, which goccy/go-yaml rejects when unquoted because it
// reads <<< as a variant of the YAML merge key <<. It handles both the
// standalone (<<<:) and dotted path (foo.<<<:) forms.
func QuoteInjectKeys(data []byte) []byte {
	// Both regexes require a literal "<<<"; almost no document contains
	// one, so skip the regex passes when none is present.
	if !bytes.Contains(data, []byte("<<<")) {
		return data
	}

	// ReplaceAll copies its input even when nothing matches, so each
	// pass runs only on a match and a "<<<" in text keeps the original
	// slice. Dotted paths go first, to avoid quoting them twice.
	if injectKeyDottedRe.Match(data) {
		data = injectKeyDottedRe.ReplaceAll(data, []byte(`${1}"${2}":`))
	}
	if injectKeyStandaloneRe.Match(data) {
		data = injectKeyStandaloneRe.ReplaceAll(data, []byte(`${1}"<<<":`))
	}
	return data
}
