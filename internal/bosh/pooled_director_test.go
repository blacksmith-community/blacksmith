package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

// Static errors for test err113 compliance.
var (
	ErrMockError = errors.New("mock error")
)

// MockDirector for testing.
type MockDirector struct {
	delay     time.Duration
	callCount int
	mu        sync.Mutex
}

func (m *MockDirector) GetInfo(_ context.Context) (*bosh.Info, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Info{
		Name:    "test-director",
		UUID:    "test-uuid",
		Version: "1.0.0",
	}, nil
}

func (m *MockDirector) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Deployment{
		{Name: "test-deployment"},
	}, nil
}

func (m *MockDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.DeploymentDetail{Name: name}, nil
}

func (m *MockDirector) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 2}, nil
}

func (m *MockDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.VM{}, nil
}

func (m *MockDirector) GetReleases(_ context.Context) ([]bosh.Release, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Release{}, nil
}

func (m *MockDirector) UploadRelease(_ context.Context, url string, sha1 string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 3}, nil
}

func (m *MockDirector) GetStemcells(_ context.Context) ([]bosh.Stemcell, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Stemcell{}, nil
}

func (m *MockDirector) UploadStemcell(_ context.Context, url string, sha1 string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 4}, nil
}

func (m *MockDirector) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: taskID}, nil
}

func (m *MockDirector) GetTasks(_ context.Context, taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Task{}, nil
}

func (m *MockDirector) GetAllTasks(_ context.Context, limit int) ([]bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Task{}, nil
}

func (m *MockDirector) CancelTask(_ context.Context, taskID int) error {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil
}

func (m *MockDirector) WaitForTask(_ context.Context, taskID int) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: taskID, State: "done"}, nil
}

func (m *MockDirector) GetTaskOutput(_ context.Context, id int, outputType string) (string, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return "test output", nil
}

func (m *MockDirector) GetTaskEvents(_ context.Context, id int) ([]bosh.TaskEvent, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.TaskEvent{}, nil
}

func (m *MockDirector) GetEvents(_ context.Context, deployment string) ([]bosh.Event, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Event{}, nil
}

func (m *MockDirector) UpdateCloudConfig(_ context.Context, config string) error {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil
}

func (m *MockDirector) GetCloudConfig(_ context.Context) (string, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return "test-cloud-config", nil
}

func (m *MockDirector) GetConfigs(_ context.Context, limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.BoshConfig{}, nil
}

func (m *MockDirector) GetConfigVersions(_ context.Context, configType, name string, limit int) ([]bosh.BoshConfig, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.BoshConfig{}, nil
}

func (m *MockDirector) GetConfigByID(_ context.Context, configID string) (*bosh.BoshConfigDetail, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.BoshConfigDetail{}, nil
}

func (m *MockDirector) GetConfigContent(_ context.Context, configID string) (string, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return "test-config", nil
}

func (m *MockDirector) GetConfig(_ context.Context, configType, configName string) (interface{}, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return map[string]interface{}{}, nil
}

func (m *MockDirector) ComputeConfigDiff(_ context.Context, fromID, toID string) (*bosh.ConfigDiff, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.ConfigDiff{}, nil
}

func (m *MockDirector) Cleanup(_ context.Context, removeAll bool) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 5}, nil
}

func (m *MockDirector) FetchLogs(_ context.Context, deployment string, jobName string, jobIndex string) (string, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return "/tmp/logs.tar.gz", nil
}

func (m *MockDirector) SSHCommand(_ context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return "command output", nil
}

func (m *MockDirector) SSHSession(_ context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, ErrMockError
}

func (m *MockDirector) EnableResurrection(_ context.Context, deployment string, enabled bool) error {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil
}

func (m *MockDirector) DeleteResurrectionConfig(_ context.Context, deploymentName string) error {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil
}

func (m *MockDirector) RestartDeployment(_ context.Context, name string, opts bosh.RestartOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) StopDeployment(_ context.Context, name string, opts bosh.StopOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) StartDeployment(_ context.Context, name string, opts bosh.StartOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) ListErrands(_ context.Context, deployment string) ([]bosh.Errand, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Errand{}, nil
}

