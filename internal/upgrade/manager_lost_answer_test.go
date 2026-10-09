package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

const (
	// secondInstanceID is the instance an upgrade deploys after the first.
	secondInstanceID = "instance-2"
	// foundTaskID is the task a lookup by context ID finds.
	foundTaskID = 77
	// lostDirectorAddress is the director the lost answers name.
	lostDirectorAddress = "https://director.example.test:25555"
)

var errAnswerLostInTest = errors.New("the connection closed before the director answered")

// lostDirector is a waitDirector whose deploys can lose their answer. It
// records the context ID each deploy carried, can end WaitForTask calls
// with ErrWatchAbandoned, and answers each lookup by context ID with the
// task the test sets. It has no WaitForContextID, the way a director with
// no batch wrapper has none.
type lostDirector struct {
	*waitDirector

	mu sync.Mutex
	// lose is how many of the next deploys lose their answer.
	lose int
	// abandonWaits is how many of the next WaitForTask calls end with
	// ErrWatchAbandoned.
	abandonWaits int
	// deployContextIDs holds the context ID each deploy carried.
	deployContextIDs []string
	// found is the task each lookup by context ID answers with, and
	// lookups holds the context ID of each lookup.
	found   *bosh.Task
	lookups []string
}

func newLostDirector() *lostDirector {
	return &lostDirector{waitDirector: newWaitDirector()}
}

func (d *lostDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error) {
	contextID := boshapi.ContextIDFrom(ctx)

	d.mu.Lock()
	d.deployContextIDs = append(d.deployContextIDs, contextID)
	lose := d.lose > 0

	if lose {
		d.lose--
	}
	d.mu.Unlock()

	if lose {
		return nil, &bosh.OutcomeUnknownError{
			Operation:  "UpdateDeployment",
			Director:   lostDirectorAddress,
			Deployment: name,
			ContextID:  contextID,
			Err:        errAnswerLostInTest,
		}
	}

	return d.waitDirector.UpdateDeployment(ctx, name, manifest)
}

func (d *lostDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.mu.Lock()
	abandon := d.abandonWaits > 0

	if abandon {
		d.abandonWaits--
	}
	d.mu.Unlock()

	if abandon {
		return nil, fmt.Errorf("%w: BOSH task %d could not be read for 6h0m0s", bosh.ErrWatchAbandoned, taskID)
	}

	return d.waitDirector.WaitForTask(ctx, taskID)
}

func (d *lostDirector) FindTaskByContextID(_ context.Context, contextID string) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.lookups = append(d.lookups, contextID)

	if d.found == nil {
		return nil, nil //nolint:nilnil // no task with the context ID is an answer
	}

	found := *d.found

	return &found, nil
}

func (d *lostDirector) contextIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.deployContextIDs...)
}

func (d *lostDirector) lookedUp() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.lookups...)
}

// contextAnswer is the answer to one wait by context ID.
type contextAnswer struct {
	task *bosh.Task
	err  error
}

// waiterDirector is a lostDirector with WaitForContextID, the way the
// batch director has it. Each wait reports its context ID on waitedFor
// and returns the next answer the test sends, or ends with its context.
type waiterDirector struct {
	*lostDirector

	waitedFor chan string
	answer    chan contextAnswer
}

func newWaiterDirector() *waiterDirector {
	return &waiterDirector{
		lostDirector: newLostDirector(),
		waitedFor:    make(chan string, 4),
		answer:       make(chan contextAnswer, 1),
	}
}

func (d *waiterDirector) WaitForContextID(ctx context.Context, contextID string) (*bosh.Task, error) {
	d.waitedFor <- contextID

	select {
	case answer := <-d.answer:
		return answer.task, answer.err
	case <-ctx.Done():
		return nil, fmt.Errorf("stopped waiting for the BOSH task with context ID %s: %w", contextID, ctx.Err())
	}
}

// receiveContextWait returns the context ID the upgrade waits for, and
// fails the test if the upgrade never waits.
func receiveContextWait(t *testing.T, director *waiterDirector) string {
	t.Helper()

	select {
	case contextID := <-director.waitedFor:
		return contextID
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited by context ID")

		return ""
	}
}

