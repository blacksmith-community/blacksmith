package upgrade

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/pkg/logger"
)

func discardReq(previewID string) CommandDiscardRequest {
	return CommandDiscardRequest{ClientRequestID: cmdRequestID, PreviewID: previewID}
}

// A discard that finds no task revokes the preview, so a create that arrives
// later with the same request ID and body makes no task and fails at the
// preview.
func TestDiscardCommandRequestRevokesPreview(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
	if err != nil || !result.Discarded || result.TaskID != "" {
		t.Fatalf("expected a discard with no task, got %+v: %v", result, err)
	}

	_, _, err = env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
	expectTasksNone(t, env)

	expectNoCommandText(t, "the log", env.log.text())

	line := env.log.text()
	if !strings.Contains(line, cmdRequestID) || !strings.Contains(line, preview.ID) {
		t.Fatal("expected a log line that names the request and the preview")
	}
}

// A discard after a create made its task reports the task and leaves the
// preview store alone.
func TestDiscardCommandRequestAfterCreateReturnsTask(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))
	other := env.previewFor(t, cmdReq("i3"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	spends := env.manager.certDeps().Previews.spends.Load()

	// The discard names the other preview, which must stay in the store.
	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(other.ID))
	if err != nil || result.Discarded || result.TaskID != task.ID {
		t.Fatalf("expected the created task %s back undiscarded, got %+v: %v", task.ID, result, err)
	}

	if _, err := env.manager.certDeps().Previews.GetCommand(other.ID); err != nil {
		t.Fatalf("a discard that found a task revoked the preview: %v", err)
	}

	if env.manager.certDeps().Previews.spends.Load() != spends {
		t.Fatal("a discard spent a preview")
	}
}

// A create whose save was never confirmed is failed in memory and is not in
// the request index, and a discard still finds it.
func TestDiscardCommandRequestFindsUnconfirmedTask(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	env.manager.createRetry = time.Hour
	env.vault.setRules(func(int) bool { return true }, nil, false)

	preview := env.previewFor(t, cmdReq("i1"))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err == nil {
		t.Fatal("expected the create to fail")
	}

	failed := onlyTask(t, env)

	if _, _, found := env.manager.requests.Lookup(cmdRequestID); found {
		t.Fatal("premise: the unconfirmed task must not be in the request index")
	}

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
	if err != nil || result.Discarded || result.TaskID != failed.ID {
		t.Fatalf("expected the failed task %s, got %+v: %v", failed.ID, result, err)
	}
}

// A discard that arrives while a create with the same request ID is inside
// its save waits for the create and then reports the task it made.
func TestDiscardCommandRequestWaitsForCreateInProgress(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))

	entered := make(chan struct{})
	release := make(chan struct{})

	var held atomicOnce

	env.vault.onPut = func(path string, _ []byte) {
		if strings.HasPrefix(path, commandTasksVaultPath+"/") && held.first() {
			close(entered)
			<-release
		}
	}

	created := make(chan *UpgradeTask, 1)

	go func() {
		task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
		if err != nil {
			task = nil
		}

		created <- task
	}()

	<-entered

	discarded := make(chan CommandDiscardResult, 1)

	go func() {
		result, _ := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
		discarded <- result
	}()

	select {
	case result := <-discarded:
		t.Fatalf("the discard did not wait for the create, it answered %+v", result)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	task := <-created
	if task == nil {
		t.Fatal("the create failed")
	}

	select {
	case result := <-discarded:
		if result.Discarded || result.TaskID != task.ID {
			t.Fatalf("expected the created task %s, got %+v", task.ID, result)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the discard never finished")
	}
}

// A preview the store does not know is discarded all the same, because the
// outcome the operator wants, no run, holds.
func TestDiscardCommandRequestUnknownPreview(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq("pv-unknown"))
	if err != nil || !result.Discarded {
		t.Fatalf("expected discarded, got %+v: %v", result, err)
	}
}

func TestDiscardCommandRequestEmptyIDs(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	for name, req := range map[string]CommandDiscardRequest{
		"no request id": {PreviewID: "pv-1"},
		"no preview id": {ClientRequestID: cmdRequestID},
		"neither":       {},
	} {
		_, err := env.manager.DiscardCommandRequest(context.Background(), req)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}

		expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)
	}
}

func TestDiscardCommandRequestBeforeLoad(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
	t.Cleanup(manager.Stop)

	_, err := manager.DiscardCommandRequest(context.Background(), discardReq("pv-1"))
	if !errors.Is(err, ErrTasksLoading) {
		t.Fatalf("expected ErrTasksLoading, got %v", err)
	}
}

// A revoked preview stays gone when a create that had spent it restores it
// after its reservation failed.
func TestRevokeBlocksRestoreOfSpentPreview(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	store := env.manager.certDeps().Previews
	preview := env.previewFor(t, cmdReq("i1"))

	spent, err := store.Spend(preview.ID, PreviewKindCommand)
	if err != nil {
		t.Fatalf("Spend: %v", err)
	}

	store.Revoke(preview.ID)
	store.restoreCommand(spent.Command)

	_, err = store.GetCommand(preview.ID)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
}

// Revoke leaves a certificate preview alone and an unknown ID is no error.
func TestRevokeLeavesCertificatePreviewAlone(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := NewPreviewStore(func() time.Time { return now })
	cert := &Preview{ID: "pv-cert"}
	store.Put(cert, time.Minute)

	store.Revoke("pv-cert")
	store.Revoke("pv-absent")

	if _, err := store.Get("pv-cert"); err != nil {
		t.Fatalf("Revoke removed a certificate preview: %v", err)
	}
}

// A tombstone for a revoked ID lapses, so the store does not grow with the
// IDs it was asked to revoke.
func TestRevokeTombstoneLapses(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := NewPreviewStore(func() time.Time { return now })

	store.Revoke("pv-1")
	now = now.Add(2 * defaultPreviewTTL)
	store.Revoke("pv-2")

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.revoked) != 1 {
		t.Fatalf("expected the lapsed tombstone to be pruned, got %d", len(store.revoked))
	}
}

// atomicOnce reports true on its first call only.
type atomicOnce struct{ done atomic.Bool }

func (o *atomicOnce) first() bool { return o.done.CompareAndSwap(false, true) }
