package vmmonitor_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
)

var (
	// errTasksUnavailable is a director that cannot list its tasks.
	errTasksUnavailable = errors.New("director responded with non-successful status code '503' listing tasks")

	// errVaultSealed is a vault that cannot be read.
	errVaultSealed = errors.New("vault is sealed")
)

// noClaims is a process where no operation holds a claim.
type noClaims struct{}

func (noClaims) OperationClaimed(string) bool { return false }

// heldClaims is a process where an operation holds the claim on each
// instance in the set. A test can take a claim while the monitor runs.
type heldClaims struct {
	mu   sync.Mutex
	held map[string]bool
}

func (c *heldClaims) OperationClaimed(instanceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.held[instanceID]
}

// take has an operation in this process claim an instance.
func (c *heldClaims) take(instanceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.held == nil {
		c.held = map[string]bool{}
	}

	c.held[instanceID] = true
}

// noRunningTask is a director with no task running on any deployment.
func noRunningTask(string) (*bosh.Task, error) {
	return nil, nil //nolint:nilnil // no running task is a nil task without an error
}

// inFlightCase is an instance whose deployment the director says is gone,
// what else is true of it when the monitor checks it, and whether the monitor
// should then mark it deleted.
type inFlightCase struct {
	name string

	// entry holds the index entry's times, on top of its service, plan, and
	// deployment name.
	entry map[string]interface{}

	// records holds the instance's records, by path.
	records map[string]map[string]interface{}

	// claimed lists the instances an operation in this process holds the
	// claim on when the check starts.
	claimed []string

	// withoutClaims builds the monitor with nothing to ask about claims.
	withoutClaims bool

	// added has the monitor learn of the instance through AddService, as
	// the broker's provision hook does, on top of the scan, which skips an
	// entry marked deleted.
	added bool

	// runningTask answers the running-task check for the monitored name.
	runningTask func(deployment string) (*bosh.Task, error)

	// afterScan changes the world after the monitor scanned the index and
	// before it checks the instance.
	afterScan func(vault *indexVault)

	// inWindow changes the world after the monitor found nothing in flight
	// and before its tombstone write reads the index under the lock.
	inWindow func(vault *indexVault, claims *heldClaims)

	want inFlightOutcome
}

// inFlightOutcome is what the monitor does about an instance whose
// deployment the director says is gone.
type inFlightOutcome int

const (
	// unset is a case that names no outcome, which fails the case.
	unset inFlightOutcome = iota

	// provisioning leaves the index entry alone, keeps monitoring the
	// instance, and records the VM status "provisioning", because something
	// shows that the instance belongs to work in flight. The monitor then
	// holds the instance as it holds a fresh provision, healthy and
	// provisioning with no failure counted.
	provisioning

	// failedCheck leaves the index entry alone, keeps monitoring the
	// instance, and records the VM status "error", because a read, a check,
	// or the tombstone write failed, so the monitor cannot tell whether the
	// instance is in flight. The monitor counts the failure and holds the
	// instance as unhealthy.
	failedCheck

	// tombstoned marks the index entry deleted and stops monitoring.
	tombstoned

	// unindexed finds no index entry to mark and stops monitoring.
	unindexed

	// alreadyMarked finds the index entry already marked deleted, writes
	// nothing to the index, records the VM status "deleted", and stops
	// monitoring.
	alreadyMarked
)

const (
	inFlightID         = "in-flight"
	inFlightPlan       = "standalone"
	inFlightDeployment = inFlightPlan + "-classic-" + inFlightID
)

