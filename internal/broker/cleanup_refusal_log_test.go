package broker_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// levelLogger records each line with the level it was logged at.
type levelLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newLevelLogger() *levelLogger {
	return &levelLogger{NoOpLogger: &logger.NoOpLogger{}}
}

func (l *levelLogger) Debug(msg string, args ...interface{})   { l.record("DEBUG", msg, args) }
func (l *levelLogger) Info(msg string, args ...interface{})    { l.record("INFO", msg, args) }
func (l *levelLogger) Warn(msg string, args ...interface{})    { l.record("WARNING", msg, args) }
func (l *levelLogger) Warning(msg string, args ...interface{}) { l.record("WARNING", msg, args) }
func (l *levelLogger) Error(msg string, args ...interface{})   { l.record("ERROR", msg, args) }

func (l *levelLogger) Named(string) logger.Logger { return l }

func (l *levelLogger) record(level, msg string, args []interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, level+" "+fmt.Sprintf(msg, args...))
}

// count returns how many lines were logged at level and hold part.
func (l *levelLogger) count(level, part string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	found := 0

	for _, line := range l.lines {
		if strings.HasPrefix(line, level+" ") && strings.Contains(line, part) {
			found++
		}
	}

	return found
}

// The sweep tries a refused cleanup again on its backoff, and the overview
// already shows the refusal. So a refusal is logged at Error only the
// first time and when its cause changes, and a repeat is logged lower.
func TestCleanupFailedDeployment_LogsARepeatedRefusalBelowError(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	const instanceID = "cleanup-refusal-log"

	own := standalonePlanID + "-" + instanceID
	other := standalonePlanID + "-another-instance"

	err = recordFailedMintedProvision(ctx, vaultClient, instanceID, standalonePlanID, other)
	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	cleaner := cleanupGuardBroker(director, vaultClient)
	log := newLevelLogger()

	const refusing = "Refusing to delete deployment "

	refuse := func(asked string, atError, lower int) {
		t.Helper()

		cleaner.CleanupFailedDeploymentLogged(ctx, instanceID, asked, log)

		if got := log.count("ERROR", refusing); got != atError {
			t.Fatalf("after asking for %s, %d refusals were logged at Error, want %d", asked, got, atError)
		}

		if got := log.count("WARNING", "refusing to delete deployment "); got != lower {
			t.Fatalf("after asking for %s, %d refusals were logged at Warning, want %d", asked, got, lower)
		}
	}

	refuse(other, 1, 0)
	refuse(other, 1, 1)
	refuse(other, 1, 2)

	// Asking for the instance's own name refuses for another cause, which
	// is logged at Error again, and its repeat is not.
	refuse(own, 2, 2)
	refuse(own, 2, 3)

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("a refused cleanup deleted %v", deleted)
	}

	requireMarkKept(ctx, t, cleaner, instanceID)

	if attention := cleaner.CleanupAttention(); len(attention) != 1 || attention[0].DeploymentName != own {
		t.Fatalf("the overview holds %+v, want one row for %s", attention, own)
	}
}
