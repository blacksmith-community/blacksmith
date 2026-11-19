package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The plans the update specs run on. The open plan has no schema, and
// the strict plan's schema declares the one parameter its manifest reads.
const (
	openUpdatePlan   = "update-open"
	strictUpdatePlan = "update-strict"
	sizedUpdatePlan  = "update-sized"
	pinnedUpdatePlan = "update-pinned"
	updateDirector   = "update-director-uuid"
)

// The parameter keys and values the update specs send and read.
const (
	policyKey       = "maxmemory-policy"
	policyLRU       = "allkeys-lru"
	policyNoEvict   = "noeviction"
	noteKey         = "note"
	sizeKey         = "size"
	orgNameKey      = "organization_name"
	indexStatusKey  = "status"
	parametersField = "parameters"
)

// updatePlans returns the catalog the update specs run on. Every plan's
// manifest puts the maxmemory-policy parameter into a job property, and
// the sized plan also reads the size parameter where graft takes it as a
// key.
func updatePlans() map[string]services.Plan {
	service := &services.Service{ID: recordedNameService, Name: recordedNameService}
	template := func() map[string]interface{} {
		return map[string]interface{}{
			"meta": map[string]interface{}{"sizes": map[string]interface{}{routedPlan: "vm-small"}},
			manifestInstanceGroupsKey: []interface{}{map[string]interface{}{
				manifestNameKey: recordedNameService, "instances": 1,
				"jobs": []interface{}{map[string]interface{}{
					manifestNameKey: recordedNameService, "release": recordedNameService,
					"properties": map[string]interface{}{policyKey: `(( grab meta.params.maxmemory-policy || "allkeys-lru" ))`},
				}},
			}},
		}
	}

	sized := template()
	sized["vm_type"] = "(( grab meta.sizes[meta.params.size] ))"

	// The pinned plan names its releases and stemcell at versions older
	// than the live deployment's, as a template does once an upgrade has
	// moved the instance forward.
	pinned := template()
	pinned[releasesKey] = []interface{}{
		map[string]interface{}{manifestNameKey: recordedNameService, manifestVersion: "1.0"},
		map[string]interface{}{manifestNameKey: "added", manifestVersion: "3.0"},
		map[string]interface{}{manifestNameKey: "newer-in-template", manifestVersion: "5.2"},
	}
	pinned["stemcells"] = []interface{}{map[string]interface{}{"alias": stemcellAlias, stemcellOSKey: liveStemcellOS, manifestVersion: "1.5"}}

	plans := withParameterSchema(map[string]services.Plan{
		recordedNameService + "/" + strictUpdatePlan: {ID: strictUpdatePlan, Name: strictUpdatePlan, Type: recordedNameService, Manifest: template(), Service: service},
	})
	plans[recordedNameService+"/"+openUpdatePlan] = services.Plan{ID: openUpdatePlan, Name: openUpdatePlan, Type: recordedNameService, Manifest: template(), Service: service}
	plans[recordedNameService+"/"+sizedUpdatePlan] = services.Plan{ID: sizedUpdatePlan, Name: sizedUpdatePlan, Type: recordedNameService, Manifest: sized, Service: service}
	plans[recordedNameService+"/"+pinnedUpdatePlan] = services.Plan{ID: pinnedUpdatePlan, Name: pinnedUpdatePlan, Type: recordedNameService, Manifest: pinned, Service: service}

	return plans
}

// updateFixture is a broker over a Vault dev server and a scripted
// director, with a change store the specs read.
type updateFixture struct {
	broker   *broker.Broker
	vault    *internalVault.Vault
	director *testutil.ScriptedBOSHDirector
	changes  *audit.ChangeStore
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()

	director := testutil.NewScriptedBOSHDirector()
	director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: updateDirector}, nil }
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

	vaultClient := routingVault(t)
	changes := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{})
	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient, Plans: updatePlans()}
	brokerInstance.SetChanges(changes)

	return &updateFixture{broker: brokerInstance, vault: vaultClient, director: director, changes: changes}
}