// Before the monitor marks an instance deleted, it applies the orphan check's
// rules for work in flight. An instance that this process holds a claim on,
// whose index entry is less than two hours old, whose task record shows a
// provision in progress, or whose deployment has a task running is kept. A
// read or a task check that fails keeps it too. The tombstone write reads the
// entry again under the index lock, and keeps the instance when the entry
// changed or a claim was taken after those checks. An instance kept because
// it is in flight shows the VM status "provisioning". One kept because a
// read, a check, or the write failed shows "error", because the monitor
// cannot tell whether it is in flight.
func TestMonitorSparesInFlightWork(t *testing.T) {
	t.Parallel()

	young := time.Now().Add(-10 * time.Minute)

	cases := []inFlightCase{
		{
			name:  "the monitor keeps a young instance whose deployment answers 70000",
			entry: map[string]interface{}{"requested_at": young.Format(time.RFC3339)},
			want:  provisioning,
		},
		{
			name: "the monitor keeps an instance with a running task",
			runningTask: func(deployment string) (*bosh.Task, error) {
				return &bosh.Task{ID: 42, State: "processing", Description: "create deployment", Deployment: deployment}, nil
			},
			want: provisioning,
		},
		{
			name:    "the monitor keeps a claimed instance",
			claimed: []string{inFlightID},
			want:    provisioning,
		},
		{
			name:        "the monitor keeps an instance when the task check fails",
			runningTask: func(string) (*bosh.Task, error) { return nil, errTasksUnavailable },
			want:        failedCheck,
		},
		{
			name: "the monitor tombstones an old, idle instance on 70000",
			want: tombstoned,
		},
		{
			name: "the monitor keeps an instance whose task record shows a provision in progress",
			records: map[string]map[string]interface{}{inFlightID + "/task": {
				"action": "provision", "state": "processing", "updated_at": young.Unix(),
			}},
			want: provisioning,
		},
		{
			name: "the monitor keeps an instance whose entry turned young after the scan",
			afterScan: func(vault *indexVault) {
				vault.setEntryTime(inFlightID, "requested_at", young.Format(time.RFC3339))
			},
			want: provisioning,
		},
		{
			name:      "the monitor keeps an instance whose index cannot be read",
			afterScan: func(vault *indexVault) { vault.failIndexReads(errVaultSealed) },
			want:      failedCheck,
		},
		{
			name:      "the monitor keeps an instance whose task record cannot be read",
			afterScan: func(vault *indexVault) { vault.failRecordReads(errVaultSealed) },
			want:      failedCheck,
		},
		{
			name:          "the monitor keeps an instance when it cannot see the claims",
			withoutClaims: true,
			want:          failedCheck,
		},
		{
			name:     "the monitor keeps an instance claimed after its in-flight check and before its tombstone write",
			inWindow: func(_ *indexVault, claims *heldClaims) { claims.take(inFlightID) },
			want:     provisioning,
		},
		{
			name: "the monitor keeps an instance whose entry changed after its in-flight check and before its tombstone write",
			inWindow: func(vault *indexVault, _ *heldClaims) {
				vault.setEntryTime(inFlightID, "requested_at", young.Format(time.RFC3339))
			},
			want: provisioning,
		},
		{
			name: "the monitor keeps an instance whose tombstone write fails",
			inWindow: func(vault *indexVault, _ *heldClaims) {
				vault.failIndexWrites(errVaultSealed)
			},
			want: failedCheck,
		},
		{
			name:     "the monitor stops watching an instance whose entry was removed before its tombstone write",
			inWindow: func(vault *indexVault, _ *heldClaims) { vault.removeEntry(inFlightID) },
			want:     unindexed,
		},
		{
			name: "the monitor stops watching an instance whose entry was just marked deleted",
			entry: map[string]interface{}{
				"status": statusDeleted, "deleted_at": young.Format(time.RFC3339), "deleted_by": deletedByMonitor,
			},
			added: true,
			want:  alreadyMarked,
		},
		{
			name: "the monitor stops watching an instance whose entry was marked deleted long ago",
			entry: map[string]interface{}{
				"status": statusDeleted, "deleted_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339), "deleted_by": deletedByMonitor,
			},
			added: true,
			want:  alreadyMarked,
		},
		{
			name: "the monitor stops watching an instance already marked deleted while index writes fail",
			entry: map[string]interface{}{
				"status": statusDeleted, "deleted_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339), "deleted_by": deletedByMonitor,
			},
			added:     true,
			afterScan: func(vault *indexVault) { vault.failIndexWrites(errVaultSealed) },
			want:      alreadyMarked,
		},
		{
			name:      "the monitor stops watching an instance whose entry was marked deleted after the scan",
			afterScan: func(vault *indexVault) { vault.markEntryDeleted(inFlightID, time.Now()) },
			want:      alreadyMarked,
		},
		{
			name: "the monitor stops watching an instance whose entry was marked deleted before its tombstone write",
			inWindow: func(vault *indexVault, _ *heldClaims) {
				vault.markEntryDeleted(inFlightID, time.Now())
			},
			want: alreadyMarked,
		},
	}

	for _, inFlight := range cases {
		t.Run(inFlight.name, func(t *testing.T) {
			t.Parallel()

			checkInFlight(t, inFlight)
		})
	}
}

