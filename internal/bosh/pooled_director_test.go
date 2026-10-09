package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Static errors for test err113 compliance.
var (
	ErrMockError = errors.New("mock error")
)

// testManifest is the manifest the pooled tests deploy. The pooled director
// refuses a manifest that names no deployment.
const testManifest = "name: test-deployment"

// finishedMockTask is a task that had already ended when the mock returned
// it, with its end time and deployment, so the pooled director frees its
// slot at once and reads nothing more about it.
func finishedMockTask(taskID int, deployment string) *bosh.Task {
	ended := time.Now()

	return &bosh.Task{ID: taskID, State: batchTaskDone, Deployment: deployment, EndedAt: &ended}
}

// MockDirector for testing. Every mutation returns a task that has ended.
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

	return finishedMockTask(1, bosh.ExtractDeploymentName(manifest)), nil
}

func (m *MockDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return finishedMockTask(2, name), nil
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

	return finishedMockTask(3, ""), nil
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

	return finishedMockTask(4, ""), nil
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

func (m *MockDirector) ListCloudConfigs(_ context.Context) ([]bosh.CloudConfigEntry, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, nil
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

	return finishedMockTask(5, ""), nil
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

	return finishedMockTask(1, name), nil
}

func (m *MockDirector) StopDeployment(_ context.Context, name string, opts bosh.StopOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return finishedMockTask(1, name), nil
}

func (m *MockDirector) StartDeployment(_ context.Context, name string, opts bosh.StartOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return finishedMockTask(1, name), nil
}

func (m *MockDirector) RecreateDeployment(_ context.Context, name string, opts bosh.RecreateOpts) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return finishedMockTask(1, name), nil
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

	return finishedMockTask(1, name), nil
}

func (m *MockDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, nil
}

func (m *MockDirector) FindRunningTasksForDeployment(_ context.Context, _ string) ([]bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, nil
}

