//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"testing"

	"blacksmith/internal/audit"
)

// An upgrade created by a request records who created it and from where,
// as a certificate task does, through the manager's own CreateTask.
func TestCreateTaskRecordsActor(t *testing.T) {
	t.Parallel()

	manager := loadManager(t, newResumeDirector(), newWaitVault())
	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "admin", RemoteAddr: "10.0.0.5:4000", ForwardedFor: "203.0.113.9"})

	task, err := manager.CreateTask(ctx, stemcellRequest(waitInstanceID))
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if task.CreatedBy != "admin" || task.CreatedFrom != "10.0.0.5:4000 via 203.0.113.9" {
		t.Fatalf("expected the actor recorded, got by %q from %q", task.CreatedBy, task.CreatedFrom)
	}

	expectCompleted(t, manager, task.ID)
}