// provisioned records an instance on planID whose provision succeeded,
// with params as its stored parameters, the way a provision leaves it.
func (f *updateFixture) provisioned(ctx context.Context, t *testing.T, planID string, params map[string]interface{}) string {
	t.Helper()

	instanceID := fmt.Sprintf("update-%s-%d", planID, time.Now().UnixNano())
	deployment := planID + "-" + instanceID

	err := f.vault.Index(ctx, instanceID, map[string]interface{}{
		serviceIDKey: recordedNameService, recordedPlanKey: planID, recordedDeploymentKey: deployment, orgNameKey: "old-org",
	})
	if err == nil {
		err = f.vault.Put(ctx, instanceID, map[string]interface{}{
			serviceIDKey: recordedNameService, recordedPlanKey: planID, recordedDeploymentKey: deployment,
			parametersField: params, "context": map[string]interface{}{orgNameKey: "old-org"},
		})
	}

	if err == nil {
		err = f.vault.Put(ctx, instanceID+"/"+deployment, map[string]interface{}{
			detailsKey: map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: planID, parametersField: params},
		})
	}

	if err == nil {
		err = f.vault.TrackProgressWithMintedDeployment(ctx, instanceID, "provision", "Creating BOSH deployment", "context-provision", planID, 0, nil)
	}

	if err == nil {
		err = f.vault.TrackProgress(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", 41, nil)
	}

	if err == nil {
		err = f.vault.TrackSucceeded(ctx, instanceID, "provision", "Provisioned", 41)
	}

	if err != nil {
		t.Fatalf("failed to record the provisioned instance: %v", err)
	}

	return instanceID
}

// snapshot reads every record an update may write, so a spec can show a
// refused update wrote none of them.
func (f *updateFixture) snapshot(ctx context.Context, t *testing.T, instanceID string) string {
	t.Helper()

	var parts []string

	instance, _, err := f.vault.FindInstance(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}

	parts = append(parts, fmt.Sprintf("%+v", instance))

	for _, path := range []string{instanceID, instanceID + "/task"} {
		var record map[string]interface{}

		_, err = f.vault.Get(ctx, path, &record)
		if err != nil {
			t.Fatal(err)
		}

		parts = append(parts, fmt.Sprint(record))
	}

	if instance != nil {
		var details map[string]interface{}

		_, err = f.vault.Get(ctx, instanceID+"/"+instance.DeploymentNameFor(instanceID), &details)
		if err != nil {
			t.Fatal(err)
		}

		parts = append(parts, fmt.Sprint(details))
	}

	return strings.Join(parts, "\n")
}

// storedParameters returns the parameters the root record and the stored
// provision request hold.
func (f *updateFixture) storedParameters(ctx context.Context, t *testing.T, instanceID string) (interface{}, interface{}) {
	t.Helper()

	var root, details map[string]interface{}

	_, err := f.vault.Get(ctx, instanceID, &root)
	if err == nil {
		planID, _ := root[recordedPlanKey].(string)
		_, err = f.vault.Get(ctx, instanceID+"/"+planID+"-"+instanceID, &details)
	}

	if err != nil {
		t.Fatal(err)
	}

	request, _ := details[detailsKey].(map[string]interface{})

	return root[parametersField], request[parametersField]
}

// updateRecords returns the instance change records the store saved.
func (f *updateFixture) updateRecords(ctx context.Context, t *testing.T) []audit.ChangeRecord {
	t.Helper()

	f.changes.Wait()

	records, err := f.changes.List(ctx, audit.ScopeInstances, audit.MaxChangeList)
	if err != nil {
		t.Fatal(err)
	}

	return records
}

// requireRefused fails the spec unless err is the refusal want accepts,
// and the refused update wrote nothing and deployed nothing.
func (f *updateFixture) requireRefused(ctx context.Context, t *testing.T, instanceID, before string, err error, want func(error) bool) {
	t.Helper()

	if err == nil || !want(err) {
		t.Fatalf("the update was answered %v", err)
	}

	if after := f.snapshot(ctx, t, instanceID); after != before {
		t.Fatalf("the refused update changed the records from\n%s\nto\n%s", before, after)
	}

	if calls := f.director.Calls("CreateDeployment"); len(calls) != 0 {
		t.Fatalf("the refused update sent %d deploys", len(calls))
	}
}