func (m *MockDirector) FindTaskByContextID(_ context.Context, _ string) (*bosh.Task, error) {
	m.incrementCallCount()

	if m.delay > 0 {
		time.Sleep(m.delay)
	}

	return nil, nil //nolint:nilnil // no task with the ID is an answer, not an error
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
	t.Cleanup(pooled.Stop)

	// Test concurrent requests exceed pool size
	// Note: Only write operations use pooling, so we use CreateDeployment
	var waitGroup sync.WaitGroup

	results := make(chan error, 10)

	for range 10 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, err := pooled.CreateDeployment(t.Context(), testManifest)
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
	t.Cleanup(pooled.Stop)

	// Fill the pool with a long-running request
	// Note: Only write operations use pooling, so we use CreateDeployment
	go func() {
		_, _ = pooled.CreateDeployment(t.Context(), testManifest)
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
	t.Cleanup(pooled.Stop)

	// Launch more concurrent requests than pool size
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	var waitGroup sync.WaitGroup
	for range 5 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, _ = pooled.CreateDeployment(t.Context(), testManifest)
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
	t.Cleanup(pooled.Stop)

	stats, err := pooled.GetPoolStats()
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if stats.MaxConnections != 4 {
		t.Errorf("Expected default pool size of 4, got %d", stats.MaxConnections)
	}

	// Test with negative (should default to 4)
	pooled2 := bosh.NewPooledDirector(mockDirector, -1, 5*time.Second, nil)
	t.Cleanup(pooled2.Stop)

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
	t.Cleanup(pooled.Stop)

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
			_, _ = pooled.CreateDeployment(context.Background(), testManifest)

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
			_, err := pooled.CreateDeployment(context.Background(), testManifest)
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
	t.Cleanup(pooled.Stop)

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
		_, err := pooled.CreateDeployment(context.Background(), testManifest)
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
		_, err := pooled.UpdateDeployment(context.Background(), "test-deployment", testManifest)
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
	t.Cleanup(pooled.Stop)

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
	t.Cleanup(pooled.Stop)

	// First request fills the pool
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	go func() {
		_, _ = pooled.CreateDeployment(t.Context(), testManifest)
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
	t.Cleanup(pooled.Stop)

	// Start several long-running operations
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	var waitGroup sync.WaitGroup
	for range 3 {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			_, _ = pooled.CreateDeployment(t.Context(), testManifest)
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
	t.Cleanup(pooled.Stop)

	// Test that errors are properly propagated
	// Use CreateDeployment which still uses pooling (read operations don't use pooling)
	_, err := pooled.CreateDeployment(t.Context(), testManifest)
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

	if stats.ActiveConnections != 0 {
		t.Errorf("Expected a rejected mutation to hold no slot, got %d active connections", stats.ActiveConnections)
	}
}

// MockErrorDirector for testing error propagation.
type MockErrorDirector struct {
	MockDirector

	errorMessage string
}

// mockRejection is the director's own error answer to a mutation, which
// shows that the mutation started no task.
func mockRejection(message string) error {
	return fmt.Errorf("%w: %s: %w", ErrMockError, message,
		&boshapi.APIError{StatusCode: 400, Method: http.MethodPost, Path: deploymentsRoute, Description: "the director refused the request", Mutation: true})
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

	return nil, mockRejection(m.errorMessage)
}

func (m *MockErrorDirector) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	m.incrementCallCount()

	return nil, mockRejection(m.errorMessage)
}

// The deployment and context IDs the pool's follow tests use, and the
// methods the fake pool base records.
const (
	poolDeployment     = "dep-1"
	poolDeleteContext  = "ctx-delete"
	poolRestartContext = "ctx-restart"
	poolCreate         = "CreateDeployment"
	poolDelete         = "DeleteDeployment"
	poolRestart        = "RestartDeployment"
	poolUploadRelease  = "UploadRelease"
	poolUploadStemcell = "UploadStemcell"
	poolCleanup        = "Cleanup"
	poolRunErrand      = "RunErrand"
	poolDeleteDesc     = "delete deployment " + poolDeployment
	poolCleanupKind    = "cleanup"
	poolWaitLimit      = 5 * time.Second
	poolQuietPeriod    = 50 * time.Millisecond
)

var (
	errPoolRead = errors.New("the read of the task broke")
	errPoolLost = errors.New("the answer was lost")
)

// callerKey marks the contexts a test's caller passes, so a test can tell
// them from the follower's own context.
type callerKey struct{}

// fakePoolAnswer is what a fake pool base mutation answers instead of
// starting a task.
type fakePoolAnswer struct {
	task *bosh.Task
	err  error
}

// fakePoolBase is a base director for the pooled director. A mutation
// starts a task that stays processing until the test finishes it, unless
// the test sets the mutation's answer.
type fakePoolBase struct {
	bosh.Director

	mu      sync.Mutex
	nextID  int
	tasks   map[int]*fakeBatchTask
	answers map[string]fakePoolAnswer
	called  map[string]int

	waitCalls map[int]int
	waitCtxs  []context.Context
	waitErr   error
	getCalls  int
	getErr    error

	// contextAnswers holds, in order, the task ID each lookup by context ID
	// finds, where 0 finds no task. Once it is empty, lookups find none.
	contextAnswers []int
	contextLookups int

	running      []bosh.Task
	runningErr   error
	runningCalls int

	errandStarted chan struct{}
}

func newFakePoolBase() *fakePoolBase {
	return &fakePoolBase{
		nextID:        100,
		tasks:         map[int]*fakeBatchTask{},
		answers:       map[string]fakePoolAnswer{},
		called:        map[string]int{},
		waitCalls:     map[int]int{},
		errandStarted: make(chan struct{}, 4),
	}
}

func (f *fakePoolBase) CreateDeployment(_ context.Context, manifest string) (*bosh.Task, error) {
	return f.mutate(poolCreate, bosh.ExtractDeploymentName(manifest), bosh.DeployTaskDescription)
}

func (f *fakePoolBase) DeleteDeployment(_ context.Context, name string) (*bosh.Task, error) {
	return f.mutate(poolDelete, name, "delete deployment "+name)
}

func (f *fakePoolBase) RestartDeployment(_ context.Context, name string, _ bosh.RestartOpts) (*bosh.Task, error) {
	return f.mutate(poolRestart, name, bosh.DeployTaskDescription)
}

func (f *fakePoolBase) UploadRelease(_ context.Context, _, _ string) (*bosh.Task, error) {
	return f.mutate(poolUploadRelease, "", "create release")
}

func (f *fakePoolBase) UploadStemcell(_ context.Context, _, _ string) (*bosh.Task, error) {
	return f.mutate(poolUploadStemcell, "", "create stemcell")
}

func (f *fakePoolBase) Cleanup(_ context.Context, _ bool) (*bosh.Task, error) {
	return f.mutate(poolCleanup, "", "clean up")
}

// RunErrand runs until its caller's context ends, the way an errand the
// caller stops waiting for runs on.
func (f *fakePoolBase) RunErrand(ctx context.Context, _, _ string, _ bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	f.mu.Lock()
	f.called[poolRunErrand]++
	f.mu.Unlock()

	f.errandStarted <- struct{}{}

	<-ctx.Done()

	return nil, fmt.Errorf("run errand: %w", ctx.Err())
}

func (f *fakePoolBase) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	f.mu.Lock()
	f.waitCalls[taskID]++
	f.waitCtxs = append(f.waitCtxs, ctx)
	started, found := f.tasks[taskID]
	waitErr := f.waitErr
	f.mu.Unlock()

	if waitErr != nil {
		return nil, waitErr
	}

	if !found {
		return nil, poolTaskNotFound(taskID)
	}

	select {
	case <-started.done:
		return f.taskSnapshot(taskID), nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for task %d: %w", taskID, ctx.Err())
	}
}

func (f *fakePoolBase) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	f.mu.Lock()
	f.getCalls++
	_, found := f.tasks[taskID]
	getErr := f.getErr
	f.mu.Unlock()

	if getErr != nil {
		return nil, getErr
	}

	if !found {
		return nil, poolTaskNotFound(taskID)
	}

	return f.taskSnapshot(taskID), nil
}

func (f *fakePoolBase) FindTaskByContextID(_ context.Context, _ string) (*bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.contextLookups++

	if len(f.contextAnswers) == 0 {
		return nil, nil //nolint:nilnil // no task with the context ID is an answer
	}

	taskID := f.contextAnswers[0]
	f.contextAnswers = f.contextAnswers[1:]

	if taskID == 0 {
		return nil, nil //nolint:nilnil // no task with the context ID is an answer
	}

	task := f.tasks[taskID].task

	return &task, nil
}

func (f *fakePoolBase) FindRunningTasksForDeployment(_ context.Context, _ string) ([]bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.runningCalls++

	if f.runningErr != nil {
		return nil, f.runningErr
	}

	return append([]bosh.Task(nil), f.running...), nil
}

func (f *fakePoolBase) mutate(method, deployment, description string) (*bosh.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.called[method]++

	if answer, ok := f.answers[method]; ok {
		if answer.task == nil {
			return nil, answer.err
		}

		task := *answer.task

		return &task, answer.err
	}

	f.nextID++
	started := &fakeBatchTask{
		task: bosh.Task{ID: f.nextID, State: batchTaskProcessing, Description: description, Deployment: deployment},
		done: make(chan struct{}),
	}
	f.tasks[started.task.ID] = started
	task := started.task

	return &task, nil
}

