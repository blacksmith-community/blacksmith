package broker_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"blacksmith/internal/bosh"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// A re-adopted update whose task succeeded, and whose deployment's
// manifest the director never answers, stops reading it at its bound and
// commits the staged values, keeping the stored manifest, so the claim
// isn't held for good after the task ended.
func TestReadopt_DeployedManifestUnreadable_CommitsAtItsBound(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	service, shutdown := context.WithCancel(ctx)

	t.Cleanup(func() {
		shutdown()
		fixture.broker.WaitForBackground(ctx)
	})

	fixture.broker.ServiceContext = service
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})

	interruptedUpdate(ctx, t, fixture, instanceID)
	recordedDeployTask(ctx, t, fixture, instanceID)

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	var before map[string]interface{}

	_, err := fixture.vault.Get(ctx, instanceID+"/manifest", &before)
	if err != nil {
		t.Fatalf("failed to read the stored manifest: %v", err)
	}

	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return nil, fmt.Errorf("reading deployment %s: %w", name, errDirectorUnreachable)
	}
	fixture.broker.SetUpdateManifestBound(followBound)

	err = fixture.broker.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	fixture.awaitReleased(t, instanceID)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the re-adopted update settled as %+v, want succeeded", answer)
	}

	root, _ := fixture.storedParameters(ctx, t, instanceID)
	if want := map[string]interface{}{policyKey: policyLRU}; !reflect.DeepEqual(root, want) {
		t.Fatalf("the stored parameters are %v, want %v", root, want)
	}

	var after map[string]interface{}

	_, err = fixture.vault.Get(ctx, instanceID+"/manifest", &after)
	if err != nil || after["manifest"] != before["manifest"] {
		t.Fatalf("the stored manifest changed to %v (%v), want it kept", after["manifest"], err)
	}

	fixture.requireOneChangeRecord(ctx, t, changeSucceeded)
	fixture.requireReleased(t, instanceID)
}