// waitForTheDeployWait waits until the upgrade waits on BOSH task 42.
func waitForTheDeployWait(t *testing.T, director *waitDirector) {
	t.Helper()

	select {
	case <-director.waitStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never waited on BOSH task 42")
	}
}

// lostAnswerMessage is what an instance says while it looks for the task
// of a deploy whose answer was lost.
func lostAnswerMessage(contextID string) string {
	return "answer lost; looking for the BOSH task by context ID " + contextID
}

// awaitCall is one call the upgrade made to AwaitTaskByContextID.
type awaitCall struct {
	contextID string
	since     time.Time
}

// recordAwaits records each call the upgrade makes to
// AwaitTaskByContextID, which it then passes to the real helper.
func recordAwaits(t *testing.T) func() []awaitCall {
	t.Helper()

	var (
		callsMu sync.Mutex
		calls   []awaitCall
	)

	original := awaitTaskByContextID

	t.Cleanup(func() { awaitTaskByContextID = original })

	awaitTaskByContextID = func(ctx context.Context, director bosh.Director, contextID string, since time.Time) (*bosh.Task, error) {
		callsMu.Lock()

		calls = append(calls, awaitCall{contextID: contextID, since: since})

		callsMu.Unlock()

		return original(ctx, director, contextID, since)
	}

	return func() []awaitCall {
		callsMu.Lock()
		defer callsMu.Unlock()

		return append([]awaitCall(nil), calls...)
	}
}