// poolTaskNotFound is the director's answer for a task it has no record
// of.
func poolTaskNotFound(taskID int) error {
	return fmt.Errorf("task %d: %w", taskID, &boshapi.APIError{
		StatusCode: 404, Method: http.MethodGet, Path: fmt.Sprintf("/tasks/%d", taskID),
		Code: boshapi.ErrorCodeTaskNotFound, Description: fmt.Sprintf("Task %d could not be found", taskID),
	})
}

// set changes what the fake answers, under its lock.
func (f *fakePoolBase) set(change func(f *fakePoolBase)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

// addTask records a task the director runs that no mutation through the
// fake started.
func (f *fakePoolBase) addTask(taskID int, deployment, description string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.tasks[taskID] = &fakeBatchTask{
		task: bosh.Task{ID: taskID, State: batchTaskProcessing, Description: description, Deployment: deployment},
		done: make(chan struct{}),
	}
}

func (f *fakePoolBase) taskSnapshot(taskID int) *bosh.Task {
	f.mu.Lock()
	defer f.mu.Unlock()

	task := f.tasks[taskID].task

	return &task
}

// finish ends a task in the state done.
func (f *fakePoolBase) finish(t *testing.T, taskID int) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	started, ok := f.tasks[taskID]
	if !ok {
		t.Fatalf("task %d was never started", taskID)
	}

	ended := time.Now()
	started.task.State, started.task.EndedAt = batchTaskDone, &ended
	close(started.done)
}

// calls reports how many times the mutation was called.
func (f *fakePoolBase) calls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.called[method]
}

func (f *fakePoolBase) waitCallsFor(taskID int) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.waitCalls[taskID]
}

// waitContexts returns the context of each WaitForTask call so far.
func (f *fakePoolBase) waitContexts() []context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]context.Context(nil), f.waitCtxs...)
}

// reads reports every read the fake answered: waits, task reads, lookups
// by context ID, and running-task checks.
func (f *fakePoolBase) reads() (int, int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	waits := 0
	for _, count := range f.waitCalls {
		waits += count
	}

	return waits, f.getCalls, f.contextLookups, f.runningCalls
}

// newTestPool returns a pooled director over the fake base with the limit
// given, a fake clock, and a logger that records each line, and stops it
// when the test ends.
func newTestPool(t *testing.T, base bosh.Director, limit int) (*bosh.PooledDirector, *fakeBatchClock, *recordingLogger) {
	t.Helper()

	logger := &recordingLogger{}
	pool := bosh.NewPooledDirector(base, limit, time.Minute, logger)
	clock := newFakeBatchClock()
	bosh.SetPoolClock(pool, clock)
	t.Cleanup(pool.Stop)

	return pool, clock, logger
}

// activeSlots reports how many pool slots are held.
func activeSlots(t *testing.T, pool *bosh.PooledDirector) int32 {
	t.Helper()

	stats, err := pool.GetPoolStats()
	if err != nil {
		t.Fatalf("GetPoolStats failed: %v", err)
	}

	return stats.ActiveConnections
}

// waitSlotsFree waits until no pool slot is held.
func waitSlotsFree(t *testing.T, pool *bosh.PooledDirector) {
	t.Helper()

	waitUntil(t, "every pool slot is free", func() bool { return activeSlots(t, pool) == 0 })
}

// checkSlotHeld waits until a lost-answer watcher has made its check and
// sleeps again, and then checks that the call's slot is still held.
func checkSlotHeld(t *testing.T, pool *bosh.PooledDirector, clock *fakeBatchClock) {
	t.Helper()

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the lost answer to hold its pool slot, got %d slots held", active)
	}
}

// stepLostChecks lets a lost-answer watcher make the next steps checks,
// one interval apart.
func stepLostChecks(t *testing.T, clock *fakeBatchClock, steps int) {
	t.Helper()

	for range steps {
		clock.waitSleeping(t, bosh.BatchLostAnswerInterval)
		clock.advance(bosh.BatchLostAnswerInterval)
	}
}

// graceChecks is how many checks one interval apart reach the grace.
const graceChecks = int(bosh.UnknownOutcomeGrace / bosh.BatchLostAnswerInterval)

// lostAnswer returns an unknown outcome for the operation.
func poolLostAnswer(operation, deployment, contextID string) error {
	return &bosh.OutcomeUnknownError{
		Operation: operation, Director: batchTestAddress, Deployment: deployment, ContextID: contextID, Err: errPoolLost,
	}
}

// runAsync runs a call on its own goroutine and returns its error on the
// channel.
func runAsync(call func() error) chan error {
	result := make(chan error, 1)

	go func() { result <- call() }()

	return result
}

// expectPending checks that the call has not returned within a short
// quiet period.
func expectPending(t *testing.T, result chan error, what string) {
	t.Helper()

	select {
	case err := <-result:
		t.Fatalf("%s returned early with %v", what, err)
	case <-time.After(poolQuietPeriod):
	}
}

// expectResult waits for the call's error.
func expectResult(t *testing.T, result chan error, what string) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(poolWaitLimit):
		t.Fatalf("%s never returned", what)

		return nil
	}
}

// createRunning sends a deploy through the pool and returns the task the
// fake base started for it.
func createRunning(t *testing.T, pool *bosh.PooledDirector) int {
	t.Helper()

	task, err := pool.CreateDeployment(t.Context(), "name: "+poolDeployment)
	if err != nil || task == nil {
		t.Fatalf("the deploy failed: %+v, %v", task, err)
	}

	return task.ID
}

