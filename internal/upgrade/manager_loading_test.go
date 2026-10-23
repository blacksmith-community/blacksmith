package upgrade

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/pkg/logger"
)

// Until the loader has settled the stored tasks, a pause, a resume, or a
// cancel is turned away with 503 tasks_loading, and the task is left as it
// was, with nothing saved, so the action can't race the loader's own
// settling of the same task.
func TestTaskActionsWaitForTheLoad(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		status TaskStatus
		act    func(m *Manager, taskID string) error
	}{
		"pause": {
			status: TaskStatusRunning,
			act:    func(m *Manager, taskID string) error { return m.PauseTask(context.Background(), taskID) },
		},
		"resume": {
			status: TaskStatusPaused,
			act:    func(m *Manager, taskID string) error { return m.ResumeTask(context.Background(), taskID) },
		},
		"cancel": {
			status: TaskStatusPaused,
			act:    func(m *Manager, taskID string) error { return m.CancelTask(context.Background(), taskID) },
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			vault := newWaitVault()

			// The manager is never started, so its load never ends, and the
			// task is put where the loader puts a stored one before it has
			// settled it.
			manager := NewManager(&logger.NoOpLogger{}, noCallDirector{}, vault)
			t.Cleanup(manager.Stop)

			task := storedUpgrade(loadedTaskID, pendingInstance(waitInstanceID, waitDeployment))
			task.Status = test.status
			task.Paused = test.status == TaskStatusPaused

			manager.mu.Lock()
			manager.tasks[task.ID] = &task
			manager.mu.Unlock()

			err := test.act(manager, task.ID)
			if !errors.Is(err, ErrTasksLoading) {
				t.Fatalf("expected ErrTasksLoading, got %v", err)
			}

			expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeTasksLoading)

			got, err := manager.GetTask(task.ID)
			if err != nil {
				t.Fatalf("reading the task: %v", err)
			}

			if got.Status != test.status || got.Paused != task.Paused || got.CompletedAt != nil {
				t.Fatalf("expected the task left %s, got %s (paused %t)", test.status, got.Status, got.Paused)
			}

			if inst := got.Instances[0]; inst.Status != InstanceStatusPending {
				t.Fatalf("expected the instance left pending, got %s", inst.Status)
			}

			if paths := vault.storedPaths(); len(paths) != 0 {
				t.Fatalf("expected nothing saved, got %v", paths)
			}
		})
	}
}

// skipLoad marks a manager that the test never starts as loaded, as a
// test that drives a runner directly needs before it pauses, resumes, or
// cancels the task.
func skipLoad(m *Manager) {
	close(m.loaded)
}
