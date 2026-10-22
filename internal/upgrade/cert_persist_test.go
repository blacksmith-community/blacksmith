//nolint:testpackage // the tests drive the unexported confirmed save
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/certinventory"
)

var (
	// errPutRefused is a write Vault refused before it stored anything.
	errPutRefused = errors.New("vault refused the write")
	// errAnswerLost is a write Vault stored whose answer never arrived.
	errAnswerLost = errors.New("the connection closed before vault answered")
	// errGetRefused is a read Vault refused.
	errGetRefused = errors.New("vault refused the read")
)

// confirmVault is a waitVault that fails the writes and reads a test
// scripts. A write can fail before it stores anything, or store and then
// fail, and a read of a path can fail. It counts the writes and reads of
// each path.
type confirmVault struct {
	*waitVault

	script sync.Mutex
	// failBefore holds, by path, how many more writes fail before they
	// store anything.
	failBefore map[string]int
	// failAfter holds, by path, how many more writes store and then fail.
	failAfter map[string]int
	// failGet holds the paths whose reads fail.
	failGet map[string]bool
	puts    map[string]int
	gets    map[string]int
}

func newConfirmVault() *confirmVault {
	return &confirmVault{
		waitVault:  newWaitVault(),
		failBefore: make(map[string]int),
		failAfter:  make(map[string]int),
		failGet:    make(map[string]bool),
		puts:       make(map[string]int),
		gets:       make(map[string]int),
	}
}

func (v *confirmVault) Put(ctx context.Context, path string, data interface{}) error {
	v.script.Lock()
	v.puts[path]++

	before := v.failBefore[path] > 0
	if before {
		v.failBefore[path]--
	}

	after := !before && v.failAfter[path] > 0
	if after {
		v.failAfter[path]--
	}
	v.script.Unlock()

	if before {
		return errPutRefused
	}

	err := v.waitVault.Put(ctx, path, data)
	if err != nil {
		return err
	}

	if after {
		return errAnswerLost
	}

	return nil
}

func (v *confirmVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	v.script.Lock()
	v.gets[path]++
	fail := v.failGet[path]
	v.script.Unlock()

	if fail {
		return false, errGetRefused
	}

	return v.waitVault.Get(ctx, path, result)
}

// scriptPut sets how many writes of the path fail before and after they
// store.
func (v *confirmVault) scriptPut(path string, before, after int) {
	v.script.Lock()
	defer v.script.Unlock()

	v.failBefore[path] = before
	v.failAfter[path] = after
}

// scriptGet makes every read of the path fail.
func (v *confirmVault) scriptGet(path string) {
	v.script.Lock()
	defer v.script.Unlock()

	v.failGet[path] = true
}

// putCount returns how many writes of the path were sent.
func (v *confirmVault) putCount(path string) int {
	v.script.Lock()
	defer v.script.Unlock()

	return v.puts[path]
}

// getCount returns how many reads of the path were sent.
func (v *confirmVault) getCount(path string) int {
	v.script.Lock()
	defer v.script.Unlock()

	return v.gets[path]
}

// certRecordPath is where the test certificate task's record is saved.
const certRecordPath = certificateTasksVaultPath + "/" + certTaskID

// confirmManager returns a loaded manager over the vault that holds the
// test certificate task.
func confirmManager(t *testing.T, vault *confirmVault) (*Manager, *UpgradeTask) {
	t.Helper()

	manager := managerWithTasks(t, vault)
	task := fullCertificateTask()

	manager.mu.Lock()
	manager.tasks[task.ID] = &task
	manager.mu.Unlock()

	return manager, &task
}

// A write that Vault refuses each time is never confirmed, and nothing is
// stored.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveFailedWrite(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	vault.scriptPut(certRecordPath, 3, 0)

	err := manager.persistCertConfirmed(context.Background(), task)
	if !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected %v, got %v", ErrSaveUnconfirmed, err)
	}

	if count := vault.putCount(certRecordPath); count != 3 {
		t.Fatalf("expected three writes of the record, got %d", count)
	}

	if _, ok := vault.storedRecord(t, certRecordPath); ok {
		t.Fatalf("expected no stored record")
	}
}

