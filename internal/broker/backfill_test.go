package broker_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// backfillStatusKey is the index entry field that marks a tombstone.
const backfillStatusKey = "status"

// errDirectorDown is the director failure the backfill must survive.
var errDirectorDown = errors.New("the director is down")

// backfillIndex is an index of instances provisioned before the broker
// recorded deployment names, beside entries the backfill must leave alone.
func backfillIndex() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		// Its deployment exists under the name the entry resolves to today.
		"legacy": {serviceIDKey: recordedNameService, recordedPlanKey: recordedNameCreated},
		// It records a name already, of a deployment created under another plan.
		"named": {serviceIDKey: recordedNameService, recordedPlanKey: "redis-large", deploymentNameKey: "redis-small-named"},
		// The director has no deployment under the name it resolves to today.
		"gone": {serviceIDKey: recordedNameService, recordedPlanKey: recordedNameCreated},
		// The director has only a deployment of another legacy shape, and
		// recording it would move the instance off the name it resolves to.
		"other-shape": {serviceIDKey: recordedNameService, recordedPlanKey: recordedNameCreated},
		// A tombstone owns nothing, even with a live deployment of its name.
		"tombstone": {serviceIDKey: recordedNameService, recordedPlanKey: recordedNameCreated, backfillStatusKey: indexStatusDeleted},
		// An entry with no plan resolves to no deployment at all.
		"no-plan": {serviceIDKey: recordedNameService},
	}
}

// backfillDirector lists the deployments the director has, and fails a
// list when down is set.
func backfillDirector(down bool) *testutil.ScriptedBOSHDirector {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentsFn = func() ([]bosh.Deployment, error) {
		if down {
			return nil, errDirectorDown
		}

		return []bosh.Deployment{
			{Name: "redis-small-legacy"},
			{Name: "redis-small-named"},
			{Name: "redis-small_other-shape"},
			{Name: "redis-small-tombstone"},
			{Name: "unrelated-deployment"},
		}, nil
	}

	return director
}

// seedBackfillIndex writes backfillIndex to a fresh Vault and returns it.
func seedBackfillIndex(t *testing.T) *internalVault.Vault {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	for instanceID, entry := range backfillIndex() {
		err = vaultClient.Index(context.Background(), instanceID, entry)
		if err != nil {
			t.Fatalf("failed to index %s: %v", instanceID, err)
		}
	}

	return vaultClient
}

// readBackfillIndex returns every entry of the index.
func readBackfillIndex(t *testing.T, vaultClient *internalVault.Vault) map[string]interface{} {
	t.Helper()

	idx, err := vaultClient.GetIndex(context.Background(), "db")
	if err != nil {
		t.Fatalf("failed to read the index: %v", err)
	}

	return idx.Data
}

// requireNoDirectorChange fails the test when the backfill asked the
// director to create or delete a deployment.
func requireNoDirectorChange(t *testing.T, director *testutil.ScriptedBOSHDirector) {
	t.Helper()

	for _, method := range []string{directorCreateDeployment, directorDeleteDeployment} {
		if calls := director.Calls(method); len(calls) != 0 {
			t.Errorf("the backfill called %s with %v", method, calls)
		}
	}
}

// An entry indexed before the broker recorded deployment names gets the
// name it resolves to today, once the director confirms that deployment
// exists, so a later plan change can't move it to a name built from the
// new plan. The backfill writes only index entries and never asks the
// director to change a deployment. Every other entry is left as it is,
// and a second run changes nothing.
func TestBackfill_RecordsLegacyDeploymentName(t *testing.T) {
	t.Parallel()

	vaultClient := seedBackfillIndex(t)
	director := backfillDirector(false)
	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient}

	result, err := brokerInstance.BackfillDeploymentNames(context.Background())
	if err != nil {
		t.Fatalf("the backfill failed: %v", err)
	}

	if !reflect.DeepEqual(result.Recorded, []string{"legacy"}) {
		t.Errorf("the backfill recorded names for %v, want [legacy]", result.Recorded)
	}

	if !reflect.DeepEqual(result.Unconfirmed, []string{"gone", "other-shape"}) {
		t.Errorf("the backfill left %v unconfirmed, want [gone other-shape]", result.Unconfirmed)
	}

	want := backfillIndex()
	want["legacy"][deploymentNameKey] = "redis-small-legacy"

	after := readBackfillIndex(t, vaultClient)
	for instanceID, wantEntry := range want {
		if got := after[instanceID]; !reflect.DeepEqual(got, interface{}(wantEntry)) {
			t.Errorf("entry %s is %v after the backfill, want %v", instanceID, got, wantEntry)
		}
	}

	again, err := brokerInstance.BackfillDeploymentNames(context.Background())
	if err != nil {
		t.Fatalf("the second backfill failed: %v", err)
	}

	if len(again.Recorded) != 0 {
		t.Errorf("a second backfill recorded names for %v, want none", again.Recorded)
	}

	if second := readBackfillIndex(t, vaultClient); !reflect.DeepEqual(second, after) {
		t.Errorf("a second backfill changed the index from %v to %v", after, second)
	}

	requireNoDirectorChange(t, director)
}

// A director that can't list its deployments can't confirm any name, so
// the backfill records nothing and reports the failure for the next boot.
func TestBackfill_RecordsNothingWhenTheDirectorFails(t *testing.T) {
	t.Parallel()

	vaultClient := seedBackfillIndex(t)
	before := readBackfillIndex(t, vaultClient)
	director := backfillDirector(true)

	_, err := (&broker.Broker{BOSH: director, Vault: vaultClient}).BackfillDeploymentNames(context.Background())
	if !errors.Is(err, errDirectorDown) {
		t.Errorf("the backfill returned %v, want the director's failure", err)
	}

	if after := readBackfillIndex(t, vaultClient); !reflect.DeepEqual(after, before) {
		t.Errorf("a backfill with the director down changed the index from %v to %v", before, after)
	}

	requireNoDirectorChange(t, director)
}
