//nolint:testpackage // the test calls the manager's unexported settler
package upgrade

import (
	"context"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/pkg/logger"
)

// readNotingVault is a waitVault that reports each path it is asked for.
type readNotingVault struct {
	*waitVault

	reads chan string
}

func (v *readNotingVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	found, err := v.waitVault.Get(ctx, path, result)
	v.reads <- path

	return found, err
}

func (v *readNotingVault) Delete(context.Context, string) error { return nil }

func TestSettleRequestsRescanOnlyWhenDiverged(t *testing.T) {
	t.Parallel()

	const (
		diverged = "instance-diverged"
		clean    = "instance-clean"
	)

	vault := &readNotingVault{waitVault: newWaitVault(), reads: make(chan string, 64)}
	store := certinventory.NewDivergenceStore(vault, nil)

	err := store.Update(context.Background(), diverged, func(*certinventory.Divergence) (*certinventory.Divergence, error) {
		return &certinventory.Divergence{Variables: []certinventory.DivergentVariable{{
			Name: "/ws-bosh/redis-diverged/redis_crt", AppliedVersionID: "v2", State: certinventory.DivergenceUnconverged,
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for len(vault.reads) > 0 {
		<-vault.reads
	}

	var (
		mu      sync.Mutex
		scanned []string
	)

	rescans := make(chan string, 4)

	manager := NewManager(&logger.NoOpLogger{}, nil, newWaitVault())

	err = manager.Configure(Deps{
		Divergence: store,
		ScanInstance: func(_ context.Context, id string) (certinventory.InstanceScan, error) {
			mu.Lock()
			scanned = append(scanned, id)
			mu.Unlock()

			rescans <- id

			return certinventory.InstanceScan{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	finished := &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	for _, id := range []string{clean, diverged} {
		task := runningUpgrade()
		task.Instances[0].InstanceID = id
		manager.settleInstance(task, &task.Instances[0], waitDeployTaskID, finished)
	}

	select {
	case id := <-rescans:
		if id != diverged {
			t.Fatalf("rescanned %s, want %s", id, diverged)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("settling a diverged instance asked for no rescan")
	}

	// Once both records were read, Stop waits for the goroutines that read
	// them, so every rescan they would ask for has been asked for.
	wantReads := map[string]bool{"certificate-divergence/" + clean: true, "certificate-divergence/" + diverged: true}
	deadline := time.After(5 * time.Second)

	for len(wantReads) > 0 {
		select {
		case path := <-vault.reads:
			delete(wantReads, path)
		case <-deadline:
			t.Fatalf("records never read: %v", wantReads)
		}
	}

	manager.Stop()

	mu.Lock()
	defer mu.Unlock()

	if len(scanned) != 1 || scanned[0] != diverged {
		t.Errorf("rescans %v, want only %s", scanned, diverged)
	}

	quiet := NewManager(&logger.NoOpLogger{}, nil, newWaitVault())
	task := runningUpgrade()
	quiet.settleInstance(task, &task.Instances[0], waitDeployTaskID, finished)
	quiet.Stop()
}