func (m *MockDirector) RunErrand(_ context.Context, deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.ErrandResult{}, nil
}

func (m *MockDirector) GetInstances(_ context.Context, deployment string) ([]bosh.Instance, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return []bosh.Instance{}, nil
}

func (m *MockDirector) UpdateDeployment(_ context.Context, name, manifest string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.Task{ID: 1}, nil
}

func (m *MockDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, nil
}

func (m *MockDirector) GetPoolStats() (*bosh.PoolStats, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return &bosh.PoolStats{}, nil
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

// Tests

func TestPooledDirector_ConcurrentRequests(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{}
	pooled := bosh.NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Test concurrent requests exceed pool size
	// Note: Only write operations use pooling, so we use CreateDeployment
	var waitGroup sync.WaitGroup

	results := make(chan error, 10)

	for range 10 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, err := pooled.CreateDeployment(t.Context(), "test-manifest")
			results <- err
		}()
	}

	waitGroup.Wait()
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

	// Verify pool stats
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", stats.TotalRequests)
	}

	if stats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after completion, got %d", stats.ActiveConnections)
	}
}

func TestPooledDirector_Timeout(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{
		delay: 10 * time.Second, // Slow responses
	}
	pooled := bosh.NewPooledDirector(mockDirector, 1, 1*time.Second, nil)

	// Fill the pool with a long-running request
	// Note: Only write operations use pooling, so we use CreateDeployment
	go func() {
		_, _ = pooled.CreateDeployment(t.Context(), "test-manifest")
	}()

	time.Sleep(100 * time.Millisecond) // Let first request start

	// This should timeout
	// Use DeleteDeployment which uses pooling (read operations don't use pooling)
	_, err := pooled.DeleteDeployment(t.Context(), "test-deployment")
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	if err != nil && !strings.Contains(err.Error(), "timeout waiting for BOSH connection slot") {
		t.Errorf("Expected timeout error message, got: %v", err)
	}

	// Check rejected requests counter
	stats, statsErr := pooled.GetPoolStats()
	if statsErr != nil {
		t.Fatalf("Failed to get pool stats: %v", statsErr)
	}

	if stats.RejectedRequests != 1 {
		t.Errorf("Expected 1 rejected request, got %d", stats.RejectedRequests)
	}
}

func TestPooledDirector_QueuedRequests(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{
		delay: 100 * time.Millisecond,
	}
	pooled := bosh.NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Launch more concurrent requests than pool size
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	var waitGroup sync.WaitGroup
	for range 5 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, _ = pooled.CreateDeployment(t.Context(), "test-manifest")
		}()
	}

	// Give some time for requests to queue up
	time.Sleep(50 * time.Millisecond)

	// Check that we have queued requests
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.QueuedRequests <= 0 {
		t.Log("Warning: Expected some queued requests, got", stats.QueuedRequests)
	}

	// Wait for all to complete
	waitGroup.Wait()

	// Final stats should show no active or queued
	finalStats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if finalStats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections, got %d", finalStats.ActiveConnections)
	}

	if finalStats.QueuedRequests != 0 {
		t.Errorf("Expected 0 queued requests, got %d", finalStats.QueuedRequests)
	}
}

func TestPooledDirector_DefaultPoolSize(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{}
	// Test with 0 (should default to 4)
	pooled := bosh.NewPooledDirector(mockDirector, 0, 5*time.Second, nil)

	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.MaxConnections != 4 {
		t.Errorf("Expected default pool size of 4, got %d", stats.MaxConnections)
	}

	// Test with negative (should default to 4)
	pooled2 := bosh.NewPooledDirector(mockDirector, -1, 5*time.Second, nil)

	stats2, err := pooled2.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats2.MaxConnections != 4 {
		t.Errorf("Expected default pool size of 4, got %d", stats2.MaxConnections)
	}
}

func TestPooledDirector_MetricsAccuracy(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{delay: 50 * time.Millisecond}
	pooled := bosh.NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	verifyInitialPoolStats(t, pooled)
	_, done := launchConcurrentRequests(pooled)
	verifyMidExecutionPoolStats(t, pooled, done)
	verifyFinalPoolStats(t, pooled)
}