// A write that Vault stored before its answer was lost is confirmed by
// reading the record back, and it is not sent again.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveCommittedButAnswerLost(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	vault.scriptPut(certRecordPath, 0, 1)

	err := manager.persistCertConfirmed(context.Background(), task)
	if err != nil {
		t.Fatalf("expected the read-back to confirm the save, got %v", err)
	}

	if count := vault.putCount(certRecordPath); count != 1 {
		t.Fatalf("expected one write of the record, got %d", count)
	}

	stored, ok := vault.storedRecord(t, certRecordPath)
	if !ok || stored.Revision != 1 {
		t.Fatalf("expected the record at revision 1, got %+v", stored)
	}
}

// A read-back that finds an older revision means the write did not land,
// so it is sent again.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveRetriesOlderRevision(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	older := fullCertificateTask()
	older.Revision = 3

	err := vault.waitVault.Put(context.Background(), certRecordPath, older)
	if err != nil {
		t.Fatalf("storing the older record: %v", err)
	}

	manager.mu.Lock()
	task.Revision = 3
	manager.mu.Unlock()

	vault.scriptPut(certRecordPath, 1, 0)

	err = manager.persistCertConfirmed(context.Background(), task)
	if err != nil {
		t.Fatalf("expected the second write to confirm the save, got %v", err)
	}

	if count := vault.putCount(certRecordPath); count != 2 {
		t.Fatalf("expected two writes of the record, got %d", count)
	}

	stored, ok := vault.storedRecord(t, certRecordPath)
	if !ok || stored.Revision != 4 {
		t.Fatalf("expected the record at revision 4, got %+v", stored)
	}
}

// A record that landed under an index write that failed is confirmed once
// a retry of the index names the task, and is unconfirmed when the retry
// fails too.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveRecordOKIndexFailed(t *testing.T) {
	t.Run("the index retry lands", func(t *testing.T) {
		vault := newConfirmVault()
		manager, task := confirmManager(t, vault)

		vault.scriptPut(certificateTasksVaultPath, 1, 0)

		err := manager.persistCertConfirmed(context.Background(), task)
		if err != nil {
			t.Fatalf("expected the index retry to confirm the save, got %v", err)
		}

		if count := vault.putCount(certificateTasksVaultPath); count != 2 {
			t.Fatalf("expected two writes of the index, got %d", count)
		}

		if !manager.persistenceFor(certTaskID).indexed.Load() {
			t.Fatalf("expected the task to be marked indexed")
		}

		var index map[string]interface{}

		exists, err := vault.Get(context.Background(), certificateTasksVaultPath, &index)
		if err != nil || !exists {
			t.Fatalf("expected the index, got exists %v and error %v", exists, err)
		}

		if _, ok := index[certTaskID]; !ok {
			t.Fatalf("expected the index to name the task, got %v", index)
		}
	})

	t.Run("the index retry fails", func(t *testing.T) {
		vault := newConfirmVault()
		manager, task := confirmManager(t, vault)

		vault.scriptPut(certificateTasksVaultPath, 2, 0)

		err := manager.persistCertConfirmed(context.Background(), task)
		if !errors.Is(err, ErrSaveUnconfirmed) {
			t.Fatalf("expected %v, got %v", ErrSaveUnconfirmed, err)
		}

		if manager.persistenceFor(certTaskID).indexed.Load() {
			t.Fatalf("expected the task to stay unindexed")
		}
	})
}

// A write whose outcome can't be read back is never confirmed.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveReadBackFails(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	vault.scriptPut(certRecordPath, 1, 0)
	vault.scriptGet(certRecordPath)

	err := manager.persistCertConfirmed(context.Background(), task)
	if !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected %v, got %v", ErrSaveUnconfirmed, err)
	}

	if count := vault.putCount(certRecordPath); count != 1 {
		t.Fatalf("expected no write after the failed read-back, got %d writes", count)
	}
}

