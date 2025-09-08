package main

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

func wrap(key string, data map[interface{}]interface{}) map[interface{}]interface{} {
	keyParts := strings.Split(key, ".")

	for i := range len(keyParts) / 2 {
		keyParts[i], keyParts[len(keyParts)-i-1] = keyParts[len(keyParts)-i-1], keyParts[i]
	}

	for _, k := range keyParts {
		data = map[interface{}]interface{}{k: data}
	}

	return data
}

func deinterface(o interface{}) interface{} {
	switch o := o.(type) {
	case map[interface{}]interface{}:
		return deinterfaceMap(o)
	case []interface{}:
		return deinterfaceList(o)
	default:
		return o
	}
}

func deinterfaceMap(o map[interface{}]interface{}) map[string]interface{} {
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

func readFile(path string) ([]byte, bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return []byte{}, false, nil
		}

		return []byte{}, true, fmt.Errorf("failed to stat file %s: %w", path, err)
	}

	b, err := safeReadFile(path)

	return b, true, err
}

func mergeFiles(required string, optional ...string) (map[interface{}]interface{}, error) {
	// Required Manifest
	b, err := safeReadFile(required)
	if err != nil {
		return nil, err
	}

	manifestData := make(map[interface{}]interface{})

	err = yaml.Unmarshal(b, &manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	// Optional Manifests
	for _, path := range optional {
		b, exists, err := readFile(path)
		if !exists {
			continue
		}

		if err != nil {
			return nil, err
		}

		tmp := make(map[interface{}]interface{})

		err = yaml.Unmarshal(b, &tmp)
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

// safeReadFile safely reads a file after validating the path.
func safeReadFile(path string) ([]byte, error) {
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

// safeOpenFile safely opens a file after validating the path.
func safeOpenFile(path string) (*os.File, error) {
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

// validateExecutablePath validates that an executable path is safe to run.
func validateExecutablePath(path string) error {
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