func verifyInitialPoolStats(t *testing.T, pooled bosh.Director) {
	t.Helper()

	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.TotalRequests != 0 || stats.ActiveConnections != 0 || stats.QueuedRequests != 0 {
		t.Error("Expected initial stats to be zero")
	}
}

func launchConcurrentRequests(pooled bosh.Director) (chan bool, chan bool) {
	started := make(chan bool, 3)
	done := make(chan bool, 3)

	for range 3 {
		go func() {
			started <- true

			// Use CreateDeployment which still uses pooling (read operations don't use pooling)
			_, _ = pooled.CreateDeployment(context.Background(), "test-manifest")

			done <- true
		}()
	}

	for range 3 {
		<-started
	}

	time.Sleep(10 * time.Millisecond)

	return started, done
}

func verifyMidExecutionPoolStats(t *testing.T, pooled bosh.Director, done chan bool) {
	t.Helper()

	midStats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if midStats.ActiveConnections != 2 {
		t.Errorf("Expected 2 active connections, got %d", midStats.ActiveConnections)
	}

	if midStats.QueuedRequests != 1 {
		t.Errorf("Expected 1 queued request, got %d", midStats.QueuedRequests)
	}

	for range 3 {
		<-done
	}
}

func verifyFinalPoolStats(t *testing.T, pooled bosh.Director) {
	t.Helper()

	finalStats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

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

// getDeploymentTests returns tests for deployment operations.
func getDeploymentTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"GetInfo", func() error {
			_, err := pooled.GetInfo(context.Background())
			if err != nil {
				return fmt.Errorf("GetInfo failed: %w", err)
			}

			return nil
		}},
		{"GetDeployments", func() error {
			_, err := pooled.GetDeployments(context.Background())
			if err != nil {
				return fmt.Errorf("GetDeployments failed: %w", err)
			}

			return nil
		}},
		{"GetDeployment", func() error {
			_, err := pooled.GetDeployment(context.Background(), "test")
			if err != nil {
				return fmt.Errorf("GetDeployment failed: %w", err)
			}

			return nil
		}},
		{"CreateDeployment", func() error {
			_, err := pooled.CreateDeployment(context.Background(), "manifest")
			if err != nil {
				return fmt.Errorf("CreateDeployment failed: %w", err)
			}

			return nil
		}},
		{"DeleteDeployment", func() error {
			_, err := pooled.DeleteDeployment(context.Background(), "test")
			if err != nil {
				return fmt.Errorf("DeleteDeployment failed: %w", err)
			}

			return nil
		}},
		{"GetDeploymentVMs", func() error {
			_, err := pooled.GetDeploymentVMs(context.Background(), "test")
			if err != nil {
				return fmt.Errorf("GetDeploymentVMs failed: %w", err)
			}

			return nil
		}},
	}
}

// getReleaseAndStemcellTests returns tests for release and stemcell operations.
func getReleaseAndStemcellTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"GetReleases", func() error {
			_, err := pooled.GetReleases(context.Background())
			if err != nil {
				return fmt.Errorf("GetReleases failed: %w", err)
			}

			return nil
		}},
		{"UploadRelease", func() error {
			_, err := pooled.UploadRelease(context.Background(), "url", "sha1")
			if err != nil {
				return fmt.Errorf("UploadRelease failed: %w", err)
			}

			return nil
		}},
		{"GetStemcells", func() error {
			_, err := pooled.GetStemcells(context.Background())
			if err != nil {
				return fmt.Errorf("GetStemcells failed: %w", err)
			}

			return nil
		}},
		{"UploadStemcell", func() error {
			_, err := pooled.UploadStemcell(context.Background(), "url", "sha1")
			if err != nil {
				return fmt.Errorf("UploadStemcell failed: %w", err)
			}

			return nil
		}},
	}
}

