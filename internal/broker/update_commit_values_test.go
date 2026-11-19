package broker_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// A write of the update's values that fails after its deploy succeeded is
// tried again, so the update settles as succeeded with the new values
// stored, and never as failed while the deployment runs them.
func TestUpdate_ValuesWriteFails_RetriesUntilCommitted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.startFailingUpdate(ctx, t)

	held.waitFollowed(t)
	proxy.FailWrites(instanceID+"/"+openUpdatePlan+"-"+instanceID, http.StatusBadRequest, "provision details write refused by the test", settleWriteFailures)
	close(held.release)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	var stored map[string]interface{}

	_, err := fixture.vault.Get(ctx, instanceID+"/"+openUpdatePlan+"-"+instanceID, &stored)
	if err != nil {
		t.Fatalf("failed to read the provision details: %v", err)
	}

	details, _ := stored["details"].(map[string]interface{})
	if parameters, _ := details["parameters"].(map[string]interface{}); parameters[policyKey] == policyNoEvict {
		t.Fatalf("the provision details are %v, want the update's parameters", stored)
	}

	fixture.requireOneChangeRecord(ctx, t, changeSucceeded)
	fixture.requireReleased(t, instanceID)
}
