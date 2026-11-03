package utils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/merger"
	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// Static errors for err113 compliance.
var (
	ErrFilePathCannotBeEmpty              = errors.New("file path cannot be empty")
	ErrPathTraversalDetected              = errors.New("path traversal detected in file path")
	ErrPotentiallyDangerousCharacterFound = errors.New("potentially dangerous character found in executable path")
)

// Wrap nests data under a dot-separated key path. Each map in data below
// its top level, including a map that is an item of a top-level list, is
// wrapped as a graft literal. A merge replaces a literal whole and never
// merges into it, and evaluation never looks inside it, so a nested map in
// a request stays inert and replaces the plan's default whole, as it did
// when spruce saw it as one opaque value. A top-level string, and each
// string item of a top-level list, is left as it is, so the merge
// evaluates any operator it holds.
func Wrap(key string, data map[string]interface{}) map[string]interface{} {
	keyParts := strings.Split(key, ".")

	for i := range len(keyParts) / 2 {
		keyParts[i], keyParts[len(keyParts)-i-1] = keyParts[len(keyParts)-i-1], keyParts[i]
	}

	wrapped := make(map[string]interface{}, len(data))
	for k, v := range data {
		wrapped[k] = inertMaps(v)
	}

	for _, k := range keyParts {
		wrapped = map[string]interface{}{k: wrapped}
	}

	return wrapped
}

// inertMaps wraps value as a graft literal when it is a string-keyed map,
// and walks a list to wrap each such map in it. Any other value is
// returned as it is.
func inertMaps(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return graft.NewLiteral(typed)
	case []interface{}:
		if typed == nil {
			return typed
		}

		items := make([]interface{}, len(typed))
		for i, item := range typed {
			items[i] = inertMaps(item)
		}

		return items
	default:
		return value
	}
}

func deinterface(obj interface{}) interface{} {
	switch value := obj.(type) {
	case map[interface{}]interface{}:
		return DeinterfaceMap(value)
	case []interface{}:
		return deinterfaceList(value)
	default:
		return value
	}
}

// DeinterfaceMap converts a map with interface{} keys to a map with string keys.
func DeinterfaceMap(o map[interface{}]interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for k, v := range o {
		m[fmt.Sprintf("%v", k)] = deinterface(v)
	}

	return m
}

func deinterfaceList(o []interface{}) []interface{} {
	l := make([]interface{}, len(o))
	for i, v := range o {
		l[i] = deinterface(v)
	}

	return l
}

func ReadFile(path string) ([]byte, bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []byte{}, false, nil
		}

		return []byte{}, true, fmt.Errorf("failed to stat file %s: %w", path, err)
	}

	fileBytes, err := SafeReadFile(path)

	return fileBytes, true, err
}

// DuplicateKeyFunc receives the path of a YAML file and the path of a key
// that the file repeats. It never receives the key's value.
type DuplicateKeyFunc func(file, key string)

// MergeFiles reads the required YAML file and each optional file that
// exists, and merges them in that order with graft's merger. It evaluates
// nothing, so operators stay as text for the render to evaluate.
//
// A (( prune )) that replaces a map key leaves the earlier content in
// place, and the marker is dropped, so no later evaluation prunes the
// path. Spruce queued that path in a process-wide list that the next
// evaluation applied, whichever render it was, so a prune in one plan
// could reach another plan's render, and that is not kept. A (( prune ))
// over a key the base does not have survives as the literal text
// "(( prune ))". A (( prune )) given as a list entry removes the entry at
// its index during the merge.
//
// When onDuplicate is not nil, a key that a file repeats keeps its last
// value and onDuplicate hears of it. When onDuplicate is nil, a repeated
// key fails the merge.
func MergeFiles(onDuplicate DuplicateKeyFunc, required string, optional ...string) (map[string]interface{}, error) {
	// Required Manifest
	requiredBytes, err := SafeReadFile(required)
	if err != nil {
		return nil, err
	}

	manifestData, err := decodeMergeFile(required, requiredBytes, onDuplicate)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	layers := []map[string]interface{}{manifestData}

	// Optional Manifests
	for _, path := range optional {
		optionalBytes, exists, err := ReadFile(path)
		if !exists {
			continue
		}

		if err != nil {
			return nil, err
		}

		layer, err := decodeMergeFile(path, optionalBytes, onDuplicate)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal optional YAML file %s: %w", path, err)
		}

		layers = append(layers, layer)
	}

	if len(layers) == 1 {
		return manifestData, nil
	}

	merged, err := merger.Merge(layers...)
	if err != nil {
		return nil, fmt.Errorf("failed to merge files: %w", err)
	}

	return merged, nil
}

// decodeMergeFile decodes one file of a merge. An empty file decodes to an
// empty map and a file that holds only a null to a nil one, as yaml.v2
// decoded them.
func decodeMergeFile(path string, data []byte, onDuplicate DuplicateKeyFunc) (map[string]interface{}, error) {
	decoded := make(map[string]interface{})

	var options yaml.DecodeOptions
	if onDuplicate != nil {
		options.OnDuplicateKey = func(key yaml.DuplicateKey) { onDuplicate(path, key.Path) }
	}

	err := yaml.UnmarshalWith(data, &decoded, options)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller says which file failed
	}

	if len(decoded) == 0 && IsNullDocument(data) {
		decoded = nil
	}

	return decoded, nil
}

// IsNullDocument reports whether data is a document that holds a null.
// It is true for ~, for null, and for a bare --- document start, which
// holds an implicit null. It is false for an empty file and for one that
// holds only comments, because neither holds a document. Only the first
// document counts. yaml.v2 told these cases apart the same way: a null
// cleared the map it decoded into, and an empty file left it alone.
func IsNullDocument(data []byte) bool {
	var node yaml.Node

	if yaml.Unmarshal(data, &node) != nil || node.Kind != yaml.DocumentNode || len(node.Content) != 1 {
		return false
	}

	root := node.Content[0]

	return root.Kind == yaml.ScalarNode && root.Tag == "!!null"
}

// validateFilePath validates that a file path is safe to use
// This helps prevent path traversal attacks and ensures we only access intended files.
func validateFilePath(path string) error {
	if path == "" {
		return ErrFilePathCannotBeEmpty
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(path)

	// Check for path traversal attempts
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("path traversal detected in file path %s: %w", path, ErrPathTraversalDetected)
	}

	return nil
}

// SafeReadFile safely reads a file after validating the path.
func SafeReadFile(path string) ([]byte, error) {
	err := validateFilePath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path) // #nosec G304 - Path has been validated by validateFilePath
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return data, nil
}

// SafeOpenFile safely opens a file after validating the path.
func SafeOpenFile(path string) (*os.File, error) {
	err := validateFilePath(path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(path) // #nosec G304 - Path has been validated by validateFilePath
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", path, err)
	}

	return file, nil
}

// ValidateExecutablePath validates that an executable path is safe to run.
func ValidateExecutablePath(path string) error {
	err := validateFilePath(path)
	if err != nil {
		return err
	}

	// Additional checks for executable paths
	cleanPath := filepath.Clean(path)

	// Check that the path doesn't contain shell metacharacters
	dangerousChars := []string{";", "&", "|", "`", "$", "(", ")", "{", "}", "[", "]", "*", "?", "~", "<", ">", "^", "!"}
	for _, char := range dangerousChars {
		if strings.Contains(cleanPath, char) {
			return fmt.Errorf("potentially dangerous character '%s' found in executable path %s: %w", char, path, ErrPotentiallyDangerousCharacterFound)
		}
	}

	return nil
}
