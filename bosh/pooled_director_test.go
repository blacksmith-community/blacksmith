package bosh

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockDirector for testing
type MockDirector struct {
	delay     time.Duration
	callCount int
	mu        sync.Mutex
}

func (m *MockDirector) incrementCallCount() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
}

func (m *MockDirector) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *MockDirector) GetInfo() (*Info, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Info{
		Name:    "test-director",
		UUID:    "test-uuid",
		Version: "1.0.0",
	}, nil
}

func (m *MockDirector) GetDeployments() ([]Deployment, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Deployment{
		{Name: "test-deployment"},
	}, nil
}

func (m *MockDirector) GetDeployment(name string) (*DeploymentDetail, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &DeploymentDetail{Name: name}, nil
}

func (m *MockDirector) CreateDeployment(manifest string) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: 1}, nil
}

func (m *MockDirector) DeleteDeployment(name string) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: 2}, nil
}

func (m *MockDirector) GetDeploymentVMs(deployment string) ([]VM, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []VM{}, nil
}

func (m *MockDirector) GetReleases() ([]Release, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Release{}, nil
}

func (m *MockDirector) UploadRelease(url string, sha1 string) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: 3}, nil
}

func (m *MockDirector) GetStemcells() ([]Stemcell, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Stemcell{}, nil
}

func (m *MockDirector) UploadStemcell(url string, sha1 string) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: 4}, nil
}

func (m *MockDirector) GetTask(id int) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: id}, nil
}

func (m *MockDirector) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Task{}, nil
}

func (m *MockDirector) GetAllTasks(limit int) ([]Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Task{}, nil
}

func (m *MockDirector) CancelTask(taskID int) error {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return nil
}

func (m *MockDirector) GetTaskOutput(id int, outputType string) (string, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return "test output", nil
}

func (m *MockDirector) GetTaskEvents(id int) ([]TaskEvent, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []TaskEvent{}, nil
}

func (m *MockDirector) GetEvents(deployment string) ([]Event, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []Event{}, nil
}

func (m *MockDirector) UpdateCloudConfig(config string) error {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return nil
}

func (m *MockDirector) GetCloudConfig() (string, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return "test-cloud-config", nil
}

func (m *MockDirector) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []BoshConfig{}, nil
}

func (m *MockDirector) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return []BoshConfig{}, nil
}

func (m *MockDirector) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &BoshConfigDetail{}, nil
}

func (m *MockDirector) GetConfigContent(configID string) (string, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return "test-config", nil
}

func (m *MockDirector) GetConfig(configType, configName string) (interface{}, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return map[string]interface{}{}, nil
}

func (m *MockDirector) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &ConfigDiff{}, nil
}

func (m *MockDirector) Cleanup(removeAll bool) (*Task, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return &Task{ID: 5}, nil
}

func (m *MockDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return "/tmp/logs.tar.gz", nil
}

func (m *MockDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return "command output", nil
}

func (m *MockDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return nil, nil
}

func (m *MockDirector) EnableResurrection(deployment string, enabled bool) error {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return nil
}

func (m *MockDirector) DeleteResurrectionConfig(deploymentName string) error {
	m.incrementCallCount()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return nil
}

// Tests

func TestPooledDirector_ConcurrentRequests(t *testing.T) {
	mockDirector := &MockDirector{}
	pooled := NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Test concurrent requests exceed pool size
	var wg sync.WaitGroup
	results := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := pooled.GetDeployments()
			results <- err
		}()
	}

	wg.Wait()
	close(results)

	// Verify all requests completed
	successCount := 0
	for err := range results {
		if err == nil {
			successCount++
		}
	}

	if successCount != 10 {
		t.Errorf("Expected 10 successful requests, got %d", successCount)
	}

	// Verify call count matches
	if mockDirector.getCallCount() != 10 {
		t.Errorf("Expected 10 calls to mock director, got %d", mockDirector.getCallCount())
	}

	// Verify pool stats
	stats := pooled.GetPoolStats()
	if stats.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", stats.TotalRequests)
	}
	if stats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after completion, got %d", stats.ActiveConnections)
	}
}

