package audit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
)

var errLostAnswer = errors.New("the answer to the index write was lost")

// A value longer than 1024 bytes is cut to 1024 bytes in the record, at a
// character boundary, so one change can't fill the trail.
func TestRecordCutsALongValue(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	store := newStore(vault, &audittest.Logger{}, time.Now())
	long := strings.Repeat("é", 1500)

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: keyInterval, Old: "1h", New: long})
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err = %v, want one", records, err)
	}

	got := records[0].New
	if len(got) != 1024 || !strings.HasPrefix(long, got) {
		t.Fatalf("value holds %d bytes, want the first 1024 bytes of the value", len(got))
	}
}

// lostAnswerKV saves the index but reports the first index write as
// failed, as a Vault whose answer was lost on the way back does, and
// counts the writes of each record body.
type lostAnswerKV struct {
	*audittest.KV

	mu     sync.Mutex
	bodies int
	lost   bool
}

func (k *lostAnswerKV) Put(ctx context.Context, path string, data interface{}) error {
	err := k.KV.Put(ctx, path, data)
	if err != nil {
		return err //nolint:wrapcheck // the fake passes the store's own error through
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	if path != audit.ChangeVaultPath {
		k.bodies++

		return nil
	}

	if !k.lost {
		k.lost = true

		return errLostAnswer
	}

	return nil
}

// A try whose index write landed but answered an error is not written
// again, because the retry finds the record in the index.
func TestARetryDoesNotRewriteARecordTheIndexNames(t *testing.T) {
	t.Parallel()

	vault := &lostAnswerKV{KV: audittest.NewKV()}
	log := &audittest.Logger{}
	store := newStore(vault, log, time.Now())

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: keyBatchJobs, Old: "5", New: "10"})
	store.Wait()

	vault.mu.Lock()
	bodies := vault.bodies
	vault.mu.Unlock()

	if bodies != 1 {
		t.Fatalf("the record body was written %d times, want once", bodies)
	}

	if text := log.Text(); strings.Contains(text, "was never saved") {
		t.Fatalf("the record was reported lost: %s", text)
	}

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err = %v, want the one record", records, err)
	}
}

// A body whose scope disagrees with its index entry is left out of the
// index entry's scope, so a hand edit can't move a record between panels.
func TestListKeepsToTheScopeTheBodyNames(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	ctx := t.Context()
	stamp := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	err := vault.Put(ctx, audit.ChangeVaultPath+"/moved", audit.ChangeRecord{ID: "moved", At: stamp, Scope: audit.ScopeEndpoints, Key: keyName, New: "lab-cf"})
	if err != nil {
		t.Fatal(err)
	}

	err = vault.Put(ctx, audit.ChangeVaultPath, map[string]interface{}{"moved": map[string]string{"at": stamp.Format(time.RFC3339Nano), fieldScope: audit.ScopeSettings}})
	if err != nil {
		t.Fatal(err)
	}

	records, err := newStore(vault, &audittest.Logger{}, stamp).List(ctx, audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 0 {
		t.Fatalf("records = %+v, want none, since the body names another scope", records)
	}
}
