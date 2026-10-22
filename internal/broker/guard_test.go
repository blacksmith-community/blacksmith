package broker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

const (
	guardServiceID = "valkey"
	guardPlanID    = "standalone"
	guardWait      = 10 * time.Second
	guardDeleteID  = 931
)

// errGuardDeleteAnswerUnclear is a failed delete that does not show the
// director started no task, so the broker does not send it again.
var errGuardDeleteAnswerUnclear = errors.New("the director's answer to the delete could not be read")

// guardedBroker builds a broker over the director and Vault that reserves
// instances in guard.
func guardedBroker(t *testing.T, director bosh.Director, vault *internalVault.Vault, guard *mutation.Guard) *broker.Broker {
	t.Helper()

	serviceCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)

	return &broker.Broker{
		Plans: map[string]services.Plan{guardServiceID + "/" + guardPlanID: {
			ID:      guardPlanID,
			Name:    guardPlanID,
			Type:    guardServiceID,
			Service: &services.Service{ID: guardServiceID, Name: guardServiceID},
		}},
		BOSH:           director,
		Vault:          vault,
		Shield:         &shield.NoopClient{},
		Config:         &config.Config{},
		InstanceLocks:  make(map[string]*sync.Mutex),
		ServiceContext: serviceCtx,
		Guard:          guard,
	}
}

// waitUntil fails the test when done does not turn true within guardWait.
func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(guardWait)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestProvisionBlockedByRotation(t *testing.T) {
	t.Parallel()

	const instanceID = "rotating-1"

	deployment := guardPlanID + "-" + instanceID

	for name, holds := range map[string]struct{ instanceID, deployment string }{
		"rotation holds the instance":            {instanceID, deployment},
		"deployment action holds the deployment": {"", deployment},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			guard := mutation.NewGuard(time.Now)

			release, err := guard.Acquire(holds.instanceID, holds.deployment, mutation.Holder{
				Kind:        mutation.KindCertificates,
				Description: "certificate rotation of " + instanceID,
			})
			if err != nil {
				t.Fatalf("holding the guard: %v", err)
			}
			defer release()

			director := testutil.NewScriptedBOSHDirector()
			brokerInstance := guardedBroker(t, director, nil, guard)

			_, async, err := brokerInstance.Provision(t.Context(), instanceID, osbapi.ProvisionRequest{
				ServiceID: guardServiceID, PlanID: guardPlanID, OrganizationGUID: "org", SpaceGUID: "space",
			}, true)
			if !errors.Is(err, osbapi.ErrConcurrencyError) || async {
				t.Fatalf("Provision = async %v, err %v, want a ConcurrencyError", async, err)
			}

			if !strings.Contains(err.Error(), "certificate rotation of "+instanceID) {
				t.Fatalf("Provision error %q does not name the holder", err)
			}

			_, async, err = brokerInstance.Deprovision(t.Context(), instanceID, osbapi.DeprovisionRequest{
				ServiceID: guardServiceID, PlanID: guardPlanID,
			}, true)
			if !errors.Is(err, osbapi.ErrConcurrencyError) || async {
				t.Fatalf("Deprovision = async %v, err %v, want a ConcurrencyError", async, err)
			}

			if brokerInstance.OperationClaimed(instanceID) {
				t.Fatal("a refused operation kept its claim")
			}

			if calls := len(director.Calls("CreateDeployment")) + len(director.Calls("DeleteDeployment")); calls != 0 {
				t.Fatalf("the director got %d deploys or deletes, want none", calls)
			}

			holder, held := guard.Holder(holds.instanceID, holds.deployment)
			if !held || holder.Kind != mutation.KindCertificates {
				t.Fatalf("Holder = %+v, %v, want the rotation to keep its reservation", holder, held)
			}
		})
	}
}

// deprovisionFixture indexes an instance in a Vault of its own and scripts
// a director whose deployment exists.
type deprovisionFixture struct {
	vault      *internalVault.Vault
	director   *testutil.ScriptedBOSHDirector
	instanceID string
	deployment string
}

