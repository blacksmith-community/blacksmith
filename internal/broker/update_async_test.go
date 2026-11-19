package broker_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/audit"
	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"

	"github.com/fivetwenty-io/graft/pkg/yaml"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The task the scripted director starts for an update's deploy, and the
// values the update specs read back.
const (
	updateTaskID    = 77
	changeSucceeded = "succeeded"
	stemcellAlias   = "default"
	taskCancelled   = "cancelled"
	manifestVersion = "version"
	stemcellOSKey   = "os"
	liveStemcellOS  = "ubuntu-noble"
	releasesKey     = "releases"
	// platformCloudFoundry and userIDKey make up the originating identity
	// of a platform user.
	platformCloudFoundry = "cloudfoundry"
	userIDKey            = "user_id"
)

// liveManifest is the manifest the director holds for the deployment. Its
// releases and stemcell are newer than the pinned plan's, except for the
// release the template names at a newer version.
const liveManifest = `name: deployment
releases:
- name: redis
  version: "1.4"
- name: newer-in-template
  version: "5.1"
stemcells:
- alias: default
  os: ubuntu-noble
  version: "1.9"
`

// deployScript scripts the director for an update's deploy. It records
// each manifest the update sends, and the deploy task ends in state.
type deployScript struct {
	mu        sync.Mutex
	manifests []string
	release   chan struct{}
}

// script points the fixture's director at the script, whose task ends in
// state once release is closed, or at once when release is nil.
func (d *deployScript) script(fixture *updateFixture, state string) {
	fixture.director.GetReleasesFn = func() ([]bosh.Release, error) { return nil, nil }
	fixture.director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name, Manifest: liveManifest}, nil
	}
	fixture.director.UpdateDeploymentFn = func(_, manifest string) (*bosh.Task, error) {
		d.mu.Lock()
		defer d.mu.Unlock()

		d.manifests = append(d.manifests, manifest)

		return &bosh.Task{ID: updateTaskID, State: outcomeTaskProcessing}, nil
	}
	fixture.director.WaitForTaskFn = func(taskID int) (*bosh.Task, error) {
		if d.release != nil {
			<-d.release
		}

		return &bosh.Task{ID: taskID, State: state}, nil
	}
}

// sent returns the manifests the update deployed.
func (d *deployScript) sent() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.manifests...)
}

// settled waits for the update's goroutine, and returns the update's
// last_operation answer.
func (f *updateFixture) settled(ctx context.Context, t *testing.T, instanceID string) osbapi.LastOperationResponse {
	t.Helper()

	f.broker.WaitForBackground(ctx)

	answer, err := f.broker.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: updateFailureAction})
	if err != nil {
		t.Fatalf("the update poll failed: %v", err)
	}

	return answer
}

// requireAccepted fails the spec unless the update was accepted as an
// asynchronous update.
func requireAccepted(t *testing.T, response osbapi.UpdateResponse, async bool, err error) {
	t.Helper()

	if err != nil || !async || response.Operation != "update" {
		t.Fatalf("the update was answered %+v, async %v, %v, want 202 with operation update", response, async, err)
	}
}

// deployedPolicy returns the maxmemory-policy the deployed manifest's job
// property holds.
func deployedPolicy(t *testing.T, deployed string) interface{} {
	t.Helper()

	var doc map[string]interface{}

	err := yaml.Unmarshal([]byte(deployed), &doc)
	if err != nil {
		t.Fatal(err)
	}

	groups, _ := doc[manifestInstanceGroupsKey].([]interface{})
	group, _ := groups[0].(map[string]interface{})
	jobs, _ := group["jobs"].([]interface{})
	job, _ := jobs[0].(map[string]interface{})
	properties, _ := job["properties"].(map[string]interface{})

	return properties[policyKey]
}

