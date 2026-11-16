package graft

import (
	"context"
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

// parseFile is the body of ParseFile and ParseFileContext. It checks ctx
// between reading and parsing, and the parse itself checks it again.
func (e *DefaultEngine) parseFile(ctx context.Context, path string) (Document, error) {
	if err := parseCanceled(ctx, "reading "+path); err != nil {
		return nil, err
	}
	data, displayPath, err := readSource(path)
	if err != nil {
		return nil, err
	}

	if isJSONPath(path) {
		doc, jsonErr := e.ParseJSONContext(ctx, data)
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
		// This route skips ParseYAMLContext's check after parsing.
		// ParseFileContext checks ctx once more before it returns.
		return NewGoPatchDocument(ops), nil
	}

	doc, yamlErr := e.ParseYAMLContext(ctx, data)
	if yamlErr != nil {
		return nil, fmt.Errorf("%s: %w", displayPath, yamlErr)
	}
	return doc, nil
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
