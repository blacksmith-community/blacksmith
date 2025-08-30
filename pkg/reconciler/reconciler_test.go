package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Mock implementations for testing

type mockScanner struct {
	mu          sync.Mutex
	deployments []DeploymentInfo
	details     map[string]*DeploymentDetail
	scanErr     error
	detailErr   error
	scanCalls   int
	detailCalls map[string]int
}

func newMockScanner() *mockScanner {
	return &mockScanner{
		details:     make(map[string]*DeploymentDetail),
		detailCalls: make(map[string]int),
	}
}

func (m *mockScanner) ScanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanCalls++
	if m.scanErr != nil {
		return nil, m.scanErr
	}
	return m.deployments, nil
}

func (m *mockScanner) GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.detailCalls[name]++
	if m.detailErr != nil {
		return nil, m.detailErr
	}
	if detail, ok := m.details[name]; ok {
		return detail, nil
	}
	return nil, errors.New("deployment not found")
}

type mockMatcher struct {
	mu         sync.Mutex
	matches    map[string]*MatchResult
	matchErr   error
	matchCalls map[string]int
}

func newMockMatcher() *mockMatcher {
	return &mockMatcher{
		matches:    make(map[string]*MatchResult),
		matchCalls: make(map[string]int),
	}
}

func (m *mockMatcher) MatchDeployment(deployment DeploymentInfo, services []Service) (*MatchResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matchCalls[deployment.Name]++
	if m.matchErr != nil {
		return nil, m.matchErr
	}
	if match, ok := m.matches[deployment.Name]; ok {
		return match, nil
	}
	return nil, nil // No match found
}

func (m *mockMatcher) ValidateMatch(match *MatchResult) error {
	return nil
}

type mockUpdater struct {
	updateCalls []updateCall
	updateErr   error
}

type updateCall struct {
	instance *InstanceData
}

func (m *mockUpdater) UpdateInstance(ctx context.Context, instance *InstanceData) error {
	m.updateCalls = append(m.updateCalls, updateCall{
		instance: instance,
	})
	return m.updateErr
}

func (m *mockUpdater) GetInstance(ctx context.Context, instanceID string) (*InstanceData, error) {
	return nil, nil
}

type mockSynchronizer struct {
	syncCalls     int
	syncErr       error
	lastInstances []string
}

func (m *mockSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	m.syncCalls++
	m.lastInstances = make([]string, len(instances))
	for i, inst := range instances {
		m.lastInstances[i] = inst.ID
	}
	return m.syncErr
}

func (m *mockSynchronizer) ValidateIndex(ctx context.Context) error {
	return nil
}

type mockMetricsCollector struct {
	metrics    map[string]interface{}
	errorCount int
}

func newMockMetricsCollector() *mockMetricsCollector {
	return &mockMetricsCollector{
		metrics: make(map[string]interface{}),
	}
}

func (m *mockMetricsCollector) ReconciliationStarted() {
	m.metrics["started"] = true
}

func (m *mockMetricsCollector) ReconciliationCompleted(duration time.Duration) {
	m.metrics["last_duration"] = duration
}

func (m *mockMetricsCollector) ReconciliationError(err error) {
	m.errorCount++
	if count, ok := m.metrics["errors"].(int); ok {
		m.metrics["errors"] = count + 1
	} else {
		m.metrics["errors"] = 1
	}
}

func (m *mockMetricsCollector) DeploymentsScanned(count int) {
	m.metrics["deployments_scanned"] = count
}

func (m *mockMetricsCollector) InstancesMatched(count int) {
	m.metrics["instances_matched"] = count
}

func (m *mockMetricsCollector) InstancesUpdated(count int) {
	m.metrics["instances_updated"] = count
}

func (m *mockMetricsCollector) GetMetrics() Metrics {
	return Metrics{
		TotalDeployments:     getIntMetric(m.metrics, "deployments_scanned"),
		TotalInstancesFound:  getIntMetric(m.metrics, "instances_matched"),
		TotalInstancesSynced: getIntMetric(m.metrics, "instances_updated"),
		TotalErrors:          getIntMetric(m.metrics, "errors"),
	}
}

func getIntMetric(metrics map[string]interface{}, key string) int64 {
	if val, ok := metrics[key].(int); ok {
		return int64(val)
	}
	return 0
}

