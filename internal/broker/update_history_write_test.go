package broker_test

import (
	"context"
	"net/http"
	"testing"
)

// A success write that lands while its history entry can't be appended
// has settled the record, so the update releases its claim and leaves its
// change record, and isn't tried again while the history keeps failing.
func TestUpdate_SuccessHistoryFails_StillSettles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, proxy := faultedUpdateFixture(t)

	service, shutdown := context.WithCancel(ctx)

	t.Cleanup(func() {
		shutdown()
		fixture.broker.WaitForBackground(ctx)
	})

	fixture.broker.ServiceContext = service

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	held := fixture.holdTask(updateTaskID)
	instanceID := fixture.startFailingUpdate(ctx, t)

	held.waitFollowed(t)
	proxy.FailWrites(instanceID+"/metadata", http.StatusBadRequest, "history write refused by the test", -1)
	close(held.release)

	fixture.awaitReleased(t, instanceID)

	if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["state"] != changeSucceeded {
		t.Fatalf("the update's record is %v, want it succeeded", record)
	}

	fixture.requireOneChangeRecord(ctx, t, changeSucceeded)
}
