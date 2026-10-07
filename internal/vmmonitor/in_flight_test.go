package vmmonitor_test

import (
	"errors"
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

// claimedInstances is a process where an operation holds the claim on each
// instance in the set.
type claimedInstances map[string]bool

func (c claimedInstances) OperationClaimed(instanceID string) bool { return c[instanceID] }

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

	// claims holds the claims of this process, and none when it is nil.
	claims vmmonitor.OperationClaims

	// withoutClaims builds the monitor with nothing to ask about claims.
	withoutClaims bool

	// runningTask answers the running-task check for the monitored name.
	runningTask func(deployment string) (*bosh.Task, error)

	// afterScan changes the world after the monitor scanned the index and
	// before it checks the instance.
	afterScan func(vault *indexVault)

	deleted bool
}

const (
	inFlightID         = "in-flight"
	inFlightPlan       = "standalone"
	inFlightDeployment = inFlightPlan + "-classic-" + inFlightID
)

// Before the monitor marks an instance deleted, it applies the orphan check's
// rules for work in flight. An instance that this process holds a claim on,
// whose index entry is less than two hours old, whose task record shows a
// provision in progress, or whose deployment has a task running is kept. A
// read or a task check that fails keeps it too.
func TestMonitorSparesInFlightWork(t *testing.T) {
	t.Parallel()

	young := time.Now().Add(-10 * time.Minute)

	cases := []inFlightCase{
		{
			name:  "the monitor keeps a young instance whose deployment answers 70000",
			entry: map[string]interface{}{"requested_at": young.Format(time.RFC3339)},
		},
		{
			name: "the monitor keeps an instance with a running task",
			runningTask: func(deployment string) (*bosh.Task, error) {
				return &bosh.Task{ID: 42, State: "processing", Description: "create deployment", Deployment: deployment}, nil
			},
		},
		{
			name:   "the monitor keeps a claimed instance",
			claims: claimedInstances{inFlightID: true},
		},
		{
			name:        "the monitor keeps an instance when the task check fails",
			runningTask: func(string) (*bosh.Task, error) { return nil, errTasksUnavailable },
		},
		{
			name:    "the monitor tombstones an old, idle instance on 70000",
			deleted: true,
		},
		{
			name: "the monitor keeps an instance whose task record shows a provision in progress",
			records: map[string]map[string]interface{}{inFlightID + "/task": {
				"action": "provision", "state": "processing", "updated_at": young.Unix(),
			}},
		},
		{
			name: "the monitor keeps an instance whose entry turned young after the scan",
			afterScan: func(vault *indexVault) {
				vault.setEntryTime(inFlightID, "requested_at", young.Format(time.RFC3339))
			},
		},
		{
			name:      "the monitor keeps an instance whose index cannot be read",
			afterScan: func(vault *indexVault) { vault.failIndexReads(errVaultSealed) },
		},
		{
			name:      "the monitor keeps an instance whose task record cannot be read",
			afterScan: func(vault *indexVault) { vault.failRecordReads(errVaultSealed) },
		},
		{
			name:          "the monitor keeps an instance when it cannot see the claims",
			withoutClaims: true,
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

	old := time.Now().Add(-3 * time.Hour)

	entry := map[string]interface{}{
		serviceKey:     serviceID,
		planKey:        inFlightPlan,
		nameKey:        inFlightDeployment,
		"requested_at": old.Format(time.RFC3339),
		"created":      float64(old.Unix()),
	}
	for field, value := range inFlight.entry {
		entry[field] = value
	}

	vault := &indexVault{entries: map[string]interface{}{inFlightID: entry}, records: inFlight.records}
	director := goneDirector(inFlight.runningTask)

	monitor := vmmonitor.NewWithVault(vault, director, inFlight.monitorClaims())

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	if inFlight.afterScan != nil {
		inFlight.afterScan(vault)
	}

	monitor.CheckScheduledServices(t.Context())

	for _, asked := range director.Calls("FindRunningTaskForDeployment") {
		if asked != inFlightDeployment {
			t.Errorf("the monitor asked for the tasks running on %q, want only %q", asked, inFlightDeployment)
		}
	}

	marked := vault.markedDeleted()
	_, stillMonitored := monitor.MonitoredDeployment(inFlightID)
	status := vault.storedStatus(inFlightID)

	if inFlight.deleted {
		if len(marked) != 1 || marked[0] != inFlightID || stillMonitored || status != statusDeleted {
			t.Errorf("an old, idle instance: marked %v, still monitored %t, status %q; want %s marked, unmonitored, and deleted",
				marked, stillMonitored, status, inFlightID)
		}

		return
	}

	if len(marked) != 0 || !stillMonitored || status != statusError {
		t.Errorf("an instance in flight: marked %v, still monitored %t, status %q; want nothing marked, still monitored, and error",
			marked, stillMonitored, status)
	}
}

// monitorClaims returns the claims the case builds the monitor with.
func (inFlight inFlightCase) monitorClaims() vmmonitor.OperationClaims {
	switch {
	case inFlight.withoutClaims:
		return nil
	case inFlight.claims != nil:
		return inFlight.claims
	default:
		return noClaims{}
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