func newDeprovisionFixture(t *testing.T, instanceID string) *deprovisionFixture {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	fixture := &deprovisionFixture{
		vault:      internalVault.New(server.Addr, server.RootToken, true),
		director:   testutil.NewScriptedBOSHDirector(),
		instanceID: instanceID,
		deployment: guardPlanID + "-" + instanceID,
	}

	err = fixture.vault.Index(t.Context(), instanceID, map[string]interface{}{
		serviceIDKey:      guardServiceID,
		indexPlanIDKey:    guardPlanID,
		deploymentNameKey: fixture.deployment,
	})
	if err != nil {
		t.Fatalf("indexing the instance: %v", err)
	}

	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name}, nil
	}
	fixture.director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
		return &bosh.Task{ID: taskID, State: outcomeTaskDone, Description: outcomeDeleteDeployment}, nil
	}
	fixture.director.WaitForTaskFn = fixture.director.GetTaskFn
	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return nil, nil //nolint:nilnil // no running task is a nil task without an error
	}

	return fixture
}

func (f *deprovisionFixture) deprovision(t *testing.T, brokerInstance *broker.Broker) (bool, error) {
	t.Helper()

	_, async, err := brokerInstance.Deprovision(t.Context(), f.instanceID, osbapi.DeprovisionRequest{
		ServiceID: guardServiceID, PlanID: guardPlanID,
	}, true)

	return async, err //nolint:wrapcheck // the test reads the broker's own answer
}

// The guard is freed before the claim is removed, so a request that finds
// the claim gone never finds the instance busy with the operation that
// just ended.
func TestReleaseOperationFreesGuardFirst(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)
	brokerInstance := guardedBroker(t, testutil.NewScriptedBOSHDirector(), nil, guard)

	if !brokerInstance.ClaimStoredWhenGuardFreed("release-order") {
		t.Fatal("the claim was removed before its reservation in the guard was freed")
	}

	if brokerInstance.OperationClaimed("release-order") {
		t.Fatal("the claim is still stored after it was given up")
	}

	if snapshot := guard.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("guard after the release = %v, want empty", snapshot)
	}
}

// waitUntilFree waits until the broker holds no claim on the instance and
// the guard holds nothing.
func waitUntilFree(t *testing.T, what string, brokerInstance *broker.Broker, guard *mutation.Guard, instanceID string) {
	t.Helper()

	waitUntil(t, what, func() bool {
		return !brokerInstance.OperationClaimed(instanceID) && len(guard.Snapshot()) == 0
	})
}

//nolint:paralleltest // each subtest runs an in-process vault
func TestDeprovisionReleasesGuardOnCompletion(t *testing.T) {
	t.Run("the deprovision finishes", deprovisionFinishesFreesGuard)
	t.Run("the deprovision fails", deprovisionFailureFreesGuard)
	t.Run("the delete fails after the deprovision is accepted", deleteFailureFreesGuard)
}

// deprovisionFinishesFreesGuard checks that the deprovision holds the guard
// while it sends its delete, and frees it once it ends.
func deprovisionFinishesFreesGuard(t *testing.T) {
	fixture := newDeprovisionFixture(t, "deprovision-done")
	guard := mutation.NewGuard(time.Now)

	deleting := make(chan struct{})
	proceed := make(chan struct{})

	var once sync.Once

	fixture.director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		once.Do(func() { close(deleting) })
		<-proceed

		return &bosh.Task{ID: guardDeleteID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
	}

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)

	async, err := fixture.deprovision(t, brokerInstance)
	if err != nil || !async {
		close(proceed)
		t.Fatalf("Deprovision = async %v, err %v, want accepted", async, err)
	}

	select {
	case <-deleting:
	case <-time.After(guardWait):
		close(proceed)
		t.Fatal("the deprovision never sent its delete")
	}

	holder, held := guard.Holder(fixture.instanceID, fixture.deployment)
	if !held || holder.Kind != mutation.KindDeprovision || holder.Description != "deprovision of "+fixture.instanceID {
		close(proceed)
		t.Fatalf("Holder while the delete is sent = %+v, %v, want the deprovision", holder, held)
	}

	close(proceed)

	waitUntilFree(t, "the deprovision gives its claim up and frees the guard", brokerInstance, guard, fixture.instanceID)
}

// deprovisionFailureFreesGuard checks that a deprovision whose accept chain
// fails frees the guard.
func deprovisionFailureFreesGuard(t *testing.T) {
	fixture := newDeprovisionFixture(t, "deprovision-failed")
	guard := mutation.NewGuard(time.Now)

	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return nil, errDirectorUnreachable
	}

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)

	_, err := fixture.deprovision(t, brokerInstance)
	if err == nil {
		t.Fatal("Deprovision succeeded, want the director's failure")
	}

	if snapshot := guard.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("guard after the failed deprovision = %v, want empty", snapshot)
	}
}

