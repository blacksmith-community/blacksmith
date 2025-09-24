package testutil

import (
	"errors"
	"fmt"

	"blacksmith/internal/bosh"
)

// Mock error constants.
var (
	ErrIntegrationMockNotImplemented = errors.New("integration mock method not implemented")
	ErrIntegrationTestError          = errors.New("integration test error")
)

// IntegrationMockBOSH provides a mock implementation of the BOSH Director interface for integration testing.
type IntegrationMockBOSH struct {
	vms    map[string][]bosh.VM
	errors map[string]string
}

// NewIntegrationMockBOSH creates a new IntegrationMockBOSH instance.
func NewIntegrationMockBOSH() *IntegrationMockBOSH {
	return &IntegrationMockBOSH{
		vms:    make(map[string][]bosh.VM),
		errors: make(map[string]string),
	}
}

// SetVMs sets mock VMs for a given deployment.
func (imb *IntegrationMockBOSH) SetVMs(deployment string, vms []bosh.VM) {
	imb.vms[deployment] = vms
}

// SetError sets a mock error for a given operation.
func (imb *IntegrationMockBOSH) SetError(operation, errorMsg string) {
	imb.errors[operation] = errorMsg
}

// GetDeploymentVMs returns VMs for a deployment.
func (imb *IntegrationMockBOSH) GetDeploymentVMs(deployment string) ([]bosh.VM, error) {
	if errorMsg, exists := imb.errors["GetDeploymentVMs"]; exists {
		return nil, fmt.Errorf("%w: %s", ErrIntegrationTestError, errorMsg)
	}

	vms, exists := imb.vms[deployment]
	if !exists {
		return []bosh.VM{}, nil
	}

	return vms, nil
}

// GetInfo implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetInfo() (*bosh.Info, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetTask implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTask(taskID int) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// CreateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) CreateDeployment(manifest string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// DeleteDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) DeleteDeployment(deployment string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetDeployment(deployment string) (*bosh.DeploymentDetail, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetDeployments implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetDeployments() ([]bosh.Deployment, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetEvents implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetEvents(deployment string) ([]bosh.Event, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetReleases implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetReleases() ([]bosh.Release, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UploadRelease implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UploadRelease(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetStemcells implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetStemcells() ([]bosh.Stemcell, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UploadStemcell implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UploadStemcell(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetTaskOutput implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTaskOutput(taskID int, typ string) (string, error) { return "", nil }

// GetTaskEvents implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTaskEvents(taskID int) ([]bosh.TaskEvent, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// FetchLogs implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) FetchLogs(deployment, job, index string) (string, error) {
	return "", nil
}

// UpdateCloudConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UpdateCloudConfig(yaml string) error { return nil }

// GetCloudConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetCloudConfig() (string, error) { return "", nil }

// Cleanup implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) Cleanup(removeAll bool) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// SSHCommand implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "", nil
}

// SSHSession implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfig(configType, configName string) (interface{}, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// EnableResurrection implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) EnableResurrection(deployment string, enabled bool) error { return nil }

// DeleteResurrectionConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) DeleteResurrectionConfig(deployment string) error { return nil }

// GetTasks implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTasks(taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetAllTasks implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetAllTasks(limit int) ([]bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// CancelTask implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) CancelTask(taskID int) error { return nil }

// GetConfigs implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigs(limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigVersions implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigVersions(configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigByID implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigByID(configID string) (*bosh.BoshConfigDetail, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigContent implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigContent(configID string) (string, error) {
	return "", nil
}

// ComputeConfigDiff implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) ComputeConfigDiff(fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RestartDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RestartDeployment(name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// StopDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) StopDeployment(name string, opts bosh.StopOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// StartDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) StartDeployment(name string, opts bosh.StartOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RecreateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RecreateDeployment(name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// ListErrands implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) ListErrands(deployment string) ([]bosh.Errand, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RunErrand implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RunErrand(deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetInstances implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetInstances(deployment string) ([]bosh.Instance, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UpdateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UpdateDeployment(name, manifest string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetPoolStats implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetPoolStats() (*bosh.PoolStats, error) {
	return nil, ErrIntegrationMockNotImplemented
}