// A parameter the manifest reads redeploys the instance with the merged
// parameters, and the success commits them, the deployed manifest, and a
// change record.
func TestUpdate_ParameterMerge_Redeploys(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict, noteKey: "kept"})

	script := &deployScript{}
	script.script(fixture, "done")

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, true)
	requireAccepted(t, response, async, err)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	sent := script.sent()
	if len(sent) != 1 || deployedPolicy(t, sent[0]) != policyLRU {
		t.Fatalf("the update deployed %q, want one deploy with %s", sent, policyLRU)
	}

	if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 1 || calls[0] != openUpdatePlan+"-"+instanceID {
		t.Fatalf("the update deployed %v, want the recorded deployment", calls)
	}

	want := map[string]interface{}{policyKey: policyLRU, noteKey: "kept"}

	root, details := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, want) || !reflect.DeepEqual(details, want) {
		t.Fatalf("the committed parameters are %v and %v, want %v", root, details, want)
	}

	var stored map[string]interface{}

	_, err = fixture.vault.Get(ctx, instanceID+"/manifest", &stored)
	if err != nil || stored["manifest"] != sent[0] {
		t.Fatalf("the stored manifest is not the deployed one (%v)", err)
	}

	record := readTaskRecord(ctx, t, fixture.vault, instanceID)
	if record["action"] != updateFailureAction || record["state"] != internalVault.TaskStateSucceeded {
		t.Fatalf("the task record is %v", record)
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != changeSucceeded || records[0].TaskID != updateTaskID || records[0].Keys != policyKey {
		t.Fatalf("the change records are %+v, want one succeeded update naming the key", records)
	}
}

// A key sent as null is removed from the stored parameters when the update
// that carries it succeeds.
func TestUpdate_ParameterNullRemovesKey(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, map[string]interface{}{policyKey: policyNoEvict, noteKey: "dropped"})

	script := &deployScript{}
	script.script(fixture, "done")

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: nil, noteKey: nil},
	}, true)
	requireAccepted(t, response, async, err)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	if sent := script.sent(); len(sent) != 1 || deployedPolicy(t, sent[0]) != policyLRU {
		t.Fatalf("the update deployed %q, want the template's default policy", sent)
	}

	// With every key removed, the records hold no parameters at all,
	// which reads as an empty set.
	root, details := fixture.storedParameters(ctx, t, instanceID)
	if root != nil || details != nil {
		t.Fatalf("the committed parameters are %v and %v, want both keys removed", root, details)
	}
}

// The deploy keeps each release and the stemcell at the live version
// wherever the live one is newer than the template's.
func TestUpdate_CarriesLiveStemcellAndReleaseVersions(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, pinnedUpdatePlan, nil)

	script := &deployScript{}
	script.script(fixture, "done")

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict},
	}, true)
	requireAccepted(t, response, async, err)
	fixture.settled(ctx, t, instanceID)

	sent := script.sent()
	if len(sent) != 1 {
		t.Fatalf("the update sent %d deploys, want 1", len(sent))
	}

	var doc map[string]interface{}

	err = yaml.Unmarshal([]byte(sent[0]), &doc)
	if err != nil {
		t.Fatal(err)
	}

	versions := map[string]interface{}{}

	releases, _ := doc[releasesKey].([]interface{})
	for _, entry := range releases {
		release, _ := entry.(map[string]interface{})
		name, _ := release[manifestNameKey].(string)
		versions[name] = release[manifestVersion]
	}

	want := map[string]interface{}{recordedNameService: "1.4", "added": "3.0", "newer-in-template": "5.2"}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("the deployed releases are %v, want %v", versions, want)
	}

	stemcells, _ := doc["stemcells"].([]interface{})
	stemcell, _ := stemcells[0].(map[string]interface{})

	if stemcell[stemcellOSKey] != liveStemcellOS || stemcell[manifestVersion] != "1.9" || stemcell["alias"] != stemcellAlias {
		t.Fatalf("the deployed stemcell is %v, want ubuntu-noble 1.9", stemcell)
	}
}

