package utils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/merger"
	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// Static errors for err113 compliance.
var (
	ErrFilePathCannotBeEmpty              = errors.New("file path cannot be empty")
	ErrPathTraversalDetected              = errors.New("path traversal detected in file path")
	ErrPotentiallyDangerousCharacterFound = errors.New("potentially dangerous character found in executable path")
)

// Wrap nests data under a dot-separated key path, in the interface-keyed
// form spruce merges. Only the top level of data is re-keyed. Its values
// pass through untouched, so a nested map from a JSON request stays a
// map[string]interface{} that a spruce merge replaces whole, as it did
// when the broker kept its parameters interface-keyed.
func Wrap(key string, data map[string]interface{}) map[interface{}]interface{} {
	keyParts := strings.Split(key, ".")

	for i := range len(keyParts) / 2 {
		keyParts[i], keyParts[len(keyParts)-i-1] = keyParts[len(keyParts)-i-1], keyParts[i]
	}

	wrapped := make(map[interface{}]interface{}, len(data))
	for k, v := range data {
		wrapped[k] = v
	}

	for _, k := range keyParts {
		wrapped = map[interface{}]interface{}{k: wrapped}
	}

	return wrapped
}

// InterfaceMap converts a map with string keys to the map with interface{}
// keys that spruce and yaml.v2 work on. It walks nested maps, including
// interface-keyed ones, and []interface{} lists, copying each, so the result
// shares no map or list with its input. A nil map converts to nil. It is the
// inverse of DeinterfaceMap, and it exists only until spruce leaves the
// module.
func InterfaceMap(o map[string]interface{}) map[interface{}]interface{} {
	if o == nil {
		return nil
	}

	m := make(map[interface{}]interface{}, len(o))
	for k, v := range o {
		m[k] = interfaceValue(v)
	}

	return m
}

func interfaceValue(obj interface{}) interface{} {
	switch value := obj.(type) {
	case map[string]interface{}:
		return InterfaceMap(value)
	case map[interface{}]interface{}:
		if value == nil {
			return value
		}

		m := make(map[interface{}]interface{}, len(value))
		for k, v := range value {
			m[k] = interfaceValue(v)
		}

		return m
	case []interface{}:
		if value == nil {
			return value
		}

		l := make([]interface{}, len(value))
		for i, v := range value {
			l[i] = interfaceValue(v)
		}

		return l
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

// MergeFiles reads the required YAML file and each optional file that
// exists, and merges them in that order with graft's merger. It only
// merges: operators stay as text for the render to evaluate, and a
// (( prune )) marker is not applied. A key a file repeats keeps its last
// value, as yaml.v2 kept it.
func MergeFiles(required string, optional ...string) (map[string]interface{}, error) {
	// Required Manifest
	requiredBytes, err := SafeReadFile(required)
	if err != nil {
		return nil, err
	}

	manifestData, err := decodeMergeFile(requiredBytes)
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

		layer, err := decodeMergeFile(optionalBytes)
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
func decodeMergeFile(data []byte) (map[string]interface{}, error) {
	decoded := make(map[string]interface{})

	err := yaml.UnmarshalWith(data, &decoded, yaml.DecodeOptions{OnDuplicateKey: func(yaml.DuplicateKey) {}})
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller says which file failed
	}

	if len(decoded) == 0 && isNullDocument(data) {
		decoded = nil
	}

	return decoded, nil
}

// isNullDocument reports whether data is a document that holds a null.
func isNullDocument(data []byte) bool {
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
