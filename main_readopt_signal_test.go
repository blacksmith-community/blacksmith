package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// Re-adoption retries until Vault answers, so a broker whose Vault is down
// waits in it at boot. A stop sent then ends the wait, and the broker
// exits as a stop the supervisor asked for, with code 0.
//
//nolint:paralleltest // the test signals its own process, so nothing else may run alongside it
func TestReadoptServiceOperationsStopsOnSIGTERM(t *testing.T) {
	// The test's own registration keeps the signal from ending the test
	// process when re-adoption does not listen for it.
	held := make(chan os.Signal, 1)
	signal.Notify(held, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(held) })

	asked := make(chan struct{})

	var askOnce sync.Once

	vaultServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		askOnce.Do(func() { close(asked) })

		http.Error(writer, `{"errors":["Vault is sealed"]}`, http.StatusBadRequest)
	}))
	t.Cleanup(vaultServer.Close)

	director := testutil.NewScriptedBOSHDirector()
	pool := bosh.NewPooledDirector(director, 1, time.Second, loggerPkg.CreateSilentLogger())
	brokerInstance := &broker.Broker{BOSH: director, Vault: internalVault.New(vaultServer.URL, "test-token", true)}
	done := make(chan struct{})

	var stopped error

	go func() {
		defer close(done)

		stopped = readoptServiceOperations(context.Background(), brokerInstance, pool, newRecordingLogger())
	}()

	select {
	case <-asked:
	case <-time.After(backfillStopWithin):
		t.Fatal("re-adoption never asked Vault for the instances")
	}

	err := syscall.Kill(os.Getpid(), syscall.SIGTERM)
	if err != nil {
		t.Fatalf("could not signal the test process: %v", err)
	}

	select {
	case <-done:
	case <-time.After(backfillStopWithin):
		t.Fatalf("re-adoption still waits on Vault %v after SIGTERM", backfillStopWithin)
	}

	if !errors.Is(stopped, errStoppedBeforeServing) {
		t.Errorf("re-adoption returned %v after SIGTERM, want an error wrapping errStoppedBeforeServing", stopped)
	}

	if code := serviceExitCode(stopped, newRecordingLogger()); code != 0 {
		t.Errorf("a broker stopped by SIGTERM during re-adoption exits with %d, want 0", code)
	}
}
