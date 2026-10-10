//nolint:testpackage // the tests load stored upgrades and read the manager's unexported state
package upgrade

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
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const (
	// loadedTaskID names the upgrade a test stores before the manager
	// loads it.
	loadedTaskID = "loaded-upgrade"
	// loadedContextID is the context ID a stored deploy carried.
	loadedContextID = "6b1f3c4e-0a7d-4c55-9a1e-3f2b8d9c0e11"
	// adoptedTaskID is the BOSH task a test director holds for a stored
	// deploy.
	adoptedTaskID = 7
	// adoptedDeployment is the deployment of that task.
	adoptedDeployment = "redis-adopted"
	// settleTimeout bounds a wait that the adapter serves, which reads the
	// task every 2 seconds.
	settleTimeout = 20 * time.Second
	// targetStemcellOS and targetStemcellVersion name the stemcell the
	// stored upgrades upgrade to.
	targetStemcellOS      = "ubuntu-noble"
	targetStemcellVersion = "1.2"
	// directorCredential is the test director's username and password.
	directorCredential = "admin"
)

var errIndexReadInTest = errors.New("vault is sealed")

// storedUpgrade returns a running upgrade with the instances given, as a
// broker that stopped mid-upgrade left it in Vault.
func storedUpgrade(taskID string, instances ...InstanceUpgrade) UpgradeTask {
	started := time.Now().Add(-time.Hour)

	return UpgradeTask{
		ID:             taskID,
		Status:         TaskStatusRunning,
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		Instances:      instances,
		TotalCount:     len(instances),
		CreatedAt:      started,
		StartedAt:      &started,
	}
}

// inFlight returns the instance waitInstanceID names, whose deploy was in
// flight when the broker stopped, in the status given, with the task ID and
// context ID given.
func inFlight(deployment string, status InstanceStatus, taskID int, contextID string) InstanceUpgrade {
	started := time.Now().Add(-time.Minute)

	return InstanceUpgrade{
		InstanceID:     waitInstanceID,
		DeploymentName: deployment,
		Status:         status,
		BOSHTaskID:     taskID,
		ContextID:      contextID,
		StartedAt:      &started,
	}
}

// storeUpgrade saves the upgrade in the vault and names it in the tasks
// index, the way the manager saves it.
func storeUpgrade(t *testing.T, vault interfaces.Vault, task UpgradeTask) {
	t.Helper()

	ctx := context.Background()

	err := vault.Put(ctx, upgradeTasksVaultPath+"/"+task.ID, task)
	if err != nil {
		t.Fatalf("storing the upgrade: %v", err)
	}

	index := map[string]interface{}{}

	_, err = vault.Get(ctx, upgradeTasksVaultPath, &index)
	if err != nil {
		t.Fatalf("reading the tasks index: %v", err)
	}

	index[task.ID] = map[string]interface{}{"status": task.Status, "created_at": task.CreatedAt.Unix()}

	err = vault.Put(ctx, upgradeTasksVaultPath, index)
	if err != nil {
		t.Fatalf("storing the tasks index: %v", err)
	}
}

// loadManager starts a manager over the director and the vault, which
// loads the upgrades stored in the vault, and waits until the load has
// read them. The manager stops when the test ends.
func loadManager(t *testing.T, director bosh.Director, vault interfaces.Vault) *Manager {
	t.Helper()

	manager := NewManager(&logger.NoOpLogger{}, director, vault)
	t.Cleanup(manager.Stop)

	waitForLoad(t, manager)

	return manager
}

// waitForLoad waits until the manager has read the stored upgrades.
func waitForLoad(t *testing.T, manager *Manager) {
	t.Helper()

	select {
	case <-manager.loaded:
	case <-time.After(waitTimeout):
		t.Fatal("the manager never finished loading the stored upgrades")
	}
}

// loadedTask returns a task with the ID of the stored upgrade, for the
// helpers that read the manager's own copy by ID.
func loadedTask() *UpgradeTask { return &UpgradeTask{ID: loadedTaskID} }

