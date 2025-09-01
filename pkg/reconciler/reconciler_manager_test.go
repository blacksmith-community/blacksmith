package reconciler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// Local test doubles matching current interfaces

type rmMockScanner struct {
	deployments []DeploymentInfo
	scanCalls   int
}

func (m *rmMockScanner) ScanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	m.scanCalls++
	return m.deployments, nil
}

func (m *rmMockScanner) GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error) {
	// Minimal detail used by reconciler paths
	return &DeploymentDetail{DeploymentInfo: DeploymentInfo{Name: name}}, nil
}

type rmMockUpdater struct {
	calls []InstanceData
	err   error
}

func (u *rmMockUpdater) UpdateInstance(ctx context.Context, inst InstanceData) (*InstanceData, error) {
	u.calls = append(u.calls, inst)
	if u.err != nil {
		return nil, u.err
	}
	// echo back instance
	return &inst, nil
}

func (u *rmMockUpdater) UpdateBatch(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	// not used by current reconciler manager paths
	u.calls = append(u.calls, instances...)
	return instances, u.err
}

type rmMockSynchronizer struct {
	calls [][]InstanceData
	err   error
}

func (s *rmMockSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	// record a copy for assertions
	cp := make([]InstanceData, len(instances))
	copy(cp, instances)
	s.calls = append(s.calls, cp)
	return s.err
}

// Helper to build a minimally wired manager with our mocks
func newTestManager(t *testing.T) (*ReconcilerManager, *rmMockScanner, *rmMockUpdater, *rmMockSynchronizer) {
	t.Helper()

	logger := newMockLogger()

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
	m.scanner = scan
	m.updater = upd
	m.synchronizer = sync

	return m, scan, upd, sync
}

func TestReconcilerManager_RunReconciliation_UpdatesStatus(t *testing.T) {
	m, scan, _, _ := newTestManager(t)
	scan.deployments = []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
	}
	m.runReconciliation()
	st := m.GetStatus()
	if !st.Running {
		t.Fatalf("expected Running=true after runReconciliation, got %+v", st)
	}
}

func TestReconcilerManager_RunReconciliation_SyncsFilteredDeployments(t *testing.T) {
	m, scan, upd, sync := newTestManager(t)

	// Only names ending with UUID and not platform/system should count
	scan.deployments = []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "blacksmith"}, // filtered out
		{Name: "cf"},         // filtered out (system)
	}

	// single synchronous run
	m.runReconciliation()

	if scan.scanCalls != 1 {
		t.Fatalf("expected exactly 1 scan call, got %d", scan.scanCalls)
	}

	// Expect synchronizer called once with the two service deployments
	if len(sync.calls) != 1 {
		t.Fatalf("expected 1 SyncIndex call, got %d", len(sync.calls))
	}
	got := sync.calls[0]
	if len(got) != 2 {
		t.Fatalf("expected 2 instances to be synced, got %d: %+v", len(got), got)
	}

	// updater should be invoked for each instance
	if len(upd.calls) != 2 {
		t.Fatalf("expected 2 updater calls, got %d", len(upd.calls))
	}
}

func TestReconcilerManager_RunReconciliation_PropagatesErrorsToStatus(t *testing.T) {
	m, scan, _, sync := newTestManager(t)
	scan.deployments = []DeploymentInfo{{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"}}
	sync.err = errors.New("sync failure")

	m.runReconciliation()

	st := m.GetStatus()
	if len(st.Errors) == 0 {
		t.Fatalf("expected status.Errors to contain reconciliation error, got empty")
	}
}

func TestReconcilerManager_ForceReconcile_BusyGuard(t *testing.T) {
	m, _, _, _ := newTestManager(t)

	// Simulate busy state
	m.isReconciling.Store(true)
	if err := m.ForceReconcile(); err == nil {
		t.Fatalf("expected error when force reconciling while busy")
	}

	// Clear and ensure it kicks a run
	m.isReconciling.Store(false)
	if err := m.ForceReconcile(); err != nil {
		t.Fatalf("unexpected error from ForceReconcile: %v", err)
	}

	// ensure any background work is signaled to stop quickly
	_ = m.Stop()
}

func TestReconcilerManager_isServiceDeployment_Naming(t *testing.T) {
	m, _, _, _ := newTestManager(t)

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
		if got := m.isServiceDeployment(tt.dep); got != tt.want {
			t.Errorf("%s: isServiceDeployment(%q)=%v, want %v", tt.name, tt.dep, got, tt.want)
		}
	}
}

func TestReconcilerManager_filterServiceDeployments(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	deps := []DeploymentInfo{
		{Name: "redis-cache-small-12345678-1234-1234-1234-123456789abc"},
		{Name: "blacksmith"},
		{Name: "postgres-basic-87654321-4321-4321-4321-cba987654321"},
		{Name: "grafana"},
	}

	got := m.filterServiceDeployments(deps)
	if len(got) != 2 {
		t.Fatalf("expected 2 filtered deployments, got %d: %+v", len(got), got)
	}
}

func TestReconcilerManager_processBatchWithConcurrency_Basic(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	// 5 service-like deployments
	var batch []DeploymentInfo
	for i := 0; i < 5; i++ {
		// generate distinct, hex-suffixed instance IDs
		name := fmt.Sprintf("redis-cache-small-12345678-1234-1234-1234-123456789a%01x", i)
		batch = append(batch, DeploymentInfo{Name: name})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	instances, err := m.processBatchWithConcurrency(ctx, batch, nil)
	if err != nil {
		t.Fatalf("processBatchWithConcurrency error: %v", err)
	}
	if len(instances) != len(batch) {
		t.Fatalf("expected %d instances, got %d", len(batch), len(instances))
	}
}
