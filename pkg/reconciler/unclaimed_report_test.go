package reconciler_test

import (
	"context"
	"testing"

	. "blacksmith/pkg/reconciler"
)

const (
	unclaimedTestService = "valkey"
	unclaimedTestPlan    = "standalone"
	unclaimedTestPrefix  = "valkey-standalone-"
)

// A deployment with no index entry and no CF service instance is left alone,
// and the operator needs to hear about it, so the manager keeps what each
// complete run found.
func TestReconcilerManager_ReportsUnclaimedDeployments(t *testing.T) {
	t.Parallel()

	const instanceID = "481c0984-f4b6-461c-83ff-98ff41872f4b"

	manager, scan, _, _ := newUnclaimedTestManager(t, &fakeCFDiscovery{})
	scan.deployments = []DeploymentInfo{{Name: unclaimedTestPrefix + instanceID}}

	before := manager.UnclaimedDeployments()
	if before.Known {
		t.Fatalf("report is known before any run: %+v", before)
	}

	manager.RunReconciliation(context.Background())

	report := manager.UnclaimedDeployments()
	if !report.Known || report.AsOf.IsZero() {
		t.Fatalf("report not known after a complete run: %+v", report)
	}

	if len(report.Deployments) != 1 {
		t.Fatalf("got %d unclaimed deployments, want 1: %+v", len(report.Deployments), report.Deployments)
	}

	got := report.Deployments[0]
	if got.InstanceID != instanceID || got.DeploymentName != unclaimedTestPrefix+instanceID {
		t.Errorf("unclaimed deployment = %+v", got)
	}

	if got.FirstSeen.IsZero() {
		t.Errorf("first seen is not set: %+v", got)
	}

	// A second run keeps the time it was first seen.
	manager.RunReconciliation(context.Background())

	again := manager.UnclaimedDeployments()
	if len(again.Deployments) != 1 || !again.Deployments[0].FirstSeen.Equal(got.FirstSeen) {
		t.Errorf("first seen moved from %v to %+v", got.FirstSeen, again.Deployments)
	}
}

// A deployment that CF knows is not an orphan, so it never appears.
func TestReconcilerManager_UnclaimedReportSkipsClaimedDeployments(t *testing.T) {
	t.Parallel()

	const instanceID = "581c0984-f4b6-461c-83ff-98ff41872f4b"

	cf := &fakeCFDiscovery{instances: []CFServiceInstanceDetails{{
		GUID: instanceID, Name: "vk3", ServiceID: unclaimedTestService, PlanID: unclaimedTestPlan,
	}}}

	manager, scan, _, _ := newUnclaimedTestManager(t, cf)
	scan.deployments = []DeploymentInfo{{Name: unclaimedTestPrefix + instanceID}}

	manager.RunReconciliation(context.Background())

	report := manager.UnclaimedDeployments()
	if !report.Known || len(report.Deployments) != 0 {
		t.Fatalf("report = %+v, want known and empty", report)
	}
}

// Without CF discovery the reconciler cannot tell, so it must not claim that
// there are none.
func TestReconcilerManager_UnclaimedReportUnknownWithoutCFDiscovery(t *testing.T) {
	t.Parallel()

	manager, scan, _, _ := newUnclaimedTestManager(t, nil)
	scan.deployments = []DeploymentInfo{{Name: unclaimedTestPrefix + "681c0984-f4b6-461c-83ff-98ff41872f4b"}}

	manager.RunReconciliation(context.Background())

	if report := manager.UnclaimedDeployments(); report.Known {
		t.Fatalf("report is known without CF discovery: %+v", report)
	}
}