// A failed deploy task fails the update with the instance usable and the
// update repeatable, and the stored values stay at their last good state.
func TestUpdate_FailedTask_KeepsLastGoodState(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	params := map[string]interface{}{policyKey: policyNoEvict}
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, params)

	script := &deployScript{}
	script.script(fixture, outcomeTaskError)

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, true)
	requireAccepted(t, response, async, err)

	answer := fixture.settled(ctx, t, instanceID)
	if answer.State != osbapi.StateFailed || answer.InstanceUsable == nil || !*answer.InstanceUsable ||
		answer.UpdateRepeatable == nil || !*answer.UpdateRepeatable || !strings.Contains(answer.Description, "77") {
		t.Fatalf("the update settled as %+v, want failed, usable, and repeatable, naming the task", answer)
	}

	root, details := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, params) || !reflect.DeepEqual(details, params) {
		t.Fatalf("the stored parameters are %v and %v, want the last good %v", root, details, params)
	}

	var stored map[string]interface{}

	found, err := fixture.vault.Get(ctx, instanceID+"/manifest", &stored)
	if err != nil || found {
		t.Fatalf("a failed update stored a manifest (%v)", err)
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Outcome != outcomeStateFailed || records[0].TaskID != updateTaskID {
		t.Fatalf("the change records are %+v, want one failed update", records)
	}
}

// A repeat of the update in progress gets 202 with the same operation, and
// a different update gets 422 ConcurrencyError, while the deploy runs.
func TestUpdate_RepeatWhileInProgress_202SameOperation(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)

	script := &deployScript{release: make(chan struct{})}
	script.script(fixture, "done")

	request := osbapi.UpdateRequest{ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict}}

	response, async, err := fixture.broker.Update(ctx, instanceID, request, true)
	requireAccepted(t, response, async, err)

	response, async, err = fixture.broker.Update(ctx, instanceID, request, true)
	requireAccepted(t, response, async, err)

	close(script.release)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	if sent := script.sent(); len(sent) != 1 {
		t.Fatalf("the repeated update sent %d deploys, want 1", len(sent))
	}
}

// A different update while one is deploying gets 422 ConcurrencyError and
// changes nothing the running update staged.
func TestUpdate_DifferentWhileInProgress_422(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)

	script := &deployScript{release: make(chan struct{})}
	script.script(fixture, "done")

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict},
	}, true)
	requireAccepted(t, response, async, err)

	_, _, err = fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, true)
	if !errors.Is(err, osbapi.ErrConcurrencyError) {
		t.Fatalf("a different update during the deploy was answered %v, want ConcurrencyError", err)
	}

	close(script.release)
	fixture.settled(ctx, t, instanceID)

	root, _ := fixture.storedParameters(ctx, t, instanceID)
	if !reflect.DeepEqual(root, map[string]interface{}{policyKey: policyNoEvict}) {
		t.Fatalf("the committed parameters are %v, want the first update's", root)
	}
}

// An update of an instance whose deployment runs a BOSH task, such as one
// an operator started, gets 422 ConcurrencyError, and nothing is sent.
func TestUpdate_Busy_422ConcurrencyError(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()
	instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)
	before := fixture.snapshot(ctx, t, instanceID)

	fixture.director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 12, State: outcomeTaskProcessing, Description: "run errand"}, nil
	}

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyLRU},
	}, true)
	fixture.requireRefused(ctx, t, instanceID, before, err, func(err error) bool { return errors.Is(err, osbapi.ErrConcurrencyError) })

	if calls := fixture.director.Calls("UpdateDeployment"); len(calls) != 0 {
		t.Fatalf("the busy update sent %d deploys", len(calls))
	}
}

