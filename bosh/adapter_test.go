package bosh_test

import (
	"os"
	"testing"

	"blacksmith/bosh"
)

func TestFactoryCreation(t *testing.T) {
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
	os.Setenv("BLACKSMITH_TEST_MODE", "true")
	defer os.Unsetenv("BLACKSMITH_TEST_MODE")

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
	os.Setenv("BLACKSMITH_TEST_MODE", "true")
	defer os.Unsetenv("BLACKSMITH_TEST_MODE")

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
	// This test just verifies that our interface is properly defined
	var _ bosh.Director = (*mockDirector)(nil)
}

// mockDirector is a simple mock implementation for testing
type mockDirector struct{}

func (m *mockDirector) GetInfo() (*bosh.Info, error)                                 { return nil, nil }
func (m *mockDirector) GetDeployments() ([]bosh.Deployment, error)                   { return nil, nil }
func (m *mockDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error)    { return nil, nil }
func (m *mockDirector) CreateDeployment(manifest string) (*bosh.Task, error)         { return nil, nil }
func (m *mockDirector) DeleteDeployment(name string) (*bosh.Task, error)             { return nil, nil }
func (m *mockDirector) GetDeploymentVMs(deployment string) ([]bosh.VM, error)        { return nil, nil }
func (m *mockDirector) GetReleases() ([]bosh.Release, error)                         { return nil, nil }
func (m *mockDirector) UploadRelease(url, sha1 string) (*bosh.Task, error)           { return nil, nil }
func (m *mockDirector) GetStemcells() ([]bosh.Stemcell, error)                       { return nil, nil }
func (m *mockDirector) UploadStemcell(url, sha1 string) (*bosh.Task, error)          { return nil, nil }
func (m *mockDirector) GetTask(id int) (*bosh.Task, error)                           { return nil, nil }
func (m *mockDirector) GetTaskOutput(id int, outputType string) (string, error)      { return "", nil }
func (m *mockDirector) GetConfig(configType, configName string) (interface{}, error) { return nil, nil }
func (m *mockDirector) GetTaskEvents(id int) ([]bosh.TaskEvent, error)               { return nil, nil }
func (m *mockDirector) GetEvents(deployment string) ([]bosh.Event, error)            { return nil, nil }
func (m *mockDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	return "", nil
}
func (m *mockDirector) UpdateCloudConfig(config string) error      { return nil }
func (m *mockDirector) GetCloudConfig() (string, error)            { return "", nil }
func (m *mockDirector) Cleanup(removeAll bool) (*bosh.Task, error) { return nil, nil }
func (m *mockDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "SSH command executed successfully", nil
}
func (m *mockDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{"session_id": "mock-session"}, nil
}
func (m *mockDirector) EnableResurrection(deployment string, enabled bool) error { return nil }
