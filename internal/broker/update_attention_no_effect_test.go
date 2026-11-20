package broker_test

import (
	"context"
	"testing"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// After an update whose staged values were lost, the stored values may not
// match what the deployment runs, because the lost update's deploy may
// have finished. An update that would change nothing against the stored
// values, or would change only the context, therefore deploys them, which
// is what puts the deployment back in step, and only then is the instance
// taken out of Attention required, then and at the next start.
func TestUpdate_NoEffectAfterStagedLost_DeploysAndClearsAttention(t *testing.T) {
	t.Parallel()

	for _, request := range []struct {
		name    string
		details osbapi.UpdateRequest
	}{
		{"its parameters equal the stored ones", osbapi.UpdateRequest{ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict}}},
		{"it changes only the context", osbapi.UpdateRequest{ServiceID: recordedNameService, Context: map[string]any{"platform": platformCloudFoundry, orgNameKey: "new-org"}}},
	} {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)
			savedRollbackManifest(ctx, t, fixture, instanceID)
			garbleStagedUpdate(ctx, t, fixture, instanceID)

			script := &deployScript{}
			script.script(fixture, outcomeTaskDone)

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.settled(ctx, t, instanceID)

			// The broker that waited for re-adoption takes no new work, so
			// the update goes to the next start's broker.
			next := restartedBroker(ctx, t, fixture, updatePlans())
			requireUpdateAttention(t, next, instanceID, stagedLostAttention, "before the update")

			response, async, err := next.Update(ctx, instanceID, request.details, true)
			requireAccepted(t, response, async, err)
			next.WaitForBackground(ctx)

			answer, err := next.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})
			if err != nil || answer.State != osbapi.StateSucceeded {
				t.Fatalf("the update was answered %+v (%v), want succeeded", answer, err)
			}

			if sent := script.sent(); len(sent) != 1 {
				t.Fatalf("the update sent %d deploys, want one", len(sent))
			}

			requireUpdateAttention(t, next, instanceID, "", "once the update deployed the stored values")
			requireUpdateAttention(t, startedBroker(ctx, t, fixture, updatePlans()), instanceID, "", "at the next start")
		})
	}
}
