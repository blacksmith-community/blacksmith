package broker_test

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// brokenPlan is a plan whose template grabs a key it never sets, so it
// renders from no parameters at all.
const brokenPlan = "update-broken"

// addBrokenPlan adds the broken plan to the fixture's catalog.
func (f *updateFixture) addBrokenPlan() {
	open := f.broker.Plans[recordedNameService+"/"+openUpdatePlan]

	broken := maps.Clone(open.Manifest)
	broken["vm_type"] = "(( grab meta.unset_vm_type ))"

	open.ID, open.Name, open.Manifest = brokenPlan, brokenPlan, broken
	f.broker.Plans[recordedNameService+"/"+brokenPlan] = open
}

// A plan whose template renders neither the stored parameters nor the
// requested ones is most likely broken on the operator's side, so the
// update is refused with a 500 that says so, and never with a 400 that
// blames only the tenant's parameters. The requested parameters may be at
// fault as well, so the answer names them too. The refusal is recorded
// with its own reason.
func TestUpdate_BrokenPlanTemplate_500NotTheTenantsFault(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	fixture.addBrokenPlan()

	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, brokenPlan, map[string]interface{}{policyKey: policyNoEvict})
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, true)

	var osbErr *osbapi.OSBError
	if errors.Is(err, osbapi.ErrInvalidParameters) || errors.As(err, &osbErr) || !strings.Contains(err.Error(), "operator") || !strings.Contains(err.Error(), "requested parameters") {
		t.Fatalf("the update of a broken plan was answered %v, want a 500 that names the operator and the requested parameters", err)
	}

	fixture.requireRefused(ctx, t, instanceID, before, err, func(error) bool { return true })

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Reason != "plan_render_failed" {
		t.Fatalf("the change records are %+v, want one refusal for plan_render_failed", records)
	}
}