// A pooled deploy holds its slot until its task ends, even when nobody
// waits for the task.
func TestPooledSlotIsHeldUntilTheTaskEndsWhenNobodyWaits(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	taskID := createRunning(t, pool)

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the running deploy to hold its slot, got %d", active)
	}

	deleted := runAsync(func() error {
		_, err := pool.DeleteDeployment(t.Context(), poolDeployment)

		return err
	})

	expectPending(t, deleted, "the second mutation")

	if base.calls(poolDelete) != 0 {
		t.Fatal("the second mutation reached the director while the first task ran")
	}

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected one slot held while the task runs, got %d", active)
	}

	base.finish(t, taskID)

	err := expectResult(t, deleted, "the second mutation")
	if err != nil {
		t.Fatalf("the second mutation failed once the slot freed: %v", err)
	}
}

// A caller whose wait ends early leaves the slot held, and only the
// follower waits on the director.
func TestPooledSlotIsHeldUntilTheTaskEndsWhenTheCallersWaitEndsEarly(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	taskID := createRunning(t, pool)

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), callerKey{}, true))
	waited := runAsync(func() error {
		_, err := pool.WaitForTask(ctx, taskID)

		return err
	})

	waitUntil(t, "the follower waits on the director", func() bool { return base.waitCallsFor(taskID) == 1 })
	cancel()

	err := expectResult(t, waited, "the caller's wait")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the caller's wait to end with context.Canceled, got %v", err)
	}

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the slot to stay held after the caller stopped waiting, got %d", active)
	}

	if calls := base.waitCallsFor(taskID); calls != 1 {
		t.Fatalf("expected one WaitForTask call on the director, by the follower, got %d", calls)
	}

	for _, waitCtx := range base.waitContexts() {
		if waitCtx.Value(callerKey{}) != nil {
			t.Fatal("the director's WaitForTask ran under the caller's context")
		}
	}

	base.finish(t, taskID)
	waitSlotsFree(t, pool)
}

// A caller's wait gets the follower's final task, and never waits on the
// director under its own context.
func TestPooledWaitReturnsTheFollowersFinalTask(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	taskID := createRunning(t, pool)
	ctx := context.WithValue(t.Context(), callerKey{}, true)

	var final *bosh.Task

	waited := runAsync(func() error {
		var err error

		final, err = pool.WaitForTask(ctx, taskID)

		return err
	})

	waitUntil(t, "the follower waits on the director", func() bool { return base.waitCallsFor(taskID) == 1 })
	base.finish(t, taskID)

	err := expectResult(t, waited, "the caller's wait")
	if err != nil {
		t.Fatalf("the wait failed: %v", err)
	}

	if final == nil || final.ID != taskID || final.State != batchTaskDone {
		t.Fatalf("expected task %d in state done, got %+v", taskID, final)
	}

	for _, waitCtx := range base.waitContexts() {
		if waitCtx.Value(callerKey{}) != nil {
			t.Fatal("the caller's wait reached the director instead of the follower")
		}
	}
}

// A task that had already ended when the call returned frees its slot at
// once, and a wait for it reads nothing from the director.
func TestPooledTaskReturnedFinishedFreesTheSlotWithNoDirectorRead(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	ended := time.Now()
	base.answers[poolCreate] = fakePoolAnswer{task: &bosh.Task{
		ID: 7, State: batchTaskDone, Description: bosh.DeployTaskDescription, Deployment: poolDeployment, EndedAt: &ended,
	}}
	pool, _, _ := newTestPool(t, base, 1)

	createRunning(t, pool)

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected a finished task to free its slot at once, got %d held", active)
	}

	task, err := pool.WaitForTask(t.Context(), 7)
	if err != nil || task == nil || task.State != batchTaskDone {
		t.Fatalf("expected the finished task, got %+v and %v", task, err)
	}

	if waits, gets, lookups, checks := base.reads(); waits+gets+lookups+checks != 0 {
		t.Fatalf("expected no director read, got %d waits, %d task reads, %d lookups, and %d running checks", waits, gets, lookups, checks)
	}
}

// The follower waits under its own lifetime context, so a caller whose
// context ends does not end the follow or free the slot.
func TestPooledFollowerUsesALifetimeContextAndNotTheCallers(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), callerKey{}, true))

	task, err := pool.CreateDeployment(ctx, "name: "+poolDeployment)
	if err != nil {
		t.Fatalf("the deploy failed: %v", err)
	}

	waitUntil(t, "the follower waits on the director", func() bool { return base.waitCallsFor(task.ID) == 1 })
	cancel()
	time.Sleep(poolQuietPeriod)

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the slot to stay held after the caller's context ended, got %d", active)
	}

	contexts := base.waitContexts()
	if len(contexts) != 1 || contexts[0].Value(callerKey{}) != nil || contexts[0].Err() != nil {
		t.Fatalf("expected the follower to wait under a live context of its own, got %d waits", len(contexts))
	}

	base.finish(t, task.ID)
	waitSlotsFree(t, pool)
}

// A delete whose answer was lost holds its slot while the follower looks
// for its task by context ID, and frees it when the task it found ends.
func TestPooledSlotIsHeldThroughALostAnswerWhileTheFollowerLooks(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.addTask(9, poolDeployment, poolDeleteDesc)
	base.contextAnswers = []int{0, 0, 9}
	base.answers[poolDelete] = fakePoolAnswer{err: poolLostAnswer(poolDelete, poolDeployment, poolDeleteContext)}
	pool, clock, _ := newTestPool(t, base, 1)

	_, err := pool.DeleteDeployment(t.Context(), poolDeployment)
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)

	waitUntil(t, "the follower follows task 9", func() bool { return base.waitCallsFor(9) == 1 })

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the found task to hold the slot, got %d", active)
	}

	base.finish(t, 9)
	waitSlotsFree(t, pool)
}

