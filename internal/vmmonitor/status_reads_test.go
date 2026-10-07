package vmmonitor_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
)

// errDirectorUnavailable is a director that cannot answer a request for a
// deployment.
var errDirectorUnavailable = errors.New("director responded with non-successful status code '503'")

// readDuringCheckVault holds each VM status the monitor stores until a
// status read that started during the same check has ended. The read starts
// before the check updates the instance's state, and the store comes after
// that update, so the read and the update overlap whatever order they run
// in.
type readDuringCheckVault struct {
	*indexVault

	readDone chan struct{}
}

func (v *readDuringCheckVault) Put(ctx context.Context, path string, data interface{}) error {
	<-v.readDone

	return v.indexVault.Put(ctx, path, data)
}

// statusReadCase is a check whose outcome the monitor records in the
// instance's state while GetStatus reads that state.
type statusReadCase struct {
	name string

	// listing answers the VM listing of the monitored deployment.
	listing func(deployment string) ([]bosh.VM, error)

	// confirm answers the director's request for the deployment.
	confirm func(deployment string) (*bosh.DeploymentDetail, error)

	// claimed has an operation in this process hold the instance's claim.
	claimed bool
}

// GetStatus serves the status endpoint from request goroutines while a
// check cycle runs, so every check must update the state GetStatus reads
// under the monitor's lock. A check that lists the VMs, a check whose
// listing fails, and a check that keeps an instance in flight each read the
// state at the same time as the update, and the race detector reports any
// update made without the lock.
func TestGetStatusReadsWhileACheckUpdatesState(t *testing.T) {
	t.Parallel()

	cases := []statusReadCase{
		{
			name: "a check that lists the VMs",
			listing: func(deployment string) ([]bosh.VM, error) {
				return []bosh.VM{{ID: deployment + "-vm", JobState: "failing"}}, nil
			},
			confirm: deploymentNotFound,
		},
		{
			name:    "a check whose listing fails",
			listing: func(string) ([]bosh.VM, error) { return nil, errProxyNotFound },
			confirm: func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnavailable },
		},
		{
			name:    "a check that keeps an instance in flight",
			listing: func(string) ([]bosh.VM, error) { return nil, errProxyNotFound },
			confirm: deploymentNotFound,
			claimed: true,
		},
	}

	for _, read := range cases {
		t.Run(read.name, func(t *testing.T) {
			t.Parallel()

			readStatusDuringCheck(t, read)
		})
	}
}

// readStatusDuringCheck runs one check of an instance and has a goroutine
// that the check's VM listing starts read the monitor's status while the
// check goes on.
func readStatusDuringCheck(t *testing.T, read statusReadCase) {
	t.Helper()

	vault := &readDuringCheckVault{
		indexVault: &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}},
		readDone:   make(chan struct{}),
	}

	claims := &heldClaims{}
	if read.claimed {
		claims.take(inFlightID)
	}

	var (
		monitor   *vmmonitor.Monitor
		startRead sync.Once
	)

	director := goneDirector(nil)
	director.GetDeploymentFn = read.confirm
	director.GetDeploymentVMsFn = func(deployment string) ([]bosh.VM, error) {
		startRead.Do(func() {
			go func() {
				defer close(vault.readDone)

				_ = monitor.GetStatus()
			}()
		})

		return read.listing(deployment)
	}

	monitor = vmmonitor.NewWithVault(vault, director, claims)

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	if status := vault.storedStatus(inFlightID); status == "" {
		t.Errorf("the check stored no VM status for %s", inFlightID)
	}
}

// A failed check that follows a check that held an instance in flight, or a
// fresh provision whose first check fails, counts the failure and leaves
// provisioning mode. GetStatus then never shows an instance as provisioning
// and failing at the same time.
func TestAFailedCheckEndsProvisioning(t *testing.T) {
	t.Parallel()

	t.Run("after a check held the instance in flight", func(t *testing.T) {
		t.Parallel()

		var (
			confirmMu   sync.Mutex
			unavailable bool
		)

		director := goneDirector(nil)
		director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
			confirmMu.Lock()
			defer confirmMu.Unlock()

			if unavailable {
				return nil, errDirectorUnavailable
			}

			return deploymentNotFound(deployment)
		}

		vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
		claims := &heldClaims{}
		claims.take(inFlightID)

		monitor := vmmonitor.NewWithVault(vault, director, claims)

		err := monitor.ScanAllServices(t.Context())
		if err != nil {
			t.Fatalf("the scan failed: %v", err)
		}

		monitor.CheckScheduledServices(t.Context())
		expectHeldAsProvisioning(t, monitor, inFlightID)

		confirmMu.Lock()
		unavailable = true
		confirmMu.Unlock()

		monitor.TriggerRefreshAll(t.Context())

		expectFailingAndNotProvisioning(t, monitor, vault)
	})

	t.Run("after a fresh provision was added", func(t *testing.T) {
		t.Parallel()

		director := goneDirector(nil)
		director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnavailable }

		vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
		monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

		err := monitor.AddService(t.Context(), inFlightID, inFlightDeployment)
		if err != nil {
			t.Fatalf("adding the instance failed: %v", err)
		}

		monitor.CheckScheduledServices(t.Context())

		expectFailingAndNotProvisioning(t, monitor, vault)
	})
}

// expectFailingAndNotProvisioning checks that the last check of the
// instance stored "error", counted one failure, and left the instance
// unhealthy and out of provisioning mode.
func expectFailingAndNotProvisioning(t *testing.T, monitor *vmmonitor.Monitor, vault *indexVault) {
	t.Helper()

	if status := vault.storedStatus(inFlightID); status != statusError {
		t.Errorf("the failed check stored status %q, want error", status)
	}

	expectHeldAsFailing(t, monitor, inFlightID, 1)

	watched, _ := watchedState(t, monitor, inFlightID)
	if watched.IsProvisioning {
		t.Errorf("a failing instance is still provisioning; want it out of provisioning mode")
	}
}