func TestPooledDirector_Timeout(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 10 * time.Second, // Slow responses
	}
	pooled := NewPooledDirector(mockDirector, 1, 1*time.Second, nil)

	// Fill the pool with a long-running request
	go func() {
		pooled.GetDeployments()
	}()

	time.Sleep(100 * time.Millisecond) // Let first request start

	// This should timeout
	_, err := pooled.GetInfo()
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout waiting for BOSH connection slot") {
		t.Errorf("Expected timeout error message, got: %v", err)
	}

	// Check rejected requests counter
	stats := pooled.GetPoolStats()
	if stats.RejectedRequests != 1 {
		t.Errorf("Expected 1 rejected request, got %d", stats.RejectedRequests)
	}
}

func TestPooledDirector_QueuedRequests(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 100 * time.Millisecond,
	}
	pooled := NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Launch more concurrent requests than pool size
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pooled.GetInfo()
		}()
	}

	// Give some time for requests to queue up
	time.Sleep(50 * time.Millisecond)

	// Check that we have queued requests
	stats := pooled.GetPoolStats()
	if stats.QueuedRequests <= 0 {
		t.Log("Warning: Expected some queued requests, got", stats.QueuedRequests)
	}

	// Wait for all to complete
	wg.Wait()

	// Final stats should show no active or queued
	finalStats := pooled.GetPoolStats()
	if finalStats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections, got %d", finalStats.ActiveConnections)
	}
	if finalStats.QueuedRequests != 0 {
		t.Errorf("Expected 0 queued requests, got %d", finalStats.QueuedRequests)
	}
}

func TestPooledDirector_DefaultPoolSize(t *testing.T) {
	mockDirector := &MockDirector{}
	// Test with 0 (should default to 4)
	pooled := NewPooledDirector(mockDirector, 0, 5*time.Second, nil)

	stats := pooled.GetPoolStats()
	if stats.MaxConnections != 4 {
		t.Errorf("Expected default pool size of 4, got %d", stats.MaxConnections)
	}

	// Test with negative (should default to 4)
	pooled2 := NewPooledDirector(mockDirector, -1, 5*time.Second, nil)

	stats2 := pooled2.GetPoolStats()
	if stats2.MaxConnections != 4 {
		t.Errorf("Expected default pool size of 4, got %d", stats2.MaxConnections)
	}
}

func TestPooledDirector_MetricsAccuracy(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 50 * time.Millisecond,
	}
	pooled := NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Initial stats should be zero
	stats := pooled.GetPoolStats()
	if stats.TotalRequests != 0 || stats.ActiveConnections != 0 || stats.QueuedRequests != 0 {
		t.Error("Expected initial stats to be zero")
	}

	// Start 3 concurrent requests (2 active, 1 queued)
	started := make(chan bool, 3)
	done := make(chan bool, 3)

	for i := 0; i < 3; i++ {
		go func() {
			started <- true
			pooled.GetInfo()
			done <- true
		}()
	}

	// Wait for all to start
	for i := 0; i < 3; i++ {
		<-started
	}

	// Give a moment for queueing
	time.Sleep(10 * time.Millisecond)

	// Check stats during execution
	midStats := pooled.GetPoolStats()
	if midStats.ActiveConnections != 2 {
		t.Errorf("Expected 2 active connections, got %d", midStats.ActiveConnections)
	}
	if midStats.QueuedRequests != 1 {
		t.Errorf("Expected 1 queued request, got %d", midStats.QueuedRequests)
	}

	// Wait for completion
	for i := 0; i < 3; i++ {
		<-done
	}

	// Final stats
	finalStats := pooled.GetPoolStats()
	if finalStats.TotalRequests != 3 {
		t.Errorf("Expected 3 total requests, got %d", finalStats.TotalRequests)
	}
	if finalStats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after completion, got %d", finalStats.ActiveConnections)
	}
	if finalStats.QueuedRequests != 0 {
		t.Errorf("Expected 0 queued requests after completion, got %d", finalStats.QueuedRequests)
	}
}

