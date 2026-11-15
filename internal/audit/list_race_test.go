package audit_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
)

// racingKV lets a record's write finish, and the record leave the queue,
// just after List has read the index and before it looks at the queue.
type racingKV struct {
	*audittest.KV

	store   *audit.ChangeStore
	armed   atomic.Bool
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func (k *racingKV) Put(ctx context.Context, path string, data interface{}) error {
	if path != audit.ChangeVaultPath {
		k.once.Do(func() { close(k.writing) })
		<-k.release
	}

	return k.KV.Put(ctx, path, data) //nolint:wrapcheck // the fake passes the store's own error through
}

func (k *racingKV) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	found, err := k.KV.Get(ctx, path, out)

	if path == audit.ChangeVaultPath && k.armed.CompareAndSwap(true, false) {
		close(k.release)
		k.store.Wait()
	}

	return found, err //nolint:wrapcheck // the fake passes the store's own error through
}

// A record whose write lands while List runs is listed, whether List finds
// it in the queue or in the index.
func TestListShowsARecordThatLandsWhileItRuns(t *testing.T) {
	t.Parallel()

	vault := &racingKV{KV: audittest.NewKV(), writing: make(chan struct{}), release: make(chan struct{})}
	store := newStore(vault, &audittest.Logger{}, time.Now())
	vault.store = store

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: keyBatchJobs, Old: "5", New: "10"})
	<-vault.writing
	vault.armed.Store(true)

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 1 || records[0].New != "10" {
		t.Fatalf("records = %+v, want the record that landed during the list", records)
	}
}

// The parts of the hand-written bodies the unreadable-record test saves.
const (
	fieldScope = "scope"
	goodID     = "good"
	badID      = "bad"
)

// A record body that can't be read, such as one edited by hand with a
// number for a value, is logged and skipped, and the rest still list.
func TestListSkipsARecordItCannotRead(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	log := &audittest.Logger{}
	ctx := t.Context()
	stamp := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	for path, body := range map[string]interface{}{
		audit.ChangeVaultPath + "/" + badID:  map[string]interface{}{"id": badID, "at": stamp, fieldScope: audit.ScopeSettings, "key": keyBatchJobs, "old": 5},
		audit.ChangeVaultPath + "/" + goodID: audit.ChangeRecord{ID: goodID, At: stamp, Scope: audit.ScopeSettings, Key: keyInterval, Old: "1h", New: halfHour},
		audit.ChangeVaultPath: map[string]interface{}{
			badID:  map[string]string{"at": stamp.Format(time.RFC3339Nano), fieldScope: audit.ScopeSettings},
			goodID: map[string]string{"at": stamp.Format(time.RFC3339Nano), fieldScope: audit.ScopeSettings},
		},
	} {
		err := vault.Put(ctx, path, body)
		if err != nil {
			t.Fatal(err)
		}
	}

	records, err := newStore(vault, log, stamp).List(ctx, audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 1 || records[0].ID != goodID {
		t.Fatalf("records = %+v, want the readable record alone", records)
	}

	if text := log.Text(); !strings.Contains(text, badID) {
		t.Fatalf("the unreadable record was not logged: %q", text)
	}
}
