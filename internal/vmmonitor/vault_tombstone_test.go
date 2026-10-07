package vmmonitor_test

import (
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
)

// startVault starts the in-process vault and returns a client for it.
func startVault(t *testing.T) *internalVault.Vault {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	return internalVault.New(server.Addr, server.RootToken, true)
}

// The monitor writes its tombstone into the vault's index itself. It marks
// an indexed instance deleted and keeps every field the entry held, and it
// never creates an entry for an instance the index does not have, because a
// bare tombstone with no service, plan, or deployment would stay behind with
// nothing to remove it.
func TestMonitorTombstonesTheVaultIndex(t *testing.T) {
	t.Parallel()

	t.Run("the monitor marks an indexed instance deleted and keeps its fields", func(t *testing.T) {
		t.Parallel()

		expectIndexedInstanceMarked(t)
	})

	t.Run("the monitor creates no tombstone for an instance that is not indexed", func(t *testing.T) {
		t.Parallel()

		expectNoTombstoneCreated(t)
	})
}

// expectIndexedInstanceMarked has the monitor tombstone an old, idle
// instance in the vault's index, and checks the mark and the fields the
// entry kept.
func expectIndexedInstanceMarked(t *testing.T) {
	t.Helper()

	vault := startVault(t)

	err := vault.UpdateIndex(t.Context(), func(entries map[string]interface{}) (bool, error) {
		entries[inFlightID] = oldEntry()

		return true, nil
	})
	if err != nil {
		t.Fatalf("seeding the index: %v", err)
	}

	monitor := vmmonitor.NewWithVault(vault, goneDirector(nil), noClaims{})

	err = monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	index, err := vault.GetIndex(t.Context(), "db")
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	entry, _ := index.Data[inFlightID].(map[string]interface{})
	deletedAt, _ := entry["deleted_at"].(string)

	_, parseErr := time.Parse(time.RFC3339, deletedAt)

	if entry["status"] != statusDeleted || parseErr != nil || entry["deleted_by"] != deletedByMonitor || entry["deletion_reason"] == nil {
		t.Errorf("the monitor left the entry %v; want it marked deleted by the vm-monitor, with a deleted_at and a deletion_reason", entry)
	}

	for field, want := range map[string]interface{}{serviceKey: serviceID, planKey: inFlightPlan, nameKey: inFlightDeployment} {
		if entry[field] != want {
			t.Errorf("the marked entry holds %s %v, want %v as before the mark", field, entry[field], want)
		}
	}

	if _, stillMonitored := monitor.MonitoredDeployment(inFlightID); stillMonitored {
		t.Errorf("the monitor still watches instance %s after marking it deleted", inFlightID)
	}
}

// expectNoTombstoneCreated has the monitor check an instance that the
// vault's index does not have, and checks that no entry was created for it.
func expectNoTombstoneCreated(t *testing.T) {
	t.Helper()

	vault := startVault(t)
	monitor := vmmonitor.NewWithVault(vault, goneDirector(nil), noClaims{})

	err := monitor.AddService(t.Context(), inFlightID, inFlightDeployment)
	if err != nil {
		t.Fatalf("adding the instance: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	_, indexed, err := vault.FindInstance(t.Context(), inFlightID)
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	status, err := monitor.GetServiceVMStatus(t.Context(), inFlightID)
	if err != nil {
		t.Fatalf("reading the VM status: %v", err)
	}

	_, stillMonitored := monitor.MonitoredDeployment(inFlightID)

	if indexed || status.Status != statusDeleted || stillMonitored {
		t.Errorf("an instance that is not indexed: indexed %t, VM status %q, still monitored %t; want not indexed, deleted, and unmonitored",
			indexed, status.Status, stillMonitored)
	}
}