func TestPooledDirector_AllMethods(t *testing.T) {
	mockDirector := &MockDirector{}
	pooled := NewPooledDirector(mockDirector, 4, 5*time.Second, nil)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"GetInfo", func() error {
			_, err := pooled.GetInfo()
			return err
		}},
		{"GetDeployments", func() error {
			_, err := pooled.GetDeployments()
			return err
		}},
		{"GetDeployment", func() error {
			_, err := pooled.GetDeployment("test")
			return err
		}},
		{"CreateDeployment", func() error {
			_, err := pooled.CreateDeployment("manifest")
			return err
		}},
		{"DeleteDeployment", func() error {
			_, err := pooled.DeleteDeployment("test")
			return err
		}},
		{"GetDeploymentVMs", func() error {
			_, err := pooled.GetDeploymentVMs("test")
			return err
		}},
		{"GetReleases", func() error {
			_, err := pooled.GetReleases()
			return err
		}},
		{"UploadRelease", func() error {
			_, err := pooled.UploadRelease("url", "sha1")
			return err
		}},
		{"GetStemcells", func() error {
			_, err := pooled.GetStemcells()
			return err
		}},
		{"UploadStemcell", func() error {
			_, err := pooled.UploadStemcell("url", "sha1")
			return err
		}},
		{"GetTask", func() error {
			_, err := pooled.GetTask(1)
			return err
		}},
		{"GetAllTasks", func() error {
			_, err := pooled.GetAllTasks(10)
			return err
		}},
		{"CancelTask", func() error {
			return pooled.CancelTask(1)
		}},
		{"GetTaskOutput", func() error {
			_, err := pooled.GetTaskOutput(1, "result")
			return err
		}},
		{"GetTaskEvents", func() error {
			_, err := pooled.GetTaskEvents(1)
			return err
		}},
		{"GetEvents", func() error {
			_, err := pooled.GetEvents("test")
			return err
		}},
		{"UpdateCloudConfig", func() error {
			return pooled.UpdateCloudConfig("config")
		}},
		{"GetCloudConfig", func() error {
			_, err := pooled.GetCloudConfig()
			return err
		}},
		{"GetConfigs", func() error {
			_, err := pooled.GetConfigs(10, []string{"cloud"})
			return err
		}},
		{"GetConfigVersions", func() error {
			_, err := pooled.GetConfigVersions("cloud", "default", 10)
			return err
		}},
		{"GetConfigByID", func() error {
			_, err := pooled.GetConfigByID("123")
			return err
		}},
		{"GetConfigContent", func() error {
			_, err := pooled.GetConfigContent("123")
			return err
		}},
		{"GetConfig", func() error {
			_, err := pooled.GetConfig("cloud", "default")
			return err
		}},
		{"ComputeConfigDiff", func() error {
			_, err := pooled.ComputeConfigDiff("123", "456")
			return err
		}},
		{"Cleanup", func() error {
			_, err := pooled.Cleanup(false)
			return err
		}},
		{"FetchLogs", func() error {
			_, err := pooled.FetchLogs("test", "job", "0")
			return err
		}},
		{"SSHCommand", func() error {
			_, err := pooled.SSHCommand("test", "instance", 0, "ls", []string{}, nil)
			return err
		}},
		{"SSHSession", func() error {
			_, err := pooled.SSHSession("test", "instance", 0, nil)
			return err
		}},
		{"EnableResurrection", func() error {
			return pooled.EnableResurrection("test", true)
		}},
		{"DeleteResurrectionConfig", func() error {
			return pooled.DeleteResurrectionConfig("test")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.fn()
			if err != nil {
				t.Errorf("%s failed: %v", test.name, err)
			}
		})
	}

	// Verify all methods went through the pool
	if mockDirector.getCallCount() != len(tests) {
		t.Errorf("Expected %d calls, got %d", len(tests), mockDirector.getCallCount())
	}

	stats := pooled.GetPoolStats()
	if stats.TotalRequests != int64(len(tests)) {
		t.Errorf("Expected %d total requests in stats, got %d", len(tests), stats.TotalRequests)
	}
}

