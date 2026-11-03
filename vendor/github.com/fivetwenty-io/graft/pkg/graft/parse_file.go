package graft

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readSource reads the raw bytes for one ParseFile/ParseMultiDocFile/
// MergeFiles input path, applying the "-" means STDIN convention graft's
// other file-consuming commands use (JSONifyFiles, cmd/graft's
// readFile/loadYamlFile): STDIN that is not actually piped (still a
// character device) is treated as empty input rather than blocking on a
// read that would never return. displayPath is the name to use in error
// messages ("STDIN" for the STDIN convention, path unchanged otherwise).
//
// Unlike readJSONSource (json.go), read errors are returned unwrapped, so
// os.IsNotExist keeps working against them - ParseFile's documented
// contract (docs/developer-guide/library-api/engine.md) promises exactly
// that for a missing file.
func readSource(path string) (data []byte, displayPath string, err error) {
	if path == "-" {
		displayPath = "STDIN"
		stat, statErr := os.Stdin.Stat()
		if statErr != nil {
			return nil, displayPath, statErr
		}
		if stat.Mode()&os.ModeCharDevice != 0 {
			return []byte{}, displayPath, nil
		}
		data, err = io.ReadAll(os.Stdin)
		return data, displayPath, err
	}

	displayPath = path
	// #nosec G304 - path is caller-provided, matching os.ReadFile's own
	// contract; graft is a file-processing library/CLI.
	data, err = os.ReadFile(path)
	return data, displayPath, err
}

// isJSONPath reports whether path's extension marks it as a JSON source
// for ParseFile's extension-based format dispatch: engine.md documents
// ".json" -> ParseJSON, everything else -> ParseYAML.
func isJSONPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

// ParseFile parses a file into a Document, auto-detecting the format from
// the path's extension: ".json" is parsed with ParseJSON, everything else
// with ParseYAML (YAML is a superset of JSON, so a ".yml"/".yaml" file -
// or one with no recognized extension - containing JSON still parses
// correctly). A path of "-" reads from STDIN instead of disk, matching
// graft's other file-consuming commands; STDIN that is not actually piped
// is treated as empty input rather than blocking.
//
// A YAML document whose root is an array (rather than a map) is retried
// as a go-patch operation list (see ParseGoPatch, NewGoPatchDocument):
// ParseFile has no flag to opt into go-patch parsing the way
// `graft merge --go-patch` does, so the document's own shape is the only
// signal available to decide.
//
// File-read errors are returned exactly as os.ReadFile/os.Stdin.Stat
// produce them (not wrapped), so callers can use os.IsNotExist to tell a
// missing file apart from a parse failure.
func (e *DefaultEngine) ParseFile(path string) (Document, error) {
	data, displayPath, err := readSource(path)
	if err != nil {
		return nil, err
	}

	if isJSONPath(path) {
		doc, jsonErr := e.ParseJSON(data)
		if jsonErr != nil {
			return nil, fmt.Errorf("%s: %w", displayPath, jsonErr)
		}
		return doc, nil
	}

	if rootErr := DetectArrayRoot(data); IsArrayError(rootErr) {
		ops, patchErr := ParseGoPatch(data)
		if patchErr != nil {
			return nil, fmt.Errorf("%s: %w", displayPath, patchErr)
		}
		return NewGoPatchDocument(ops), nil
	}

	doc, yamlErr := e.ParseYAML(data)
	if yamlErr != nil {
		return nil, fmt.Errorf("%s: %w", displayPath, yamlErr)
	}
	return doc, nil
}

// ParseReader parses YAML content (which is a superset of JSON, so plain
// JSON content works too) read from reader into a Document. Unlike
// ParseFile, there is no path/extension to dispatch on and no go-patch
// fallback: a reader has no filename to signal go-patch intent from,
// matching the documented contract (engine.md) that ParseReader always
// treats its content as YAML.
func (e *DefaultEngine) ParseReader(reader io.Reader) (Document, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("error reading input: %w", err)
	}

	return e.ParseYAML(data)
}

// ParseMultiDocFile reads path (or STDIN for "-", per ParseFile's
// convention) and parses it as multi-document YAML, splitting on the
// literal "\n---\n" separator - the library counterpart to the CLI's
// `graft merge --multi-doc`/`graft json --multi-doc` file splitting
// (splitLoadYamlFile in cmd/graft/main.go). Each returned Document is one
// document from the file, in file order.
func (e *DefaultEngine) ParseMultiDocFile(path string) ([]Document, error) {
	data, displayPath, err := readSource(path)
	if err != nil {
		return nil, err
	}

	docs, err := e.ParseMultiDocYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", displayPath, err)
	}
	return docs, nil
}