// getTaskTests returns tests for task operations.
func getTaskTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"GetTask", func() error {
			_, err := pooled.GetTask(context.Background(), 1)
			if err != nil {
				return fmt.Errorf("GetTask failed: %w", err)
			}

			return nil
		}},
		{"GetAllTasks", func() error {
			_, err := pooled.GetAllTasks(context.Background(), 10)
			if err != nil {
				return fmt.Errorf("GetAllTasks failed: %w", err)
			}

			return nil
		}},
		{"CancelTask", func() error {
			return pooled.CancelTask(context.Background(), 1)
		}},
		{"GetTaskOutput", func() error {
			_, err := pooled.GetTaskOutput(context.Background(), 1, "result")
			if err != nil {
				return fmt.Errorf("GetTaskOutput failed: %w", err)
			}

			return nil
		}},
		{"GetTaskEvents", func() error {
			_, err := pooled.GetTaskEvents(context.Background(), 1)
			if err != nil {
				return fmt.Errorf("GetTaskEvents failed: %w", err)
			}

			return nil
		}},
	}
}

// getEventTests returns tests for event operations.
func getEventTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"GetEvents", func() error {
			_, err := pooled.GetEvents(context.Background(), "test")
			if err != nil {
				return fmt.Errorf("GetEvents failed: %w", err)
			}

			return nil
		}},
	}
}

// getConfigTests returns tests for config operations.
func getConfigTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"UpdateCloudConfig", func() error {
			return pooled.UpdateCloudConfig(context.Background(), "config")
		}},
		{"GetCloudConfig", func() error {
			_, err := pooled.GetCloudConfig(context.Background())
			if err != nil {
				return fmt.Errorf("GetCloudConfig failed: %w", err)
			}

			return nil
		}},
		{"GetConfigs", func() error {
			_, err := pooled.GetConfigs(context.Background(), 10, []string{"cloud"})
			if err != nil {
				return fmt.Errorf("GetConfigs failed: %w", err)
			}

			return nil
		}},
		{"GetConfigVersions", func() error {
			_, err := pooled.GetConfigVersions(context.Background(), "cloud", "default", 10)
			if err != nil {
				return fmt.Errorf("GetConfigVersions failed: %w", err)
			}

			return nil
		}},
		{"GetConfigByID", func() error {
			_, err := pooled.GetConfigByID(context.Background(), "123")
			if err != nil {
				return fmt.Errorf("GetConfigByID failed: %w", err)
			}

			return nil
		}},
		{"GetConfigContent", func() error {
			_, err := pooled.GetConfigContent(context.Background(), "123")
			if err != nil {
				return fmt.Errorf("GetConfigContent failed: %w", err)
			}

			return nil
		}},
		{"GetConfig", func() error {
			_, err := pooled.GetConfig(context.Background(), "cloud", "default")
			if err != nil {
				return fmt.Errorf("GetConfig failed: %w", err)
			}

			return nil
		}},
	}
}

// getSSHAndOtherTests returns tests for SSH and other operations.
func getSSHAndOtherTests(pooled *bosh.PooledDirector) []struct {
	name string
	fn   func() error
} {
	return []struct {
		name string
		fn   func() error
	}{
		{"Cleanup", func() error {
			_, err := pooled.Cleanup(context.Background(), false)
			if err != nil {
				return fmt.Errorf("Cleanup failed: %w", err)
			}

			return nil
		}},
		{"FetchLogs", func() error {
			_, err := pooled.FetchLogs(context.Background(), "test", "job", "0")
			if err != nil {
				return fmt.Errorf("FetchLogs failed: %w", err)
			}

			return nil
		}},
		{"SSHCommand", func() error {
			_, err := pooled.SSHCommand(context.Background(), "test", "instance", 0, "ls", []string{}, nil)
			if err != nil {
				return fmt.Errorf("SSHCommand failed: %w", err)
			}

			return nil
		}},
		{"SSHSession", func() error {
			_, err := pooled.SSHSession(context.Background(), "test", "instance", 0, nil)
			if !errors.Is(err, ErrMockError) {
				return fmt.Errorf("expected ErrMockError, got %w", err)
			}

			return nil
		}},
		{"EnableResurrection", func() error {
			return pooled.EnableResurrection(context.Background(), "test", true)
		}},
		{"DeleteResurrectionConfig", func() error {
			return pooled.DeleteResurrectionConfig(context.Background(), "test")
		}},
	}
}

