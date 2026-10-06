package upgrade

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// A tasks index that cannot be read is not an empty one. Writing a fresh index
// over it would drop every other task, and loadPersistedTasks would forget
// them at the next restart.
func TestPersistTaskKeepsTheTasksIndexWhenItCannotBeRead(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start the in-process Vault: %v", err)
	}

	proxy, err := testutil.NewVaultFaultProxy(server.Addr)
	if err != nil {
		t.Fatalf("failed to start the fault proxy: %v", err)
	}

	t.Cleanup(proxy.Close)

	seeded := map[string]interface{}{
		"earlier-task": map[string]interface{}{"status": "completed", "created_at": float64(1)},
	}

	err = server.WriteSecret(upgradeTasksVaultPath, seeded)
	if err != nil {
		t.Fatalf("failed to seed the tasks index: %v", err)
	}

	proxy.FailReads(upgradeTasksVaultPath, http.StatusForbidden, "permission denied", -1)

	manager := &Manager{
		logger: logger.Get().Named("upgrade-manager-test"),
		vault:  internalVault.New(proxy.Addr, server.RootToken, true),
	}

	task := &UpgradeTask{ID: "later-task", Status: TaskStatusCancelled, CreatedAt: time.Now()}

	err = manager.persistTask(context.Background(), task)
	if err == nil {
		t.Fatal("persistTask reported success although it could not read the tasks index")
	}

	if writes := proxy.WritesTo(upgradeTasksVaultPath); len(writes) != 1 {
		t.Fatalf("expected only the task's own record to be written under %s, got %v", upgradeTasksVaultPath, writes)
	}

	index, err := server.ReadSecret(upgradeTasksVaultPath)
	if err != nil {
		t.Fatalf("the tasks index is gone: %v", err)
	}

	if _, ok := index["earlier-task"]; !ok || len(index) != 1 {
		t.Fatalf("the tasks index changed although it could not be read: %v", index)
	}
}
