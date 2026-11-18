package broker_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The tasks of an instance whose update failed: the create task of its
// original provision, which also failed before an operator repaired the
// deployment, and the task of the update's deploy.
const (
	updateFailureAction       = "update"
	updateFailureCreateTaskID = 189
	updateFailureUpdateTaskID = 501
	updateFailureContextID    = "update-failure-context"
	updateFailureWaitBound    = 30 * time.Second
)

// updateFailure is an indexed instance on a director where every task has
// failed and the deployment exists.
type updateFailure struct {
	instanceID     string
	deploymentName string
	vault          *internalVault.Vault
	director       *testutil.ScriptedBOSHDirector
	broker         *broker.Broker
}

// failedTask is the director's answer for a task of the deployment, which
// has failed.
func failedTask(taskID int, deploymentName string) *bosh.Task {
	return &bosh.Task{ID: taskID, State: outcomeTaskError, Description: "deploy " + deploymentName}
}

// newUpdateFailure indexes an instance for the named path and returns it
// with a broker and a director whose tasks have all failed.
func newUpdateFailure(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, path string) *updateFailure {
	t.Helper()

	instanceID := "update-failure-" + path
	deploymentName := standalonePlanID + "-" + instanceID

	err := vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
		ID: instanceID, ServiceID: valkeyServiceID, PlanID: standalonePlanID, DeploymentName: deploymentName,
	})
	if err != nil {
		t.Fatalf("failed to index the instance: %v", err)
	}

	director := testutil.NewScriptedBOSHDirector()
	director.GetTaskFn = func(taskID int) (*bosh.Task, error) { return failedTask(taskID, deploymentName), nil }
	director.WaitForTaskFn = director.GetTaskFn
	director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) {
		return failedTask(updateFailureUpdateTaskID, deploymentName), nil
	}
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
	director.GetEventsFn = func(string) ([]bosh.Event, error) {
		return append(deploymentEvents(updateFailureAction, deploymentName, updateFailureUpdateTaskID),
			deploymentEvents(boshEventCreate, deploymentName, updateFailureCreateTaskID)...), nil
	}
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
	}
	director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: updateFailureUpdateTaskID + 1, State: outcomeTaskQueued}, nil
	}

	plan := services.Plan{
		ID:      standalonePlanID,
		Name:    standalonePlanID,
		Type:    valkeyServiceID,
		Service: &services.Service{ID: valkeyServiceID, Name: valkeyServiceID},
	}

	return &updateFailure{
		instanceID:     instanceID,
		deploymentName: deploymentName,
		vault:          vaultClient,
		director:       director,
		broker: &broker.Broker{
			Plans:          map[string]services.Plan{valkeyServiceID + "/" + standalonePlanID: plan},
			BOSH:           director,
			Vault:          vaultClient,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: t.Context(),
		},
	}
}

// recordUpdateTask records the update as running in its deploy task.
func recordUpdateTask(ctx context.Context, t *testing.T, f *updateFailure) {
	t.Helper()

	err := f.vault.TrackProgress(ctx, f.instanceID, updateFailureAction,
		"BOSH deployment in progress (task "+strconv.Itoa(updateFailureUpdateTaskID)+")", updateFailureUpdateTaskID, nil)
	if err != nil {
		t.Fatalf("failed to record the update: %v", err)
	}
}

// recordLostUpdate records an update whose deploy answer was lost longer
// ago than the grace period.
func recordLostUpdate(ctx context.Context, t *testing.T, f *updateFailure) {
	t.Helper()

	err := f.vault.TrackOutcomeUnknown(ctx, f.instanceID, updateFailureAction, "The update's answer was lost",
		updateFailureContextID, time.Now().Add(-bosh.UnknownOutcomeGrace-time.Minute))
	if err != nil {
		t.Fatalf("failed to record the unknown outcome: %v", err)
	}
}

// recordNothing leaves the instance with no record.
func recordNothing(context.Context, *testing.T, *updateFailure) {}