// eventuallyWithin is eventually with its own deadline.
func eventuallyWithin(t *testing.T, manager *Manager, taskID string, within time.Duration, what string, done func(UpgradeTask) bool) UpgradeTask {
	t.Helper()

	deadline := time.Now().Add(within)

	for {
		manager.mu.RLock()
		live, exists := manager.tasks[taskID]

		var current UpgradeTask
		if exists {
			current = *copyTask(live)
		}

		manager.mu.RUnlock()

		if exists && done(current) {
			return current
		}

		if time.Now().After(deadline) {
			t.Fatalf("%s never happened; the upgrade task is %+v", what, current)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// taskStateDirector starts a test director that answers each read of the
// adopted task with the next answer of answers, repeating the last, and
// returns an adapter over it.
func taskStateDirector(t *testing.T, answers ...func(writer http.ResponseWriter)) bosh.Director {
	t.Helper()

	var (
		readsMu sync.Mutex
		reads   int
	)

	director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != fmt.Sprintf("/tasks/%d", adoptedTaskID) {
			return false
		}

		readsMu.Lock()
		answer := answers[min(reads, len(answers)-1)]
		reads++
		readsMu.Unlock()

		answer(writer)

		return true
	})

	adapter, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  director.URL,
		Username: directorCredential,
		Password: directorCredential,
		CACert:   director.CACert,
	})
	if err != nil {
		t.Fatalf("creating the director adapter: %v", err)
	}

	return adapter
}

// answerTask answers a read of the adopted task in the state given.
func answerTask(state string) func(writer http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
			"id":          adoptedTaskID,
			"state":       state,
			"description": "create deployment",
			"timestamp":   1759766400,
			"started_at":  1759766000,
			"result":      "",
			"user":        directorCredential,
			"deployment":  adoptedDeployment,
			"context_id":  loadedContextID,
		})
	}
}

// expectSettledAsSuccess fails the test unless the loaded instance
// settled as a success from the task it carried, counted once.
func expectSettledAsSuccess(t *testing.T, done UpgradeTask, taskID int) {
	t.Helper()

	instance := done.Instances[0]
	if instance.Status != InstanceStatusSuccess || instance.BOSHTaskID != taskID || instance.CompletedAt == nil {
		t.Fatalf("expected the instance to settle as a success from BOSH task %d, got %+v", taskID, instance)
	}

	if done.CompletedCount != 1 || done.FailedCount != 0 || done.UnknownCount != 0 {
		t.Fatalf("expected one success and nothing else counted, got %+v", done)
	}
}

// replaceAwait replaces the lookup by context ID with lookup, and records
// the time each lookup's grace starts from.
func replaceAwait(t *testing.T, lookup func(ctx context.Context) (*bosh.Task, error)) func() []time.Time {
	t.Helper()

	var (
		sinceMu sync.Mutex
		since   []time.Time
	)

	original := awaitTaskByContextID

	t.Cleanup(func() { awaitTaskByContextID = original })

	awaitTaskByContextID = func(ctx context.Context, _ bosh.Director, _ string, from time.Time) (*bosh.Task, error) {
		sinceMu.Lock()

		since = append(since, from)

		sinceMu.Unlock()

		return lookup(ctx)
	}

	return func() []time.Time {
		sinceMu.Lock()
		defer sinceMu.Unlock()

		return append([]time.Time(nil), since...)
	}
}

// noTaskWithTheContextID is a lookup whose grace passed with no task.
func noTaskWithTheContextID(context.Context) (*bosh.Task, error) {
	return nil, fmt.Errorf("%w %s, %v after the answer was lost", bosh.ErrNoTaskWithContextID, loadedContextID, bosh.UnknownOutcomeGrace)
}

// busyDirector is a waitDirector that reports the task running on a
// deployment, or none when running is nil.
type busyDirector struct {
	*waitDirector

	running *bosh.Task
	asked   chan string
}

func (d *busyDirector) FindRunningTaskForDeployment(_ context.Context, deployment string) (*bosh.Task, error) {
	select {
	case d.asked <- deployment:
	default:
	}

	if d.running == nil {
		return nil, nil //nolint:nilnil // no running task is an answer
	}

	running := *d.running

	return &running, nil
}