// checkInFlight monitors an instance whose recorded deployment name differs
// from the one its plan ID builds, runs one check in which the director says
// that deployment is gone, and checks what the monitor did about the
// instance.
func checkInFlight(t *testing.T, inFlight inFlightCase) {
	t.Helper()

	entry := oldEntry()
	for field, value := range inFlight.entry {
		entry[field] = value
	}

	vault := &indexVault{entries: map[string]interface{}{inFlightID: entry}, records: inFlight.records}
	director := goneDirector(inFlight.runningTask)

	claims := &heldClaims{}
	for _, instanceID := range inFlight.claimed {
		claims.take(instanceID)
	}

	if inFlight.inWindow != nil {
		vault.beforeIndexWrite(func() { inFlight.inWindow(vault, claims) })
	}

	var monitorClaims vmmonitor.OperationClaims = claims
	if inFlight.withoutClaims {
		monitorClaims = nil
	}

	monitor := vmmonitor.NewWithVault(vault, director, monitorClaims)

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	if inFlight.added {
		err = monitor.AddService(t.Context(), inFlightID, inFlightDeployment)
		if err != nil {
			t.Fatalf("adding the instance failed: %v", err)
		}
	}

	if inFlight.afterScan != nil {
		inFlight.afterScan(vault)
	}

	savesBefore := vault.savedWrites()

	monitor.CheckScheduledServices(t.Context())

	for _, asked := range director.Calls("FindRunningTaskForDeployment") {
		if asked != inFlightDeployment {
			t.Errorf("the monitor asked for the tasks running on %q, want only %q", asked, inFlightDeployment)
		}
	}

	marked := vault.markedDeleted()
	_, stillMonitored := monitor.MonitoredDeployment(inFlightID)
	status := vault.storedStatus(inFlightID)

	switch inFlight.want {
	case tombstoned:
		if len(marked) != 1 || marked[0] != inFlightID || stillMonitored || status != statusDeleted {
			t.Errorf("an old, idle instance: marked %v, still monitored %t, status %q; want %s marked, unmonitored, and deleted",
				marked, stillMonitored, status, inFlightID)
		}
	case alreadyMarked:
		saved := vault.savedWrites() - savesBefore
		if saved != 0 || stillMonitored || status != statusDeleted {
			t.Errorf("an instance already marked deleted: %d index writes, still monitored %t, status %q; want no write, unmonitored, and deleted",
				saved, stillMonitored, status)
		}
	case unindexed:
		if len(marked) != 0 || stillMonitored || status != statusDeleted {
			t.Errorf("an instance no longer indexed: marked %v, still monitored %t, status %q; want nothing marked, unmonitored, and deleted",
				marked, stillMonitored, status)
		}
	case provisioning:
		if len(marked) != 0 || !stillMonitored || status != statusProvisioning {
			t.Errorf("an instance in flight: marked %v, still monitored %t, status %q; want nothing marked, still monitored, and provisioning",
				marked, stillMonitored, status)
		}

		expectHeldAsProvisioning(t, monitor, inFlightID)
	case failedCheck:
		if len(marked) != 0 || !stillMonitored || status != statusError {
			t.Errorf("an instance the monitor cannot judge: marked %v, still monitored %t, status %q; want nothing marked, still monitored, and error",
				marked, stillMonitored, status)
		}

		expectHeldAsFailing(t, monitor, inFlightID, 1)
	case unset:
		t.Fatalf("the case names no outcome")
	}
}

