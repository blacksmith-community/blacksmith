package testutil

import (
	"errors"
)

// Static errors for test err113 compliance.
var (
	ErrUnsupportedOutputType = errors.New("unsupported output type")
	ErrUnsupportedDataType   = errors.New("unsupported data type")
)

// MockVault provides a mock implementation of the Vault interface for testing.
type MockVault struct {
	data   map[string]map[string]interface{}
	errors map[string]error
}

// NewMockVault creates a new MockVault instance.
func NewMockVault() *MockVault {
	return &MockVault{
		data:   make(map[string]map[string]interface{}),
		errors: make(map[string]error),
	}
}

// SetData sets mock data for a given path.
func (mv *MockVault) SetData(path string, data map[string]interface{}) {
	mv.data[path] = data
}

// SetError sets a mock error for a given path.
func (mv *MockVault) SetError(path string, err error) {
	mv.errors[path] = err
}

// Get retrieves data from the mock vault.
func (mv *MockVault) Get(path string, out interface{}) (bool, error) {
	if err, exists := mv.errors[path]; exists {
		return false, err
	}

	data, exists := mv.data[path]
	if !exists {
		return false, nil
	}

	if outMap, ok := out.(*map[string]interface{}); ok {
		*outMap = data

		return true, nil
	}

	return false, ErrUnsupportedOutputType
}

// Put stores data in the mock vault.
func (mv *MockVault) Put(path string, data interface{}) error {
	if dataMap, ok := data.(map[string]interface{}); ok {
		mv.data[path] = dataMap

		return nil
	}

	return ErrUnsupportedDataType
}

// Index implements the Vault interface.
func (mv *MockVault) Index(instanceID string, data map[string]interface{}) error { return nil }

// GetIndex implements the Vault interface.
func (mv *MockVault) GetIndex(name string) (interface{}, error) { return nil, ErrUnsupportedOutputType }

// TrackProgress implements the Vault interface.
func (mv *MockVault) TrackProgress(instanceID, operation, message string, taskID int, params map[interface{}]interface{}) error {
	return nil
}