// A lost delete answer follows the delete task, not a deploy that also
// runs on the deployment.
func TestPooledLostDeleteAnswerFollowsOnlyTheDeleteTask(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.addTask(20, poolDeployment, bosh.DeployTaskDescription)
	base.addTask(21, poolDeployment, poolDeleteDesc)
	base.running = []bosh.Task{
		{ID: 20, State: batchTaskProcessing, Description: bosh.DeployTaskDescription, Deployment: poolDeployment},
		{ID: 21, State: batchTaskProcessing, Description: poolDeleteDesc, Deployment: poolDeployment},
	}
	base.answers[poolDelete] = fakePoolAnswer{err: poolLostAnswer(poolDelete, poolDeployment, poolDeleteContext)}
	pool, _, _ := newTestPool(t, base, 1)

	_, err := pool.DeleteDeployment(t.Context(), poolDeployment)
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	waitUntil(t, "the follower follows the delete task", func() bool { return base.waitCallsFor(21) == 1 })

	if calls := base.waitCallsFor(20); calls != 0 {
		t.Fatalf("expected the deploy task not to be followed, got %d waits on it", calls)
	}

	base.finish(t, 21)
	waitSlotsFree(t, pool)
}

// A lost restart answer holds its slot past the grace while any task runs
// on the deployment, and never looks the task up by context ID.
func TestPooledLostJobStateAnswerHoldsTheSlotWhileAnyTaskRuns(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.running = []bosh.Task{{ID: 30, State: batchTaskProcessing, Description: errandDescription, Deployment: poolDeployment}}
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}
	pool, clock, _ := newTestPool(t, base, 1)

	_, err := pool.RestartDeployment(t.Context(), poolDeployment, bosh.RestartOpts{})
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	stepLostChecks(t, clock, graceChecks)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 2)
	checkSlotHeld(t, pool, clock)

	base.set(func(f *fakePoolBase) { f.running = nil })
	stepLostChecks(t, clock, 1)
	waitSlotsFree(t, pool)

	if _, _, lookups, checks := base.reads(); lookups != 0 || checks == 0 {
		t.Fatalf("expected running-task checks and no lookup by context ID, got %d lookups and %d checks", lookups, checks)
	}
}

// A lost restart answer with no task running on the deployment frees its
// slot at the first check at or after the grace.
func TestPooledLostJobStateAnswerFreesTheSlotAfterTheGraceWithNoTaskRunning(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}
	pool, clock, logger := newTestPool(t, base, 1)

	_, err := pool.RestartDeployment(t.Context(), poolDeployment, bosh.RestartOpts{})
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	stepLostChecks(t, clock, graceChecks-1)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)
	waitSlotsFree(t, pool)

	if logger.count("ERROR", "No task runs on deployment "+poolDeployment) != 1 {
		t.Fatalf("expected an Error line that says no task runs on %s, got %q", poolDeployment, poolLogLines(logger))
	}
}

// A lost restart answer keeps its slot while every running-task check
// fails, past the grace.
func TestPooledLostJobStateAnswerKeepsTheSlotWhileChecksFail(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.runningErr = errPoolRead
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}
	pool, clock, _ := newTestPool(t, base, 1)

	_, err := pool.RestartDeployment(t.Context(), poolDeployment, bosh.RestartOpts{})
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	stepLostChecks(t, clock, graceChecks+5)
	checkSlotHeld(t, pool, clock)
}

// A lost restart answer whose deployment stays busy is given up once the
// watch limit has passed since the answer was lost.
func TestPooledJobStateHoldGivesUpAfterTheCapWhileBusy(t *testing.T) {
	t.Parallel()

	const watchMax = 20 * time.Minute

	base := newFakePoolBase()
	base.running = []bosh.Task{{ID: 30, State: batchTaskProcessing, Description: errandDescription, Deployment: poolDeployment}}
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}

	logger := &recordingLogger{}
	pool := bosh.NewPooledDirector(base, 1, time.Minute, logger)
	clock := newFakeBatchClock()
	bosh.SetPoolClock(pool, clock)
	bosh.SetPoolWatchMax(pool, watchMax)
	t.Cleanup(pool.Stop)

	_, err := pool.RestartDeployment(t.Context(), poolDeployment, bosh.RestartOpts{})
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	waited := runAsync(func() error {
		_, err := bosh.AwaitPoolContextID(t.Context(), pool, poolRestartContext)

		return err
	})

	capChecks := int(watchMax / bosh.BatchLostAnswerInterval)
	stepLostChecks(t, clock, capChecks-1)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)
	waitSlotsFree(t, pool)

	if logger.count("ERROR", "Deployment "+poolDeployment, "never went idle") != 1 {
		t.Fatalf("expected an Error line that says %s never went idle, got %q", poolDeployment, poolLogLines(logger))
	}

	err = expectResult(t, waited, "the waiter")
	if !errors.Is(err, bosh.ErrWatchAbandoned) {
		t.Fatalf("expected the waiter to get ErrWatchAbandoned, got %v", err)
	}
}

// newTestPoolWithWatchMax builds a pool like newTestPool whose watch limit
// is watchMax.
func newTestPoolWithWatchMax(t *testing.T, base bosh.Director, watchMax time.Duration) (*bosh.PooledDirector, *fakeBatchClock, *recordingLogger) {
	t.Helper()

	logger := &recordingLogger{}
	pool := bosh.NewPooledDirector(base, 1, time.Minute, logger)
	clock := newFakeBatchClock()
	bosh.SetPoolClock(pool, clock)
	bosh.SetPoolWatchMax(pool, watchMax)
	t.Cleanup(pool.Stop)

	return pool, clock, logger
}

// poolCheckFailure is the director's error answer, with the HTTP status
// given, to a check for the tasks running on a deployment.
func poolCheckFailure(status int) error {
	return fmt.Errorf("the check for running tasks failed: %w", &boshapi.APIError{
		StatusCode: status, Method: http.MethodGet, Path: taskListRoute, Description: http.StatusText(status),
	})
}

