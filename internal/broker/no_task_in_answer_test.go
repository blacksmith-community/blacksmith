package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// emptyAnswerDirectorAddress is the director address the broker's
// configuration names in these tests.
const emptyAnswerDirectorAddress = "https://10.0.0.6:25555"

// emptyAnswerBroker returns a broker whose configuration names
// emptyAnswerDirectorAddress, over director and vault.
func emptyAnswerBroker(director bosh.Director, vault *testutil.VaultDevServer) *broker.Broker {
	cfg := &config.Config{}
	cfg.BOSH.Address = emptyAnswerDirectorAddress

	instance := &broker.Broker{
		BOSH:          director,
		Shield:        &shield.NoopClient{},
		Config:        cfg,
		InstanceLocks: make(map[string]*sync.Mutex),
	}

	if vault != nil {
		instance.Vault = internalVault.New(vault.Addr, vault.RootToken, true)
	}

	return instance
}

// The unknown outcome of a mutation the director accepted with no task in
// its answer names the director the broker sent it to.
func TestNoTaskInAnswerNamesTheDirector(t *testing.T) {
	t.Parallel()

	err := emptyAnswerBroker(nil, nil).NoTaskInAnswer("CreateDeployment", "dep-1", "ctx-1")

	var unknown *bosh.OutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.Director != emptyAnswerDirectorAddress {
		t.Fatalf("expected an unknown outcome that names director %s, got %#v", emptyAnswerDirectorAddress, err)
	}

	message := err.Error()
	if !strings.Contains(message, "CreateDeployment on BOSH director "+emptyAnswerDirectorAddress+" got no answer") {
		t.Errorf("expected the message to name the director, got: %s", message)
	}

	if strings.Contains(message, "director  got") {
		t.Errorf("expected no gap where the director belongs, got: %s", message)
	}
}

// emptyResendDirector refuses the first delete and answers every resend
// with neither a task nor an error, and records each delete's context ID.
// The deployment exists, and no task runs on it.
type emptyResendDirector struct {
	bosh.Director

	mu      sync.Mutex
	deletes []string
}

func (d *emptyResendDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
}

func (d *emptyResendDirector) FindRunningTasksForDeployment(context.Context, string) ([]bosh.Task, error) {
	return []bosh.Task{}, nil
}

func (d *emptyResendDirector) DeleteDeployment(ctx context.Context, _ string) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.deletes = append(d.deletes, boshapi.ContextIDFrom(ctx))

	if len(d.deletes) == 1 {
		return nil, fmt.Errorf("delete: %w", &boshapi.APIError{
			StatusCode: http.StatusBadRequest, Method: http.MethodDelete, Path: "/deployments/dep-1", Code: 440001,
			Description: "director refused the delete", Mutation: true,
		})
	}

	return nil, nil //nolint:nilnil // the director's answer named no task
}

func (d *emptyResendDirector) sent() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.deletes...)
}

// A resent delete whose answer named no task is an unknown outcome that
// names the resend's own context ID, not the first delete's.
//
//nolint:paralleltest // it shortens the delete retry backoff, a package variable
func TestEmptyDeleteAnswerNamesTheContextIDOfItsAttempt(t *testing.T) {
	vault, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	defer vault.Close()
	defer broker.SetDeleteRetryBackoff(time.Millisecond)()

	director := &emptyResendDirector{}
	firstID := uuid.NewString()
	instanceID := "empty-resend-" + uuid.NewString()

	err = emptyAnswerBroker(director, vault).DeleteDeploymentWithRetry(context.Background(), instanceID, "dep-1", firstID)

	deletes := director.sent()
	if len(deletes) != 2 || deletes[0] != firstID || deletes[1] == "" || deletes[1] == firstID {
		t.Fatalf("expected the first delete under %s and one resend under a fresh context ID, got %q", firstID, deletes)
	}

	var unknown *bosh.OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected an unknown outcome, got %v", err)
	}

	if unknown.ContextID != deletes[1] {
		t.Errorf("expected the unknown outcome to name the resend's context ID %s, got %s", deletes[1], unknown.ContextID)
	}

	if unknown.Director != emptyAnswerDirectorAddress {
		t.Errorf("expected the unknown outcome to name director %s, got %q", emptyAnswerDirectorAddress, unknown.Director)
	}
}