// failingIndexVault is a waitVault whose first reads of the tasks index
// fail.
type failingIndexVault struct {
	*waitVault

	mu       sync.Mutex
	failures int
	reads    int
}

func (v *failingIndexVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	if path == upgradeTasksVaultPath {
		v.mu.Lock()
		v.reads++
		fail := v.reads <= v.failures
		v.mu.Unlock()

		if fail {
			return false, fmt.Errorf("read the tasks index: %w", errIndexReadInTest)
		}
	}

	return v.waitVault.Get(ctx, path, result)
}

func (v *failingIndexVault) indexReads() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.reads
}

//nolint:paralleltest // the adapter's task reads take seconds
func TestLoadSettlesAnInstanceInterruptedAtShutdown(t *testing.T) {
	adapter := taskStateDirector(t, answerTask(waitStateRunning), answerTask(waitStateRunning), answerTask(boshTaskDone))
	batch := newTestBatchDirector(t, adapter)
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID)))

	manager := loadManager(t, batch, vault)

	if active, _, _ := batch.GetBatchPoolStats(); active != 1 {
		t.Fatalf("expected the loaded deploy to count against the batch, got %d active", active)
	}

	done := eventuallyWithin(t, manager, loadedTaskID, settleTimeout, "the interrupted instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusInterrupted
	})

	expectSettledAsSuccess(t, done, adoptedTaskID)

	saved := eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool {
		return saved.Instances[0].Status == InstanceStatusSuccess
	})
	if saved.CompletedCount != 1 {
		t.Fatalf("expected the saved upgrade to count the success, got %+v", saved)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestLoadSettlesARunningInstanceWhoseShutdownWriteWasLost(t *testing.T) {
	director := newWaitDirector()
	batch := newTestBatchDirector(t, director)
	vault := newWaitVault()

	// The shutdown's save of interrupted never reached Vault, so the
	// instance is still stored running.
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusRunning, waitDeployTaskID, loadedContextID)))

	manager := loadManager(t, batch, vault)

	waitForTheDeployWait(t, director)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := eventually(t, manager, loadedTask(), "the running instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	expectSettledAsSuccess(t, done, waitDeployTaskID)
}

//nolint:paralleltest // the settle waits on a shared test director
func TestLoadWaitsDirectlyWhenTheDirectorCannotAdopt(t *testing.T) {
	// The waitDirector has no Adopt, as the plain director the upgrade
	// handler falls back to has none.
	director := newWaitDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusInterrupted, waitDeployTaskID, loadedContextID)))

	manager := loadManager(t, director, vault)

	waitForTheDeployWait(t, director)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := eventually(t, manager, loadedTask(), "the interrupted instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusInterrupted
	})

	expectSettledAsSuccess(t, done, waitDeployTaskID)
}

//nolint:paralleltest // the adapter's task reads take seconds
func TestLoadSettlesAPrunedTaskAsOutcomeUnknown(t *testing.T) {
	adapter := taskStateDirector(t, func(writer http.ResponseWriter) {
		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeTaskNotFound, fmt.Sprintf("Task %d not found", adoptedTaskID))
	})
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID)))

	manager := loadManager(t, adapter, vault)

	done := eventuallyWithin(t, manager, loadedTaskID, settleTimeout, "the pruned task's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusInterrupted
	})

	instance := done.Instances[0]
	if instance.Status != InstanceStatusOutcomeUnknown || !strings.Contains(instance.Error, loadedContextID) {
		t.Fatalf("expected the instance to settle with an unknown outcome that names context ID %s, got %+v", loadedContextID, instance)
	}

	if done.UnknownCount != 1 || done.FailedCount != 0 || done.CompletedCount != 0 {
		t.Fatalf("expected one unknown outcome and nothing else counted, got %+v", done)
	}
}

