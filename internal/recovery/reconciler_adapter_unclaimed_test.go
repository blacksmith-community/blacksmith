//nolint:testpackage // the tests set the adapter's unexported manager
package recovery

import (
	"testing"
	"time"

	"blacksmith/pkg/reconciler"
)

// reportingManager is a manager that can list unclaimed deployments.
type reportingManager struct {
	recordingManager

	report reconciler.UnclaimedReport
}

func (m *reportingManager) UnclaimedDeployments() reconciler.UnclaimedReport { return m.report }

// The adapter hands the running manager's unclaimed deployments to the API.
func TestReconcilerAdapterReportsUnclaimedDeployments(t *testing.T) {
	t.Parallel()

	want := reconciler.UnclaimedReport{
		Known: true,
		AsOf:  time.Now(),
		Deployments: []reconciler.UnclaimedDeployment{
			{InstanceID: "stray", DeploymentName: "valkey-standalone-stray"},
		},
	}

	adapter := newLifecycleAdapter(nil)
	adapter.manager = &reportingManager{report: want}

	got := adapter.UnclaimedDeployments()
	if !got.Known || len(got.Deployments) != 1 || got.Deployments[0].InstanceID != "stray" {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
}

// Before Start, or with a manager that cannot report, the answer is unknown
// rather than an empty list.
func TestReconcilerAdapterUnclaimedUnknownWithoutAReportingManager(t *testing.T) {
	t.Parallel()

	adapter := newLifecycleAdapter(nil)
	if adapter.UnclaimedDeployments().Known {
		t.Error("report is known before a manager exists")
	}

	adapter.manager = &recordingManager{}
	if adapter.UnclaimedDeployments().Known {
		t.Error("report is known for a manager that cannot report")
	}
}