// isUnprocessable reports whether err is an OSB error answered 422.
func isUnprocessable(err error) bool {
	var osbErr *osbapi.OSBError

	return errors.As(err, &osbErr) && osbErr.StatusCode == http.StatusUnprocessableEntity
}

func isError(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}

// A key the manifest doesn't read changes nothing in the deployment, so
// the update is committed with no deploy and answered 200.
func TestUpdate_NoEffect_Returns200(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{noteKey: "kept for the tenant"},
	}, true)
	if err != nil || async || response.Operation != "" {
		t.Fatalf("the update was answered %+v, async %v, %v, want a synchronous 200", response, async, err)
	}

	want := map[string]interface{}{policyKey: policyNoEvict, noteKey: "kept for the tenant"}

	root, details := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, want) || !reflect.DeepEqual(details, want) {
		t.Fatalf("the committed parameters are %v and %v, want %v", root, details, want)
	}

	if calls := fixture.director.Calls("CreateDeployment"); len(calls) != 0 {
		t.Fatalf("a no-effect update sent %d deploys", len(calls))
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != "no-op" || records[0].Keys != noteKey || records[0].Action != updateFailureAction {
		t.Fatalf("the change records are %+v, want one no-op update naming the key", records)
	}

	if strings.Contains(fmt.Sprintf("%+v", records[0]), "kept for the tenant") {
		t.Fatalf("the change record holds a parameter value: %+v", records[0])
	}
}

// An update that changes only the context writes it to the instance's
// records and its index entry, and is answered 200 with no deploy.
func TestUpdate_ContextOnly_Returns200(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})

	contextData := map[string]interface{}{"platform": "cloudfoundry", orgNameKey: "new-org", "space_name": "new-space", "instance_name": "renamed"}

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService, Context: contextData}, false)
	if err != nil || async || response.Operation != "" {
		t.Fatalf("the update was answered %+v, async %v, %v, want a synchronous 200", response, async, err)
	}

	index, err := fixture.vault.GetIndex(ctx, "db")
	if err != nil {
		t.Fatal(err)
	}

	entry, _ := index.Data[instanceID].(map[string]interface{})
	for key, value := range contextData {
		if entry[key] != value {
			t.Fatalf("the index entry holds %s %v, want %v: %v", key, entry[key], value, entry)
		}
	}

	var root map[string]interface{}

	_, err = fixture.vault.Get(ctx, instanceID, &root)
	if err != nil || !reflect.DeepEqual(root["context"], contextData) {
		t.Fatalf("the root record holds context %v (%v), want %v", root["context"], err, contextData)
	}

	if calls := fixture.director.Calls("CreateDeployment"); len(calls) != 0 {
		t.Fatalf("a context-only update sent %d deploys", len(calls))
	}

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Outcome != "no-op" {
		t.Fatalf("the change records are %+v, want one no-op update", records)
	}
}

// A value the plan's schema refuses is a 400, and nothing is written.
func TestUpdate_InvalidParameters_400(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, strictUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: "volatile-ttl"},
	}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrInvalidParameters))

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != "refused" || records[0].Reason != "invalid_parameters" {
		t.Fatalf("the change records are %+v, want one refusal for invalid parameters", records)
	}
}

// A key the plan's schema doesn't declare is a 400.
func TestUpdate_UnknownKeyWithSchema_400(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, strictUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{noteKey: "undeclared"},
	}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrInvalidParameters))
}

// A parameter where the plan reads a key is refused by graft, and the
// update gets a 400 rather than a failed deploy.
func TestUpdate_GraftLiteralRefused_400(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, sizedUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{sizeKey: routedPlan},
	}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrInvalidParameters))

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Reason != "literal_refused" {
		t.Fatalf("the change records are %+v, want one refusal graft made", records)
	}
}

// An update that needs a deploy gets 422 AsyncRequired from a platform
// that won't accept an asynchronous answer.
func TestUpdate_AsyncRequired_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict})
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, false)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrAsyncRequired))
}

