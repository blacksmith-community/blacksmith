package broker_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

// monitoredRun is a deprovision whose delete monitor follows its delete
// task. finish lets the task end, and shutdown ends the broker's service
// context, as a broker shutdown does.
type monitoredRun struct {
	fixture  *deprovisionFixture
	guard    *mutation.Guard
	finish   func()
	shutdown func()
}

// monitoredDeprovision starts a deprovision whose delete task runs until
// finish is called, and waits until its claim is gone, which leaves only
// the delete monitor following the task.
func monitoredDeprovision(t *testing.T, instanceID string) monitoredRun {
	t.Helper()

	fixture := newDeprovisionFixture(t, instanceID)
	guard := mutation.NewGuard(time.Now)

	var finished atomic.Bool

	fixture.director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: guardDeleteID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
	}
	fixture.director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
		state := outcomeTaskProcessing
		if finished.Load() {
			state = outcomeTaskDone
		}

		return &bosh.Task{ID: taskID, State: state, Description: outcomeDeleteDeployment}, nil
	}
	fixture.director.WaitForTaskFn = fixture.director.GetTaskFn

	serviceCtx, shutdown := context.WithCancel(context.Background())
	t.Cleanup(shutdown)

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)
	brokerInstance.ServiceContext = serviceCtx

	async, err := fixture.deprovision(t, brokerInstance)
	if err != nil || !async {
		t.Fatalf("Deprovision = async %v, err %v, want accepted", async, err)
	}

	waitUntil(t, "the deprovision gives its claim up", func() bool { return !brokerInstance.OperationClaimed(fixture.instanceID) })
	waitUntil(t, "the delete monitor polls the task", func() bool { return len(fixture.director.Calls("GetTask")) > 0 })

	return monitoredRun{fixture: fixture, guard: guard, finish: func() { finished.Store(true) }, shutdown: shutdown}
}

// The deprovision keeps the instance reserved while its delete monitor
// follows the delete task, so an upgrade created then gets 409
// instance_busy, and the reservation is freed once the monitor ends.
//
//nolint:paralleltest // the test changes the package's task poll interval
func TestDeprovisionHoldsGuardThroughDeleteMonitor(t *testing.T) {
	defer broker.SetTaskPollInterval(10 * time.Millisecond)()

	run := monitoredDeprovision(t, "deprovision-monitored")
	fixture, guard, finish := run.fixture, run.guard, run.finish

	holder, held := guard.Holder(fixture.instanceID, fixture.deployment)
	if !held || holder.Kind != mutation.KindDeprovision {
		finish()
		t.Fatalf("Holder while the delete monitor runs = %+v, %v, want the deprovision", holder, held)
	}

	upgrades := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Director: fixture.director,
		Vault:    fixture.vault,
		Guard:    guard,
	})
	t.Cleanup(upgrades.Stop)

	select {
	case <-upgrades.GetManager().Loaded():
	case <-time.After(guardWait):
		finish()
		t.Fatal("the upgrade manager never loaded")
	}

	body := strings.NewReader(`{"instance_ids":["` + fixture.instanceID + `"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"}}`)
	recorder := httptest.NewRecorder()
	upgrades.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/tasks", body))

	if recorder.Code != http.StatusConflict {
		finish()
		t.Fatalf("upgrade create answered %d %s, want 409", recorder.Code, recorder.Body.String())
	}

	var answer map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &answer)
	if err != nil || answer["code"] != apierr.CodeInstanceBusy {
		finish()
		t.Fatalf("upgrade create body = %s, want code %s", recorder.Body.String(), apierr.CodeInstanceBusy)
	}

	finish()

	waitUntil(t, "the delete monitor frees the guard", func() bool { return len(guard.Snapshot()) == 0 })
}

