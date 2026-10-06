package cf

import (
	"context"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/interfaces"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	cfservices "blacksmith/pkg/services/cf"
	"blacksmith/pkg/testutil"
)

// blockingOperations holds a registration open until release is closed, then
// reports one progress record and finishes.
type blockingOperations struct {
	release chan struct{}
}

func (ops *blockingOperations) PerformRegistration(_ *cfservices.RegistrationRequest, progressChan chan<- cfservices.RegistrationProgress) error {
	<-ops.release

	progressChan <- cfservices.RegistrationProgress{
		Step:      "register-broker",
		Status:    "completed",
		Message:   "broker registered",
		Timestamp: time.Now(),
	}

	close(progressChan)

	return nil
}

// The registration runs after the handler has answered and the request
// context has ended, and its status and progress still have to land.
func TestAsyncRegistrationSurvivesTheRequestContext(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start the in-process Vault: %v", err)
	}

	vault := internalVault.New(server.Addr, server.RootToken, true)
	log := logger.Get().Named("cf-registration-context-test")
	handler := NewHandler(log, nil, nil, vault)

	const registrationID = "reg-context-test"

	err = vault.SaveCFRegistration(context.Background(), map[string]interface{}{
		"id":     registrationID,
		"name":   "context-test",
		"status": "pending",
	})
	if err != nil {
		t.Fatalf("failed to seed the registration: %v", err)
	}

	requestCtx, endRequest := context.WithCancel(context.Background())
	operations := &blockingOperations{release: make(chan struct{})}

	handler.startAsyncRegistration(httptest.NewRecorder(), requestCtx, log, registrationID, &cfservices.RegistrationRequest{ID: registrationID}, operations)

	// The handler has answered, so the server cancels the request context.
	endRequest()
	close(operations.release)

	deadline := time.Now().Add(15 * time.Second)

	for {
		var registration map[string]interface{}

		_, getErr := vault.GetCFRegistration(context.Background(), registrationID, &registration)
		if getErr == nil && registration["status"] == "active" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("registration never reached active after the request context ended; last read %v (err %v)", registration, getErr)
		}

		time.Sleep(50 * time.Millisecond)
	}

	var progress map[string]interface{}

	for {
		progress, err = server.ReadSecret("secret/blacksmith/registrations/" + registrationID + "/progress")
		if err == nil {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("the progress record was not stored after the request context ended: %v", err)
		}

		time.Sleep(50 * time.Millisecond)
	}

	if progress["step"] != "register-broker" {
		t.Fatalf("the stored progress has step %v, want register-broker", progress["step"])
	}
}

// orderingVault records the status writes of a registration, and the writes
// that had landed when the registration began. Its write of "registering"
// yields until the registration has begun, so a goroutine that was started
// before the write gets every chance to get ahead of it. The wait is counted in
// yields and not in time, and it ends at once when the write comes first.
type orderingVault struct {
	interfaces.Vault

	mu          sync.Mutex
	statuses    []string
	seenAtStart []string
	started     atomic.Bool
	finished    chan struct{}
}

func (v *orderingVault) UpdateCFRegistrationStatus(_ context.Context, _, status, _ string) error {
	if status == "registering" {
		const maxYields = 20000

		for yields := 0; yields < maxYields && !v.started.Load(); yields++ {
			runtime.Gosched()
		}
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	v.statuses = append(v.statuses, status)

	if status == "active" || status == "failed" {
		close(v.finished)
	}

	return nil
}

func (v *orderingVault) snapshot() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	return append([]string(nil), v.statuses...)
}

// orderingOperations takes its snapshot of the recorded statuses before it
// says it has begun, so a "registering" write that is still to come cannot be
// counted as having come first.
type orderingOperations struct {
	vault *orderingVault
}

func (ops *orderingOperations) PerformRegistration(_ *cfservices.RegistrationRequest, progressChan chan<- cfservices.RegistrationProgress) error {
	snapshot := ops.vault.snapshot()

	ops.vault.mu.Lock()
	ops.vault.seenAtStart = snapshot
	ops.vault.mu.Unlock()

	ops.vault.started.Store(true)

	close(progressChan)

	return nil
}

// A registration that finishes at once writes "active" or "failed" while a
// "registering" write could still be on its way. The write has to come first,
// or it overwrites the final status.
func TestRegistrationIsRegisteringBeforeItBegins(t *testing.T) {
	t.Parallel()

	vault := &orderingVault{finished: make(chan struct{})}
	log := logger.Get().Named("cf-registration-order-test")
	handler := NewHandler(log, nil, nil, vault)
	handler.persistProgress = nil

	const registrationID = "reg-order-test"

	handler.startAsyncRegistration(httptest.NewRecorder(), context.Background(), log, registrationID, &cfservices.RegistrationRequest{ID: registrationID}, &orderingOperations{vault: vault})

	select {
	case <-vault.finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("the registration never reached a final status; statuses written: %v", vault.snapshot())
	}

	vault.mu.Lock()
	seenAtStart := vault.seenAtStart
	vault.mu.Unlock()

	if want := []string{"registering"}; !reflect.DeepEqual(seenAtStart, want) {
		t.Errorf("the registration began with the statuses %v written, want %v", seenAtStart, want)
	}

	if want := []string{"registering", "active"}; !reflect.DeepEqual(vault.snapshot(), want) {
		t.Errorf("the statuses were written in the order %v, want %v", vault.snapshot(), want)
	}
}
