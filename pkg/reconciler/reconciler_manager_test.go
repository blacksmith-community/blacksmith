package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

// Static errors for this test file
var (
	errSyncFailure = errors.New("sync failure")
)

// Local test doubles matching current interfaces

type rmMockScanner struct {
	mu          sync.Mutex
	deployments []DeploymentInfo
	scanCalls   int
}

func (m *rmMockScanner) ScanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.scanCalls++

	return m.deployments, nil
}

func (m *rmMockScanner) GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error) {
	// Minimal detail used by reconciler paths
	return &DeploymentDetail{DeploymentInfo: DeploymentInfo{Name: name}}, nil
}

func (m *rmMockScanner) getScanCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.scanCalls
}

type rmMockUpdater struct {
	mu    sync.Mutex
	calls []InstanceData
	err   error
}

func (u *rmMockUpdater) UpdateInstance(ctx context.Context, inst InstanceData) (*InstanceData, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.calls = append(u.calls, inst)
	if u.err != nil {
		return nil, u.err
	}
	// echo back instance
	return &inst, nil
}

func (u *rmMockUpdater) UpdateBatch(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	// not used by current reconciler manager paths
	u.mu.Lock()
	defer u.mu.Unlock()

	u.calls = append(u.calls, instances...)

	return instances, u.err
}

func (u *rmMockUpdater) CheckBindingHealth(instanceID string) ([]BindingInfo, []string, error) {
	return nil, nil, nil
}

func (u *rmMockUpdater) ReconstructBindingWithBroker(instanceID, bindingID string, broker BrokerInterface) error {
	return nil
}

func (u *rmMockUpdater) RepairInstanceBindings(instanceID string, broker BrokerInterface) error {
	return nil
}

func (u *rmMockUpdater) UpdateInstanceWithBindingRepair(ctx context.Context, instance InstanceData, broker BrokerInterface) (*InstanceData, error) {
	return &instance, nil
}

func (u *rmMockUpdater) getCalls() []InstanceData {
	u.mu.Lock()
	defer u.mu.Unlock()

	callsCopy := make([]InstanceData, len(u.calls))
	copy(callsCopy, u.calls)

	return callsCopy
}

type rmMockSynchronizer struct {
	mu    sync.Mutex
	calls [][]InstanceData
	err   error
}

func (s *rmMockSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	// record a copy for assertions
	cp := make([]InstanceData, len(instances))
	copy(cp, instances)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, cp)

	return s.err
}

func (s *rmMockSynchronizer) getCalls() [][]InstanceData {
	s.mu.Lock()
	defer s.mu.Unlock()

	callsCopy := make([][]InstanceData, len(s.calls))
	for i, call := range s.calls {
		callCopy := make([]InstanceData, len(call))
		copy(callCopy, call)
		callsCopy[i] = callCopy
	}

	return callsCopy
}

// Helper to build a minimally wired manager with our mocks.
func newTestManager(t *testing.T) (*ReconcilerManager, *rmMockScanner, *rmMockUpdater, *rmMockSynchronizer) {
	t.Helper()

	logger := NewMockLogger()

	cfg := ReconcilerConfig{
		Enabled: true,
		// Intentionally small; validation will warn but manager still initializes
		Interval: 50 * time.Millisecond,
		Concurrency: ConcurrencyConfig{
			MaxConcurrent:  2,
			QueueSize:      10,
			WorkerPoolSize: 2,
			CooldownPeriod: time.Millisecond,
		},
		Batch: BatchConfig{
			Size:            5,
			AdaptiveScaling: false,
		},
		APIs: APIConfigs{
			BOSH:  APIConfig{RateLimit: RateLimitConfig{RequestsPerSecond: 1000, Burst: 1000, WaitTimeout: time.Millisecond}},
			CF:    APIConfig{RateLimit: RateLimitConfig{RequestsPerSecond: 1000, Burst: 1000, WaitTimeout: time.Millisecond}},
			Vault: APIConfig{RateLimit: RateLimitConfig{RequestsPerSecond: 1000, Burst: 1000, WaitTimeout: time.Millisecond}},
		},
		Timeouts: TimeoutConfig{
			ReconciliationRun:   2 * time.Second,
			ShutdownGracePeriod: 500 * time.Millisecond,
		},
		Metrics: MetricsConfig{Enabled: false},
	}

	// Use constructor to get limiters, breakers, worker pool
	m := NewReconcilerManager(cfg, nil, nil, nil, logger, nil)

	// Swap in our mocks
	scan := &rmMockScanner{}
	upd := &rmMockUpdater{}
	sync := &rmMockSynchronizer{}
	m.Scanner = scan
	m.Updater = upd
	m.Synchronizer = sync

	return m, scan, upd, sync
}