type mockLogger struct {
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func newMockLogger() *mockLogger {
	return &mockLogger{}
}

func (l *mockLogger) Debug(format string, args ...interface{}) {
	l.debugLogs = append(l.debugLogs, format)
}

func (l *mockLogger) Info(format string, args ...interface{}) {
	l.infoLogs = append(l.infoLogs, format)
}

func (l *mockLogger) Warning(format string, args ...interface{}) {
	l.warnLogs = append(l.warnLogs, format)
}

func (l *mockLogger) Error(format string, args ...interface{}) {
	l.errorLogs = append(l.errorLogs, format)
}

// Test ReconcilerManager

func TestReconcilerManager_Start(t *testing.T) {
	config := ReconcilerConfig{
		Enabled:        true,
		Interval:       100 * time.Millisecond,
		MaxConcurrency: 2,
		BatchSize:      5,
	}

	scanner := newMockScanner()
	matcher := newMockMatcher()
	updater := &mockUpdater{}
	synchronizer := &mockSynchronizer{}
	metrics := newMockMetricsCollector()
	logger := newMockLogger()

	ctx, cancel := context.WithCancel(context.Background())
	manager := &reconcilerManager{
		config:       config,
		scanner:      scanner,
		matcher:      matcher,
		updater:      updater,
		synchronizer: synchronizer,
		metrics:      metrics,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
	}

	ctx2 := context.Background()
	err := manager.Start(ctx2)
	if err != nil {
		t.Fatalf("Failed to start manager: %v", err)
	}

	// Let it run for a bit
	time.Sleep(50 * time.Millisecond)

	status := manager.GetStatus()
	if !status.Running {
		t.Error("Manager should be running")
	}

	// Stop the manager
	err = manager.Stop()
	if err != nil {
		t.Fatalf("Failed to stop manager: %v", err)
	}

	status = manager.GetStatus()
	if status.Running {
		t.Error("Manager should not be running after stop")
	}
}

func TestReconcilerManager_RunReconciliation(t *testing.T) {
	scanner := newMockScanner()
	matcher := newMockMatcher()
	updater := &mockUpdater{}
	synchronizer := &mockSynchronizer{}
	metrics := newMockMetricsCollector()
	logger := newMockLogger()

	// Setup test data with service deployments that match new naming convention
	scanner.deployments = []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "blacksmith"}, // Platform deployment, should be filtered out
	}

	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	manager := &reconcilerManager{
		config: ReconcilerConfig{
			MaxConcurrency: 2,
			BatchSize:      2,
			RetryAttempts:  1,
			RetryDelay:     10 * time.Millisecond,
		},
		scanner:      scanner,
		matcher:      matcher,
		updater:      updater,
		synchronizer: synchronizer,
		metrics:      metrics,
		logger:       logger,
		ctx:          ctx3,
		cancel:       cancel3,
		cfManager:    nil,         // No CF manager in test
		services:     []Service{}, // Empty services for test
	}

	// Run reconciliation directly
	manager.runReconciliation()

	// Verify scanner was called
	if scanner.scanCalls != 1 {
		t.Errorf("Expected 1 scan call, got %d", scanner.scanCalls)
	}

	// In the new CF-first approach, we should have:
	// - 2 service deployments scanned (blacksmith filtered out)
	// - 0 CF instances (no CF manager)
	// - Metrics for deployments scanned should be 2
	if metrics.metrics["deployments_scanned"] != 2 {
		t.Errorf("Expected 2 deployments scanned, got %v", metrics.metrics["deployments_scanned"])
	}

	// Since we don't have CF instances and the new logic builds  data,
	// we expect 2 instances matched (from BOSH deployments)
	if metrics.metrics["instances_matched"] != 2 {
		t.Errorf("Expected 2 instances matched, got %v", metrics.metrics["instances_matched"])
	}

	// Verify synchronizer was called with the instances
	if synchronizer.syncCalls != 1 {
		t.Errorf("Expected 1 sync call, got %d", synchronizer.syncCalls)
	}
}

func TestReconcilerManager_RunReconciliation_WithErrors(t *testing.T) {
	scanner := newMockScanner()
	matcher := newMockMatcher()
	updater := &mockUpdater{}
	synchronizer := &mockSynchronizer{}
	metrics := newMockMetricsCollector()
	logger := newMockLogger()

	// Simulate error during scanning
	scanner.scanErr = fmt.Errorf("scan error")

	ctx4, cancel4 := context.WithCancel(context.Background())
	defer cancel4()
	manager := &reconcilerManager{
		config: ReconcilerConfig{
			MaxConcurrency: 2,
			BatchSize:      2,
		},
		scanner:      scanner,
		matcher:      matcher,
		updater:      updater,
		synchronizer: synchronizer,
		metrics:      metrics,
		logger:       logger,
		ctx:          ctx4,
		cancel:       cancel4,
		cfManager:    nil,
		services:     []Service{},
	}

	// Run reconciliation with error
	manager.runReconciliation()

	// Verify error was recorded in metrics
	if errorCount, ok := metrics.metrics["errors"].(int); !ok || errorCount == 0 {
		t.Error("Expected error to be recorded in metrics")
	}

	// Verify status contains error
	status := manager.GetStatus()
	if len(status.Errors) == 0 {
		t.Errorf("Expected error to be recorded in status. Status: %+v, Scanner calls: %d, Scanner error: %v", status, scanner.scanCalls, scanner.scanErr)
	}
}

