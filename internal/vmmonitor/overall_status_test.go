package vmmonitor_test

import (
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
)

// The director writes the job state "unresponsive agent" for a VM whose
// agent does not answer. That state outranks a running VM, so it becomes
// the deployment's overall status, and the VM does not count as healthy.
func TestVMMonitorReportsAnUnresponsiveAgentAsTheOverallStatus(t *testing.T) {
	t.Parallel()

	const unresponsiveAgent = "unresponsive agent"

	monitor := vmmonitor.NewWithVault(&indexVault{}, testutil.NewScriptedBOSHDirector(), noClaims{})

	status, healthy := monitor.OverallStatus([]bosh.VM{
		{ID: "vm-1", Job: serviceID, JobState: unresponsiveAgent},
		{ID: "vm-2", Job: serviceID, JobState: vmStatusRunning},
	})

	if status != unresponsiveAgent {
		t.Errorf("the overall status was %q, want %q", status, unresponsiveAgent)
	}

	if healthy != 1 {
		t.Errorf("the healthy count was %d, want 1", healthy)
	}
}

// A provisioning instance whose deployment reports an unresponsive agent
// has finished provisioning and failed, as one that reports failing has.
// The check ends its provisioning, counts the failure, and marks it
// unhealthy, so an agent that never answers cannot hold the instance in
// provisioning.
func TestVMMonitorEndsProvisioningOnAnUnresponsiveAgent(t *testing.T) {
	t.Parallel()

	for _, jobState := range []string{"unresponsive agent", "unresponsive"} {
		t.Run(jobState, func(t *testing.T) {
			t.Parallel()

			director := testutil.NewScriptedBOSHDirector()
			director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
				return []bosh.VM{
					{ID: "silent-vm", Job: serviceID, JobState: jobState},
					{ID: "running-vm", Job: serviceID, JobState: vmStatusRunning},
				}, nil
			}

			vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
			monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

			err := monitor.AddService(t.Context(), inFlightID, inFlightDeployment)
			if err != nil {
				t.Fatalf("adding the instance failed: %v", err)
			}

			monitor.CheckScheduledServices(t.Context())

			if status := vault.storedStatus(inFlightID); status != jobState {
				t.Errorf("the check stored status %q, want %q", status, jobState)
			}

			watched, _ := watchedState(t, monitor, inFlightID)
			if watched.IsProvisioning {
				t.Error("the instance is still provisioning, though its agent does not answer")
			}

			expectHeldAsFailing(t, monitor, inFlightID, 1)
		})
	}
}