// awaitLostRestart starts a lost restart answer on the pool and returns a
// channel that gets the error its waiter gets.
func awaitLostRestart(t *testing.T, pool *bosh.PooledDirector) chan error {
	t.Helper()

	_, err := pool.RestartDeployment(t.Context(), poolDeployment, bosh.RestartOpts{})
	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected the lost answer, got %v", err)
	}

	return runAsync(func() error {
		_, err := bosh.AwaitPoolContextID(t.Context(), pool, poolRestartContext)

		return err
	})
}

// A lost restart answer whose checks find the deployment busy until just
// before the cap, and then fail, is given up at the cap and not later,
// because a failed check neither ends the hold nor moves the cap.
func TestPooledLostJobStateAnswerCapIgnoresFailedReads(t *testing.T) {
	t.Parallel()

	const watchMax = 20 * time.Minute

	base := newFakePoolBase()
	base.running = []bosh.Task{{ID: 30, State: batchTaskProcessing, Description: errandDescription, Deployment: poolDeployment}}
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}
	pool, clock, logger := newTestPoolWithWatchMax(t, base, watchMax)
	waited := awaitLostRestart(t, pool)

	capChecks := int(watchMax / bosh.BatchLostAnswerInterval)
	stepLostChecks(t, clock, capChecks-2)
	checkSlotHeld(t, pool, clock)

	base.set(func(f *fakePoolBase) { f.runningErr = poolCheckFailure(http.StatusInternalServerError) })
	stepLostChecks(t, clock, 1)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)
	waitSlotsFree(t, pool)

	err := expectResult(t, waited, "the waiter")
	if !errors.Is(err, bosh.ErrWatchAbandoned) {
		t.Fatalf("expected the waiter to get ErrWatchAbandoned, got %v", err)
	}

	if logger.count("ERROR", "deployment "+poolDeployment, watchMax.String(), "released its slot") != 1 {
		t.Fatalf("expected one Error line that names %s and the %v cap and frees the slot, got %q", poolDeployment, watchMax, poolLogLines(logger))
	}
}

// A lost restart answer whose every check the director refuses keeps its
// slot past the grace, and frees it once the watch limit has passed since
// the answer was lost.
func TestPooledLostJobStateAnswerKeepsTheSlotThroughRefusedReads(t *testing.T) {
	t.Parallel()

	const watchMax = 30 * time.Minute

	base := newFakePoolBase()
	base.runningErr = poolCheckFailure(http.StatusForbidden)
	base.answers[poolRestart] = fakePoolAnswer{err: poolLostAnswer(poolRestart, poolDeployment, poolRestartContext)}
	pool, clock, logger := newTestPoolWithWatchMax(t, base, watchMax)
	waited := awaitLostRestart(t, pool)

	stepLostChecks(t, clock, graceChecks+1)
	checkSlotHeld(t, pool, clock)

	capChecks := int(watchMax / bosh.BatchLostAnswerInterval)
	stepLostChecks(t, clock, capChecks-graceChecks-2)
	checkSlotHeld(t, pool, clock)
	stepLostChecks(t, clock, 1)
	waitSlotsFree(t, pool)

	err := expectResult(t, waited, "the waiter")
	if !errors.Is(err, bosh.ErrWatchAbandoned) {
		t.Fatalf("expected the waiter to get ErrWatchAbandoned, got %v", err)
	}

	if logger.count("ERROR", "deployment "+poolDeployment, watchMax.String(), "released its slot") != 1 {
		t.Fatalf("expected one Error line that names %s and the %v cap and frees the slot, got %q", poolDeployment, watchMax, poolLogLines(logger))
	}
}

// A lost upload or Cleanup answer, which has no task the director can be
// asked for, holds its slot for the grace and asks the director nothing.
func TestPooledLostUploadAndCleanupAnswersHoldTheSlotForTheGrace(t *testing.T) {
	t.Parallel()

	cases := []struct {
		method string
		kind   string
		call   func(ctx context.Context, pool *bosh.PooledDirector) error
	}{
		{poolUploadRelease, "release upload", func(ctx context.Context, pool *bosh.PooledDirector) error {
			_, err := pool.UploadRelease(ctx, "https://127.0.0.1/release.tgz", "sha1")

			return err
		}},
		{poolUploadStemcell, "stemcell upload", func(ctx context.Context, pool *bosh.PooledDirector) error {
			_, err := pool.UploadStemcell(ctx, "https://127.0.0.1/stemcell.tgz", "sha1")

			return err
		}},
		{poolCleanup, poolCleanupKind, func(ctx context.Context, pool *bosh.PooledDirector) error {
			_, err := pool.Cleanup(ctx, true)

			return err
		}},
	}

	for _, test := range cases {
		t.Run(test.method, func(t *testing.T) {
			t.Parallel()

			contextID := "ctx-" + test.method
			base := newFakePoolBase()
			base.answers[test.method] = fakePoolAnswer{err: poolLostAnswer(test.method, "", contextID)}
			pool, clock, logger := newTestPool(t, base, 1)

			err := test.call(t.Context(), pool)
			if !errors.Is(err, bosh.ErrOutcomeUnknown) {
				t.Fatalf("expected the lost answer, got %v", err)
			}

			waited := runAsync(func() error {
				_, err := bosh.AwaitPoolContextID(t.Context(), pool, contextID)

				return err
			})

			stepLostChecks(t, clock, graceChecks-1)
			checkSlotHeld(t, pool, clock)
			stepLostChecks(t, clock, 1)
			waitSlotsFree(t, pool)

			if waits, gets, lookups, checks := base.reads(); waits+gets+lookups+checks != 0 {
				t.Fatalf("expected no director call after the error, got %d waits, %d task reads, %d lookups, and %d running checks",
					waits, gets, lookups, checks)
			}

			if logger.count("ERROR", "the "+test.kind, "can't be asked") != 1 {
				t.Fatalf("expected an Error line that names the %s, got %q", test.kind, poolLogLines(logger))
			}

			err = expectResult(t, waited, "the waiter")
			if !errors.Is(err, bosh.ErrWatchAbandoned) {
				t.Fatalf("expected the waiter to get ErrWatchAbandoned, got %v", err)
			}
		})
	}
}

