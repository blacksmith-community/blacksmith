//nolint:testpackage // the tests reach the unexported session recorder
package upgrade

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"
)

// Names the session tests share.
const (
	sessionDeployment0 = "dep-0"
	sessionDeployment1 = "dep-1"
	sessionHostAddress = "10.0.0.10"
	// killedCleanup is the record of a cleanup that ended the command.
	killedCleanup = "cleanup started=yes exit=- killed=yes"
)

// firstSetupTaskID is the setup task the first session sshTaskRunner opens
// reports. Each later session's is two more, and each session's cleanup
// task is its setup task plus one.
const firstSetupTaskID = 501

// sshTaskRunner opens the fixture's fake sessions the way the director
// does. It tells the observer on the open's context of a setup task before
// it returns, and the session's Close tells that observer of the cleanup
// task. After each setup it waits, as the director's setup task runs, for
// the task record in Vault to hold that setup task, and notes whether it
// did before the open returned.
type sshTaskRunner struct {
	fix *taskFixture

	mu    sync.Mutex
	next  int
	saved map[int]bool
	// failOpen makes each open fail once the setup was heard.
	failOpen bool
	// noWait makes each open return as soon as the setup was heard.
	noWait bool
}

// setupRuns is how long the fake setup task runs at most while it waits
// for its ID to be saved.
const setupRuns = 2 * time.Second

// promptly is how long a call that must not wait for Vault may take.
const promptly = time.Second

var errSetupFailed = errors.New("the SSH setup task failed")

func observeSSHTasks(fix *taskFixture) *sshTaskRunner {
	runner := &sshTaskRunner{fix: fix, next: firstSetupTaskID, saved: map[int]bool{}}

	fix.mu.Lock()
	fix.runner = runner.open
	fix.mu.Unlock()

	return runner
}

func (r *sshTaskRunner) open(ctx context.Context, deployment, _ string, indices []int) (PatchSession, error) {
	r.mu.Lock()
	setup := r.next
	r.next += 2
	failOpen := r.failOpen
	noWait := r.noWait
	r.mu.Unlock()

	observer := sshconn.TaskObserverFrom(ctx)
	observer.SetupStarted(setup)

	if !noWait {
		saved := r.fix.awaitStoredSetup(ctx, deployment, setup)

		r.mu.Lock()
		r.saved[setup] = saved
		r.mu.Unlock()
	}

	if failOpen {
		observer.CleanupStarted(setup + 1)

		return nil, errSetupFailed
	}

	sess := &taskSession{fix: r.fix, deployment: deployment, indices: slices.Clone(indices)}

	for _, index := range indices {
		sess.hosts = append(sess.hosts, sshconn.Host{Job: certJob, IndexOrID: strconv.Itoa(index), Address: sessionHostAddress})
	}

	return &observedSession{taskSession: sess, observer: observer, cleanup: setup + 1}, nil
}

// savedBeforeOpen reports whether the setup task was in Vault before the
// open that heard it returned.
func (r *sshTaskRunner) savedBeforeOpen(setup int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.saved[setup]
}

// observedSession is a fake session whose Close tells the observer of
// its cleanup task first, as the director's cleanup does.
type observedSession struct {
	*taskSession

	observer sshconn.TaskObserver
	cleanup  int
}

func (s *observedSession) Close() error {
	s.observer.CleanupStarted(s.cleanup)

	return s.taskSession.Close()
}

// storedSessions returns the SSH sessions the task record in Vault holds
// for the instance on deployment.
func (f *taskFixture) storedSessions(ctx context.Context, deployment string) []SSHSessionTasks {
	var task UpgradeTask

	found, err := f.vault.Get(ctx, commandTasksVaultPath+"/"+taskFixtureID, &task)
	if err != nil || !found {
		return nil
	}

	for _, inst := range task.Instances {
		if inst.DeploymentName == deployment {
			return inst.SSHSessions
		}
	}

	return nil
}

