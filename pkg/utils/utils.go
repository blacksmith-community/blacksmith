package utils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

// Static errors for err113 compliance.
var (
	ErrFilePathCannotBeEmpty              = errors.New("file path cannot be empty")
	ErrPathTraversalDetected              = errors.New("path traversal detected in file path")
	ErrPotentiallyDangerousCharacterFound = errors.New("potentially dangerous character found in executable path")
)

// Wrap wraps data in nested maps using a dot-separated key path.
func Wrap(key string, data map[interface{}]interface{}) map[interface{}]interface{} {
	keyParts := strings.Split(key, ".")

	for i := range len(keyParts) / 2 {
		keyParts[i], keyParts[len(keyParts)-i-1] = keyParts[len(keyParts)-i-1], keyParts[i]
	}

	for _, k := range keyParts {
		data = map[interface{}]interface{}{k: data}
	}

	return data
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

func MergeFiles(required string, optional ...string) (map[interface{}]interface{}, error) {
	// Required Manifest
	requiredBytes, err := SafeReadFile(required)
	if err != nil {
		return nil, err
	}

	manifestData := make(map[interface{}]interface{})

	err = yaml.Unmarshal(requiredBytes, &manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	// Optional Manifests
	for _, path := range optional {
		optionalBytes, exists, err := ReadFile(path)
		if !exists {
			continue
		}

		if err != nil {
			return nil, err
		}

		tmp := make(map[interface{}]interface{})

		err = yaml.Unmarshal(optionalBytes, &tmp)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal optional YAML file %s: %w", path, err)
		}

		manifestData, err = spruce.Merge(manifestData, tmp)
		if err != nil {
			return nil, fmt.Errorf("failed to merge files: %w", err)
		}
	}

	return manifestData, nil
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