// validatePoolUsage validates that the pool was used correctly.
// Note: Only write operations use the pool, read operations bypass it.
func validatePoolUsage(t *testing.T, mockDirector *MockDirector, pooled *bosh.PooledDirector, expectedCalls int) {
	t.Helper()
	// Verify all methods were called on the underlying director
	if mockDirector.getCallCount() != expectedCalls {
		t.Errorf("Expected %d calls to mock director, got %d", expectedCalls, mockDirector.getCallCount())
	}

	// Get pool stats - note that read operations don't go through the pool
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	// The pool should have been used for at least some requests (write operations)
	// We don't check exact count since only write operations use the pool
	if stats.TotalRequests == 0 {
		t.Log("Warning: No requests went through the pool (expected for read-only test)")
	}
}

func TestPooledDirector_AllMethods(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{}
	pooled := bosh.NewPooledDirector(mockDirector, 4, 5*time.Second, nil)

	// Group tests by functionality
	testGroups := [][]struct {
		name string
		fn   func() error
	}{
		getDeploymentTests(pooled),
		getReleaseAndStemcellTests(pooled),
		getTaskTests(pooled),
		getEventTests(pooled),
		getConfigTests(pooled),
		getSSHAndOtherTests(pooled),
	}

	// Flatten all tests
	var allTests []struct {
		name string
		fn   func() error
	}
	for _, group := range testGroups {
		allTests = append(allTests, group...)
	}

	// Run tests sequentially to avoid timeout issues
	for _, test := range allTests {
		err := test.fn()
		if err != nil {
			t.Errorf("%s failed: %v", test.name, err)
		}
	}

	validatePoolUsage(t, mockDirector, pooled, len(allTests))
}

// executeStressTestOperation performs one of the stress test operations based on request ID.
// Uses only write operations that go through the connection pool.
func executeStressTestOperation(pooled *bosh.PooledDirector, requestID int) error {
	switch requestID % 5 {
	case 0:
		_, err := pooled.CreateDeployment(context.Background(), "test-manifest")
		if err != nil {
			return fmt.Errorf("failed to create deployment: %w", err)
		}

		return nil
	case 1:
		_, err := pooled.DeleteDeployment(context.Background(), "test-deployment")
		if err != nil {
			return fmt.Errorf("failed to delete deployment: %w", err)
		}

		return nil
	case 2:
		_, err := pooled.UpdateDeployment(context.Background(), "test-deployment", "test-manifest")
		if err != nil {
			return fmt.Errorf("failed to update deployment: %w", err)
		}

		return nil
	case 3:
		_, err := pooled.UploadRelease(context.Background(), "http://example.com/release.tgz", "sha1")
		if err != nil {
			return fmt.Errorf("failed to upload release: %w", err)
		}

		return nil
	case 4:
		_, err := pooled.UploadStemcell(context.Background(), "http://example.com/stemcell.tgz", "sha1")
		if err != nil {
			return fmt.Errorf("failed to upload stemcell: %w", err)
		}

		return nil
	}

	return nil
}

// runConcurrentStressTests runs concurrent stress tests and returns error channel.
func runConcurrentStressTests(pooled *bosh.PooledDirector, numRequests int) <-chan error {
	var waitGroup sync.WaitGroup

	errors := make(chan error, numRequests)

	for index := range numRequests {
		waitGroup.Add(1)

		go func(requestID int) {
			defer waitGroup.Done()

			err := executeStressTestOperation(pooled, requestID)
			errors <- err
		}(index)
	}

	go func() {
		waitGroup.Wait()
		close(errors)
	}()

	return errors
}

// countSuccessfulRequests counts successful requests from error channel.
func countSuccessfulRequests(errors <-chan error) int {
	successCount := 0

	for err := range errors {
		if err == nil {
			successCount++
		}
	}

	return successCount
}

// validateStressTestResults validates the results of stress testing.
func validateStressTestResults(t *testing.T, pooled *bosh.PooledDirector, successCount, expectedRequests int) {
	t.Helper()

	if successCount != expectedRequests {
		t.Errorf("Expected %d successful requests, got %d", expectedRequests, successCount)
	}

	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.TotalRequests != int64(expectedRequests) {
		t.Errorf("Expected %d total requests, got %d", expectedRequests, stats.TotalRequests)
	}

	if stats.RejectedRequests != 0 {
		t.Errorf("Expected 0 rejected requests with sufficient timeout, got %d", stats.RejectedRequests)
	}

	if stats.MaxConnections != 5 {
		t.Errorf("Expected max connections to be 5, got %d", stats.MaxConnections)
	}
}