//nolint:paralleltest // it replaces the package's lookup by context ID
func TestCancelDuringALoadedContextIDLookupCancelsTheFoundTask(t *testing.T) {
	director := &gatedLookupDirector{lostDirector: lostAnswerDirector(), lookupStarted: make(chan struct{}), gate: make(chan struct{})}
	vault := newWaitVault()

	lost := inFlight(waitDeployment, InstanceStatusRunning, 0, loadedContextID)
	unknownSince := time.Now().Add(-time.Minute)
	lost.UnknownSince = &unknownSince

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID, lost))

	manager := loadManager(t, director, vault)

	select {
	case <-director.lookupStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the load never looked for the deploy by its context ID")
	}

	// The lookup has not found the task yet, so the cancel has no task ID
	// to send, and the load cancels the task once it finds it.
	cancelUpgrade(t, manager, loadedTask())
	close(director.gate)

	expectOneCancelOfTheFoundTask(t, manager, loadedTask(), director.waitDirector)
}

//nolint:paralleltest // it replaces the package's lookup by context ID
func TestLoadReturnsAnUnsentDeployToPending(t *testing.T) {
	since := replaceAwait(t, noTaskWithTheContextID)
	vault := newWaitVault()

	// The context ID was saved, but the broker stopped before the deploy
	// went out, so no answer was ever lost.
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusRunning, 0, loadedContextID)))

	loading := time.Now()
	manager := loadManager(t, newWaitDirector(), vault)

	done := eventually(t, manager, loadedTask(), "the unsent deploy's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	expectPending(t, liveInstance, done.Instances[0])
	expectNoCounts(t, done)

	expectPending(t, savedInstance, eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool {
		return saved.Instances[0].Status == InstanceStatusPending
	}).Instances[0])

	if starts := since(); len(starts) != 1 || starts[0].Before(loading) {
		t.Fatalf("expected one lookup whose grace starts at the load, after %v, got %v", loading, starts)
	}
}

//nolint:paralleltest // it replaces the package's lookup by context ID
func TestLoadFailsALostAnswerWhoseGracePassed(t *testing.T) {
	since := replaceAwait(t, noTaskWithTheContextID)
	vault := newWaitVault()

	lost := inFlight(waitDeployment, InstanceStatusInterrupted, 0, loadedContextID)
	unknownSince := time.Now().Add(-time.Hour).Truncate(time.Second)
	lost.UnknownSince = &unknownSince

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID, lost))

	manager := loadManager(t, newWaitDirector(), vault)

	done := eventually(t, manager, loadedTask(), "the lost answer's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusInterrupted
	})

	instance := done.Instances[0]
	if instance.Status != InstanceStatusFailed || !strings.Contains(instance.Error, loadedContextID) {
		t.Fatalf("expected the instance to fail naming context ID %s, got %+v", loadedContextID, instance)
	}

	if done.FailedCount != 1 || done.CompletedCount != 0 || done.UnknownCount != 0 {
		t.Fatalf("expected one failure and nothing else counted, got %+v", done)
	}

	if starts := since(); len(starts) != 1 || !starts[0].Equal(unknownSince) {
		t.Fatalf("expected one lookup whose grace starts when the answer was lost, %v, got %v", unknownSince, starts)
	}
}

//nolint:paralleltest // the settle runs on a shared test director
func TestLoadResetsARunningInstanceThatNeverSentItsDeploy(t *testing.T) {
	director := &busyDirector{waitDirector: newWaitDirector(), asked: make(chan string, 1)}
	vault := newWaitVault()

	// The broker stopped after it saved the instance running and before it
	// saved the deploy's context ID.
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusRunning, 0, "")))

	manager := loadManager(t, director, vault)

	done := eventually(t, manager, loadedTask(), "the instance's reset", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	select {
	case deployment := <-director.asked:
		if deployment != waitDeployment {
			t.Fatalf("expected the load to ask about deployment %s, got %s", waitDeployment, deployment)
		}
	default:
		t.Fatal("the load reset the instance without asking whether a task runs on its deployment")
	}

	expectPending(t, liveInstance, done.Instances[0])
	expectNoCounts(t, done)
}