// expectSlotFreedAtOnce checks that a mutation whose error shows that no
// task started frees its slot at once and starts no watcher.
func expectSlotFreedAtOnce(t *testing.T, base *fakePoolBase, rejection error, reason string) {
	t.Helper()

	base.answers[poolCreate] = fakePoolAnswer{err: rejection}
	pool, _, logger := newTestPool(t, base, 1)

	_, err := pool.CreateDeployment(t.Context(), "name: "+poolDeployment)
	if err == nil {
		t.Fatal("expected the deploy to fail")
	}

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected the slot to free at once, got %d held", active)
	}

	if running := bosh.PoolWatchersRunning(pool); running != 0 {
		t.Fatalf("expected no watcher, got %d", running)
	}

	if logger.count("INFO", "Released the pool slot of deployment "+poolDeployment+" at once", reason) != 1 {
		t.Fatalf("expected the release to be logged with %q, got %q", reason, poolLogLines(logger))
	}

	deleted := runAsync(func() error {
		_, err := pool.DeleteDeployment(t.Context(), poolDeployment)

		return err
	})

	err = expectResult(t, deleted, "the next mutation")
	if err != nil {
		t.Fatalf("the next mutation failed: %v", err)
	}
}

// A rejection from the director frees the slot at once.
func TestPooledSlotIsReleasedAtOnceOnARejection(t *testing.T) {
	t.Parallel()

	rejection := fmt.Errorf("deploy: %w", &boshapi.APIError{
		StatusCode: 400, Method: http.MethodPost, Path: deploymentsRoute, Description: "the manifest is invalid", Mutation: true,
	})

	expectSlotFreedAtOnce(t, newFakePoolBase(), rejection, "the director answered the deploy with an error")
}

// A request that never reached the director frees the slot at once.
func TestPooledSlotIsReleasedAtOnceOnAnUnsentRequest(t *testing.T) {
	t.Parallel()

	unsent := fmt.Errorf("deploy: %w", &boshapi.TransportError{
		Method: http.MethodPost, Path: deploymentsRoute, Sent: false, Mutation: true, Err: errPoolRead,
	})

	expectSlotFreedAtOnce(t, newFakePoolBase(), unsent, "the deploy request never reached the director")
}

// The director's 401 to a sent request, whose token then failed to
// refresh, carries an AuthError as well. It still frees the slot as the
// director's answer, because the request was sent.
func TestPooledSlotNamesTheDirectorsAnswerWhenARefreshFailsAfterA401(t *testing.T) {
	t.Parallel()

	rejected := fmt.Errorf("deploy: %w; the request was not resent, because refreshing the token failed: %w",
		&boshapi.APIError{StatusCode: http.StatusUnauthorized, Method: http.MethodPost, Path: deploymentsRoute, Mutation: true},
		&boshapi.AuthError{Stage: boshapi.AuthStageToken, Err: errPoolRead})

	expectSlotFreedAtOnce(t, newFakePoolBase(), rejected, "the director answered the deploy with an error")
}

// A task the director has no record of frees its slot.
func TestPooledFollowerReleasesTheSlotWhenTheTaskIs404(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.answers[poolCreate] = fakePoolAnswer{task: &bosh.Task{ID: 55, State: batchTaskProcessing, Deployment: poolDeployment}}
	pool, _, logger := newTestPool(t, base, 1)

	createRunning(t, pool)
	waitSlotsFree(t, pool)

	if logger.count("ERROR", "The director has no BOSH task 55 of deployment "+poolDeployment+", so the pooled director released its slot") != 1 {
		t.Fatalf("expected the 404 to be logged, got %q", poolLogLines(logger))
	}
}

// A task whose reads fail for the watch limit frees its slot.
func TestPooledFollowerReleasesTheSlotAfterTheMaximumWatch(t *testing.T) {
	t.Parallel()

	const watchMax = 10 * time.Minute

	base := newFakePoolBase()
	base.waitErr = errPoolRead
	base.getErr = errPoolRead

	logger := &recordingLogger{}
	pool := bosh.NewPooledDirector(base, 1, time.Minute, logger)
	clock := newFakeBatchClock()
	bosh.SetPoolClock(pool, clock)
	bosh.SetPoolWatchMax(pool, watchMax)
	t.Cleanup(pool.Stop)

	taskID := createRunning(t, pool)

	for elapsed := time.Duration(0); elapsed < watchMax; {
		waitUntil(t, "the follower waits to read the task again", func() bool { return clock.retrySleep() > 0 })

		if active := activeSlots(t, pool); active != 1 {
			t.Fatalf("expected the slot held after %v of failed reads, got %d", elapsed, active)
		}

		backoff := clock.retrySleep()
		clock.advance(backoff)
		elapsed += backoff
	}

	waitSlotsFree(t, pool)

	if logger.count("ERROR", fmt.Sprintf("could not read BOSH task %d of deployment %s", taskID, poolDeployment)) != 1 {
		t.Fatalf("expected the give-up to be logged, got %q", poolLogLines(logger))
	}
}

