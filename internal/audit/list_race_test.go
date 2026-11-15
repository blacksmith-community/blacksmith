package audit_test

import (
	"context"
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
