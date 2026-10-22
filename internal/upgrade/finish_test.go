//nolint:testpackage // the tests call the manager's unexported finisher
package upgrade

import (
	"context"
	"reflect"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

// boshTaskError is the state of a BOSH task that failed.
const boshTaskError = "error"

// recordingFinisher notes each deploy a certificate task hands it.
type recordingFinisher struct {
	calls []finishedDeploy
}

// finishedDeploy is one deploy a recordingFinisher received.
type finishedDeploy struct {
	taskID     string
	instanceID string
	boshTaskID int
	finished   *bosh.Task
}

func (r *recordingFinisher) certAfterDeploy(_ context.Context, task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task) {
	r.calls = append(r.calls, finishedDeploy{taskID: task.ID, instanceID: inst.InstanceID, boshTaskID: taskID, finished: finished})
}

// runningUpgrade returns an upgrade whose one instance is deploying.
func runningUpgrade() *UpgradeTask {
	started := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

	return &UpgradeTask{
		ID:         "finish-upgrade",
		Name:       "upgrade redis",
		Kind:       TaskKindUpgrade,
		Status:     TaskStatusRunning,
		TotalCount: 1,
		CreatedAt:  started,
		Instances: []InstanceUpgrade{{
			InstanceID:     waitInstanceID,
			DeploymentName: waitDeployment,
			Status:         InstanceStatusRunning,
			StartedAt:      &started,
			BOSHTaskID:     waitDeployTaskID,
			ContextID:      loadedContextID,
		}},
	}
}

// For an upgrade, finishing a followed deploy settles the instance
// exactly as settleInstance does, counters included.
func TestFinishFollowedDeployUpgradeUnchanged(t *testing.T) {
	t.Parallel()

	cases := map[string]*bosh.Task{
		"done":      {ID: waitDeployTaskID, State: boshTaskDone},
		"failed":    {ID: waitDeployTaskID, State: boshTaskError, Result: "compilation failed"},
		"cancelled": {ID: waitDeployTaskID, State: boshTaskCancelled},
	}

	for name, finished := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			settled := runningUpgrade()
			settler := NewManager(&logger.NoOpLogger{}, nil, newWaitVault())
			settler.settleInstance(settled, &settled.Instances[0], waitDeployTaskID, finished)

			finishedTask := runningUpgrade()
			finisher := NewManager(&logger.NoOpLogger{}, nil, newWaitVault())
			finisher.finishFollowedDeploy(context.Background(), finishedTask, &finishedTask.Instances[0], waitDeployTaskID, finished)

			if settled.Instances[0].CompletedAt == nil || finishedTask.Instances[0].CompletedAt == nil {
				t.Fatalf("expected both instances to record a completion time, got %v and %v",
					settled.Instances[0].CompletedAt, finishedTask.Instances[0].CompletedAt)
			}

			settled.Instances[0].CompletedAt = nil
			finishedTask.Instances[0].CompletedAt = nil

			if !reflect.DeepEqual(settled, finishedTask) {
				t.Fatalf("expected finishing to match settling\nsettled:  %+v\nfinished: %+v", settled, finishedTask)
			}
		})
	}
}

// For a certificate task, finishing a followed deploy hands the BOSH task
// to the certificate finisher and leaves the instance to it.
func TestFinishFollowedDeployRoutesCertificates(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, nil, newWaitVault())
	recorder := &recordingFinisher{}
	manager.certFinisher = recorder

	task := deployingTask()
	task.Status = TaskStatusRunning
	task.Instances[0].Status = InstanceStatusRunning
	before := copyTask(&task)

	finished := &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}
	manager.finishFollowedDeploy(context.Background(), &task, &task.Instances[0], waitDeployTaskID, finished)

	if len(recorder.calls) != 1 {
		t.Fatalf("expected the certificate finisher to run once, got %d calls", len(recorder.calls))
	}

	call := recorder.calls[0]
	if call.taskID != certTaskID || call.instanceID != waitInstanceID || call.boshTaskID != waitDeployTaskID || call.finished != finished {
		t.Fatalf("expected the finisher to receive BOSH task %d of %s, got %+v", waitDeployTaskID, waitInstanceID, call)
	}

	if !reflect.DeepEqual(before, &task) {
		t.Fatalf("expected settleInstance not to touch the task\nbefore: %+v\nafter:  %+v", before, &task)
	}
}
