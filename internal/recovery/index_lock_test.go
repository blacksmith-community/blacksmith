package recovery

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
)

// The reconciler gets the broker's own Vault value through vaultWrapper, so
// its index writes and the broker's must serialize on one lock. Each writer
// adds a different key at the same moment, and every key must survive.
func TestReconcilerAndBrokerIndexWritesBothSurvive(t *testing.T) {
	ctx := t.Context()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	wrapped := &vaultWrapper{vault: vaultClient}
	log := &loggerWrapper{logger: logger.Get().Named("index-lock-test")}
	updater := reconciler.NewVaultUpdater(wrapped, log, reconciler.BackupConfig{})
	synchronizer := reconciler.NewIndexSynchronizer(wrapped, log)

	const writers = 12

	entry := func(id string) map[string]interface{} {
		return map[string]interface{}{"service_id": "valkey", "plan_id": "standalone", "deployment_name": "standalone-" + id}
	}

	// The sync rewrites the entries of the instances it reconciled, so it
	// gets entries of its own to update.
	instances := make([]reconciler.InstanceData, 0, 4)

	for i := range 4 {
		id := fmt.Sprintf("synced-%d", i)

		err = vaultClient.Index(ctx, id, entry(id))
		if err != nil {
			t.Fatalf("failed to seed %s: %v", id, err)
		}

		instances = append(instances, reconciler.InstanceData{
			ID: id, ServiceID: "valkey", PlanID: "standalone",
			Deployment: reconciler.DeploymentDetail{DeploymentInfo: reconciler.DeploymentInfo{Name: "standalone-" + id}},
		})
	}

	start := make(chan struct{})
	errs := make(chan error, 3*writers)

	var group sync.WaitGroup

	for i := range writers {
		brokerID := fmt.Sprintf("broker-%d", i)
		reconcilerID := fmt.Sprintf("reconciler-%d", i)

		group.Go(func() {
			<-start

			errs <- vaultClient.Index(ctx, brokerID, entry(brokerID))
		})
		group.Go(func() {
			<-start

			errs <- updater.UpdateIndex(reconcilerID, entry(reconcilerID))
		})
		group.Go(func() {
			<-start

			errs <- synchronizer.SyncIndex(ctx, instances)
		})
	}

	close(start)
	group.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("an index write failed: %v", err)
		}
	}

	idx, err := vaultClient.GetIndex(ctx, "db")
	if err != nil {
		t.Fatalf("failed to read the index: %v", err)
	}

	var lost []string

	for i := range writers {
		for _, id := range []string{fmt.Sprintf("broker-%d", i), fmt.Sprintf("reconciler-%d", i)} {
			if _, ok := idx.Data[id]; !ok {
				lost = append(lost, id)
			}
		}
	}

	for _, instance := range instances {
		data, ok := idx.Data[instance.ID].(map[string]interface{})
		if !ok || data["reconciled"] != true {
			lost = append(lost, instance.ID+" (reconciled)")
		}
	}

	if len(lost) > 0 {
		t.Fatalf("%d of %d index writes were lost: %v", len(lost), 2*writers+len(instances), lost)
	}
}

// Phase 4 checks for a deleted mark without the lock, then spends a while on
// credentials and bindings before it writes the instance's index entry. The
// vm-monitor can mark the instance deleted in that window, so the write must
// keep the mark, and the broker's deprovision request, that it finds there.
func TestReconcilerInstanceWriteKeepsTheDeletedMark(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	wrapped := &vaultWrapper{vault: vaultClient}
	log := &loggerWrapper{logger: logger.Get().Named("index-mark-test")}
	updater := reconciler.NewVaultUpdater(wrapped, log, reconciler.BackupConfig{})

	const instanceID = "marked-0"

	err = vaultClient.Index(ctx, instanceID, map[string]interface{}{
		"service_id":      "valkey",
		"plan_id":         "standalone",
		"deployment_name": "standalone-" + instanceID,
	})
	if err != nil {
		t.Fatalf("failed to seed the entry: %v", err)
	}

	err = vaultClient.UpdateIndexEntry(ctx, instanceID, map[string]interface{}{
		"deprovision_requested_at": "2026-10-06T12:00:00Z",
		"status":                   "deprovision_requested",
	})
	if err != nil {
		t.Fatalf("failed to record the deprovision request: %v", err)
	}

	err = markDeletedAsTheMonitorDoes(ctx, vaultClient, instanceID)
	if err != nil {
		t.Fatalf("failed to mark the instance deleted: %v", err)
	}

	_, err = updater.UpdateInstance(ctx, reconciler.InstanceData{
		ID: instanceID, ServiceID: "valkey", PlanID: "standalone",
		Deployment: reconciler.DeploymentDetail{DeploymentInfo: reconciler.DeploymentInfo{Name: "standalone-" + instanceID}},
	})
	if err != nil {
		t.Fatalf("the reconciler's update failed: %v", err)
	}

	idx, err := vaultClient.GetIndex(ctx, "db")
	if err != nil {
		t.Fatalf("failed to read the index: %v", err)
	}

	entry, ok := idx.Data[instanceID].(map[string]interface{})
	if !ok {
		t.Fatalf("the entry for %s is gone", instanceID)
	}

	if entry["reconciled"] != true {
		t.Errorf("the reconciler's own fields did not land: reconciled = %v", entry["reconciled"])
	}

	if entry["status"] != "deleted" {
		t.Errorf("the deleted mark was lost: status = %v", entry["status"])
	}

	for _, field := range []string{"deleted_at", "deleted_by", "deletion_reason", "deprovision_requested_at"} {
		if _, has := entry[field]; !has {
			t.Errorf("the reconciler's write dropped %s", field)
		}
	}
}

// markDeletedAsTheMonitorDoes marks an indexed instance deleted the way the vm-monitor does,
// in one index update that keeps every field of the entry. It marks nothing
// when the instance is not indexed.
func markDeletedAsTheMonitorDoes(ctx context.Context, client *internalVault.Vault, instanceID string) error {
	err := client.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		entry, indexed := entries[instanceID].(map[string]interface{})
		if !indexed {
			return false, nil
		}

		entry["status"] = "deleted"
		entry["deleted_at"] = time.Now().Format(time.RFC3339)
		entry["deleted_by"] = "vm-monitor"
		entry["deletion_reason"] = "deployment not found in BOSH director"

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("mark %s deleted: %w", instanceID, err)
	}

	return nil
}