// A save dropped because a newer snapshot of the task already landed
// counts as confirmed, since the newer state holds everything it held.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveDroppedAsOlder(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	persistence := manager.persistenceFor(certTaskID)
	persistence.mu.Lock()

	saved := make(chan error, 1)

	go func() { saved <- manager.persistCertConfirmed(context.Background(), task) }()

	deadline := time.Now().Add(waitTimeout)

	for {
		manager.mu.RLock()

		revision := task.Revision

		manager.mu.RUnlock()

		if revision == 1 {
			break
		}

		if time.Now().After(deadline) {
			persistence.mu.Unlock()
			t.Fatal("the confirmed save never took its snapshot")
		}

		time.Sleep(time.Millisecond)
	}

	// A newer snapshot lands while the confirmed save waits its turn.
	manager.mu.Lock()
	newer := manager.snapshotLocked(task)
	manager.mu.Unlock()

	err := vault.Put(context.Background(), certRecordPath, newer.task)
	if err != nil {
		persistence.mu.Unlock()
		t.Fatalf("storing the newer snapshot: %v", err)
	}

	persistence.written = newer.seq
	persistence.mu.Unlock()

	select {
	case err = <-saved:
	case <-time.After(waitTimeout):
		t.Fatal("the confirmed save never returned")
	}

	if err != nil {
		t.Fatalf("expected the dropped save to count as confirmed, got %v", err)
	}

	if count := vault.putCount(certRecordPath); count != 1 {
		t.Fatalf("expected only the newer snapshot's write, got %d writes", count)
	}
}

// Once a confirmed save has seen the task in the index, later saves do not
// read the index again.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveReadsTheIndexOnce(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	for range 2 {
		err := manager.persistCertConfirmed(context.Background(), task)
		if err != nil {
			t.Fatalf("expected the save to be confirmed, got %v", err)
		}
	}

	// The load reads the index once, and so does each addToTasksIndex.
	// Only the first confirmed save adds a read of its own.
	if count := vault.getCount(certificateTasksVaultPath); count != 4 {
		t.Fatalf("expected four reads of the index, got %d", count)
	}
}

// A confirmed save that bumped a lower revision leaves a later plain save
// unable to land an older state over it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestConfirmedSaveBlocksAnOlderSnapshot(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	manager.mu.Lock()
	older := manager.snapshotLocked(task)
	manager.mu.Unlock()

	vault.scriptPut(certRecordPath, 0, 1)

	err := manager.persistCertConfirmed(context.Background(), task)
	if err != nil {
		t.Fatalf("expected the read-back to confirm the save, got %v", err)
	}

	err = manager.writeSnapshot(context.Background(), older)
	if err != nil {
		t.Fatalf("writing the older snapshot: %v", err)
	}

	stored, ok := vault.storedRecord(t, certRecordPath)
	if !ok || stored.Revision != 1 {
		t.Fatalf("expected the confirmed revision to stay saved, got %+v", stored)
	}
}

// A save that can't be confirmed pauses the task and keeps the instance in
// its phase, with a message that names the step it did not take. Nothing is
// saved, so Vault keeps the last confirmed record.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestPauseUnconfirmedKeepsPhase(t *testing.T) {
	vault := newConfirmVault()
	manager, task := confirmManager(t, vault)

	inst := &task.Instances[0]

	manager.pauseUnconfirmed(task, inst, "replace the files on redis/0")

	manager.mu.RLock()
	defer manager.mu.RUnlock()

	if inst.Certificates.Phase != RotationPhasePatch || inst.Status != InstanceStatusRunning {
		t.Fatalf("expected the instance to stay running in the patch phase, got %s in %s", inst.Status, inst.Certificates.Phase)
	}

	if !task.Paused || task.Status != TaskStatusPaused {
		t.Fatalf("expected the task to be paused, got paused %v and status %s", task.Paused, task.Status)
	}

	want := "Blacksmith could not confirm that it saved this task's progress to Vault, so it did not replace the files on redis/0. Resume once Vault is healthy."
	if inst.Error != want {
		t.Fatalf("expected the message %q, got %q", want, inst.Error)
	}

	if count := vault.putCount(certRecordPath); count != 0 {
		t.Fatalf("expected nothing to be saved, got %d writes", count)
	}
}

// The sentinel is one value under two names, so errors.Is matches it
// through either.
func TestErrSaveUnconfirmedMatchesBothNames(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("save: %w", ErrSaveUnconfirmed)
	if !errors.Is(err, certinventory.ErrSaveUnconfirmed) {
		t.Fatalf("expected the upgrade name to match the certinventory name")
	}

	err = fmt.Errorf("divergence: %w", certinventory.ErrSaveUnconfirmed)
	if !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("expected the certinventory name to match the upgrade name")
	}
}