// Stop ends every follow, frees its slot, and ends its waiters with
// context.Canceled.
func TestPooledStopEndsFollowsAndFreesTheirSlots(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 2)

	first := createRunning(t, pool)
	second := createRunning(t, pool)

	waits := make([]chan error, 0, 2)

	for _, taskID := range []int{first, second} {
		waits = append(waits, runAsync(func() error {
			_, err := pool.WaitForTask(t.Context(), taskID)

			return err
		}))
	}

	waitUntil(t, "both follows wait on the director", func() bool {
		return base.waitCallsFor(first) == 1 && base.waitCallsFor(second) == 1
	})

	pool.Stop()

	for _, waited := range waits {
		err := expectResult(t, waited, "a waiter")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected a waiter to get context.Canceled, got %v", err)
		}
	}

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected Stop to free every slot, got %d held", active)
	}

	if running := bosh.PoolWatchersRunning(pool); running != 0 {
		t.Fatalf("expected no watcher after Stop, got %d", running)
	}
}

// A mutation the director accepts after Stop is not followed, and its slot
// frees when the call returns.
func TestPooledMutationAfterStopIsNotFollowed(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, logger := newTestPool(t, base, 1)

	pool.Stop()

	taskID := createRunning(t, pool)

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected the slot to free when the call returned, got %d held", active)
	}

	want := fmt.Sprintf("The pooled director is stopped, so it released the slot of BOSH task %d of deployment %s without following it", taskID, poolDeployment)
	if logger.count("INFO", want) != 1 {
		t.Fatalf("expected %q, got %q", want, poolLogLines(logger))
	}
}

// Adopt counts a task in a full pool without waiting, and the next
// mutation waits until both the running task and the adopted one end.
func TestPooledAdoptCountsTheTaskWithoutWaiting(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	base.addTask(3, "", bosh.DeployTaskDescription)
	pool, _, _ := newTestPool(t, base, 1)

	running := createRunning(t, pool)

	adopted := runAsync(func() error {
		pool.Adopt(3)

		return nil
	})

	err := expectResult(t, adopted, "Adopt")
	if err != nil {
		t.Fatalf("Adopt failed: %v", err)
	}

	if active := activeSlots(t, pool); active != 2 {
		t.Fatalf("expected the adopted task to be counted past the limit, got %d held", active)
	}

	deleted := runAsync(func() error {
		_, err := pool.DeleteDeployment(t.Context(), poolDeployment)

		return err
	})

	expectPending(t, deleted, "the next mutation")
	base.finish(t, running)
	waitUntil(t, "one slot is held", func() bool { return activeSlots(t, pool) == 1 })
	expectPending(t, deleted, "the next mutation")

	if base.calls(poolDelete) != 0 {
		t.Fatal("the next mutation reached the director before the adopted task ended")
	}

	base.finish(t, 3)

	err = expectResult(t, deleted, "the next mutation")
	if err != nil {
		t.Fatalf("the next mutation failed: %v", err)
	}
}

// Adopting a task the pool already follows keeps one follow and one slot.
func TestPooledAdoptOfAFollowedTaskKeepsOneFollow(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, logger := newTestPool(t, base, 1)

	taskID := createRunning(t, pool)
	waitUntil(t, "the follower waits on the director", func() bool { return base.waitCallsFor(taskID) == 1 })

	pool.Adopt(taskID)

	want := fmt.Sprintf("The pooled director already follows BOSH task %d, so it is not adopted again", taskID)
	if logger.count("INFO", want) != 1 {
		t.Fatalf("expected %q, got %q", want, poolLogLines(logger))
	}

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected one slot held, got %d", active)
	}

	if running := bosh.PoolWatchersRunning(pool); running != 1 {
		t.Fatalf("expected one watcher, got %d", running)
	}
}

// An errand holds its slot for the call only, so a caller whose context
// ends frees the slot while the errand runs on.
func TestPooledRunErrandFreesItsSlotWhenTheCallReturns(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	ctx, cancel := context.WithCancel(t.Context())
	errand := runAsync(func() error {
		_, err := pool.RunErrand(ctx, poolDeployment, "smoke-tests", bosh.ErrandOpts{})

		return err
	})

	select {
	case <-base.errandStarted:
	case <-time.After(poolWaitLimit):
		t.Fatal("the errand never reached the director")
	}

	if active := activeSlots(t, pool); active != 1 {
		t.Fatalf("expected the errand to hold its slot during the call, got %d", active)
	}

	cancel()

	err := expectResult(t, errand, "the errand")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the errand to end with its caller's context, got %v", err)
	}

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected the errand's slot to free when the call returned, got %d held", active)
	}

	if running := bosh.PoolWatchersRunning(pool); running != 0 {
		t.Fatalf("expected no watcher for an errand, got %d", running)
	}
}

// A manifest that names no deployment is refused before a slot is taken or
// the director is called.
func TestPooledCreateDeploymentRefusesAManifestWithNoName(t *testing.T) {
	t.Parallel()

	base := newFakePoolBase()
	pool, _, _ := newTestPool(t, base, 1)

	_, err := pool.CreateDeployment(t.Context(), "instance_groups: []\n")
	if !errors.Is(err, bosh.ErrDeploymentNameExtractionFailed) {
		t.Fatalf("expected ErrDeploymentNameExtractionFailed, got %v", err)
	}

	if calls := base.calls(poolCreate); calls != 0 {
		t.Fatalf("expected no call to the director, got %d", calls)
	}

	stats, statsErr := pool.GetPoolStats()
	if statsErr != nil {
		t.Fatalf("GetPoolStats failed: %v", statsErr)
	}

	if stats.TotalRequests != 0 || stats.ActiveConnections != 0 {
		t.Fatalf("expected no slot taken, got %d requests and %d held", stats.TotalRequests, stats.ActiveConnections)
	}
}

// poolLogLines returns a copy of every line the logger recorded.
func poolLogLines(logger *recordingLogger) []string {
	logger.mu.Lock()
	defer logger.mu.Unlock()

	return append([]string(nil), logger.lines...)
}
