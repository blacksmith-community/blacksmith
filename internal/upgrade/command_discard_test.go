package upgrade

import (
	"context"
	"encoding/json"
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

	spends := env.spends.Load()

	// The discard names the other preview, which must stay in the store.
	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(other.ID))
	if err != nil || result.Discarded || result.TaskID != task.ID {
		t.Fatalf("expected the created task %s back undiscarded, got %+v: %v", task.ID, result, err)
	}

	if _, err := env.manager.certDeps().Previews.GetCommand(other.ID); err != nil {
		t.Fatalf("a discard that found a task revoked the preview: %v", err)
	}

	if env.spends.Load() != spends {
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

	store.Revoke(preview.ID, cmdRequestID)
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

	store.Revoke("pv-cert", cmdRequestID)
	store.Revoke("pv-absent", cmdRequestID)

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

	store.Revoke("pv-1", cmdRequestID)
	now = now.Add(2 * defaultPreviewTTL)
	store.Revoke("pv-2", cmdRequestID)

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.revoked) != 1 {
		t.Fatalf("expected the lapsed tombstone to be pruned, got %d", len(store.revoked))
	}
}

// atomicOnce reports true on its first call only.
type atomicOnce struct{ done atomic.Bool }

func (o *atomicOnce) first() bool { return o.done.CompareAndSwap(false, true) }

// An ID longer than the bound is refused before the lock or the store sees
// it, so a caller cannot grow the revoked set with huge IDs.
func TestDiscardCommandRequestRefusesLongIDs(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	long := strings.Repeat("a", maxDiscardIDBytes+1)

	for _, req := range []CommandDiscardRequest{
		{ClientRequestID: long, PreviewID: "pv-1"},
		{ClientRequestID: cmdRequestID, PreviewID: long},
	} {
		_, err := env.manager.DiscardCommandRequest(context.Background(), req)
		expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)
	}

	store := env.manager.certDeps().Previews

	store.mu.Lock()
	tombstones := len(store.revoked)
	store.mu.Unlock()

	if tombstones != 0 {
		t.Fatal("a refused discard left a tombstone")
	}

	// An ID at the bound is still accepted.
	atBound := strings.Repeat("a", maxDiscardIDBytes)

	result, err := env.manager.DiscardCommandRequest(context.Background(), CommandDiscardRequest{ClientRequestID: atBound, PreviewID: atBound})
	if err != nil || !result.Discarded {
		t.Fatalf("expected an ID at the bound to be discarded, got %+v: %v", result, err)
	}
}

// The scan finds a task of any kind, so a certificate task that carries the
// request ID is reported and its preview is left alone.
func TestDiscardCommandRequestFindsTaskOfAnyKind(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))

	env.manager.mu.Lock()
	env.manager.tasks["cert-task"] = &UpgradeTask{ID: "cert-task", Kind: TaskKindCertificates, ClientRequestID: cmdRequestID}
	env.manager.mu.Unlock()

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
	if err != nil || result.Discarded || result.TaskID != "cert-task" {
		t.Fatalf("expected the certificate task, got %+v: %v", result, err)
	}

	if _, err := env.manager.certDeps().Previews.GetCommand(preview.ID); err != nil {
		t.Fatalf("the preview was revoked although a task exists: %v", err)
	}

	env.manager.mu.Lock()
	delete(env.manager.tasks, "cert-task")
	env.manager.mu.Unlock()
}

// boundCmdReq is cmdReq for a preview bound to the request ID.
func boundCmdReq(requestID string, ids ...string) CommandPreviewRequest {
	req := cmdReq(ids...)
	req.ClientRequestID = requestID

	return req
}

// A discard of a preview bound to another request is refused with a 409
// preview_mismatch, and the preview stays, so one operator's discard can't
// remove the preview another is about to create from.
func TestDiscardCommandRequestRefusesAPreviewOfAnotherRequest(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, boundCmdReq("other-request", "i1"))

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
	expectAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)

	if result.Discarded {
		t.Error("the refused discard says it discarded the preview")
	}

	if _, err := env.manager.certDeps().Previews.GetCommand(preview.ID); err != nil {
		t.Fatalf("the refused discard removed the preview: %v", err)
	}
}

// A discard that names the request a preview is bound to revokes it, as an
// unbound discard does.
func TestDiscardCommandRequestRevokesItsOwnBoundPreview(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, boundCmdReq(cmdRequestID, "i1"))

	result, err := env.manager.DiscardCommandRequest(context.Background(), discardReq(preview.ID))
	if err != nil || !result.Discarded {
		t.Fatalf("expected the bound preview to be discarded, got %+v: %v", result, err)
	}

	_, err = env.manager.certDeps().Previews.GetCommand(preview.ID)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
}

// The binding outlives a spend, so a discard naming another request can't
// revoke a preview a create has spent and may still put back.
func TestRevokeOfASpentBoundPreviewNeedsItsRequest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := NewPreviewStore(func() time.Time { return now })
	preview := &CommandPreview{ID: "pv-bound", clientRequestID: cmdRequestID}
	store.PutCommand(preview, time.Minute)

	spent, err := store.Spend(preview.ID, PreviewKindCommand)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	if store.Revoke(preview.ID, "other-request") {
		t.Fatal("a discard of another request revoked the spent preview")
	}

	store.restoreCommand(spent.Command)

	if _, err := store.GetCommand(preview.ID); err != nil {
		t.Fatalf("the refused revoke kept the spent preview from coming back: %v", err)
	}
}

// A preview request takes an optional client_request_id, bounded like the
// create's.
func TestPreviewCommandTakesABoundedClientRequestID(t *testing.T) {
	t.Parallel()

	var req CommandPreviewRequest

	if err := json.Unmarshal([]byte(`{"client_request_id":"7c1d"}`), &req); err != nil || req.ClientRequestID != "7c1d" {
		t.Fatalf("the preview request read client_request_id as %q: %v", req.ClientRequestID, err)
	}

	env := newCreateCmdEnv(t, nil)

	_, err := env.manager.PreviewCommand(context.Background(), boundCmdReq(strings.Repeat("r", maxClientRequestIDBytes+1), "i1"))
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)
}