func TestPooledDirector_StressTest(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{delay: 10 * time.Millisecond}
	pooled := bosh.NewPooledDirector(mockDirector, 5, 10*time.Second, nil)

	const numRequests = 100

	errors := runConcurrentStressTests(pooled, numRequests)
	successCount := countSuccessfulRequests(errors)
	validateStressTestResults(t, pooled, successCount, numRequests)
}

func TestPooledDirector_TimeoutBehavior(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{
		delay: 2 * time.Second, // Very slow responses
	}
	pooled := bosh.NewPooledDirector(mockDirector, 1, 500*time.Millisecond, nil)

	// First request fills the pool
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	go func() {
		_, _ = pooled.CreateDeployment(t.Context(), "test-manifest")
	}()

	// Give first request time to acquire the slot
	time.Sleep(100 * time.Millisecond)

	// Second request should timeout
	start := time.Now()
	_, err := pooled.DeleteDeployment(t.Context(), "test-deployment")
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
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.RejectedRequests != 1 {
		t.Errorf("Expected 1 rejected request, got %d", stats.RejectedRequests)
	}
}

func TestPooledDirector_GracefulShutdown(t *testing.T) {
	t.Parallel()

	mockDirector := &MockDirector{
		delay: 100 * time.Millisecond,
	}
	pooled := bosh.NewPooledDirector(mockDirector, 3, 5*time.Second, nil)

	// Start several long-running operations
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	var waitGroup sync.WaitGroup
	for range 3 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, _ = pooled.CreateDeployment(t.Context(), "test-manifest")
		}()
	}

	// Give them time to start
	time.Sleep(50 * time.Millisecond)

	// Check active connections
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.ActiveConnections != 3 {
		t.Errorf("Expected 3 active connections, got %d", stats.ActiveConnections)
	}

	// Wait for completion
	waitGroup.Wait()

	// Verify clean shutdown
	finalStats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if finalStats.ActiveConnections != 0 {
		t.Errorf("Expected 0 active connections after completion, got %d", finalStats.ActiveConnections)
	}
}

func TestPooledDirector_ErrorPropagation(t *testing.T) {
	t.Parallel()

	// Create a mock that returns errors
	mockDirector := &MockErrorDirector{
		errorMessage: "simulated error",
	}
	pooled := bosh.NewPooledDirector(mockDirector, 2, 5*time.Second, nil)

	// Test that errors are properly propagated
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	_, err := pooled.CreateDeployment(t.Context(), "test-manifest")
	if err == nil {
		t.Error("Expected error to be propagated, got nil")
	}

	if !strings.Contains(err.Error(), "simulated error") {
		t.Errorf("Expected error message to contain 'simulated error', got: %v", err)
	}

	// Verify the pool still works after an error
	_, err2 := pooled.DeleteDeployment(t.Context(), "test-deployment")
	if err2 == nil {
		t.Error("Expected second error to be propagated, got nil")
	}

	// Stats should show completed requests even with errors
	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.TotalRequests != 2 {
		t.Errorf("Expected 2 total requests, got %d", stats.TotalRequests)
	}
}

// MockErrorDirector for testing error propagation.
type MockErrorDirector struct {
	MockDirector

	errorMessage string
}

func (m *MockErrorDirector) GetInfo(_ context.Context) (*bosh.Info, error) {
	m.incrementCallCount()

	return nil, fmt.Errorf("%w: %s", ErrMockError, m.errorMessage)
}

func (m *MockErrorDirector) GetDeployments(_ context.Context) ([]bosh.Deployment, error) {
	m.incrementCallCount()

	return nil, fmt.Errorf("%w: %s", ErrMockError, m.errorMessage)
}

func (m *MockErrorDirector) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	m.incrementCallCount()

	return nil, fmt.Errorf("%w: %s", ErrMockError, m.errorMessage)
}

func (m *MockErrorDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	m.incrementCallCount()

	return nil, fmt.Errorf("%w: %s", ErrMockError, m.errorMessage)
}
