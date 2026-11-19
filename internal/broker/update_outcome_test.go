package broker_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// updateOutcomeWait is how long the update waits between resolver passes
// in the lost-answer specs.
const updateOutcomeWait = 10 * time.Millisecond

// lostAnswer scripts the director to lose its answer to the update's
// deploy, and to name a task for the deploy's context ID only once the
// update has looked for it lookups times without one.
type lostAnswer struct {
	mu         sync.Mutex
	contextIDs []string
	lookups    int
}

// script points the fixture's director at the lost answer. found is the
// task the lookup by context ID finds, and nil finds none.
func (l *lostAnswer) script(fixture *updateFixture, found *bosh.Task, misses int) {
	fixture.director.UpdateDeploymentFn = func(string, string) (*bosh.Task, error) {
		return nil, fmt.Errorf("the deploy request timed out: %w", bosh.ErrOutcomeUnknown)
	}
	fixture.director.FindTaskByContextIDFn = func(contextID string) (*bosh.Task, error) {
		l.mu.Lock()
		defer l.mu.Unlock()

		l.contextIDs = append(l.contextIDs, contextID)
		l.lookups++

		if found == nil || l.lookups <= misses {
			return nil, nil //nolint:nilnil // no task found is a nil task without an error
		}

		return found, nil
	}

	fixture.broker.SetUpdateOutcomeWait(updateOutcomeWait)
}

// lookedUp returns the context IDs the update looked its task up by.
func (l *lostAnswer) lookedUp() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.contextIDs...)
}

// pastGraceClock returns a clock that reads now on its first call, which
// records when the outcome became unknown, and a time past the grace
// period after that.
func pastGraceClock() func() time.Time {
	start := time.Now()

	var calls atomic.Int64

	return func() time.Time {
		if calls.Add(1) == 1 {
			return start
		}

		return start.Add(bosh.UnknownOutcomeGrace + time.Minute)
	}
}

// lostAnswerCase is one way a lost deploy answer resolves.
type lostAnswerCase struct {
	name      string
	found     *bosh.Task
	taskState string
	clock     func() time.Time
	want      osbapi.OperationState
	outcome   string
	taskID    int
}

// requireResolved fails the spec unless the update settled as the case
// wants, looked its task up only by its own context ID, sent one deploy,
// deleted nothing, and left one change record.
func (c lostAnswerCase) requireResolved(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string, lost *lostAnswer, answer osbapi.LastOperationResponse) {
	t.Helper()

	if answer.State != c.want {
		t.Fatalf("the update settled as %+v, want %s", answer, c.want)
	}

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	contextID, _ := record["context_id"].(string)

	for _, looked := range lost.lookedUp() {
		if looked != contextID {
			t.Fatalf("the update looked its task up by context ID %q, want the recorded %q", looked, contextID)
		}
	}

	if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 1 {
		t.Fatalf("the update sent %d deploys, want 1", len(calls))
	}

	if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 || record["cleanup_pending"] != nil {
		t.Fatalf("the update deleted %v or marked a cleanup as owed: %v", calls, record)
	}

	if record["action"] != updateFailureAction || record["state"] == internalVault.TaskStateOutcomeUnknown {
		t.Fatalf("the task record is %v, want the update settled", record)
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != c.outcome || records[0].TaskID != c.taskID {
		t.Fatalf("the change records are %+v, want one %s update naming task %d", records, c.outcome, c.taskID)
	}

	if c.found == nil && !strings.Contains(answer.Description, contextID) {
		t.Fatalf("the never-deployed update was answered %q, want its context ID", answer.Description)
	}
}

// requireLastGood fails the spec unless a failed update was answered
// usable and repeatable and kept the stored parameters.
func requireLastGood(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string, params map[string]interface{}, answer osbapi.LastOperationResponse) {
	t.Helper()

	if answer.InstanceUsable == nil || !*answer.InstanceUsable || answer.UpdateRepeatable == nil || !*answer.UpdateRepeatable {
		t.Fatalf("the failed update was answered %+v, want usable and repeatable", answer)
	}

	root, _ := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, params) {
		t.Fatalf("the stored parameters are %v, want the last good %v", root, params)
	}
}

// A lost deploy answer keeps the update's goroutine and its claim, and
// the update resolves the outcome itself by its context ID. A task found
// that way is followed and settles the update, a task that failed fails
// it, and a lookup that finds no task past the grace period fails the
// update as never deployed. No path sends a second deploy or deletes the
// deployment.
func TestUpdate_OutcomeUnknown_ResolvesByContextID(t *testing.T) {
	t.Parallel()

	running := &bosh.Task{ID: updateTaskID, State: outcomeTaskProcessing}

	for _, lostCase := range []lostAnswerCase{
		{"a task found by its context ID succeeds", running, outcomeTaskDone, nil, osbapi.StateSucceeded, changeSucceeded, updateTaskID},
		{"a task found by its context ID fails", running, outcomeTaskError, nil, osbapi.StateFailed, outcomeStateFailed, updateTaskID},
		{"no task past the grace period", nil, outcomeTaskDone, pastGraceClock(), osbapi.StateFailed, outcomeStateFailed, 0},
	} {
		t.Run(lostCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			params := map[string]interface{}{policyKey: policyNoEvict}
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, params)

			script := &deployScript{}
			script.script(fixture, lostCase.taskState)

			lost := &lostAnswer{}
			lost.script(fixture, lostCase.found, 2)

			if lostCase.clock != nil {
				fixture.broker.SetUnknownOutcomeClock(lostCase.clock)
			}

			response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
				ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
			}, true)
			requireAccepted(t, response, async, err)

			answer := fixture.settled(ctx, t, instanceID)
			lostCase.requireResolved(ctx, t, fixture, instanceID, lost, answer)

			if lostCase.want == osbapi.StateFailed {
				requireLastGood(ctx, t, fixture, instanceID, params, answer)
			}
		})
	}
}