// settled waits until the upgrade task is no longer running.
func settled(t *testing.T, manager *Manager, task *UpgradeTask) UpgradeTask {
	t.Helper()

	return eventually(t, manager, task, "the upgrade's end", func(current UpgradeTask) bool {
		return current.Status != TaskStatusRunning && current.Status != TaskStatusPending
	})
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWithALostAnswerIsNotMarkedFailed(t *testing.T) {
	director := newWaiterDirector()
	director.lose = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault())

	contextID := receiveContextWait(t, director)

	waiting := snapshot(manager, task)
	instance := waiting.Instances[0]

	if instance.Status != InstanceStatusRunning || instance.CompletedAt != nil {
		t.Fatalf("expected the instance to stay running while its task is looked for, got %+v", instance)
	}

	if instance.Error != lostAnswerMessage(contextID) {
		t.Errorf("expected the instance to say %q, got %q", lostAnswerMessage(contextID), instance.Error)
	}

	if waiting.FailedCount != 0 {
		t.Fatalf("expected no failed count while the task is looked for, got %+v", waiting)
	}

	director.answer <- contextAnswer{task: &bosh.Task{ID: foundTaskID, State: boshTaskDone}}

	done := settled(t, manager, task)

	instance = done.Instances[0]
	if instance.Status != InstanceStatusSuccess || instance.BOSHTaskID != foundTaskID || instance.Error != "" {
		t.Fatalf("expected the instance to succeed with BOSH task %d and no message, got %+v", foundTaskID, instance)
	}

	if done.Status != TaskStatusCompleted || done.CompletedCount != 1 || done.FailedCount != 0 {
		t.Fatalf("expected one successful instance, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstancePutsItsContextIDOnTheContext(t *testing.T) {
	director := newLostDirector()
	vault := newWaitVault()

	// The deploy is held once it is sent, so the test can read what the
	// manager saved before sending it.
	director.updateStarted = make(chan struct{})
	director.updateGate = make(chan struct{})

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	select {
	case <-director.updateStarted:
	case <-time.After(waitTimeout):
		t.Fatal("the upgrade never sent its deploy")
	}

	savedBeforeTheDeploy := vault.savedTask(t, task.ID).Instances[0].ContextID

	close(director.updateGate)
	waitForTheDeployWait(t, director.waitDirector)

	sent := director.contextIDs()
	if len(sent) != 1 || sent[0] == "" {
		t.Fatalf("expected one deploy that carried a context ID, got %q", sent)
	}

	instance := snapshot(manager, task).Instances[0]
	if instance.ContextID != sent[0] {
		t.Errorf("expected the instance to hold the deploy's context ID %q, got %q", sent[0], instance.ContextID)
	}

	if savedBeforeTheDeploy != sent[0] {
		t.Errorf("expected context ID %q to be saved before the deploy was sent, got %q", sent[0], savedBeforeTheDeploy)
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	if done := settled(t, manager, task); done.Instances[0].Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to succeed, got %+v", done.Instances[0])
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstancePersistsUnknownSinceWhenTheAnswerIsLost(t *testing.T) {
	director := newWaiterDirector()
	director.lose = 1
	vault := newWaitVault()

	before := time.Now()

	_, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	contextID := receiveContextWait(t, director)
	after := time.Now()

	saved := vault.savedTask(t, task.ID).Instances[0]

	if saved.UnknownSince == nil || saved.UnknownSince.Before(before) || saved.UnknownSince.After(after) {
		t.Fatalf("expected the saved instance to hold the time the answer was lost, between %s and %s, got %v", before, after, saved.UnknownSince)
	}

	if saved.ContextID != contextID || saved.Status != InstanceStatusRunning || saved.Error != lostAnswerMessage(contextID) {
		t.Fatalf("expected the saved instance to be running with context ID %s, got %+v", contextID, saved)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWithNoTaskForTheContextIDIsMarkedFailedNamingTheID(t *testing.T) {
	director := newWaiterDirector()
	director.lose = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault())

	contextID := receiveContextWait(t, director)
	director.answer <- contextAnswer{err: fmt.Errorf("%w %s", bosh.ErrNoTaskWithContextID, contextID)}

	done := settled(t, manager, task)
	instance := done.Instances[0]

	want := fmt.Sprintf("no BOSH task with context ID %s turned up within %v after the deploy's answer was lost", contextID, bosh.UnknownOutcomeGrace)
	if instance.Status != InstanceStatusFailed || instance.Error != want || instance.CompletedAt == nil {
		t.Fatalf("expected the instance to fail saying %q, got %+v", want, instance)
	}

	if done.FailedCount != 1 || done.Status != TaskStatusFailed {
		t.Fatalf("expected one failed instance and a failed upgrade, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceSupersededByALaterDeployIsSkippedNamingIt(t *testing.T) {
	director := newWaiterDirector()
	director.lose = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault())

	contextID := receiveContextWait(t, director)
	sentAt := time.Date(2026, time.October, 8, 14, 3, 11, 0, time.UTC)

	director.answer <- contextAnswer{err: fmt.Errorf("context ID %s: %w", contextID,
		&bosh.LostAnswerSupersededError{Deployment: waitDeployment, At: sentAt})}

	done := settled(t, manager, task)
	instance := done.Instances[0]

	want := "skipped, because a later deploy of " + waitDeployment + ", sent at 2026-10-08T14:03:11Z, " +
		"replaced the deploy whose answer was lost (context ID " + contextID + ")"
	if instance.Status != InstanceStatusSkipped || instance.Error != want {
		t.Fatalf("expected the instance to be skipped saying %q, got %+v", want, instance)
	}

	if done.SkippedCount != 1 || done.FailedCount != 0 || done.Status != TaskStatusCompleted {
		t.Fatalf("expected one skipped instance, no failed one, and a completed upgrade, got %+v", done)
	}
}

// expectOutcomeUnknown checks that the first instance settled as
// outcome_unknown with a message that names the context ID, and the BOSH
// task when one is given, that the second instance was deployed, and that
// the upgrade completed with no failure.
func expectOutcomeUnknown(t *testing.T, done UpgradeTask, contextID string, taskID int) {
	t.Helper()

	unknown := done.Instances[0]

	if unknown.Status != InstanceStatusOutcomeUnknown || unknown.CompletedAt == nil {
		t.Fatalf("expected the first instance to settle as %s, got %+v", InstanceStatusOutcomeUnknown, unknown)
	}

	followed := ""
	if taskID > 0 {
		followed = fmt.Sprintf(" (BOSH task %d)", taskID)
	}

	want := "Blacksmith stopped following the deploy with context ID " + contextID + followed +
		" before its outcome was known. Check the director's task list for that context ID."
	if unknown.Error != want {
		t.Errorf("expected the first instance to say %q, got %q", want, unknown.Error)
	}

	if next := done.Instances[1]; next.Status != InstanceStatusSuccess || next.BOSHTaskID != waitDeployTaskID {
		t.Fatalf("expected the next instance to be deployed, got %+v", next)
	}

	if done.UnknownCount != 1 || done.FailedCount != 0 || done.CompletedCount != 1 || done.Status != TaskStatusCompleted {
		t.Fatalf("expected one unknown and one successful instance in a completed upgrade, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWithAnAbandonedWatchSettlesAsOutcomeUnknown(t *testing.T) {
	director := newWaiterDirector()
	director.lose = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault(), waitInstanceID, secondInstanceID)

	contextID := receiveContextWait(t, director)
	director.answer <- contextAnswer{err: fmt.Errorf("%w: the task of the deploy of %s could not be looked up for 6h0m0s", bosh.ErrWatchAbandoned, waitDeployment)}

	waitForTheDeployWait(t, director.waitDirector)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	expectOutcomeUnknown(t, settled(t, manager, task), contextID, 0)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWhoseFollowedTaskIsAbandonedSettlesAsOutcomeUnknown(t *testing.T) {
	director := newLostDirector()
	director.abandonWaits = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault(), waitInstanceID, secondInstanceID)

	waitForTheDeployWait(t, director.waitDirector)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := settled(t, manager, task)

	sent := director.contextIDs()
	if len(sent) != 2 {
		t.Fatalf("expected two deploys, got %q", sent)
	}

	expectOutcomeUnknown(t, done, sent[0], waitDeployTaskID)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeWithOnlyAnUnknownInstanceIsCompletedNotFailed(t *testing.T) {
	director := newLostDirector()
	director.abandonWaits = 1

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, newWaitVault())

	done := settled(t, manager, task)

	if done.Instances[0].Status != InstanceStatusOutcomeUnknown {
		t.Fatalf("expected the instance to settle as %s, got %+v", InstanceStatusOutcomeUnknown, done.Instances[0])
	}

	if done.Status != TaskStatusCompleted || done.CompletedAt == nil {
		t.Fatalf("expected the upgrade to complete, not fail, got %+v", done)
	}

	if done.UnknownCount != 1 || done.FailedCount != 0 || done.CompletedCount != 0 {
		t.Fatalf("expected one unknown instance and no failed or successful one, got %+v", done)
	}
}

//nolint:paralleltest // the upgrade package's tests run serially
func TestUpgradeTaskSummaryCarriesTheUnknownCount(t *testing.T) {
	task := UpgradeTask{ID: "task-1", TotalCount: 3, CompletedCount: 1, UnknownCount: 2}

	summary := task.ToSummary()
	if summary.UnknownCount != 2 {
		t.Fatalf("expected the summary to carry an unknown count of 2, got %+v", summary)
	}

	for what, value := range map[string]interface{}{"summary": summary, "task": task} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("failed to marshal the %s: %v", what, err)
		}

		if !strings.Contains(string(raw), `"unknown_count":2`) {
			t.Errorf("expected the %s's JSON to hold unknown_count, got %s", what, raw)
		}
	}
}

// expectTheFoundTaskIsFollowed checks that the upgrade looked the task up
// through AwaitTaskByContextID, from the time it saved as the moment the
// answer was lost, and then followed the task it found to its end.
func expectTheFoundTaskIsFollowed(t *testing.T, manager *Manager, task *UpgradeTask, vault *waitVault, director *lostDirector, awaits func() []awaitCall) {
	t.Helper()

	waitForTheDeployWait(t, director.waitDirector)

	contextID := director.contextIDs()[0]

	running := snapshot(manager, task).Instances[0]
	if running.Status != InstanceStatusRunning || running.BOSHTaskID != waitDeployTaskID {
		t.Fatalf("expected the instance to follow the BOSH task it found, %d, got %+v", waitDeployTaskID, running)
	}

	saved := vault.savedTask(t, task.ID).Instances[0]
	if saved.BOSHTaskID != waitDeployTaskID || saved.UnknownSince == nil {
		t.Fatalf("expected the found task and the time the answer was lost to be saved, got %+v", saved)
	}

	calls := awaits()
	if len(calls) != 1 || calls[0].contextID != contextID || !calls[0].since.Equal(*saved.UnknownSince) {
		t.Fatalf("expected one AwaitTaskByContextID call for %s from %s, got %+v", contextID, saved.UnknownSince, calls)
	}

	if lookups := director.lookedUp(); len(lookups) == 0 || lookups[0] != contextID {
		t.Fatalf("expected the director to be asked for context ID %s, got %q", contextID, lookups)
	}

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := settled(t, manager, task)
	if done.Instances[0].Status != InstanceStatusSuccess || done.CompletedCount != 1 || done.FailedCount != 0 {
		t.Fatalf("expected the instance to succeed, got %+v", done)
	}
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWithNoWatcherUsesAwaitTaskByContextID(t *testing.T) {
	awaits := recordAwaits(t)

	director := newWaiterDirector()
	director.lose = 1
	director.found = &bosh.Task{ID: waitDeployTaskID, State: waitStateRunning, Deployment: waitDeployment}
	vault := newWaitVault()

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	contextID := receiveContextWait(t, director)
	director.answer <- contextAnswer{err: fmt.Errorf("%w: %s", bosh.ErrNoWatcherForContextID, contextID)}

	expectTheFoundTaskIsFollowed(t, manager, task, vault, director.lostDirector, awaits)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceWithNoBatchDirectorUsesAwaitTaskByContextID(t *testing.T) {
	awaits := recordAwaits(t)

	director := newLostDirector()
	director.lose = 1
	director.found = &bosh.Task{ID: waitDeployTaskID, State: waitStateRunning, Deployment: waitDeployment}
	vault := newWaitVault()

	manager, task, _ := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

	expectTheFoundTaskIsFollowed(t, manager, task, vault, director, awaits)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeInstanceStoppedLeavesTheInstanceAsItIs(t *testing.T) {
	cases := map[string]func(director *waiterDirector, stop func()){
		"the manager stops": func(_ *waiterDirector, stop func()) { stop() },
		"the batch director stops": func(director *waiterDirector, _ func()) {
			director.answer <- contextAnswer{err: fmt.Errorf("the batch director stopped and no longer follows the deploy: %w", context.Canceled)}
		},
	}

	for name, stopWaiting := range cases {
		t.Run(name, func(t *testing.T) {
			director := newWaiterDirector()
			director.lose = 1
			vault := newWaitVault()

			manager, task, stop := beginUpgrade(t, &logger.NoOpLogger{}, director, vault)

			contextID := receiveContextWait(t, director)
			stopWaiting(director, stop)

			// The upgrade has stopped waiting once it ends, or once the
			// manager has stopped.
			eventually(t, manager, task, "the end of the wait", func(current UpgradeTask) bool {
				return current.Status != TaskStatusRunning || name == "the manager stops"
			})

			for what, instance := range map[string]InstanceUpgrade{
				"instance":       snapshot(manager, task).Instances[0],
				"saved instance": vault.savedTask(t, task.ID).Instances[0],
			} {
				if instance.Status != InstanceStatusRunning || instance.CompletedAt != nil || instance.Error != lostAnswerMessage(contextID) {
					t.Errorf("expected the %s to stay running and looking for its task, got %+v", what, instance)
				}

				if instance.ContextID != contextID || instance.UnknownSince == nil {
					t.Errorf("expected the %s to keep context ID %s and the time the answer was lost, got %+v", what, contextID, instance)
				}
			}

			if counts := snapshot(manager, task); counts.FailedCount != 0 || counts.UnknownCount != 0 || counts.SkippedCount != 0 || counts.CompletedCount != 0 {
				t.Fatalf("expected no count to change, got %+v", counts)
			}
		})
	}
}
