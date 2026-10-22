package upgrade_test

import (
	"context"
	"sync"
	"testing"
	"time"

	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/pkg/logger"
)

// indexWatchVault holds the manager's first read of the upgrade tasks index
// until the test lets it go, then records whether the manager was configured
// at that moment.
type indexWatchVault struct {
	*memoryVault

	release chan struct{}
	seen    chan struct{}
	once    sync.Once
	handler func() bool
	result  bool
}

func (v *indexWatchVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if path == "upgrade-tasks" {
		v.once.Do(func() {
			<-v.release

			v.result = v.handler()

			close(v.seen)
		})
	}

	return v.memoryVault.Get(ctx, path, result)
}

func TestHandlerConfiguresBeforeLoad(t *testing.T) {
	t.Parallel()

	vault := &indexWatchVault{
		memoryVault: newMemoryVault(),
		release:     make(chan struct{}),
		seen:        make(chan struct{}),
	}

	var handler *upgradehandler.Handler

	vault.handler = func() bool { return handler.GetManager().Configured() }

	handler = upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: nil,
		Vault:    vault,
	})
	t.Cleanup(handler.Stop)

	close(vault.release)

	select {
	case <-vault.seen:
	case <-time.After(loadTimeout):
		t.Fatal("the manager never read the tasks index")
	}

	if !vault.result {
		t.Fatal("the manager read the stored tasks before its dependencies were set")
	}
}