// deleteFailureFreesGuard checks that an accepted deprovision whose delete
// fails frees the guard.
func deleteFailureFreesGuard(t *testing.T) {
	fixture := newDeprovisionFixture(t, "deprovision-delete-failed")
	guard := mutation.NewGuard(time.Now)

	fixture.director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return nil, errGuardDeleteAnswerUnclear
	}

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)

	async, err := fixture.deprovision(t, brokerInstance)
	if err != nil || !async {
		t.Fatalf("Deprovision = async %v, err %v, want accepted", async, err)
	}

	waitUntilFree(t, "the deprovision gives its claim up and frees the guard", brokerInstance, guard, fixture.instanceID)
}

//nolint:paralleltest // the test changes the package's re-adoption bound
func TestReadoptHoldsGuard(t *testing.T) {
	defer broker.SetReadoptBound(300 * time.Millisecond)()

	fixture := newDeprovisionFixture(t, "readopt-guard")
	guard := mutation.NewGuard(time.Now)

	err := fixture.vault.TrackProgressWithDeployment(t.Context(), fixture.instanceID, "deprovision", "BOSH deletion in progress",
		"context-readopt-guard", fixture.deployment, guardDeleteID, nil)
	if err != nil {
		t.Fatalf("recording the deprovision: %v", err)
	}

	// The director can't be reached, so the re-adopted deprovision stays
	// in flight until its bound passes.
	fixture.director.GetTaskFn = func(int) (*bosh.Task, error) { return nil, errDirectorUnreachable }
	fixture.director.WaitForTaskFn = fixture.director.GetTaskFn

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)

	err = brokerInstance.ReadoptInFlightOperations(t.Context())
	if err != nil {
		t.Fatalf("ReadoptInFlightOperations: %v", err)
	}

	if !brokerInstance.DeprovisionActive(fixture.instanceID) {
		t.Fatal("re-adoption did not claim the deprovision")
	}

	holder, held := guard.Holder(fixture.instanceID, fixture.deployment)
	if !held || holder.Kind != mutation.KindDeprovision {
		t.Fatalf("Holder while re-adopted = %+v, %v, want the deprovision", holder, held)
	}

	_, err = guard.Acquire("", fixture.deployment, mutation.Holder{Kind: mutation.KindDeploymentAction, Description: "restart from the UI"})
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("a deployment action acquired the guard while re-adoption held it: %v", err)
	}

	waitUntilFree(t, "re-adoption gives its claim up and frees the guard", brokerInstance, guard, fixture.instanceID)
}

// A deprovision reserves the deployment its instance records, which can
// differ from the one the plan and the instance ID name, so a deployment
// action on the recorded deployment refuses it.
func TestDeprovisionReservesRecordedDeployment(t *testing.T) {
	t.Parallel()

	fixture := newDeprovisionFixture(t, "deprovision-recorded")
	fixture.deployment = "recorded-" + fixture.instanceID

	err := fixture.vault.Index(t.Context(), fixture.instanceID, map[string]interface{}{
		serviceIDKey:      guardServiceID,
		indexPlanIDKey:    guardPlanID,
		deploymentNameKey: fixture.deployment,
	})
	if err != nil {
		t.Fatalf("indexing the instance: %v", err)
	}

	guard := mutation.NewGuard(time.Now)

	release, err := guard.Acquire("", fixture.deployment, mutation.Holder{
		Kind:        mutation.KindDeploymentAction,
		Description: "restart from the UI",
	})
	if err != nil {
		t.Fatalf("holding the deployment: %v", err)
	}
	defer release()

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)

	async, err := fixture.deprovision(t, brokerInstance)
	if !errors.Is(err, osbapi.ErrConcurrencyError) || async {
		t.Fatalf("Deprovision = async %v, err %v, want a ConcurrencyError", async, err)
	}

	if !strings.Contains(err.Error(), "restart from the UI") {
		t.Fatalf("Deprovision error %q does not name the holder", err)
	}

	if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 {
		t.Fatalf("the director got deletes %v, want none", calls)
	}
}
