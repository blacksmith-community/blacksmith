package vmmonitor_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
)

// errStoreAfterDeadline is the error deadlineVault gives a write whose
// context has ended.
var errStoreAfterDeadline = errors.New("the write's context had ended")

const (
	// deadlineInstance is the instance the deadline specs monitor.
	deadlineInstance = "slow"

	// deadlinePlan is the plan of that instance.
	deadlinePlan = "standalone"

	// deadlineDeployment is the deployment the monitor checks for it.
	deadlineDeployment = deadlinePlan + "-classic-" + deadlineInstance

	// checkDeadline is the deadline the specs give each check.
	checkDeadline = 50 * time.Millisecond

	// checkGuard is how long a spec waits for a check to return.
	checkGuard = 10 * time.Second
)

// hangingDirector is a director whose VM listing blocks until its context
// ends, as a listing does while its BOSH task sits behind a busy director's
// queue. It records whether that context carried a deadline, and its
// deployment lookup refuses an ended context, as the director adapter does.
type hangingDirector struct {
	*testutil.ScriptedBOSHDirector

	mu          sync.Mutex
	deadline    time.Time
	hasDeadline bool
	listed      chan struct{}
}

func newHangingDirector() *hangingDirector {
	director := &hangingDirector{
		ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector(),
		listed:               make(chan struct{}, 1),
	}

	director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: deployment}, nil
	}
	director.FindRunningTaskForDeploymentFn = noRunningTask

	return director
}

func (d *hangingDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]bosh.VM, error) {
	d.mu.Lock()
	d.deadline, d.hasDeadline = ctx.Deadline()
	d.mu.Unlock()

	select {
	case d.listed <- struct{}{}:
	default:
	}

	<-ctx.Done()

	return nil, ctx.Err() //nolint:wrapcheck // the listing ends with its context's own error, as the adapter's does
}

func (d *hangingDirector) GetDeployment(ctx context.Context, deployment string) (*bosh.DeploymentDetail, error) {
	err := ctx.Err()
	if err != nil {
		return nil, err //nolint:wrapcheck // the lookup ends with its context's own error, as the adapter's does
	}

	return d.ScriptedBOSHDirector.GetDeployment(ctx, deployment) //nolint:wrapcheck // the scripted director's own answer
}

func (d *hangingDirector) listingDeadline() (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.deadline, d.hasDeadline
}

// deadlineVault is an indexVault whose status writes fail once their
// context has ended, as a write to the real vault does.
type deadlineVault struct {
	*indexVault
}

func (v deadlineVault) Put(ctx context.Context, path string, data interface{}) error {
	if ctx.Err() != nil {
		return errStoreAfterDeadline
	}

	return v.indexVault.Put(ctx, path, data)
}

// checkWithDeadline monitors one instance through director, runs one check
// cycle with the spec's deadline on each check, and returns the vault once
// the cycle returns. It fails the spec if the cycle runs past the guard.
func checkWithDeadline(t *testing.T, director *hangingDirector) *indexVault {
	t.Helper()

	vault := &indexVault{entries: map[string]interface{}{
		deadlineInstance: map[string]interface{}{serviceKey: serviceID, planKey: deadlinePlan, nameKey: deadlineDeployment},
	}}

	monitor := vmmonitor.NewWithVault(deadlineVault{indexVault: vault}, director, noClaims{})
	monitor.SetCheckTimeout(checkDeadline)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	err := monitor.ScanAllServices(ctx)
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		monitor.CheckScheduledServices(ctx)
	}()

	select {
	case <-done:
	case <-time.After(checkGuard):
		cancel()
		<-done
		t.Fatalf("the check was still running %s after it started, though its deadline was %s", checkGuard, checkDeadline)
	}

	return vault
}

// A check whose VM listing hangs ends at the check's deadline, so one slow
// director task cannot hold the monitor's worker. The listing gets a
// context that carries the deadline.
func TestVMMonitorCheckEndsAtItsDeadline(t *testing.T) {
	t.Parallel()

	director := newHangingDirector()
	started := time.Now()

	checkWithDeadline(t, director)

	finished := time.Now()

	select {
	case <-director.listed:
	default:
		t.Fatal("the check never listed the deployment's VMs")
	}

	deadline, hasDeadline := director.listingDeadline()
	if !hasDeadline {
		t.Fatal("the VM listing's context carried no deadline")
	}

	if deadline.Before(started.Add(checkDeadline)) || deadline.After(finished.Add(checkDeadline)) {
		t.Errorf("the VM listing's deadline was %s, want %s after a moment between %s and %s", deadline, checkDeadline, started, finished)
	}
}

// recordingDirector is a director whose VM listing answers at once with a
// running VM and records the deadline its context carried.
type recordingDirector struct {
	*testutil.ScriptedBOSHDirector

	mu          sync.Mutex
	deadline    time.Time
	hasDeadline bool
}

func (d *recordingDirector) GetDeploymentVMs(ctx context.Context, _ string) ([]bosh.VM, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.deadline, d.hasDeadline = ctx.Deadline()

	return []bosh.VM{{ID: "vm-1", Job: serviceID, JobState: vmStatusRunning}}, nil
}

// New reads vm_monitoring.timeout, in seconds, as the deadline of each
// check's VM listing.
func TestVMMonitorNewReadsTheCheckDeadlineFromTheConfig(t *testing.T) {
	t.Parallel()

	const configured = 45 * time.Second

	cfg := &config.Config{}
	cfg.VMMonitoring.Timeout = int(configured / time.Second)

	director := &recordingDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}

	monitor := vmmonitor.New(nil, director, noClaims{}, cfg)
	monitor.UseVault(&indexVault{entries: map[string]interface{}{
		deadlineInstance: map[string]interface{}{serviceKey: serviceID, planKey: deadlinePlan, nameKey: deadlineDeployment},
	}})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	started := time.Now()

	monitor.CheckScheduledServices(t.Context())

	finished := time.Now()

	director.mu.Lock()
	deadline, hasDeadline := director.deadline, director.hasDeadline
	director.mu.Unlock()

	if !hasDeadline {
		t.Fatal("the VM listing's context carried no deadline")
	}

	if deadline.Before(started.Add(configured)) || deadline.After(finished.Add(configured)) {
		t.Errorf("the VM listing's deadline was %s, want %s after a moment between %s and %s", deadline, configured, started, finished)
	}
}

// A check that hit its deadline is stored as a failed check. The deadline
// bounds only the listing, so the lookup of the deployment and the store of
// the result run on the service's context and still succeed.
func TestVMMonitorStoresTheResultOfACheckThatHitItsDeadline(t *testing.T) {
	t.Parallel()

	director := newHangingDirector()

	vault := checkWithDeadline(t, director)

	if lookups := director.Calls("GetDeployment"); len(lookups) != 1 || lookups[0] != deadlineDeployment {
		t.Errorf("the monitor looked up %v, want only %q", lookups, deadlineDeployment)
	}

	if status := vault.storedStatus(deadlineInstance); status != statusError {
		t.Errorf("the stored status was %q, want %q", status, statusError)
	}
}
