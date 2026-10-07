package vmmonitor_test

import (
	"errors"
	"fmt"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
)

var (
	// errProxyNotFound is the error a plain 404 from something in front of
	// the director, such as a proxy with no route, gives a VM listing.
	errProxyNotFound = errors.New("director responded with non-successful status code '404' response '404 page not found'")

	// errDirectorDown is a director that cannot be reached.
	errDirectorDown = errors.New("dial tcp 127.0.0.1:25555: connect: connection refused")

	// errDirectorFailed is a VM listing the director failed for a reason
	// that says nothing about whether the deployment exists.
	errDirectorFailed = errors.New("director responded with non-successful status code '500'")
)

// The service and the index entry fields the specs set, and the VM statuses
// they expect.
const (
	serviceID          = "valkey"
	serviceKey         = "service_id"
	planKey            = "plan_id"
	nameKey            = "deployment_name"
	statusDeleted      = "deleted"
	statusError        = "error"
	statusProvisioning = "provisioning"
	vmStatusRunning    = "running"

	// deletedByMonitor is the deleted_by of an entry the monitor marked.
	deletedByMonitor = "vm-monitor"
)

// checkErrorCase is a failed VM listing, the director's answer when the
// monitor asks for the deployment itself, and whether the instance should
// then be marked deleted.
type checkErrorCase struct {
	name    string
	vmsErr  func(deployment string) error
	confirm func(deployment string) (*bosh.DeploymentDetail, error)
	deleted bool
}

// A failed VM listing never proves a deployment gone. A 404 can come from a
// proxy in front of the director, and the director adapter wraps any 404 in
// the deployment-not-found error. Before the monitor marks an instance
// deleted, it asks the director for the deployment it monitors, and only the
// director's deployment-not-found code, 70000, counts. Any other answer keeps
// the instance, and the check is recorded as failed.
func TestMonitorMarksAnInstanceDeletedOnlyWhenTheDirectorConfirmsIt(t *testing.T) {
	t.Parallel()

	live := func(deployment string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: deployment}, nil
	}

	cases := []checkErrorCase{
		{
			name:    "a 404 from a proxy while the deployment is live",
			vmsErr:  func(string) error { return errProxyNotFound },
			confirm: live,
		},
		{
			name: "a 404 the adapter wrapped as not found while the deployment is live",
			vmsErr: func(deployment string) error {
				return fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, deployment)
			},
			confirm: live,
		},
		{
			name:    "a 404 the director cannot confirm",
			vmsErr:  func(string) error { return errProxyNotFound },
			confirm: func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorDown },
		},
		{
			name:    "a 404 the director confirms with its deployment-not-found code",
			vmsErr:  func(string) error { return errProxyNotFound },
			confirm: deploymentNotFound,
			deleted: true,
		},
		{
			name:    "a failed listing of a deployment the director confirms gone",
			vmsErr:  func(string) error { return errDirectorFailed },
			confirm: deploymentNotFound,
			deleted: true,
		},
	}

	for _, checkError := range cases {
		t.Run(checkError.name, func(t *testing.T) {
			t.Parallel()

			checkAfterError(t, checkError)
		})
	}
}

// checkAfterError monitors an adopted instance whose recorded deployment
// name differs from the one its plan ID builds, runs one check whose VM
// listing fails, and checks what the monitor did about the instance.
func checkAfterError(t *testing.T, checkError checkErrorCase) {
	t.Helper()

	const (
		instanceID = "adopted"
		planID     = "standalone"
		monitored  = planID + "-classic-" + instanceID
	)

	vault := &indexVault{entries: map[string]interface{}{
		instanceID: map[string]interface{}{serviceKey: serviceID, planKey: planID, nameKey: monitored},
	}}

	// The director answers the case's way for the deployment the monitor
	// watches, and deployment-not-found for any other name, so a
	// confirmation that asks for another name is caught.
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(deployment string) ([]bosh.VM, error) {
		return nil, checkError.vmsErr(deployment)
	}
	director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
		if deployment == monitored {
			return checkError.confirm(deployment)
		}

		return deploymentNotFound(deployment)
	}
	director.FindRunningTaskForDeploymentFn = noRunningTask

	monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	confirmations := director.Calls("GetDeployment")
	if len(confirmations) != 1 || confirmations[0] != monitored {
		t.Errorf("the monitor asked the director for %v, want only %q", confirmations, monitored)
	}

	marked := vault.markedDeleted()
	_, stillMonitored := monitor.MonitoredDeployment(instanceID)
	status := vault.storedStatus(instanceID)

	if checkError.deleted {
		if len(marked) != 1 || marked[0] != instanceID || stillMonitored || status != statusDeleted {
			t.Errorf("a deployment the director confirmed gone: marked %v, still monitored %t, status %q; want %s marked, unmonitored, and deleted",
				marked, stillMonitored, status, instanceID)
		}

		return
	}

	if len(marked) != 0 || !stillMonitored || status != statusError {
		t.Errorf("a deployment the director did not confirm gone: marked %v, still monitored %t, status %q; want nothing marked, still monitored, and error",
			marked, stillMonitored, status)
	}
}

// deploymentNotFound answers a GetDeployment the way the director adapter
// does when the director answers with its deployment-not-found code.
func deploymentNotFound(deployment string) (*bosh.DeploymentDetail, error) {
	return nil, fmt.Errorf("deployment %s: %w", deployment, bosh.ErrDeploymentNotFound)
}
