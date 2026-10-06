package testutil

import (
	"context"
	"errors"

	"blacksmith/internal/bosh"
)

// Mock error constants.
var (
	ErrMockNotImplemented = errors.New("mock method not implemented")
)

// MockBOSHDirector provides a mock implementation of the BOSH Director interface for testing.
type MockBOSHDirector struct {
	vms         map[string][]bosh.VM
	credentials interface{}
	error       error
}

// NewMockBOSHDirector creates a new MockBOSHDirector instance.
func NewMockBOSHDirector() *MockBOSHDirector {
	return &MockBOSHDirector{
		vms: make(map[string][]bosh.VM),
	}
}

// SetVMs sets mock VMs for a given deployment.
func (mb *MockBOSHDirector) SetVMs(deployment string, vms []bosh.VM) {
	mb.vms[deployment] = vms
}

// SetCredentials sets mock credentials.
func (mb *MockBOSHDirector) SetCredentials(creds interface{}) {
	mb.credentials = creds
}

// SetError sets a mock error.
func (mb *MockBOSHDirector) SetError(err error) {
	mb.error = err
}

// GetDeploymentVMs returns VMs for a deployment.
func (mb *MockBOSHDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	if mb.error != nil {
		return nil, mb.error
	}

	vms, exists := mb.vms[deployment]
	if !exists {
		return []bosh.VM{}, nil
	}

	return vms, nil
}

// GetInfo implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetInfo(_ context.Context) (*bosh.Info, error) {
	return nil, ErrMockNotImplemented
}

// GetTask implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// CreateDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// DeleteDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) DeleteDeployment(_ context.Context, deployment string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetDeployment(_ context.Context, deployment string) (*bosh.DeploymentDetail, error) {
	return nil, ErrMockNotImplemented
}

// GetDeployments implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	return nil, ErrMockNotImplemented
}

// GetEvents implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetEvents(_ context.Context, deployment string) ([]bosh.Event, error) {
	return nil, ErrMockNotImplemented
}

// GetReleases implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetReleases(_ context.Context) ([]bosh.Release, error) {
	return nil, ErrMockNotImplemented
}

// UploadRelease implements the BOSH Director interface.
func (mb *MockBOSHDirector) UploadRelease(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetStemcells implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetStemcells(_ context.Context) ([]bosh.Stemcell, error) {
	return nil, ErrMockNotImplemented
}

// UploadStemcell implements the BOSH Director interface.
func (mb *MockBOSHDirector) UploadStemcell(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetTaskOutput implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetTaskOutput(_ context.Context, taskID int, typ string) (string, error) {
	return "", nil
}

// GetConfig implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetConfig(_ context.Context, configType, configName string) (interface{}, error) {
	return nil, ErrMockNotImplemented
}

// GetTaskEvents implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetTaskEvents(_ context.Context, taskID int) ([]bosh.TaskEvent, error) {
	return nil, ErrMockNotImplemented
}

// FetchLogs implements the BOSH Director interface.
func (mb *MockBOSHDirector) FetchLogs(_ context.Context, deployment, job, index string) (string, error) {
	return "", nil
}

// UpdateCloudConfig implements the BOSH Director interface.
func (mb *MockBOSHDirector) UpdateCloudConfig(_ context.Context, yaml string) error { return nil }

// GetCloudConfig implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetCloudConfig(_ context.Context) (string, error) { return "", nil }

// Cleanup implements the BOSH Director interface.
func (mb *MockBOSHDirector) Cleanup(_ context.Context, removeAll bool) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// SSHCommand implements the BOSH Director interface.
func (mb *MockBOSHDirector) SSHCommand(_ context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "", nil
}

// SSHSession implements the BOSH Director interface.
func (mb *MockBOSHDirector) SSHSession(_ context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return nil, ErrMockNotImplemented
}

// EnableResurrection implements the BOSH Director interface.
func (mb *MockBOSHDirector) EnableResurrection(_ context.Context, deployment string, enabled bool) error {
	return nil
}

// DeleteResurrectionConfig implements the BOSH Director interface.
func (mb *MockBOSHDirector) DeleteResurrectionConfig(_ context.Context, deployment string) error {
	return nil
}

// GetTasks implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetTasks(_ context.Context, taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetAllTasks implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetAllTasks(_ context.Context, limit int) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// CancelTask implements the BOSH Director interface.
func (mb *MockBOSHDirector) CancelTask(_ context.Context, taskID int) error { return nil }

// WaitForTask implements the BOSH Director interface.
func (mb *MockBOSHDirector) WaitForTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetConfigs implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetConfigs(_ context.Context, limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}

// GetConfigVersions implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetConfigVersions(_ context.Context, configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}

// GetConfigByID implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetConfigByID(_ context.Context, configID string) (*bosh.BoshConfigDetail, error) {
	return nil, ErrMockNotImplemented
}

// GetConfigContent implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetConfigContent(_ context.Context, configID string) (string, error) {
	return "", nil
}

// ComputeConfigDiff implements the BOSH Director interface.
func (mb *MockBOSHDirector) ComputeConfigDiff(_ context.Context, fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, ErrMockNotImplemented
}

// RestartDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) RestartDeployment(_ context.Context, name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// StopDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) StopDeployment(_ context.Context, name string, opts bosh.StopOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// StartDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) StartDeployment(_ context.Context, name string, opts bosh.StartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// RecreateDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// ListErrands implements the BOSH Director interface.
func (mb *MockBOSHDirector) ListErrands(_ context.Context, deployment string) ([]bosh.Errand, error) {
	return nil, ErrMockNotImplemented
}

// RunErrand implements the BOSH Director interface.
func (mb *MockBOSHDirector) RunErrand(_ context.Context, deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return nil, ErrMockNotImplemented
}

// GetInstances implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetInstances(_ context.Context, deployment string) ([]bosh.Instance, error) {
	return nil, ErrMockNotImplemented
}

// UpdateDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) UpdateDeployment(_ context.Context, name, manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// FindRunningTaskForDeployment implements the BOSH Director interface.
func (mb *MockBOSHDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

// GetPoolStats implements the BOSH Director interface.
func (mb *MockBOSHDirector) GetPoolStats() (*bosh.PoolStats, error) {
	return nil, ErrMockNotImplemented
}
