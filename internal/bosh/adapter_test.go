package bosh_test

import (
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

func (m *mockDirector) GetInfo() (*bosh.Info, error)               { return nil, ErrMockNotImplemented }
func (m *mockDirector) GetDeployments() ([]bosh.Deployment, error) { return nil, ErrMockNotImplemented }
func (m *mockDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) CreateDeployment(manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) DeleteDeployment(name string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetDeploymentVMs(deployment string) ([]bosh.VM, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetReleases() ([]bosh.Release, error) { return nil, ErrMockNotImplemented }
func (m *mockDirector) UploadRelease(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetStemcells() ([]bosh.Stemcell, error) { return nil, ErrMockNotImplemented }
func (m *mockDirector) UploadStemcell(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetTask(id int) (*bosh.Task, error) { return nil, ErrMockNotImplemented }

func (m *mockDirector) GetTasks(taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetAllTasks(limit int) ([]bosh.Task, error) { return nil, ErrMockNotImplemented }
func (m *mockDirector) CancelTask(taskID int) error                { return ErrMockNotImplemented }
func (m *mockDirector) GetTaskOutput(id int, outputType string) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) GetConfig(configType, configName string) (interface{}, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetTaskEvents(id int) ([]bosh.TaskEvent, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetEvents(deployment string) ([]bosh.Event, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) UpdateCloudConfig(config string) error { return ErrMockNotImplemented }
func (m *mockDirector) GetCloudConfig() (string, error)       { return "", ErrMockNotImplemented }
func (m *mockDirector) GetConfigs(limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigVersions(configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigByID(configID string) (*bosh.BoshConfigDetail, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetConfigContent(configID string) (string, error) {
	return "", ErrMockNotImplemented
}
func (m *mockDirector) ComputeConfigDiff(fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) Cleanup(removeAll bool) (*bosh.Task, error) { return nil, ErrMockNotImplemented }
func (m *mockDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "SSH command executed successfully", nil
}
func (m *mockDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"session_id": "mock-session"}, nil
}
func (m *mockDirector) EnableResurrection(deployment string, enabled bool) error {
	return ErrMockNotImplemented
}
func (m *mockDirector) DeleteResurrectionConfig(deployment string) error {
	return ErrMockNotImplemented
}
func (m *mockDirector) RestartDeployment(name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) StopDeployment(name string, opts bosh.StopOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) StartDeployment(name string, opts bosh.StartOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) RecreateDeployment(name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) ListErrands(deployment string) ([]bosh.Errand, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) RunErrand(deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetInstances(deployment string) ([]bosh.Instance, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) UpdateDeployment(name, manifest string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) FindRunningTaskForDeployment(deploymentName string) (*bosh.Task, error) {
	return nil, ErrMockNotImplemented
}
func (m *mockDirector) GetPoolStats() (*bosh.PoolStats, error) {
	return nil, ErrMockNotImplemented
}
