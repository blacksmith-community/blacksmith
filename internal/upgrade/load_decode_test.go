//nolint:testpackage // the tests load stored upgrades and read the manager's unexported state
package upgrade

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	vaultPkg "blacksmith/pkg/vault"
)

// undecodableTaskID names the stored upgrade whose record can't be decoded.
const undecodableTaskID = "undecodable-upgrade"

// undecodableVault is a waitVault whose read of one stored upgrade fails
// the way the Vault client fails a record that doesn't fit UpgradeTask.
type undecodableVault struct {
	*waitVault

	reads atomic.Int32
}

func (v *undecodableVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if path == upgradeTasksVaultPath+"/"+undecodableTaskID {
		v.reads.Add(1)

		return false, fmt.Errorf("%w: failed to convert data", vaultPkg.ErrCouldNotRemarshalData)
	}

	return v.waitVault.Get(ctx, path, result)
}

// A stored upgrade that can't be decoded is skipped with an Error that
// names its record, so the load finishes, the other stored upgrades
// resume, and new upgrades are accepted.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadSkipsAStoredUpgradeThatCannotBeDecoded(t *testing.T) {
	director := newResumeDirector()

	stored := newWaitVault()
	storeUpgrade(t, stored, storedUpgrade(undecodableTaskID, pendingInstance("instance-2", nextDeployment)))
	storeUpgrade(t, stored, storedUpgrade(loadedTaskID, pendingInstance("instance-1", firstDeployment)))

	vault := &undecodableVault{waitVault: stored}
	log := newProcessLogger()
	manager := loadManagerLogging(t, log, director, vault)

	expectCompleted(t, manager, loadedTaskID)

	_, err := manager.GetTask(undecodableTaskID)
	if err == nil {
		t.Fatalf("expected the undecodable upgrade to stay unloaded")
	}

	if reads := vault.reads.Load(); reads != 1 {
		t.Fatalf("expected one read of the undecodable upgrade, got %d", reads)
	}

	if !log.loggedError(upgradeTasksVaultPath+"/"+undecodableTaskID, "by hand") {
		t.Fatalf("expected an error naming the undecodable record, got %v", log.errors)
	}

	if count := director.deployCount(nextDeployment); count != 0 {
		t.Fatalf("expected no deploy of the undecodable upgrade's instance, got %d", count)
	}

	created, err := manager.CreateTask(t.Context(), stemcellRequest("instance-3"))
	if err != nil {
		t.Fatalf("expected a new upgrade once the load finished, got %v", err)
	}

	expectCompleted(t, manager, created.ID)
}
