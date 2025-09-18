package testutil

import (
	"errors"
	"fmt"
)

// Static errors for integration test err113 compliance.
var (
	ErrIntegrationTestError       = errors.New("integration test error")
	ErrIntegrationUnsupportedType = errors.New("unsupported output type")
	ErrIntegrationDataType        = errors.New("unsupported data type")
)

// IntegrationMockVault provides a mock implementation of the Vault interface for integration testing.
type IntegrationMockVault struct {
	data   map[string]map[string]interface{}
	errors map[string]string
}

// NewIntegrationMockVault creates a new IntegrationMockVault instance.
func NewIntegrationMockVault() *IntegrationMockVault {
	return &IntegrationMockVault{
		data:   make(map[string]map[string]interface{}),
		errors: make(map[string]string),
	}
}

// SetData sets mock data for a given path.
func (imv *IntegrationMockVault) SetData(path string, data map[string]interface{}) {
	imv.data[path] = data
}

// GetData retrieves mock data for a given path.
func (imv *IntegrationMockVault) GetData(path string) map[string]interface{} {
	return imv.data[path]
}

// SetError sets a mock error for a given path.
func (imv *IntegrationMockVault) SetError(path, errorMsg string) {
	imv.errors[path] = errorMsg
}

// Get retrieves data from the mock vault.
func (imv *IntegrationMockVault) Get(path string, out interface{}) (bool, error) {
	if errorMsg, exists := imv.errors[path]; exists {
		return false, fmt.Errorf("%w: %s", ErrIntegrationTestError, errorMsg)
	}

	data, exists := imv.data[path]
	if !exists {
		return false, nil
	}

	if outMap, ok := out.(*map[string]interface{}); ok {
		*outMap = data

		return true, nil
	}

	return false, ErrIntegrationUnsupportedType
}

// Put stores data in the mock vault.
func (imv *IntegrationMockVault) Put(path string, data interface{}) error {
	if dataMap, ok := data.(map[string]interface{}); ok {
		imv.data[path] = dataMap

		return nil
	}

	return ErrIntegrationDataType
}

// Index implements the Vault interface.
func (imv *IntegrationMockVault) Index(instanceID string, data map[string]interface{}) error {
	return nil
}

// GetIndex implements the Vault interface.
func (imv *IntegrationMockVault) GetIndex(name string) (interface{}, error) { return nil, ErrIntegrationUnsupportedType }

// TrackProgress implements the Vault interface.
func (imv *IntegrationMockVault) TrackProgress(instanceID, operation, message string, taskID int, params map[interface{}]interface{}) error {
	return nil
}
