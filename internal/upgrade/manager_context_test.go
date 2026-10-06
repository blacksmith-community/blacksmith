package upgrade

import (
	"context"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// An upgrade task changes in memory and at the director before persistTask
// runs, so persistTask must save it even when the request or manager context
// has ended.
func TestPersistTaskLandsAfterTheContextEnds(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start the in-process Vault: %v", err)
	}

	manager := &Manager{
		logger: logger.Get().Named("upgrade-manager-test"),
		vault:  internalVault.New(server.Addr, server.RootToken, true),
	}

	task := &UpgradeTask{ID: "task-after-context-ends", Status: TaskStatusCancelled, CreatedAt: time.Now()}

	ended, cancel := context.WithCancel(context.Background())
	cancel()

	err = manager.persistTask(ended, task)
	if err != nil {
		t.Fatalf("persistTask failed on a context that had ended: %v", err)
	}

	stored, err := server.ReadSecret(upgradeTasksVaultPath + "/" + task.ID)
	if err != nil {
		t.Fatalf("the task was not saved: %v", err)
	}

	if stored["status"] != string(TaskStatusCancelled) {
		t.Fatalf("the saved task has status %v, want %s", stored["status"], TaskStatusCancelled)
	}

	index, err := server.ReadSecret(upgradeTasksVaultPath)
	if err != nil {
		t.Fatalf("the tasks index was not saved: %v", err)
	}

	if _, ok := index[task.ID]; !ok {
		t.Fatalf("the tasks index has no entry for %s: %v", task.ID, index)
	}
}