//nolint:paralleltest // the settle waits on a shared test director
func TestLoadWaitsForATaskOnTheDeploymentBeforeResetting(t *testing.T) {
	director := &busyDirector{
		waitDirector: newWaitDirector(),
		running:      &bosh.Task{ID: waitDeployTaskID, State: waitStateRunning, Deployment: waitDeployment},
		asked:        make(chan string, 1),
	}
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusRunning, 0, "")))

	manager := loadManager(t, director, vault)

	waitForTheDeployWait(t, director.waitDirector)

	if waiting := snapshot(manager, loadedTask()).Instances[0]; waiting.Status != InstanceStatusRunning {
		t.Fatalf("expected the instance to stay running while a task runs on its deployment, got %+v", waiting)
	}

	// The task on the deployment succeeds, but nothing ties it to the
	// instance, so the instance takes nothing from it.
	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := eventually(t, manager, loadedTask(), "the instance's reset", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	expectPending(t, liveInstance, done.Instances[0])
	expectNoCounts(t, done)

	if done.Instances[0].BOSHTaskID != 0 {
		t.Fatalf("expected the instance not to take the deployment's task, got %+v", done.Instances[0])
	}
}

func TestLoadWithoutABOSHDirectorMarksInstancesInterrupted(t *testing.T) {
	t.Parallel()

	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusRunning, waitDeployTaskID, loadedContextID)))

	manager := loadManager(t, nil, vault)

	done := eventually(t, manager, loadedTask(), "the instance's interruption", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusRunning
	})

	for what, instance := range map[string]InstanceUpgrade{
		liveInstance: done.Instances[0],
		savedInstance: eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool {
			return saved.Instances[0].Status != InstanceStatusRunning
		}).Instances[0],
	} {
		if instance.Status != InstanceStatusInterrupted || instance.Error != noDirectorMessage || instance.BOSHTaskID != waitDeployTaskID {
			t.Errorf("expected the %s to be interrupted, keep BOSH task %d, and say %q, got %+v", what, waitDeployTaskID, noDirectorMessage, instance)
		}
	}

	expectNoCounts(t, done)

	manager.mu.RLock()
	settling := manager.settling[loadedTaskID]
	manager.mu.RUnlock()

	if settling != 1 {
		t.Fatalf("expected the instance to stay settling until a restart with a director, got a settling count of %d", settling)
	}
}

//nolint:paralleltest // it shortens the package's load backoff
func TestLoadRetriesWhenTheIndexReadFails(t *testing.T) {
	previous := loadRetryBackoff
	loadRetryBackoff = time.Millisecond

	t.Cleanup(func() { loadRetryBackoff = previous })

	vault := &failingIndexVault{waitVault: newWaitVault(), failures: 2}

	storeUpgrade(t, vault.waitVault, storedUpgrade(loadedTaskID))

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), vault)
	t.Cleanup(manager.Stop)

	eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the load of the stored upgrade", func(UpgradeTask) bool { return true })

	if reads := vault.indexReads(); reads != 3 {
		t.Fatalf("expected the index to be read until it answered, 3 times, got %d", reads)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestCancelSendsACancelForAnInterruptedInstance(t *testing.T) {
	director := newWaitDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(waitDeployment, InstanceStatusInterrupted, waitDeployTaskID, loadedContextID)))

	manager := loadManager(t, director, vault)
	endTheTaskAtCleanup(t, director)

	waitForTheDeployWait(t, director)

	cancelUpgrade(t, manager, loadedTask())

	if cancels := director.cancelled(); len(cancels) != 1 || cancels[0] != waitDeployTaskID {
		t.Fatalf("expected the cancel to reach BOSH task %d of the interrupted instance, got %v", waitDeployTaskID, cancels)
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskCancelled}

	done := eventually(t, manager, loadedTask(), "the interrupted instance's settling", func(current UpgradeTask) bool {
		return current.Instances[0].Status != InstanceStatusInterrupted
	})

	if done.Instances[0].Status != InstanceStatusCancelled || done.CancelledCount != 1 {
		t.Fatalf("expected the instance to settle as cancelled, got %+v", done)
	}
}
