package broker_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	"blacksmith/shield"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// stagedLostAttention is what the reason says of staged values that don't
// decode.
const stagedLostAttention = "can't be read back"

// requireUpdateAttention fails t unless the Attention required entries b
// reports for updates are one for instanceID whose reason holds fragment,
// or none when fragment is empty.
func requireUpdateAttention(t *testing.T, b *broker.Broker, instanceID, fragment, when string) {
	t.Helper()

	entries := b.UpdateAttention()
	if fragment == "" {
		if len(entries) != 0 {
			t.Fatalf("%s, the update attention entries are %+v, want none", when, entries)
		}

		return
	}

	if len(entries) != 1 || entries[0].InstanceID != instanceID || entries[0].DeploymentName == "" || !strings.Contains(entries[0].Reason, fragment) || entries[0].Since.IsZero() {
		t.Fatalf("%s, the update attention entries are %+v, want one for %s naming its deployment and saying %q", when, entries, instanceID, fragment)
	}
}

// restartedBroker returns a broker of the next start over the fixture's
// Vault and director, with plans in its catalog, once its re-adoption
// ran, with its background work still open to new operations. The spec's
// cleanup shuts the broker down and waits for that work before the
// fixture's Vault closes, so no goroutine of it outlives the spec.
func restartedBroker(ctx context.Context, t *testing.T, fixture *updateFixture, plans map[string]services.Plan) *broker.Broker {
	t.Helper()

	serviceCtx, shutdown := context.WithCancel(ctx)

	next := &broker.Broker{BOSH: fixture.director, Vault: fixture.vault, Plans: plans, Shield: &shield.NoopClient{}, ServiceContext: serviceCtx}
	next.SetChanges(fixture.changes)

	t.Cleanup(func() {
		shutdown()

		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()

		next.WaitForBackground(waitCtx)
	})

	err := next.ReadoptInFlightOperations(ctx)
	if err != nil {
		t.Fatalf("re-adoption failed: %v", err)
	}

	return next
}

// startedBroker returns a broker of the next start as restartedBroker
// does, once the work its re-adoption started has finished. The broker
// then takes no new background work, so a spec that sends it an
// operation uses restartedBroker.
func startedBroker(ctx context.Context, t *testing.T, fixture *updateFixture, plans map[string]services.Plan) *broker.Broker {
	t.Helper()

	next := restartedBroker(ctx, t, fixture, plans)
	next.WaitForBackground(ctx)

	return next
}

// acceptUpdate has next accept an update of instanceID.
func acceptUpdate(ctx context.Context, t *testing.T, next *broker.Broker, instanceID string) {
	t.Helper()

	response, async, err := next.Update(ctx, instanceID, failingUpdateRequest(), true)
	requireAccepted(t, response, async, err)
	next.WaitForBackground(ctx)
}

// acceptDeprovision has next accept the deprovision of instanceID.
func acceptDeprovision(ctx context.Context, t *testing.T, next *broker.Broker, instanceID string) {
	t.Helper()

	_, async, err := next.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: recordedNameService, PlanID: openUpdatePlan}, true)
	if err != nil || !async {
		t.Fatalf("the deprovision was answered async %v (%v), want accepted", async, err)
	}

	next.WaitForBackground(ctx)
}

// An update re-adoption can't carry through is listed in Attention
// required once it settles, with the reason an operator needs, because
// the tenant alone can't put it right. The entry is found again at every
// start while its record still holds it. An entry for a plan that left
// the catalog goes once the plan is back, and the entry for staged values
// that don't decode goes once the next update or a deprovision is
// accepted.
func TestReadopt_UnresumableUpdate_ListedForAttention(t *testing.T) {
	t.Parallel()

	for _, unresumable := range []struct {
		name     string
		spoil    func(ctx context.Context, t *testing.T, fixture *updateFixture, instanceID string)
		reason   string
		restored string
		accept   func(ctx context.Context, t *testing.T, next *broker.Broker, instanceID string)
	}{
		{planLeftCatalog, dropOpenPlan, "an operator restores the plan", "", acceptUpdate},
		{"its staged values don't decode, and an update follows", garbleStagedUpdate, stagedLostAttention, stagedLostAttention, acceptUpdate},
		{"its staged values don't decode, and a deprovision follows", garbleStagedUpdate, stagedLostAttention, stagedLostAttention, acceptDeprovision},
	} {
		t.Run(unresumable.name, func(t *testing.T) {
			t.Parallel()

			fixture := newUpdateFixture(t)
			ctx := context.Background()
			instanceID := fixture.provisioned(ctx, t, openUpdatePlan, lastGoodParams())

			interruptedUpdate(ctx, t, fixture, instanceID)
			recordedDeployTask(ctx, t, fixture, instanceID)
			savedRollbackManifest(ctx, t, fixture, instanceID)
			unresumable.spoil(ctx, t, fixture, instanceID)
			(&deployScript{}).script(fixture, outcomeTaskDone)

			requireUpdateAttention(t, fixture.broker, instanceID, "", "before re-adoption")

			err := fixture.broker.ReadoptInFlightOperations(ctx)
			if err != nil {
				t.Fatalf("re-adoption failed: %v", err)
			}

			fixture.settled(ctx, t, instanceID)
			requireUpdateAttention(t, fixture.broker, instanceID, unresumable.reason, "once the update settled")

			// The next start, with the catalog as it was, finds it again.
			requireUpdateAttention(t, startedBroker(ctx, t, fixture, fixture.broker.Plans), instanceID, unresumable.reason, "at the next start")

			// A start with the plan back in the catalog lists only what
			// the plan's return doesn't put right.
			restored := restartedBroker(ctx, t, fixture, updatePlans())
			requireUpdateAttention(t, restored, instanceID, unresumable.restored, "once the plan is back")

			unresumable.accept(ctx, t, restored, instanceID)
			requireUpdateAttention(t, restored, instanceID, "", "once the next operation was accepted")
		})
	}
}
