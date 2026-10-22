package mutation_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/mutation"
)

func fixedNow() time.Time {
	return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
}

func TestGuardConflictNamesHolder(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)

	_, err := guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{
		Kind:        mutation.KindUpgrade,
		TaskID:      "task-1",
		Description: `upgrade "noble 1.512"`,
	})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	release, err := guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{Kind: mutation.KindCertificates})
	if err == nil {
		release()
		t.Fatal("second acquire succeeded, want busy")
	}

	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("errors.Is(err, ErrInstanceBusy) = false for %v", err)
	}

	var busy *mutation.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("error is %T, want *BusyError", err)
	}

	if busy.Holder.Description != `upgrade "noble 1.512"` {
		t.Fatalf("holder description = %q", busy.Holder.Description)
	}

	if busy.Holder.Kind != mutation.KindUpgrade || busy.Holder.TaskID != "task-1" {
		t.Fatalf("holder = %+v", busy.Holder)
	}

	if want := `instance is busy: upgrade "noble 1.512"`; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}

	if !busy.Holder.Since.Equal(fixedNow()) {
		t.Fatalf("holder since = %v, want %v", busy.Holder.Since, fixedNow())
	}
}

func TestGuardDeploymentKeyBlocksInstanceKey(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)

	_, err := guard.Acquire("", "redis-7a1b", mutation.Holder{
		Kind:        mutation.KindDeploymentAction,
		Description: "restart from the UI",
	})
	if err != nil {
		t.Fatalf("deployment acquire: %v", err)
	}

	_, err = guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{Kind: mutation.KindCertificates})
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("instance acquire err = %v, want busy", err)
	}

	holder, held := guard.Holder("7a1b", "redis-7a1b")
	if !held || holder.Kind != mutation.KindDeploymentAction {
		t.Fatalf("Holder = %+v, %v", holder, held)
	}

	// A different deployment and instance are unaffected.
	release, err := guard.Acquire("8c2d", "redis-8c2d", mutation.Holder{Kind: mutation.KindCertificates})
	if err != nil {
		t.Fatalf("unrelated acquire: %v", err)
	}

	release()
}

func TestGuardInstanceKeyBlocksDeploymentKey(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)

	_, err := guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{Kind: mutation.KindProvision})
	if err != nil {
		t.Fatalf("instance acquire: %v", err)
	}

	_, err = guard.Acquire("", "redis-7a1b", mutation.Holder{Kind: mutation.KindDeploymentAction})
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("deployment acquire err = %v, want busy", err)
	}

	// The same instance under another deployment name is still busy.
	_, err = guard.Acquire("7a1b", "", mutation.Holder{Kind: mutation.KindCertificates})
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("instance-only acquire err = %v, want busy", err)
	}
}

func TestGuardReleaseIdempotent(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)
	holder := mutation.Holder{Kind: mutation.KindUpgrade, Description: "upgrade"}

	first, err := guard.Acquire("a", "dep-a", holder)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	first()

	if _, held := guard.Holder("a", "dep-a"); held {
		t.Fatal("still held after release")
	}

	second, err := guard.Acquire("a", "dep-a", holder)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	first()

	_, err = guard.Acquire("a", "dep-a", holder)
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("acquire after stale release err = %v, want busy", err)
	}

	second()
	second()

	if snap := guard.Snapshot(); len(snap) != 0 {
		t.Fatalf("snapshot after release = %v, want empty", snap)
	}
}

func TestGuardRejectsEmptyKeys(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)

	_, err := guard.Acquire("", "", mutation.Holder{Kind: mutation.KindUpgrade})
	if err == nil {
		t.Fatal("acquire with no keys succeeded")
	}

	if errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("empty-key error must not read as busy: %v", err)
	}
}

func TestGuardSnapshotCopies(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(fixedNow)

	_, err := guard.Acquire("i-1", "dep-1", mutation.Holder{Kind: mutation.KindUpgrade, TaskID: "t"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	_, err = guard.Acquire("", "dep-2", mutation.Holder{Kind: mutation.KindDeploymentAction})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	snap := guard.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %v, want two reservations", snap)
	}

	if snap["i-1"].TaskID != "t" {
		t.Fatalf("snapshot[i-1] = %+v", snap["i-1"])
	}

	if snap["dep-2"].Kind != mutation.KindDeploymentAction {
		t.Fatalf("snapshot[dep-2] = %+v", snap["dep-2"])
	}

	delete(snap, "i-1")

	if _, held := guard.Holder("i-1", ""); !held {
		t.Fatal("mutating the snapshot changed the guard")
	}
}

func TestGuardConcurrentAcquireOneWins(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)

	const workers = 64

	var (
		group   sync.WaitGroup
		lock    sync.Mutex
		winners int
	)

	start := make(chan struct{})

	for worker := range workers {
		group.Add(1)

		go func(worker int) {
			defer group.Done()

			<-start

			kind := mutation.KindCertificates
			if worker%2 == 0 {
				kind = mutation.KindUpgrade
			}

			_, err := guard.Acquire("7a1b", "redis-7a1b", mutation.Holder{Kind: kind})
			if err == nil {
				lock.Lock()
				winners++
				lock.Unlock()

				return
			}

			if !errors.Is(err, mutation.ErrInstanceBusy) {
				t.Errorf("worker %d: unexpected error %v", worker, err)
			}
		}(worker)
	}

	close(start)
	group.Wait()

	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