// newTestManagerOnly creates a test manager without returning the mock dependencies.
// Use this when the test only needs the manager itself.
func newTestManagerOnly(t *testing.T) *ReconcilerManager {
	t.Helper()
	m, scanner, updater, synchronizer := newTestManager(t)
	// Mocks are configured but not used in these tests
	_ = scanner
	_ = updater
	_ = synchronizer

	return m
}

func TestReconcilerManager_RunReconciliation_UpdatesStatus(t *testing.T) {
	t.Parallel()
	m, scan, _, _ := newTestManager(t)
	scan.deployments = []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
	}

	m.RunReconciliation(context.Background())

	st := m.GetStatus()
	if !st.Running {
		t.Fatalf("expected Running=true after runReconciliation, got %+v", st)
	}
}

func TestReconcilerManager_RunReconciliation_SyncsFilteredDeployments(t *testing.T) {
	t.Parallel()
	m, scan, upd, sync := newTestManager(t)

	// Only names ending with UUID and not platform/system should count
	scan.deployments = []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "blacksmith"}, // filtered out
		{Name: "cf"},         // filtered out (system)
	}

	// single synchronous run
	m.RunReconciliation(context.Background())

	if scanCalls := scan.getScanCalls(); scanCalls != 1 {
		t.Fatalf("expected exactly 1 scan call, got %d", scanCalls)
	}

	// Expect synchronizer called once with the two service deployments
	syncCalls := sync.getCalls()
	if len(syncCalls) != 1 {
		t.Fatalf("expected 1 SyncIndex call, got %d", len(syncCalls))
	}

	got := syncCalls[0]
	if len(got) != 2 {
		t.Fatalf("expected 2 instances to be synced, got %d: %+v", len(got), got)
	}

	// updater should be invoked for each instance
	updCalls := upd.getCalls()
	if len(updCalls) != 2 {
		t.Fatalf("expected 2 updater calls, got %d", len(updCalls))
	}
}

func TestReconcilerManager_RunReconciliation_PropagatesErrorsToStatus(t *testing.T) {
	t.Parallel()
	m, scan, _, sync := newTestManager(t)
	scan.deployments = []DeploymentInfo{{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"}}
	sync.err = errSyncFailure

	m.RunReconciliation(context.Background())

	st := m.GetStatus()
	if len(st.Errors) == 0 {
		t.Fatalf("expected status.Errors to contain reconciliation error, got empty")
	}
}

func TestReconcilerManager_ForceReconcile_BusyGuard(t *testing.T) {
	t.Parallel()
	m := newTestManagerOnly(t)

	// Simulate busy state
	m.IsReconciling.Store(true)

	err := m.ForceReconcile()
	if err == nil {
		t.Fatalf("expected error when force reconciling while busy")
	}

	// Clear and ensure it kicks a run
	m.IsReconciling.Store(false)

	err = m.ForceReconcile()
	if err != nil {
		t.Fatalf("unexpected error from ForceReconcile: %v", err)
	}

	// ensure any background work is signaled to stop quickly
	_ = m.Stop()
}

func TestReconcilerManager_isServiceDeployment_Naming(t *testing.T) {
	t.Parallel()
	m := newTestManagerOnly(t)

	tests := []struct {
		name string
		dep  string
		want bool
	}{
		{"empty", "", false},
		{"system bosh", "bosh", false},
		{"system cf prefix", "cf-router", false},
		{"valid service", "redis-cache-small-12345678-1234-1234-1234-123456789abc", true},
		{"invalid no uuid", "redis-cache-small", false},
		{"random", "some-deployment-name", false},
	}

	for _, tt := range tests {
		if got := m.IsServiceDeployment(tt.dep); got != tt.want {
			t.Errorf("%s: IsServiceDeployment(%q)=%v, want %v", tt.name, tt.dep, got, tt.want)
		}
	}
}

func TestReconcilerManager_filterServiceDeployments(t *testing.T) {
	t.Parallel()
	m := newTestManagerOnly(t)
	deps := []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "blacksmith"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "grafana"},
	}

	got := m.FilterServiceDeployments(deps)
	if len(got) != 2 {
		t.Fatalf("expected 2 filtered deployments, got %d: %+v", len(got), got)
	}
}

func TestReconcilerManager_processBatchWithConcurrency_Basic(t *testing.T) {
	t.Parallel()
	m := newTestManagerOnly(t)
	// 5 service-like deployments
	batch := make([]DeploymentInfo, 0, 5)

	for i := range 5 {
		// generate distinct, hex-suffixed instance IDs
		name := fmt.Sprintf("redis-cache-small-12345678-1234-1234-1234-123456789a%01x", i)
		batch = append(batch, DeploymentInfo{Name: name})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	instances, err := m.ProcessBatchWithConcurrency(ctx, batch, nil)
	if err != nil {
		t.Fatalf("ProcessBatchWithConcurrency error: %v", err)
	}

	if len(instances) != len(batch) {
		t.Fatalf("expected %d instances, got %d", len(batch), len(instances))
	}
}