// updateFailurePath is a route into the cleanup of a failed deployment.
// It records what its instance holds and then runs.
type updateFailurePath struct {
	name   string
	action string
	record func(ctx context.Context, t *testing.T, f *updateFailure)
	run    func(ctx context.Context, t *testing.T, f *updateFailure)
}

// current reads the record the way a poll or re-adoption reads it.
func (f *updateFailure) current(ctx context.Context, t *testing.T) broker.RecordedOperation {
	t.Helper()

	recorded, err := f.broker.RecordedOperation(ctx, f.instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	return recorded
}

// poll runs a last_operation poll with the given operation data.
func (f *updateFailure) poll(ctx context.Context, operation string) {
	_, _ = f.broker.LastOperation(ctx, f.instanceID, osbapi.LastOperationRequest{
		ServiceID: valkeyServiceID, PlanID: standalonePlanID, Operation: operation,
	})
}

// requireNothingDeleted waits for the broker's background work and fails
// the test when the deployment was deleted or the record no longer holds
// the action it held before.
func (f *updateFailure) requireNothingDeleted(ctx context.Context, t *testing.T, action string) {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, updateFailureWaitBound)
	defer cancel()

	f.broker.WaitForBackground(waitCtx)

	if calls := f.director.Calls(directorDeleteDeployment); len(calls) != 0 {
		t.Errorf("the failed deploy deleted deployment %v", calls)
	}

	recorded := f.current(ctx, t)
	if recorded.Action != action {
		t.Errorf("the record now holds a %q with state %q, want the %q left on record", recorded.Action, recorded.State, action)
	}
}

// updateFailurePaths returns the routes into the cleanup of a failed
// deployment.
func updateFailurePaths() []updateFailurePath {
	return []updateFailurePath{
		{"readopt after a restart", updateFailureAction, recordUpdateTask, func(ctx context.Context, t *testing.T, f *updateFailure) {
			t.Helper()

			_, _ = f.broker.SettleReadoptedTask(ctx, f.instanceID, f.deploymentName, f.current(ctx, t))
		}},
		{"lost deploy answer", updateFailureAction, recordLostUpdate, func(ctx context.Context, t *testing.T, f *updateFailure) {
			t.Helper()

			_, _ = f.broker.ResolveUnknownOutcome(ctx, f.instanceID, f.deploymentName, f.current(ctx, t))
		}},
		{"last_operation poll with no operation data", updateFailureAction, recordUpdateTask, func(ctx context.Context, t *testing.T, f *updateFailure) {
			t.Helper()

			f.poll(ctx, "")
		}},
		{"last_operation poll with update operation data", updateFailureAction, recordUpdateTask, func(ctx context.Context, t *testing.T, f *updateFailure) {
			t.Helper()

			f.poll(ctx, updateFailureAction)
		}},
		// An instance the reconciler adopted has no record at all, so nothing
		// says its deployment came from a provision this broker accepted.
		{"last_operation poll over no record", "", recordNothing, func(ctx context.Context, t *testing.T, f *updateFailure) {
			t.Helper()

			f.poll(ctx, "")
		}},
	}
}

// A failed update leaves a deployment that holds a tenant's data, so no
// path that settles a failed deploy may delete it. Each route into the
// cleanup of a failed deployment runs here over a record that holds an
// update whose deploy failed: re-adoption after a restart, the resolver of
// a lost deploy answer, and a last_operation poll. A poll over an instance
// with no record deletes nothing either.
func TestFailedUpdate_DeletesNoDeployment(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	// One Vault value serves every subtest, because the index lock
	// belongs to a Vault value.
	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	for index, path := range updateFailurePaths() {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			failure := newUpdateFailure(ctx, t, vaultClient, strconv.Itoa(index))

			path.record(ctx, t, failure)
			path.run(ctx, t, failure)

			failure.requireNothingDeleted(ctx, t, path.action)
		})
	}
}