// A delete monitor that a shutdown ends frees the reservation too.
//
//nolint:paralleltest // the test changes the package's task poll interval
func TestDeleteMonitorShutdownFreesGuard(t *testing.T) {
	defer broker.SetTaskPollInterval(10 * time.Millisecond)()

	run := monitoredDeprovision(t, "deprovision-shutdown")
	fixture, guard, finish, shutdown := run.fixture, run.guard, run.finish, run.shutdown

	defer finish()

	if _, held := guard.Holder(fixture.instanceID, fixture.deployment); !held {
		t.Fatal("the guard was freed before the delete monitor ended")
	}

	shutdown()

	waitUntil(t, "the stopped delete monitor frees the guard", func() bool { return len(guard.Snapshot()) == 0 })
}

// abandonedTeardown starts the teardown of an abandoned deployment from a
// provision that holds the instance in the guard, with a delete task that
// runs until finish is called, and waits until the delete monitor polls
// the task.
func abandonedTeardown(t *testing.T, instanceID string) monitoredRun {
	t.Helper()

	fixture := newDeprovisionFixture(t, instanceID)
	guard := mutation.NewGuard(time.Now)

	var finished atomic.Bool

	fixture.director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: guardDeleteID, State: outcomeTaskProcessing, Description: outcomeDeleteDeployment}, nil
	}
	fixture.director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
		state := outcomeTaskProcessing
		if finished.Load() {
			state = outcomeTaskDone
		}

		return &bosh.Task{ID: taskID, State: state, Description: outcomeDeleteDeployment}, nil
	}
	fixture.director.WaitForTaskFn = fixture.director.GetTaskFn

	release, err := guard.Acquire(instanceID, fixture.deployment, mutation.Holder{
		Kind:        mutation.KindProvision,
		Description: "provision of " + instanceID,
	})
	if err != nil {
		t.Fatalf("reserving %s for its provision: %v", instanceID, err)
	}

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)
	brokerInstance.TearDownAbandonedDeploymentHolding(t.Context(), instanceID, fixture.deployment, release)

	waitUntil(t, "the delete monitor polls the task", func() bool { return len(fixture.director.Calls("GetTask")) > 0 })

	return monitoredRun{fixture: fixture, guard: guard, finish: func() { finished.Store(true) }}
}

// The teardown of an abandoned deployment keeps the provision's
// reservation while its delete monitor follows the delete task, and frees
// it once the monitor ends.
//
//nolint:paralleltest // the test changes the package's task poll interval
func TestAbandonedTeardownHoldsGuardThroughDeleteMonitor(t *testing.T) {
	defer broker.SetTaskPollInterval(10 * time.Millisecond)()

	run := abandonedTeardown(t, "abandoned-monitored")
	fixture, guard, finish := run.fixture, run.guard, run.finish

	defer finish()

	holder, held := guard.Holder(fixture.instanceID, fixture.deployment)
	if !held || holder.Kind != mutation.KindProvision {
		t.Fatalf("Holder while the delete monitor runs = %+v, %v, want the provision", holder, held)
	}

	finish()

	waitUntil(t, "the delete monitor frees the guard", func() bool { return len(guard.Snapshot()) == 0 })
}

// A teardown whose delete fails frees the reservation when it returns,
// since no monitor follows a delete.
//
//nolint:paralleltest // each run starts an in-process vault
func TestAbandonedTeardownFreesGuardWhenTheDeleteFails(t *testing.T) {
	fixture := newDeprovisionFixture(t, "abandoned-delete-fails")
	guard := mutation.NewGuard(time.Now)

	fixture.director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return nil, errGuardDeleteAnswerUnclear
	}

	release, err := guard.Acquire(fixture.instanceID, fixture.deployment, mutation.Holder{
		Kind:        mutation.KindProvision,
		Description: "provision of " + fixture.instanceID,
	})
	if err != nil {
		t.Fatalf("reserving %s for its provision: %v", fixture.instanceID, err)
	}

	brokerInstance := guardedBroker(t, fixture.director, fixture.vault, guard)
	brokerInstance.TearDownAbandonedDeploymentHolding(t.Context(), fixture.instanceID, fixture.deployment, release)

	if holder, held := guard.Holder(fixture.instanceID, fixture.deployment); held {
		t.Fatalf("expected the failed teardown to free the guard, held by %+v", holder)
	}
}
