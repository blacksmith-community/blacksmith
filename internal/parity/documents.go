package parity

import (
	"sort"
	"testing"
)

// Document is one named piece of YAML text for a parity test to feed a
// reader.
type Document struct {
	Name string
	Body string
}

// oddDocuments are texts whose reading depends on the YAML library. The
// secret-looking values are placeholders.
var oddDocuments = map[string]string{
	"empty":        "",
	"comment-only": "# nothing here\n",
	"null":         "~\n",
	"scalar-root":  "just a string\n",
	"list-root":    "- 1\n- two\n- yes\n",
	"unterminated": "name: fixture\nproperties: {a: 1\n",
	"tab-indent":   "name: fixture\nproperties:\n\ta: 1\n",
	"yaml11-scalars": `name: fixture
properties:
  truthy: [yes, no, on, off, y, n, Yes, NO, True, FALSE]
  numbers: [1e3, 0755, 0o17, 0x1f, 1_000, .5, 1.10, +3, -0, .inf, .NaN]
  nulls: [~, null, Null, ""]
  dates: [2026-01-02, 2026-01-02T03:04:05Z, 12:30:45]
  strings: ["yes", "1e3", '0755', ":leading-colon", "a: b", "- x", "# not a comment"]
  octal-ish: [08, 09, 010, 0b101]
  version: 1.10
  password: fixture-password-not-real
`,
	"anchors-and-merge": `name: fixture
defaults: &defaults
  port: 5432
  user: fixture-user
properties:
  primary:
    <<: *defaults
    host: a.example.test
  replica:
    <<: *defaults
    port: 5433
    host: b.example.test
  list: &items [one, two]
  again: *items
`,
	"duplicate-keys": "name: first\nname: second\nproperties:\n  a: 1\n  a: 2\n",
	"non-string-keys": `name: fixture
properties:
  1: one
  true: yes-key
  1.5: float-key
  ~: null-key
  [a, b]: not-supported-everywhere
`,
	"multiline": `name: fixture
properties:
  literal: |
    line one
    line two
  folded: >
    folded one
    folded two
  keep: |+
    kept

  strip: |-
    stripped
  quoted: "tab\there\nnewline"
`,
	"blacksmith-metadata": `name: fixture-deployment
properties:
  blacksmith:
    service_id: fixture-service
    plan_id: fixture-plan
    instance_id: 00000000-0000-0000-0000-000000000001
meta:
  blacksmith:
    instance_id: 00000000-0000-0000-0000-000000000001
instance_groups:
- name: fixture-group
  instances: 3
  jobs:
  - name: fixture-job
    release: fixture-release
releases:
- name: fixture-release
  version: 1.2.3
stemcells:
- alias: default
  os: ubuntu-noble
  version: latest
`,
	"mixed-group-names": `name: fixture
instance_groups:
- name: 123
- name: second
`,
	"instance-groups-not-a-list": "name: fixture\ninstance_groups: {name: oops}\n",
	"binary-and-tags":            "name: fixture\nproperties:\n  blob: !!binary aGVsbG8=\n  forced: !!str 123\n  floaty: !!float 3\n  seq: !!seq [a]\n",
}

// OddDocuments returns the library-sensitive texts, sorted by name.
func OddDocuments() []Document {
	names := make([]string, 0, len(oddDocuments))

	for name := range oddDocuments {
		names = append(names, name)
	}

	sort.Strings(names)

	documents := make([]Document, 0, len(names))

	for _, name := range names {
		documents = append(documents, Document{Name: name, Body: oddDocuments[name]})
	}

	return documents
}

// Corpus returns the rendered forge manifests, prefixed "forge_", followed
// by the odd documents, prefixed "odd_".
func Corpus(t *testing.T) []Document {
	t.Helper()

	var corpus []Document

	for _, rendered := range RenderedManifests(t) {
		corpus = append(corpus, Document{Name: "forge_" + rendered.Name, Body: rendered.Body})
	}

	for _, odd := range OddDocuments() {
		corpus = append(corpus, Document{Name: "odd_" + odd.Name, Body: odd.Body})
	}

	return corpus
}

// Section formats one named result for a golden that holds many.
func Section(name, body string) string {
	if body != "" && body[len(body)-1] != '\n' {
		body += "\n"
	}

	return "=== " + name + " ===\n" + body
}
