package testutil

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"blacksmith/internal/bosh"
)

// Mock error constants.
var (
	ErrIntegrationMockNotImplemented = errors.New("integration mock method not implemented")
	ErrIntegrationTestError          = errors.New("integration test error")
)

// IntegrationMockBOSH provides a mock implementation of the BOSH Director interface for integration testing.
type IntegrationMockBOSH struct {
	mu     sync.RWMutex
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
	imb.mu.Lock()
	defer imb.mu.Unlock()

	imb.vms[deployment] = vms
}

// SetError sets a mock error for a given operation.
func (imb *IntegrationMockBOSH) SetError(operation, errorMsg string) {
	imb.mu.Lock()
	defer imb.mu.Unlock()

	imb.errors[operation] = errorMsg
}

// GetDeploymentVMs returns VMs for a deployment.
func (imb *IntegrationMockBOSH) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	imb.mu.RLock()
	defer imb.mu.RUnlock()

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
func (imb *IntegrationMockBOSH) GetInfo(_ context.Context) (*bosh.Info, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetTask implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// CreateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// DeleteDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) DeleteDeployment(_ context.Context, deployment string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetDeployment(_ context.Context, deployment string) (*bosh.DeploymentDetail, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetDeployments implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetEvents implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetEvents(_ context.Context, deployment string) ([]bosh.Event, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetReleases implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetReleases(_ context.Context) ([]bosh.Release, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UploadRelease implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UploadRelease(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetStemcells implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetStemcells(_ context.Context) ([]bosh.Stemcell, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UploadStemcell implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UploadStemcell(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetTaskOutput implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTaskOutput(_ context.Context, taskID int, typ string) (string, error) {
	return "", nil
}

// GetTaskEvents implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTaskEvents(_ context.Context, taskID int) ([]bosh.TaskEvent, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// FetchLogs implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) FetchLogs(_ context.Context, deployment, job, index string) (string, error) {
	return "", nil
}

// UpdateCloudConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UpdateCloudConfig(_ context.Context, yaml string) error { return nil }

// GetCloudConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetCloudConfig(_ context.Context) (string, error) { return "", nil }

// ListCloudConfigs implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) ListCloudConfigs(_ context.Context) ([]bosh.CloudConfigEntry, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// Cleanup implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) Cleanup(_ context.Context, removeAll bool) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// SSHCommand implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) SSHCommand(_ context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "", nil
}

// SSHSession implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) SSHSession(_ context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfig(_ context.Context, configType, configName string) (interface{}, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// EnableResurrection implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) EnableResurrection(_ context.Context, deployment string, enabled bool) error {
	return nil
}

// DeleteResurrectionConfig implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) DeleteResurrectionConfig(_ context.Context, deployment string) error {
	return nil
}

// GetTasks implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetTasks(_ context.Context, taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetAllTasks implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetAllTasks(_ context.Context, limit int) ([]bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// CancelTask implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) CancelTask(_ context.Context, taskID int) error { return nil }

// WaitForTask implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) WaitForTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigs implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigs(_ context.Context, limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigVersions implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigVersions(_ context.Context, configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigByID implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigByID(_ context.Context, configID string) (*bosh.BoshConfigDetail, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetConfigContent implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetConfigContent(_ context.Context, configID string) (string, error) {
	return "", nil
}

// ComputeConfigDiff implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) ComputeConfigDiff(_ context.Context, fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RestartDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RestartDeployment(_ context.Context, name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// StopDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) StopDeployment(_ context.Context, name string, opts bosh.StopOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// StartDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) StartDeployment(_ context.Context, name string, opts bosh.StartOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RecreateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// ListErrands implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) ListErrands(_ context.Context, deployment string) ([]bosh.Errand, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// RunErrand implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) RunErrand(_ context.Context, deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetInstances implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetInstances(_ context.Context, deployment string) ([]bosh.Instance, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// UpdateDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) UpdateDeployment(_ context.Context, name, manifest string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// FindRunningTaskForDeployment implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// FindTaskByContextID implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) FindTaskByContextID(_ context.Context, _ string) (*bosh.Task, error) {
	return nil, ErrIntegrationMockNotImplemented
}

// GetPoolStats implements the BOSH Director interface.
func (imb *IntegrationMockBOSH) GetPoolStats() (*bosh.PoolStats, error) {
	return nil, ErrIntegrationMockNotImplemented
}