// awaitStoredSetup reports whether the task record in Vault names the
// setup task for the instance on deployment within setupRuns.
func (f *taskFixture) awaitStoredSetup(ctx context.Context, deployment string, setup int) bool {
	deadline := time.Now().Add(setupRuns)

	for {
		if slices.ContainsFunc(f.storedSessions(ctx, deployment), func(s SSHSessionTasks) bool { return s.SetupTaskID == setup }) {
			return true
		}

		if time.Now().After(deadline) {
			return false
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// awaitStoredSessions waits until the task record in Vault holds want for
// the instance on deployment.
func (f *taskFixture) awaitStoredSessions(deployment string, want []SSHSessionTasks) {
	f.t.Helper()

	var got []SSHSessionTasks

	waitUntil(f.t, "the SSH sessions of "+deployment+" are saved", func() bool {
		got = f.storedSessions(f.t.Context(), deployment)

		return slices.Equal(got, want)
	})
}

// Each instance records the one session its nodes share, with the setup
// task saved before the session opened and the cleanup task saved once
// the session closed. Two nodes of one group make one entry.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestCommandTaskRecordsTheSSHSessionOfEachInstance(t *testing.T) {
	fix := newTaskFixture(t, 2, 2)
	fix.setSlots(1)
	runner := observeSSHTasks(fix)
	fix.queue()

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	want := map[string][]SSHSessionTasks{
		sessionDeployment0: {{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1}},
		sessionDeployment1: {{Group: certJob, SetupTaskID: firstSetupTaskID + 2, CleanupTaskID: firstSetupTaskID + 3}},
	}

	for deployment, sessions := range want {
		fix.awaitStoredSessions(deployment, sessions)

		if !runner.savedBeforeOpen(sessions[0].SetupTaskID) {
			t.Errorf("setup task %d of %s was not saved before its session opened", sessions[0].SetupTaskID, deployment)
		}
	}

	final := fix.snapshot()
	for _, inst := range final.Instances {
		if got := want[inst.DeploymentName]; !slices.Equal(inst.SSHSessions, got) {
			t.Errorf("instance %s holds sessions %+v, want %+v", inst.InstanceID, inst.SSHSessions, got)
		}
	}

	if done.CompletedCount != 2 {
		t.Errorf("%d instances completed, want 2", done.CompletedCount)
	}
}

// The session's cleanup task is saved however the instance ends: after a
// command that failed, one that timed out, and a cancel.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestCommandTaskRecordsTheSSHCleanupHoweverTheNodeEnds(t *testing.T) {
	cases := map[string]struct {
		arrange func(*taskFixture) func()
		status  string
	}{
		"a failed command": {
			arrange: func(fix *taskFixture) func() {
				fix.exits["dep-0/0"] = 3

				return nil
			},
			status: CommandStatusFailed,
		},
		"a timeout": {
			arrange: func(fix *taskFixture) func() {
				fix.exits["dep-0/0"] = exitTimedOut
				fix.cleanups["dep-0/0"] = "cleanup started=yes exit=124 killed=no"

				return nil
			},
			status: CommandStatusTimedOut,
		},
		"a cancel": {
			arrange: func(fix *taskFixture) func() {
				fix.cleanups["dep-0/0"] = killedCleanup
				script := fix.blockExec()

				return func() {
					script.wait(fix.t, 1)
					fix.cancel()
				}
			},
			status: CommandStatusFailed,
		},
	}

	for name, ending := range cases {
		t.Run(name, func(t *testing.T) { //nolint:paralleltest // as above
			fix := newTaskFixture(t, 2, 1)
			runner := observeSSHTasks(fix)
			act := ending.arrange(fix)

			fix.queue()

			if act != nil {
				act()
			}

			ended := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
				return nodeOf(task, 0, 0).Phase == CommandPhaseSettled
			})

			if got := nodeOf(ended, 0, 0).Status; got != ending.status {
				t.Errorf("the node is %s, want %s", got, ending.status)
			}

			fix.awaitStoredSessions(sessionDeployment0, []SSHSessionTasks{{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1}})

			if !runner.savedBeforeOpen(firstSetupTaskID) {
				t.Error("the setup task was not saved before the session opened")
			}
		})
	}
}

// Every session a command opens is its own entry, so a group opened again
// after its session closed records a second one, and a setup the director
// accepted is recorded even when the session never opens.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestCommandSessionsRecordEachSessionTheyOpen(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	runner := observeSSHTasks(fix)

	inst := &fix.task.Instances[0]
	sessions := fix.manager.commandSessionsFor(fix.task, inst)

	for range 2 {
		_, err := sessions.group(t.Context(), certJob, []int{0, 1})
		if err != nil {
			t.Fatalf("opening the session: %v", err)
		}

		sessions.close()
	}

	runner.mu.Lock()
	runner.failOpen = true
	runner.mu.Unlock()

	_, err := sessions.group(t.Context(), certJob, []int{0, 1})
	if !errors.Is(err, errSetupFailed) {
		t.Fatalf("the failed open answered %v", err)
	}

	fix.awaitStoredSessions(sessionDeployment0, []SSHSessionTasks{
		{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1},
		{Group: certJob, SetupTaskID: firstSetupTaskID + 2, CleanupTaskID: firstSetupTaskID + 3},
		{Group: certJob, SetupTaskID: firstSetupTaskID + 4, CleanupTaskID: firstSetupTaskID + 5},
	})
}