// An instance the index doesn't have, a tombstone, and an instance whose
// deprovision was accepted or finished each get a 422.
func TestUpdate_TombstonedOrDeleted_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	cases := map[string]func(instanceID string) error{
		"not indexed": func(string) error { return nil },
		"tombstone": func(instanceID string) error {
			return fixture.vault.Index(ctx, instanceID, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: openUpdatePlan, indexStatusKey: indexStatusDeleted, indexStatusDeleted: true})
		},
		"deprovision requested": func(instanceID string) error {
			return fixture.vault.Index(ctx, instanceID, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: openUpdatePlan, indexStatusKey: "deprovision_requested"})
		},
		"deprovision recorded": func(instanceID string) error {
			err := fixture.vault.Index(ctx, instanceID, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: openUpdatePlan})
			if err != nil {
				return fmt.Errorf("failed to index the instance: %w", err)
			}

			return fixture.vault.TrackProgressWithContextID(ctx, instanceID, "deprovision", "Deleting", "context-delete", 0, nil)
		},
	}

	for name, record := range cases {
		instanceID := fmt.Sprintf("update-gone-%s-%d", strings.ReplaceAll(name, " ", "-"), time.Now().UnixNano())

		err := record(instanceID)
		if err != nil {
			t.Fatal(err)
		}

		before := fixture.snapshot(ctx, t, instanceID)

		_, _, err = fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU}}, true)
		fixture.requireRefused(ctx, t, instanceID, before, err, isUnprocessable)
	}
}

// An instance whose provision is still running or failed gets a 422 that
// says it is not ready.
func TestUpdate_ProvisionNotSucceeded_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	for _, failed := range []bool{false, true} {
		instanceID := fmt.Sprintf("update-not-ready-%v-%d", failed, time.Now().UnixNano())
		indexLiveInstance(ctx, t, fixture.vault, instanceID)

		err := fixture.vault.TrackProgressWithContextID(ctx, instanceID, "provision", "BOSH deployment in progress (task 41)", "context-41", 41, nil)
		if err == nil && failed {
			err = fixture.vault.TrackFailed(ctx, instanceID, "provision", "BOSH deployment task 41 is error", "context-41", 41)
		}

		if err != nil {
			t.Fatal(err)
		}

		before := fixture.snapshot(ctx, t, instanceID)

		_, _, err = fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService}, true)
		fixture.requireRefused(ctx, t, instanceID, before, err, func(err error) bool {
			return isUnprocessable(err) && strings.Contains(err.Error(), "not ready")
		})
	}
}

// An instance that still owes a failed provision's cleanup gets a 422
// until the cleanup settles, so the update never drops the mark.
func TestUpdate_CleanupOwed_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fmt.Sprintf("update-cleanup-owed-%d", time.Now().UnixNano())
	indexLiveInstance(ctx, t, fixture.vault, instanceID)

	err := fixture.vault.TrackProgressWithMintedDeployment(ctx, instanceID, "provision", "Creating", "context-41", routedPlan, 41, nil)
	if err == nil {
		err = fixture.vault.TrackFailedCleanupPending(ctx, instanceID, "provision", "failed", "context-41", 41)
	}

	if err != nil {
		t.Fatal(err)
	}

	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err = fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, func(err error) bool {
		return isUnprocessable(err) && strings.Contains(err.Error(), "cleaned up")
	})
}

// A plan change gets a 422 that says plan changes aren't supported yet.
func TestUpdate_PlanChange_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService, PlanID: strictUpdatePlan}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, func(err error) bool {
		return isUnprocessable(err) && strings.Contains(err.Error(), "Plan changes are not supported yet")
	})

	if records := fixture.updateRecords(ctx, t); len(records) != 1 || records[0].Reason != "plan_change" || records[0].New != strictUpdatePlan {
		t.Fatalf("the change records are %+v, want one plan-change refusal", records)
	}
}

// A request that carries maintenance_info gets 422 MaintenanceInfoConflict,
// because the catalog publishes none.
func TestUpdate_MaintenanceInfo_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, MaintenanceInfo: &osbapi.MaintenanceInfo{Version: "1.2.3"},
	}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrMaintenanceInfoConflict))
}

// A request that names another service is a 400.
func TestUpdate_ServiceMismatch_400(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: "another-service"}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, isError(osbapi.ErrBadRequest))
}