func TestReconcilerManager_ForceReconcile(t *testing.T) {
	config := ReconcilerConfig{
		Enabled:  true,
		Interval: 10 * time.Second, // Long interval
	}

	scanner := newMockScanner()
	scanner.deployments = []DeploymentInfo{
		{Name: "test-deployment"},
	}

	matcher := newMockMatcher()
	updater := &mockUpdater{}
	synchronizer := &mockSynchronizer{}
	metrics := newMockMetricsCollector()
	logger := newMockLogger()

	ctx, cancel := context.WithCancel(context.Background())
	manager := &reconcilerManager{
		config:       config,
		scanner:      scanner,
		matcher:      matcher,
		updater:      updater,
		synchronizer: synchronizer,
		metrics:      metrics,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
	}

	ctx2 := context.Background()
	err := manager.Start(ctx2)
	if err != nil {
		t.Fatalf("Failed to start manager: %v", err)
	}

	// Run reconciliation directly since we don't have ForceReconcile
	go manager.runReconciliation()

	// Give it time to process
	time.Sleep(100 * time.Millisecond)

	// Verify scanner was called
	if scanner.scanCalls == 0 {
		t.Error("Expected scanner to be called after force reconcile")
	}

	manager.Stop()
}

func TestReconcilerManager_ProcessBatch(t *testing.T) {
	scanner := newMockScanner()
	matcher := newMockMatcher()
	updater := &mockUpdater{}
	logger := newMockLogger()

	// Setup test data
	deployments := []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "invalid-deployment"},
	}

	scanner.details = map[string]*DeploymentDetail{
		"redis-cache-small-12345678-1234-1234-1234-123456789abc": {
			DeploymentInfo: DeploymentInfo{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		},
		"postgres-basic-87654321-4321-4321-4321-cba987654321": {
			DeploymentInfo: DeploymentInfo{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		},
		"invalid-deployment": {
			DeploymentInfo: DeploymentInfo{Name: "invalid-deployment"},
		},
	}

	// Only the properly named deployments have matches
	matcher.matches["redis-cache-small-12345678-1234-1234-1234-123456789abc"] = &MatchResult{
		InstanceID: "12345678-1234-1234-1234-123456789abc",
		ServiceID:  "redis-cache",
		PlanID:     "small",
	}
	matcher.matches["postgres-basic-87654321-4321-4321-4321-cba987654321"] = &MatchResult{
		InstanceID: "87654321-4321-4321-4321-cba987654321",
		ServiceID:  "postgres",
		PlanID:     "basic",
	}

	manager := &reconcilerManager{
		config: ReconcilerConfig{
			MaxConcurrency: 2,
		},
		scanner: scanner,
		matcher: matcher,
		updater: updater,
		logger:  logger,
	}

	ctx := context.Background()
	matches, err := manager.processBatch(ctx, deployments)
	if err != nil {
		t.Fatalf("processBatch failed: %v", err)
	}

	// Should have 2 matches (properly named deployments)
	if len(matches) != 2 {
		t.Errorf("Expected 2 matches, got %d", len(matches))
	}

	// Verify all deployments were processed
	if len(matcher.matchCalls) != 3 {
		t.Errorf("Expected 3 match calls, got %d", len(matcher.matchCalls))
	}

	// processBatch doesn't call updater - that happens in updateVault
	if len(updater.updateCalls) != 0 {
		t.Errorf("Expected 0 update calls in processBatch, got %d", len(updater.updateCalls))
	}
}

func TestReconcilerManager_ProcessBatch_Concurrent(t *testing.T) {
	scanner := newMockScanner()
	matcher := newMockMatcher()
	updater := &mockUpdater{}
	logger := newMockLogger()

	// Setup test data - many deployments
	var deployments []DeploymentInfo
	for i := 0; i < 10; i++ {
		uuid := fmt.Sprintf("12345678-1234-1234-1234-%012d", i)
		name := fmt.Sprintf("redis-cache-small-%s", uuid)
		deployments = append(deployments, DeploymentInfo{Name: name})
		scanner.details[name] = &DeploymentDetail{
			DeploymentInfo: DeploymentInfo{Name: name},
		}
		// Only first 5 have matches
		if i < 5 {
			matcher.matches[name] = &MatchResult{
				InstanceID: uuid,
				ServiceID:  "redis-cache",
				PlanID:     "small",
			}
		}
	}

	manager := &reconcilerManager{
		config: ReconcilerConfig{
			MaxConcurrency: 3, // Limited concurrency
		},
		scanner: scanner,
		matcher: matcher,
		updater: updater,
		logger:  logger,
	}

	ctx := context.Background()
	matches, err := manager.processBatch(ctx, deployments)
	if err != nil {
		t.Fatalf("processBatch failed: %v", err)
	}

	// Should have 5 matches
	if len(matches) != 5 {
		t.Errorf("Expected 5 matches, got %d", len(matches))
	}

	// Verify all deployments were processed
	if len(matcher.matchCalls) != 10 {
		t.Errorf("Expected 10 match calls, got %d", len(matcher.matchCalls))
	}

	// processBatch doesn't call updater - that happens in updateVault
	if len(updater.updateCalls) != 0 {
		t.Errorf("Expected 0 update calls in processBatch, got %d", len(updater.updateCalls))
	}
}
