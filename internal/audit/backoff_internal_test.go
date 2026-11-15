package audit

import (
	"errors"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit/audittest"
)

var errVaultSealed = errors.New("vault is sealed")

// A write Vault keeps refusing is tried again after waits that double
// from one second, so a Vault that is down for half a minute still gets
// the record.
func TestAFailedWriteBacksOffOverHalfAMinute(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	vault.Fail(errVaultSealed)

	var (
		waits  sync.Mutex
		waited []time.Duration
	)

	store := NewChangeStore(vault, &audittest.Logger{})
	store.sleep = func(wait time.Duration) {
		waits.Lock()
		defer waits.Unlock()

		waited = append(waited, wait)
	}

	store.Record(t.Context(), ChangeRecord{Scope: ScopeSettings, Key: "max_batch_jobs", Old: "5", New: "10"})
	store.Wait()

	waits.Lock()
	defer waits.Unlock()

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if len(waited) != len(want) {
		t.Fatalf("waited %v between tries, want %v", waited, want)
	}

	for i := range want {
		if waited[i] != want[i] {
			t.Fatalf("waited %v between tries, want %v", waited, want)
		}
	}
}