// A failed update never deletes the deployment and never marks a cleanup
// as owed, whatever state its task ended in.
func TestUpdate_FailureNeverCleansUpDeployment(t *testing.T) {
	t.Parallel()

	for _, state := range []string{outcomeTaskError, taskCancelled, "timeout"} {
		fixture := newUpdateFixture(t)
		ctx := context.Background()
		instanceID := fixture.provisioned(ctx, t, openUpdatePlan, nil)

		script := &deployScript{}
		script.script(fixture, state)

		response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
			ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict},
		}, true)
		requireAccepted(t, response, async, err)

		if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
			t.Fatalf("an update whose task is %s settled as %+v, want failed", state, answer)
		}

		if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 {
			t.Fatalf("a failed update whose task is %s deleted %v", state, calls)
		}

		if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["cleanup_pending"] != nil {
			t.Fatalf("a failed update whose task is %s marked a cleanup as owed: %v", state, record)
		}
	}

	// A rollback that restores the deployment, and one that fails, never
	// delete it either.
	for _, rollbackState := range []string{outcomeTaskDone, outcomeTaskError} {
		fixture := newUpdateFixture(t)
		ctx := context.Background()

		fixture.rollbackOn(ctx, t, true)

		script := &rollbackScript{}
		script.script(fixture, rollbackState)

		instanceID := fixture.startFailingUpdate(ctx, t)

		if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateFailed {
			t.Fatalf("an update whose rollback is %s settled as %+v, want failed", rollbackState, answer)
		}

		if calls := fixture.director.Calls("DeleteDeployment"); len(calls) != 0 {
			t.Fatalf("an update whose rollback is %s deleted %v", rollbackState, calls)
		}

		if record := readTaskRecord(ctx, t, fixture.vault, instanceID); record["cleanup_pending"] != nil {
			t.Fatalf("an update whose rollback is %s marked a cleanup as owed: %v", rollbackState, record)
		}
	}
}

// The change record of a refused update names the platform user the
// request came from.
func TestUpdate_ChangeRecordNamesTheOriginatingUser(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	instanceID := fixture.provisioned(context.Background(), t, openUpdatePlan, nil)
	ctx := osbapi.ContextWithOriginatingIdentity(context.Background(), osbapi.OriginatingIdentity{
		Platform: platformCloudFoundry, Value: map[string]any{userIDKey: "user-1"},
	})

	_, _, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{ServiceID: recordedNameService, PlanID: strictUpdatePlan}, true)
	if err == nil {
		t.Fatal("a plan change was accepted")
	}

	records := fixture.updateRecords(ctx, t)
	if len(records) != 1 || records[0].Actor != "cloudfoundry:user-1" || records[0].Scope != audit.ScopeInstances {
		t.Fatalf("the change records are %+v, want one naming the platform user", records)
	}
}

// The change record an update's goroutine leaves when the update settles
// names the platform user who sent it, though the goroutine outlives the
// request, and so does the record re-adoption leaves after a restart.
func TestUpdate_SettledChangeRecordNamesTheOriginatingUser(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	instanceID := fixture.provisioned(context.Background(), t, openUpdatePlan, nil)
	ctx := osbapi.ContextWithOriginatingIdentity(context.Background(), osbapi.OriginatingIdentity{
		Platform: platformCloudFoundry, Value: map[string]any{userIDKey: "user-2"},
	})

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{policyKey: policyNoEvict},
	}, true)
	requireAccepted(t, response, async, err)

	fixture.settled(context.Background(), t, instanceID)

	records := fixture.updateRecords(context.Background(), t)
	if len(records) != 1 || records[0].Outcome != changeSucceeded || records[0].Actor != "cloudfoundry:user-2" {
		t.Fatalf("the change records are %+v, want one succeeded update naming the platform user", records)
	}

	staged, _ := readTaskRecord(context.Background(), t, fixture.vault, instanceID)[internalVault.TaskRecordUpdateKey].(map[string]interface{})
	if staged["actor"] != "cloudfoundry:user-2" {
		t.Fatalf("the staged update names the actor %v, want the platform user, so re-adoption can name it", staged["actor"])
	}
}
