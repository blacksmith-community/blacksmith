//nolint:testpackage // the tests drive the adapter's unexported manager factory
package recovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
)

// recordingManager counts the calls the adapter makes to it.
type recordingManager struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (m *recordingManager) Start(context.Context) error {
	m.starts.Add(1)

	return nil
}

func (m *recordingManager) Stop() error {
	m.stops.Add(1)

	return nil
}

func (m *recordingManager) ForceReconcile() error { return nil }
func (m *recordingManager) GetStatus() reconciler.Status {
	return reconciler.Status{Running: m.starts.Load() > m.stops.Load()}
}
func (m *recordingManager) UpdateInterval(time.Duration) {}
func (m *recordingManager) GetInterval() time.Duration   { return time.Minute }

func newLifecycleAdapter(factory func() reconciler.Manager) *ReconcilerAdapter {
	adapter := &ReconcilerAdapter{
		logger:     logger.Get().Named("reconciler-test"),
		newManager: factory,
	}
	adapter.config.Enabled = true

	return adapter
}

// A Stop that lands while Start is still building its manager must leave Start
// with nothing to run, since nothing would stop a manager started afterwards.
func TestReconcilerAdapterStopBeforeStartFinishesStartsNothing(t *testing.T) {
	t.Parallel()

	manager := &recordingManager{}
	building := make(chan struct{})
	release := make(chan struct{})

	adapter := newLifecycleAdapter(func() reconciler.Manager {
		close(building)
		<-release

		return manager
	})

	startErr := make(chan error, 1)

	go func() { startErr <- adapter.Start(t.Context()) }()

	<-building

	err := adapter.Stop()
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}

	close(release)

	err = <-startErr
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if got := manager.starts.Load(); got != 0 {
		t.Fatalf("expected Start to skip a stopped adapter, but the manager was started %d times", got)
	}
}

// A Start whose context ends under it must not build or start a manager.
func TestReconcilerAdapterStartWithCancelledContextStartsNothing(t *testing.T) {
	t.Parallel()

	manager := &recordingManager{}
	adapter := newLifecycleAdapter(func() reconciler.Manager { return manager })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := adapter.Start(ctx)
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if got := manager.starts.Load(); got != 0 {
		t.Fatalf("expected no manager start under a cancelled context, got %d", got)
	}
}

// A context that ends while Start waits for Vault ends the wait at once.
func TestReconcilerAdapterStartReturnsWhenContextEndsDuringVaultWait(t *testing.T) {
	t.Parallel()

	probed := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case probed <- struct{}{}:
		default:
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"initialized":true,"sealed":true}`))
	}))
	defer server.Close()

	manager := &recordingManager{}
	adapter := newLifecycleAdapter(func() reconciler.Manager { return manager })
	adapter.vault = internalVault.New(server.URL, "test-token", true)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startErr := make(chan error, 1)

	go func() { startErr <- adapter.Start(ctx) }()

	<-probed
	cancel()

	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("start failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start kept waiting for Vault after its context ended")
	}

	if got := manager.starts.Load(); got != 0 {
		t.Fatalf("expected no manager start after cancellation, got %d", got)
	}
}

// Start, Stop, and the readers can run at once without a data race.
func TestReconcilerAdapterConcurrentLifecycleIsRaceFree(t *testing.T) {
	t.Parallel()

	for range 50 {
		adapter := newLifecycleAdapter(func() reconciler.Manager { return &recordingManager{} })

		var group sync.WaitGroup

		group.Add(4)

		go func() { defer group.Done(); _ = adapter.Start(t.Context()) }()
		go func() { defer group.Done(); _ = adapter.Stop() }()
		go func() { defer group.Done(); _ = adapter.GetStatus() }()
		go func() {
			defer group.Done()

			_ = adapter.ForceReconcile()
			_ = adapter.UpdateInterval(t.Context(), time.Minute)
		}()

		group.Wait()
	}
}

// A zero-value adapter, as the server wires before the real one exists, stays
// safe to call.
func TestReconcilerAdapterZeroValueIsSafe(t *testing.T) {
	t.Parallel()

	adapter := &ReconcilerAdapter{}

	err := adapter.Start(t.Context())
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}

	if adapter.GetStatus().Running {
		t.Fatal("expected a zero-value adapter not to be running")
	}

	err = adapter.ForceReconcile()
	if err == nil {
		t.Fatal("expected ForceReconcile to report an uninitialized reconciler")
	}

	err = adapter.Stop()
	if err != nil {
		t.Fatalf("stop failed: %v", err)
	}
}
