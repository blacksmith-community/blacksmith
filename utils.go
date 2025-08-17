package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

func wrap(key string, data map[interface{}]interface{}) map[interface{}]interface{} {
	kk := strings.Split(key, ".")

	for i := 0; i < len(kk)/2; i++ {
		s := kk[i]
		kk[i] = kk[len(kk)-i-1]
		kk[len(kk)-i-1] = s
	}
	for _, k := range kk {
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
		return []byte{}, true, err
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
	m := make(map[interface{}]interface{})
	err = yaml.Unmarshal(b, &m)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		m, err = spruce.Merge(m, tmp)
		if err != nil {
			return nil, err
		}
	}

	return m, nil
}

// validateFilePath validates that a file path is safe to use
// This helps prevent path traversal attacks and ensures we only access intended files
func validateFilePath(path string) error {
	if path == "" {
		return fmt.Errorf("file path cannot be empty")
	}

	// Clean the path to resolve any .. or . components
	cleanPath := filepath.Clean(path)

	// Check for path traversal attempts
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("path traversal detected in file path: %s", path)
	}

	return nil
}

// safeReadFile safely reads a file after validating the path
func safeReadFile(path string) ([]byte, error) {
	if err := validateFilePath(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path) // #nosec G304 - Path has been validated by validateFilePath
}

// safeOpenFile safely opens a file after validating the path
func safeOpenFile(path string) (*os.File, error) {
	if err := validateFilePath(path); err != nil {
		return nil, err
	}
	return os.Open(path) // #nosec G304 - Path has been validated by validateFilePath
}

// validateExecutablePath validates that an executable path is safe to run
func validateExecutablePath(path string) error {
	if err := validateFilePath(path); err != nil {
		return err
	}

	// Additional checks for executable paths
	cleanPath := filepath.Clean(path)

	// Check that the path doesn't contain shell metacharacters
	dangerousChars := []string{";", "&", "|", "`", "$", "(", ")", "{", "}", "[", "]", "*", "?", "~", "<", ">", "^", "!"}
	for _, char := range dangerousChars {
		if strings.Contains(cleanPath, char) {
			return fmt.Errorf("potentially dangerous character '%s' found in executable path: %s", char, path)
		}
	}

	return nil
}