// watchedState returns the monitor's state for an instance and the number of
// instances its status summary counts as failing.
func watchedState(t *testing.T, monitor *vmmonitor.Monitor, instanceID string) (vmmonitor.ServiceStatus, int) {
	t.Helper()

	status := monitor.GetStatus()
	failing, _ := status.Summary["failing"].(int)

	for _, watched := range status.Services {
		if watched.ServiceID == instanceID {
			return watched, failing
		}
	}

	t.Fatalf("the monitor does not watch instance %s", instanceID)

	return vmmonitor.ServiceStatus{}, failing
}

// expectHeldAsProvisioning checks that the monitor holds an instance as it
// holds a fresh provision, healthy and provisioning with no failure counted,
// and that its summary counts nothing as failing.
func expectHeldAsProvisioning(t *testing.T, monitor *vmmonitor.Monitor, instanceID string) {
	t.Helper()

	watched, failing := watchedState(t, monitor, instanceID)
	if watched.FailureCount != 0 || !watched.IsHealthy || !watched.IsProvisioning || failing != 0 {
		t.Errorf("an instance in flight is held with %d failures, healthy %t, provisioning %t, and %d failing in the summary; want 0, healthy, provisioning, and 0 failing",
			watched.FailureCount, watched.IsHealthy, watched.IsProvisioning, failing)
	}
}

// expectHeldAsFailing checks that the monitor counted failures for an
// instance, holds it as unhealthy, and counts it as failing in its summary.
func expectHeldAsFailing(t *testing.T, monitor *vmmonitor.Monitor, instanceID string, failures int) {
	t.Helper()

	watched, failing := watchedState(t, monitor, instanceID)
	if watched.FailureCount != failures || watched.IsHealthy || failing != 1 {
		t.Errorf("an instance the monitor cannot judge is held with %d failures, healthy %t, and %d failing in the summary; want %d, unhealthy, and 1 failing",
			watched.FailureCount, watched.IsHealthy, failing, failures)
	}
}

// An instance whose earlier check failed and that a later check finds in
// flight is held as a fresh provision is, so the earlier failure no longer
// counts against it.
func TestMonitorHoldsAnInFlightInstanceAsAProvision(t *testing.T) {
	t.Parallel()

	tasksFail := true

	var tasksMu sync.Mutex

	director := goneDirector(func(string) (*bosh.Task, error) {
		tasksMu.Lock()
		defer tasksMu.Unlock()

		if tasksFail {
			return nil, errTasksUnavailable
		}

		return noRunningTask("")
	})

	vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
	claims := &heldClaims{}
	monitor := vmmonitor.NewWithVault(vault, director, claims)

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	if status := vault.storedStatus(inFlightID); status != statusError {
		t.Fatalf("the first check stored status %q, want error", status)
	}

	expectHeldAsFailing(t, monitor, inFlightID, 1)

	tasksMu.Lock()
	tasksFail = false
	tasksMu.Unlock()

	claims.take(inFlightID)
	monitor.TriggerRefreshAll(t.Context())

	if status := vault.storedStatus(inFlightID); status != statusProvisioning {
		t.Fatalf("the second check stored status %q, want provisioning", status)
	}

	expectHeldAsProvisioning(t, monitor, inFlightID)
}

// oldEntry is the index entry of an instance whose deployment name differs
// from the one its plan ID builds, requested and created three hours ago.
func oldEntry() map[string]interface{} {
	old := time.Now().Add(-3 * time.Hour)

	return map[string]interface{}{
		serviceKey:     serviceID,
		planKey:        inFlightPlan,
		nameKey:        inFlightDeployment,
		"requested_at": old.Format(time.RFC3339),
		"created":      float64(old.Unix()),
	}
}

// goneDirector lists no VMs and answers deployment-not-found for every name.
// It answers the running-task check with runningTask, or with no task when
// that is nil, for the monitored name only, and with no task for any other
// name, so a running-task check that asks for another name is caught.
func goneDirector(runningTask func(deployment string) (*bosh.Task, error)) *testutil.ScriptedBOSHDirector {
	if runningTask == nil {
		runningTask = noRunningTask
	}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return nil, errProxyNotFound }
	director.GetDeploymentFn = deploymentNotFound
	director.FindRunningTaskForDeploymentFn = func(deployment string) (*bosh.Task, error) {
		if deployment != inFlightDeployment {
			return noRunningTask(deployment)
		}

		return runningTask(deployment)
	}

	return director
}
