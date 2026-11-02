package broker_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/broker"
	"blacksmith/internal/parity"
)

// TestWriteYamlFileParity records the file WriteYamlFile writes for each
// document, or the error it returns. It sets the instance data directory, so
// it cannot run in parallel.
//
//nolint:paralleltest // sets an environment variable
func TestWriteYamlFileParity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BLACKSMITH_INSTANCE_DATA_DIR", dir+"/")

	var recorded strings.Builder

	for index, doc := range parity.Corpus(t) {
		id := "parity-" + strconv.Itoa(index)

		err := broker.WriteYamlFile(id, []byte(doc.Body))
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "ERROR: "+strings.ReplaceAll(err.Error(), dir, "<dir>")+"\n"))

			continue
		}

		written, readErr := os.ReadFile(filepath.Join(dir, id+".yml")) //nolint:gosec // a test file
		if readErr != nil {
			t.Fatal(readErr)
		}

		recorded.WriteString(parity.Section(doc.Name, string(written)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "write-yaml-file.txt"), recorded.String())
}

// TestYamlGsubParity records the credentials a YAML round trip returns after
// a string is replaced. Replacements that change the YAML syntax are the
// interesting ones, so some of them are hostile.
func TestYamlGsubParity(t *testing.T) {
	t.Parallel()

	credentials := map[string]interface{}{
		"username": "fixture-user",
		"password": "fixture-password-not-real",
		"port":     5432,
		"tls":      true,
		"hosts":    []interface{}{"10.0.0.1", "10.0.0.2"},
		"nested": map[string]interface{}{
			"username": "fixture-user",
			"ratio":    1.5,
			"nothing":  nil,
			"quoted":   "yes",
			"digits":   "0755",
		},
		"uri": "postgres://fixture-user:fixture-password-not-real@10.0.0.1:5432/db",
	}

	replacements := []struct{ name, orig, with string }{
		{"plain", "fixture-user", "dynamic-user"},
		{"password", "fixture-password-not-real", "dynamic-password"},
		{"to-yes", "fixture-user", "yes"},
		{"to-number", "fixture-user", "1e3"},
		{"to-octal", "fixture-user", "0755"},
		{"to-date", "fixture-user", "2026-01-02"},
		{"to-null", "fixture-user", "null"},
		{"to-colon", "fixture-user", "a: b"},
		{"to-hash", "fixture-user", "a #b"},
		{"to-leading-colon", "fixture-user", ":leading"},
		{"to-dash", "fixture-user", "- item"},
		{"to-quote", "fixture-user", `say "hi"`},
		{"to-newline", "fixture-user", "line1\nline2"},
		{"to-broken", "fixture-user", "[unclosed"},
		{"to-brace", "fixture-user", "{a: 1"},
		{"to-empty", "fixture-user", ""},
		{"no-match", "absent-string", "anything"},
		{"empty-orig", "", "x"},
		{"key-name", "username", "login"},
		{"key-to-colon", "password", "pass: word"},
	}

	var recorded strings.Builder

	for _, replacement := range replacements {
		got, err := broker.YamlGsub(credentials, replacement.orig, replacement.with)
		if err != nil {
			recorded.WriteString(parity.Section(replacement.name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		recorded.WriteString(parity.Section(replacement.name, parity.Dump(got)))
	}

	for _, doc := range parity.OddDocuments() {
		got, err := broker.YamlGsub(doc.Body, "fixture", "swapped")
		if err != nil {
			recorded.WriteString(parity.Section("text "+doc.Name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		recorded.WriteString(parity.Section("text "+doc.Name, parity.Dump(got)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "yaml-gsub.dump"), recorded.String())
}