func TestPooledDirector_StressTest(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 10 * time.Millisecond,
	}
	pooled := NewPooledDirector(mockDirector, 5, 10*time.Second, nil)

	// Launch 100 concurrent requests
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Mix of different operations
			switch id % 5 {
			case 0:
				_, err := pooled.GetInfo()
				errors <- err
			case 1:
				_, err := pooled.GetDeployments()
				errors <- err
			case 2:
				_, err := pooled.GetTask(id)
				errors <- err
			case 3:
				_, err := pooled.GetReleases()
				errors <- err
			case 4:
				_, err := pooled.GetStemcells()
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Count successes
	successCount := 0
	for err := range errors {
		if err == nil {
			successCount++
		}
	}

	if successCount != 100 {
		t.Errorf("Expected 100 successful requests, got %d", successCount)
	}

	// Verify stats
	stats := pooled.GetPoolStats()
	if stats.TotalRequests != 100 {
		t.Errorf("Expected 100 total requests, got %d", stats.TotalRequests)
	}
	if stats.RejectedRequests != 0 {
		t.Errorf("Expected 0 rejected requests with sufficient timeout, got %d", stats.RejectedRequests)
	}

	// Pool should have handled peak load
	if stats.MaxConnections != 5 {
		t.Errorf("Expected max connections to be 5, got %d", stats.MaxConnections)
	}
}

func TestPooledDirector_TimeoutBehavior(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 2 * time.Second, // Very slow responses
	}
	pooled := NewPooledDirector(mockDirector, 1, 500*time.Millisecond, nil)

	// First request fills the pool
	go func() {
		pooled.GetDeployments()
	}()

	// Give first request time to acquire the slot
	time.Sleep(100 * time.Millisecond)

	// Second request should timeout
	start := time.Now()
	_, err := pooled.GetInfo()
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "timeout waiting for BOSH connection slot") {
		t.Errorf("Expected timeout error message, got: %v", err)
	}

	// Should have waited approximately the timeout duration
	if elapsed < 400*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Errorf("Expected timeout after ~500ms, took %v", elapsed)
	}

	// Check stats
	stats := pooled.GetPoolStats()
	if stats.RejectedRequests != 1 {
		t.Errorf("Expected 1 rejected request, got %d", stats.RejectedRequests)
	}
}

func TestPooledDirector_GracefulShutdown(t *testing.T) {
	mockDirector := &MockDirector{
		delay: 100 * time.Millisecond,
	}
	pooled := NewPooledDirector(mockDirector, 3, 5*time.Second, nil)

	// Start several long-running operations
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pooled.GetDeployments()
		}()
	}

	// Give them time to start
	time.Sleep(50 * time.Millisecond)

	// Check active connections
	stats := pooled.GetPoolStats()
	if stats.ActiveConnections != 3 {
		t.Errorf("Expected 3 active connections, got %d", stats.ActiveConnections)
	}

	// Wait for completion
	wg.Wait()

	// Verify clean shutdown
	finalStats := pooled.GetPoolStats()
	if finalStats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after completion, got %d", finalStats.ActiveConnections)
	}
}

func TestPooledDirector_ErrorPropagation(t *testing.T) {
	// Create a mock that returns errors
	mockDirector := &MockErrorDirector{
		errorMessage: "simulated error",
	}
	pooled := NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Test that errors are properly propagated
	_, err := pooled.GetInfo()
	if err == nil {
		t.Error("Expected error to be propagated, got nil")
	}
	if !strings.Contains(err.Error(), "simulated error") {
		t.Errorf("Expected error message to contain 'simulated error', got: %v", err)
	}

	// Verify the pool still works after an error
	_, err2 := pooled.GetDeployments()
	if err2 == nil {
		t.Error("Expected second error to be propagated, got nil")
	}

	// Stats should show completed requests even with errors
	stats := pooled.GetPoolStats()
	if stats.TotalRequests != 2 {
		t.Errorf("Expected 2 total requests, got %d", stats.TotalRequests)
	}
}

// MockErrorDirector for testing error propagation
type MockErrorDirector struct {
	MockDirector
	errorMessage string
}

func (m *MockErrorDirector) GetInfo() (*Info, error) {
	m.incrementCallCount()
	return nil, fmt.Errorf("%s", m.errorMessage)
}

func (m *MockErrorDirector) GetDeployments() ([]Deployment, error) {
	m.incrementCallCount()
	return nil, fmt.Errorf("%s", m.errorMessage)
}