// holdSessionSave makes the first save of the task record that names an
// SSH session block until the release it returns is called, which the
// test's cleanup calls too.
func holdSessionSave(fix *taskFixture) (<-chan struct{}, func()) {
	held, release := fix.vault.holdNext(func(task UpgradeTask) bool {
		return len(task.Instances[0].SSHSessions) > 0
	})
	fix.t.Cleanup(release)

	return held, release
}

// within fails the test unless done closes within promptly.
func within(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(promptly):
		t.Fatalf("%s waited for the blocked save", what)
	}
}

// A save of the IDs that Vault holds up delays neither the setup nor the
// cleanup that reported them.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestSSHSessionTasksDoNotWaitForTheirSave(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	held, _ := holdSessionSave(fix)
	observer := fix.manager.sshSessionObserver(fix.task, &fix.task.Instances[0], certJob)

	setup := make(chan struct{})

	go func() {
		observer.Setup(firstSetupTaskID)
		close(setup)
	}()

	within(t, "the setup", setup)
	within(t, "the save of the setup task", held)

	cleanup := make(chan struct{})

	go func() {
		observer.Cleanup(firstSetupTaskID + 1)
		close(cleanup)
	}()

	within(t, "the cleanup", cleanup)
}

// The cleanup task heard while the save of the setup task is held up is
// in the record once the sessions close, and closing them waits for that
// save rather than leaving it behind.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestSSHSessionTasksHeardDuringASaveAreSaved(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	runner := observeSSHTasks(fix)
	runner.noWait = true
	held, release := holdSessionSave(fix)

	sessions := fix.manager.commandSessionsFor(fix.task, &fix.task.Instances[0])

	_, err := sessions.group(t.Context(), certJob, []int{0})
	if err != nil {
		t.Fatalf("opening the session: %v", err)
	}

	within(t, "the save of the setup task", held)

	closed := make(chan struct{})

	go func() {
		sessions.close()
		close(closed)
	}()

	waitUntil(t, "the cleanup task is heard", func() bool {
		return slices.Equal(fix.snapshot().Instances[0].SSHSessions, []SSHSessionTasks{{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1}})
	})

	select {
	case <-closed:
		t.Fatal("the sessions closed before the held save of their tasks ended")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	within(t, "closing the sessions", closed)

	want := []SSHSessionTasks{{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1}}
	if got := fix.storedSessions(t.Context(), sessionDeployment0); !slices.Equal(got, want) {
		t.Errorf("Vault holds sessions %+v once the sessions closed, want %+v", got, want)
	}
}

// The writer that saves a task's session IDs ends once nothing is left to
// save, and closing the sessions waits for it to end.
//
//nolint:paralleltest // it runs alone, like every other task fixture test
func TestSSHSessionSaveWriterEnds(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	held, release := holdSessionSave(fix)
	observer := fix.manager.sshSessionObserver(fix.task, &fix.task.Instances[0], certJob)

	observer.Setup(firstSetupTaskID)
	within(t, "the save of the setup task", held)
	observer.Cleanup(firstSetupTaskID + 1)

	fix.manager.mu.RLock()
	writer := fix.manager.sshSaves[fix.task]
	fix.manager.mu.RUnlock()

	if writer == nil {
		t.Fatal("no writer saves the session IDs")
	}

	release()
	within(t, "the writer", writer.done)

	fix.manager.mu.RLock()
	left := len(fix.manager.sshSaves)
	fix.manager.mu.RUnlock()

	if left != 0 {
		t.Errorf("%d writers are left once the saves ended, want none", left)
	}

	fix.awaitStoredSessions(sessionDeployment0, []SSHSessionTasks{{Group: certJob, SetupTaskID: firstSetupTaskID, CleanupTaskID: firstSetupTaskID + 1}})
}
