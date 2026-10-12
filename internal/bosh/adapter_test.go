package bosh_test

import (
	"context"
	"errors"
	"testing"

	"blacksmith/internal/bosh"
)

func TestFactoryCreation(t *testing.T) {
	t.Parallel()
	// Test default factory (now always uses BOSH CLI)
	factory := bosh.NewDefaultFactory()
	if factory == nil {
		t.Fatal("Expected factory to be created")
	}

	// Test explicit factory creation
	factory = bosh.NewFactory()
	if factory == nil {
		t.Fatal("Expected factory to be created")
	}
}

func TestFactoryNew(t *testing.T) {
	// Set test mode to skip network calls
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	factory := bosh.NewFactory()
	if factory == nil {
		t.Fatal("Expected factory to be created")
	}

	// Try to create a director with dummy config
	config := bosh.Config{
		Address:           "https://10.0.0.1:25555",
		Username:          "admin",
		Password:          "password",
		SkipSSLValidation: true,
	}

	director, err := factory.New(config)
	if err != nil {
		t.Errorf("Unexpected error creating director: %v", err)
	}

	if director == nil {
		t.Error("Expected director to be created")
	}
}

func TestCreateDirectorFromLegacyConfig(t *testing.T) {
	// Set test mode to skip network calls
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	// This test verifies the function exists and creates a director
	// The BOSH CLI director doesn't connect until first use
	director, err := bosh.CreateDirectorFromLegacyConfig(
		"https://invalid.bosh.local",
		"admin",
		"password",
		true,
	)

	// The director should be created successfully
	// Connection errors will occur when trying to use it
	if err != nil {
		t.Fatalf("Unexpected error creating director: %v", err)
	}

	if director == nil {
		t.Fatal("Expected director to be created")
	}
}

func TestDirectorInterface(t *testing.T) {
	t.Parallel()
	// This test just verifies that our interface is properly defined
	var _ bosh.Director = (*mockDirector)(nil)
}

// Errors for mock director.
var (
	ErrMockNotImplemented = errors.New("mock method not implemented")
)

// mockDirector is a simple mock implementation for testing.
type mockDirector struct{}

func (m *mockDirector) GetInfo(_ context.Context) (*bosh.Info, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetReleases(_ context.Context) ([]bosh.Release, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) UploadRelease(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetStemcells(_ context.Context) ([]bosh.Stemcell, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) UploadStemcell(_ context.Context, url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetTask(_ context.Context, id int) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}

func (m *mockDirector) GetTasks(_ context.Context, taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetAllTasks(_ context.Context, limit int) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) CancelTask(_ context.Context, taskID int) error { return ErrMockNotImplemented }

func (m *mockDirector) WaitForTask(_ context.Context, taskID int) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetTaskOutput(_ context.Context, id int, outputType string) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) GetConfig(_ context.Context, configType, configName string) (interface{}, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetTaskEvents(_ context.Context, id int) ([]bosh.TaskEvent, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetEvents(_ context.Context, deployment string) ([]bosh.Event, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FetchLogs(_ context.Context, deployment string, jobName string, instanceID string, _ *bosh.LogBudget) (*bosh.LogBundle, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) UpdateCloudConfig(_ context.Context, config string) error {
	return ErrMockNotImplemented
}
func (m *mockDirector) ListCloudConfigs(_ context.Context) ([]bosh.CloudConfigEntry, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetCloudConfig(_ context.Context) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) GetConfigs(_ context.Context, limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigVersions(_ context.Context, configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigByID(_ context.Context, configID string) (*bosh.BoshConfigDetail, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigContent(_ context.Context, configID string) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) ComputeConfigDiff(_ context.Context, fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) Cleanup(_ context.Context, removeAll bool) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) SSHCommand(_ context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "SSH command executed successfully", nil
}
func (m *mockDirector) SSHSession(_ context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"session_id": "mock-session"}, nil
}
func (m *mockDirector) EnableResurrection(_ context.Context, deployment string, enabled bool) error {
	return ErrMockNotImplemented
}
func (m *mockDirector) DeleteResurrectionConfig(_ context.Context, deployment string) error {
	return ErrMockNotImplemented
}
func (m *mockDirector) RestartDeployment(_ context.Context, name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) StopDeployment(_ context.Context, name string, opts bosh.StopOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) StartDeployment(_ context.Context, name string, opts bosh.StartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) ListErrands(_ context.Context, deployment string) ([]bosh.Errand, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) RunErrand(_ context.Context, deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetInstances(_ context.Context, deployment string) ([]bosh.Instance, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) UpdateDeployment(_ context.Context, name, manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FindRunningTasksForDeployment(_ context.Context, _ string) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FindTaskByContextID(_ context.Context, _ string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetPoolStats() (*bosh.PoolStats, error) {
	return nil, ErrMockNotImplemented
}
