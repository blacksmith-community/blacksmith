package broker_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// clusterPlanID is a second plan, which the instance in a guard spec can
// move to, and recordedDeploymentKey is the key of the name an index
// entry records.
const (
	clusterPlanID         = "cluster"
	recordedDeploymentKey = "deployment_name"
)

// cleanupGuardDirector returns a director on which the deployment exists,
// no delete runs on it, and a delete is accepted.
func cleanupGuardDirector() *testutil.ScriptedBOSHDirector {
	director := testutil.NewScriptedBOSHDirector()
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: name}, nil
	}
	director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		return &bosh.Task{ID: 1}, nil
	}

	return director
}

// cleanupGuardBroker returns a broker over director and vaultClient whose
// catalog holds the given plans. The cleanup's ownership check reads the
// record and never the catalog, so a guard spec can leave it empty.
func cleanupGuardBroker(director bosh.Director, vaultClient *internalVault.Vault, planIDs ...string) *broker.Broker {
	plans := make(map[string]services.Plan, len(planIDs))
	for _, planID := range planIDs {
		plans[recordedNameService+"/"+planID] = services.Plan{ID: planID, Name: planID}
	}

	return &broker.Broker{BOSH: director, Vault: vaultClient, Plans: plans}
}

// cleanupGuardRecord is a record the cleanup of a failed deployment may
// find, and whether it deletes the deployment over it.
type cleanupGuardRecord struct {
	name    string
	record  func(instanceID string) error
	deletes bool
}

// recordFailedProvisionOwingCleanup records a provision of the standalone
// plan that failed in task 7 and still owes the delete of its deployment,
// which its record names.
func recordFailedProvisionOwingCleanup(ctx context.Context, vaultClient *internalVault.Vault, instanceID string) error {
	return recordFailedProvisionNaming(ctx, vaultClient, instanceID, standalonePlanID+"-"+instanceID)
}

// recordFailedProvisionNaming records a provision that failed in task 7
// and still owes the delete of its deployment, with deploymentName on its
// record, or none when deploymentName is empty.
func recordFailedProvisionNaming(ctx context.Context, vaultClient *internalVault.Vault, instanceID, deploymentName string) error {
	err := vaultClient.TrackProgressWithDeployment(ctx, instanceID, outcomeProvision, "Creating BOSH deployment", "context-7", deploymentName, 0, nil)
	if err == nil {
		err = vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task 7)", 7, nil)
	}

	if err == nil {
		err = vaultClient.TrackFailedCleanupPending(ctx, instanceID, outcomeProvision, "BOSH deployment task 7 is error", "context-7", 7)
	}

	if err != nil {
		return fmt.Errorf("recording the failed provision: %w", err)
	}

	return nil
}

// recordFailedMintedProvision records a provision whose deploy minted
// its deployment's name under planID, failed in task 7, and still owes
// the delete of that deployment. When deploymentName is not the minted
// name, it then overwrites the record's name with deploymentName, as a
// damaged record might hold.
func recordFailedMintedProvision(ctx context.Context, vaultClient *internalVault.Vault, instanceID, planID, deploymentName string) error {
	err := vaultClient.TrackProgressWithMintedDeployment(ctx, instanceID, outcomeProvision, "Creating BOSH deployment", "context-7", planID, 0, nil)
	if err == nil {
		err = vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task 7)", 7, nil)
	}

	if err == nil {
		err = vaultClient.TrackFailedCleanupPending(ctx, instanceID, outcomeProvision, "BOSH deployment task 7 is error", "context-7", 7)
	}

	if err != nil {
		return fmt.Errorf("recording the failed provision: %w", err)
	}

	if deploymentName == planID+"-"+instanceID {
		return nil
	}

	var record map[string]interface{}

	_, err = vaultClient.Get(ctx, instanceID+"/task", &record)
	if err == nil {
		record[internalVault.TaskRecordDeploymentNameKey] = deploymentName
		err = vaultClient.Put(ctx, instanceID+"/task", record)
	}

	if err != nil {
		return fmt.Errorf("damaging the record's name: %w", err)
	}

	return nil
}

// cleanupGuardRecords returns the records the cleanup is run over. Only a
// failed provision that still owes its cleanup deletes.
func cleanupGuardRecords(ctx context.Context, vaultClient *internalVault.Vault) []cleanupGuardRecord {
	return []cleanupGuardRecord{
		{"a failed provision owing its cleanup", func(instanceID string) error {
			return recordFailedProvisionOwingCleanup(ctx, vaultClient, instanceID)
		}, true},
		{"a failed provision whose cleanup ran", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment task 7 is error", -1, nil)
		}, false},
		{"a provision still running", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task 7)", 7, nil)
		}, false},
		{"a failed update", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, updateFailureAction, "BOSH deployment task 7 is error", -1, nil)
		}, false},
		{"a failed deprovision", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, outcomeDeprovision, "BOSH deletion task 7 is error", -1, nil)
		}, false},
		{"a failed unknown action", func(instanceID string) error {
			return vaultClient.TrackProgress(ctx, instanceID, "unknown", "BOSH task 7 is error", -1, nil)
		}, false},
		{"a failure with no action", func(instanceID string) error {
			return vaultClient.Put(ctx, instanceID+"/task", map[string]interface{}{"state": outcomeStateFailed, "task": -1})
		}, false},
		{"no record", func(string) error { return nil }, false},
	}
}

// The cleanup of a failed deployment deletes the deployment, which is right
// only for a provision that never produced a usable instance. It takes no
// caller's word for what failed. It reads the instance's own record, and
// any record but a failed provision that still owes its cleanup, including
// one it cannot read, is refused before the director is asked anything.
func TestCleanupFailedDeployment_DecidesFromTheRecord(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	records := cleanupGuardRecords(ctx, vaultClient)

	for index, record := range records {
		t.Run(record.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-guard-" + strconv.Itoa(index)
			deploymentName := standalonePlanID + "-" + instanceID

			err := record.record(instanceID)
			if err != nil {
				t.Fatalf("failed to write the record: %v", err)
			}

			director := cleanupGuardDirector()

			cleanupGuardBroker(director, vaultClient).CleanupFailedDeployment(ctx, instanceID, deploymentName)

			deleted := director.Calls(directorDeleteDeployment)

			switch {
			case record.deletes && (len(deleted) != 1 || deleted[0] != deploymentName):
				t.Errorf("expected the failed provision's deployment to be deleted once, got %v", deleted)
			case !record.deletes && len(deleted) != 0:
				t.Errorf("the cleanup over %s deleted %v", record.name, deleted)
			case !record.deletes && len(director.Calls("GetDeployment")) != 0:
				t.Errorf("the cleanup over %s asked the director for %v", record.name, director.Calls("GetDeployment"))
			}
		})
	}

	t.Run("an unreadable record", func(t *testing.T) {
		t.Parallel()

		director := cleanupGuardDirector()
		unreachable := internalVault.New("http://127.0.0.1:1", server.RootToken, true)

		cleanupGuardBroker(director, unreachable).CleanupFailedDeployment(ctx, "cleanup-guard-unreadable", standalonePlanID+"-cleanup-guard-unreadable")

		if calls := director.Calls(directorDeleteDeployment); len(calls) != 0 {
			t.Errorf("the cleanup over an unreadable record deleted %v", calls)
		}
	})
}

// cleanupAnswer is how the director answers the cleanup of a failed
// deployment, and what the cleanup leaves on the record.
type cleanupAnswer struct {
	name     string
	director func(director *testutil.ScriptedBOSHDirector)
	deletes  int
	marked   bool
}

// The mark that says the cleanup is owed comes off the record once the
// delete is sent or the director says the deployment is gone, and not
// before, so a cleanup the director refused is tried again.
func TestCleanupFailedDeployment_ClearsTheMarkOnceTheDeleteIsSent(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	answers := []cleanupAnswer{
		{"the delete is sent", func(*testutil.ScriptedBOSHDirector) {}, 1, false},
		{"the deployment is gone", func(director *testutil.ScriptedBOSHDirector) {
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, bosh.ErrDeploymentNotFound }
		}, 0, false},
		{"the deployment goes before the delete", func(director *testutil.ScriptedBOSHDirector) {
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) { return nil, bosh.ErrDeploymentNotFound }
		}, 1, false},
		{"the delete is refused", func(director *testutil.ScriptedBOSHDirector) {
			director.DeleteDeploymentFn = func(string) (*bosh.Task, error) { return nil, errDirectorUnreachable }
		}, 1, true},
		{"the director cannot be reached", func(director *testutil.ScriptedBOSHDirector) {
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnreachable }
		}, 0, true},
	}

	for index, answer := range answers {
		t.Run(answer.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-mark-" + strconv.Itoa(index)

			err := recordFailedProvisionOwingCleanup(ctx, vaultClient, instanceID)
			if err != nil {
				t.Fatalf("failed to write the record: %v", err)
			}

			director := cleanupGuardDirector()
			answer.director(director)
			cleaner := cleanupGuardBroker(director, vaultClient)

			cleaner.CleanupFailedDeployment(ctx, instanceID, standalonePlanID+"-"+instanceID)

			recorded, err := cleaner.RecordedOperation(ctx, instanceID)
			if err != nil {
				t.Fatalf("failed to read the record: %v", err)
			}

			if deletes := len(director.Calls(directorDeleteDeployment)); deletes != answer.deletes || recorded.CleanupPending != answer.marked {
				t.Fatalf("the cleanup sent %d deletes and left the mark %v, want %d and %v", deletes, recorded.CleanupPending, answer.deletes, answer.marked)
			}
		})
	}
}

// The mark comes off the record even when the director's answer to the
// delete takes the whole of the cleanup's bound. The clear is a record
// write with a bound of its own, so a slow director never leaves a
// cleanup that was sent marked as owed.
func TestCleanupFailedDeployment_ClearsTheMarkAfterASlowDelete(t *testing.T) { //nolint:paralleltest // it shortens the package's cleanup bound
	const bound = 50 * time.Millisecond

	restore := broker.SetFailedDeploymentCleanupTimeout(bound)
	t.Cleanup(restore)

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()
	instanceID := "cleanup-mark-slow"

	err = recordFailedProvisionOwingCleanup(ctx, vaultClient, instanceID)
	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	director.DeleteDeploymentFn = func(string) (*bosh.Task, error) {
		time.Sleep(2 * bound)

		return &bosh.Task{ID: 1}, nil
	}

	cleaner := cleanupGuardBroker(director, vaultClient)
	cleaner.CleanupFailedDeployment(ctx, instanceID, standalonePlanID+"-"+instanceID)

	recorded, err := cleaner.RecordedOperation(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	if deletes := len(director.Calls(directorDeleteDeployment)); deletes != 1 || recorded.CleanupPending {
		t.Fatalf("the cleanup sent %d deletes and left the mark %v, want 1 and false", deletes, recorded.CleanupPending)
	}
}

// cleanupOwnership is a deployment a cleanup is asked to delete, the name
// the instance's record holds, and whether the instance is indexed under
// the standalone plan.
type cleanupOwnership struct {
	name string
	// plan is the plan the record says its name was minted under, or
	// empty for a record written before the plan was recorded.
	plan     string
	recorded func(instanceID string) string
	asked    func(instanceID string) string
	// index is the plan of the instance's index entry, or empty for an
	// instance with no index entry. indexName is the name that entry
	// records, or empty for an entry that records none.
	index     string
	indexName func(instanceID string) string
	// catalog holds the plan IDs of the broker's catalog now.
	catalog []string
	deletes bool
}

// The cleanup deletes only the instance's own deployment. The name it is
// asked to delete must be the one recorded for the instance, which is the
// record's, or the index entry's when the record holds none. When the
// record says which plan the broker minted the name under, the name must
// be exactly that plan's ID, a dash, and the instance ID. A record from
// before the plan was recorded falls back to the name's shape, which is
// the instance ID after a dash and a plan ID before it. Neither the
// catalog nor the instance's plan now plays a part. Otherwise the cleanup
// deletes nothing, asks the director nothing, and leaves the refusal
// where an operator sees it.
func TestCleanupFailedDeployment_DeletesOnlyTheInstancesOwnDeployment(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	own := func(instanceID string) string { return standalonePlanID + "-" + instanceID }
	other := func(string) string { return standalonePlanID + "-another-instance" }
	cluster := func(instanceID string) string { return clusterPlanID + "-" + instanceID }
	otherPlan := func(instanceID string) string { return "other-plan-" + instanceID }
	none := func(string) string { return "" }

	// A prefixed impostor is the deployment of instance x-<id> under the
	// standalone plan, and a suffixed one that of instance <id>-x. Both
	// hold the instance ID and the plan, and neither was minted for it.
	prefixed := func(instanceID string) string { return standalonePlanID + "-x-" + instanceID }
	suffixed := func(instanceID string) string { return standalonePlanID + "-" + instanceID + "-x" }

	both := []string{standalonePlanID, clusterPlanID}
	onlyCluster := []string{clusterPlanID}

	cases := []cleanupOwnership{
		{name: "the record names the deployment minted under its plan", plan: standalonePlanID, recorded: own, asked: own, catalog: both, deletes: true},
		{name: "the record names another instance's deployment", plan: standalonePlanID, recorded: other, asked: other, catalog: both},
		{name: "the cleanup is asked for another instance's deployment", plan: standalonePlanID, recorded: own, asked: other, catalog: both},
		{name: "the cleanup is asked for a deployment the record does not name", plan: standalonePlanID, recorded: own, asked: otherPlan, catalog: both},
		{name: "the record names a prefixed impostor", plan: standalonePlanID, recorded: prefixed, asked: prefixed, catalog: both},
		{name: "the record names a suffixed impostor", plan: standalonePlanID, recorded: suffixed, asked: suffixed, catalog: both},
		{name: "the record names the instance under another catalog plan than its own", plan: standalonePlanID, recorded: cluster, asked: cluster, catalog: both},
		{name: "the plan the name was minted under has left the catalog", plan: standalonePlanID, recorded: own, asked: own, catalog: onlyCluster, deletes: true},
		{
			name: "the instance moved to another plan after its name was minted", plan: standalonePlanID, recorded: own, asked: own,
			index: clusterPlanID, indexName: own, catalog: onlyCluster, deletes: true,
		},
		{name: "a record without its plan names the instance's deployment", recorded: own, asked: own, catalog: both, deletes: true},
		{name: "a record without its plan names another instance's deployment", recorded: other, asked: other, catalog: both},
		{name: "a record without its plan is asked for a deployment it does not name", recorded: own, asked: otherPlan, catalog: both},
		{name: "a record without its plan names a suffixed impostor", recorded: suffixed, asked: suffixed, catalog: both},
		{name: "a record without its plan names a plan that has left the catalog", recorded: otherPlan, asked: otherPlan, catalog: both, deletes: true},
		{name: "an unnamed record of an indexed instance", recorded: none, asked: own, index: standalonePlanID, catalog: both, deletes: true},
		{name: "an unnamed record of an indexed instance asked for another name", recorded: none, asked: otherPlan, index: standalonePlanID, catalog: both},
		{name: "an unnamed record of an indexed instance whose plan has left the catalog", recorded: none, asked: own, index: standalonePlanID, catalog: onlyCluster, deletes: true},
		{name: "an unnamed record with no index entry", recorded: none, asked: own, catalog: both},
	}

	for index, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-owner-" + strconv.Itoa(index)

			recordCleanupOwnership(ctx, t, vaultClient, instanceID, entry)

			director := cleanupGuardDirector()
			cleaner := cleanupGuardBroker(director, vaultClient, entry.catalog...)
			asked := entry.asked(instanceID)

			cleaner.CleanupFailedDeployment(ctx, instanceID, asked)

			requireCleanupOwnership(ctx, t, cleaner, director, instanceID, asked, entry.deletes)
		})
	}
}

// recordCleanupOwnership writes the failed provision and the index entry
// that entry describes for instanceID.
func recordCleanupOwnership(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, instanceID string, entry cleanupOwnership) {
	t.Helper()

	var err error

	if entry.plan == "" {
		err = recordFailedProvisionNaming(ctx, vaultClient, instanceID, entry.recorded(instanceID))
	} else {
		err = recordFailedMintedProvision(ctx, vaultClient, instanceID, entry.plan, entry.recorded(instanceID))
	}

	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	if entry.index == "" {
		return
	}

	indexEntry := map[string]interface{}{recordedPlanKey: entry.index, serviceIDKey: recordedNameService}
	if entry.indexName != nil {
		indexEntry[recordedDeploymentKey] = entry.indexName(instanceID)
	}

	err = vaultClient.Index(ctx, instanceID, indexEntry)
	if err != nil {
		t.Fatalf("failed to index the instance: %v", err)
	}
}

// requireCleanupOwnership checks that a cleanup asked for asked either
// deleted it once and left nothing for an operator, or, when deletes is
// false, asked the director nothing, left one attention entry naming the
// instance and asked, and kept the mark on the record.
func requireCleanupOwnership(ctx context.Context, t *testing.T, cleaner *broker.Broker, director *testutil.ScriptedBOSHDirector, instanceID, asked string, deletes bool) {
	t.Helper()

	deleted := director.Calls(directorDeleteDeployment)
	attention := cleaner.CleanupAttention()

	switch {
	case deletes && (len(deleted) != 1 || deleted[0] != asked):
		t.Fatalf("expected %s to be deleted once, got %v", asked, deleted)
	case deletes && len(attention) != 0:
		t.Fatalf("a cleanup that deleted its own deployment left %v for an operator", attention)
	case deletes:
		return
	case len(deleted) != 0 || len(director.Calls("GetDeployment")) != 0:
		t.Fatalf("the cleanup deleted %v and asked for %v, want no director call", deleted, director.Calls("GetDeployment"))
	case len(attention) != 1 || attention[0].InstanceID != instanceID || attention[0].DeploymentName != asked || attention[0].Reason == "":
		t.Fatalf("the refusal left %+v for an operator, want one entry for %s and %s", attention, instanceID, asked)
	}

	requireMarkKept(ctx, t, cleaner, instanceID)
}

// requireMarkKept checks that the record of instanceID still owes its
// cleanup.
func requireMarkKept(ctx context.Context, t *testing.T, cleaner *broker.Broker, instanceID string) {
	t.Helper()

	recorded, err := cleaner.RecordedOperation(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	if !recorded.CleanupPending {
		t.Fatal("a refused cleanup took the mark off the record")
	}
}

// A record written before deployment names were recorded, whose instance
// has no index entry, gets its name from the provision's own task, found
// by the record's context ID, and only when that task names a deployment
// minted for the instance.
func TestCleanupFailedDeployment_RecoversTheNameOfAnUnnamedRecord(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	own := func(instanceID string) string { return standalonePlanID + "-" + instanceID }
	found := func(name string) func(string) (*bosh.Task, error) {
		return func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: 7, State: outcomeTaskError, Deployment: name}, nil
		}
	}

	// Each refusal names its own cause on the overview, and the row of a
	// task that points at another deployment names that deployment.
	cases := []nameRecoveryCase{
		{"the task names the instance's deployment", func(id string) func(string) (*bosh.Task, error) { return found(own(id)) }, own, true, nil},
		{"the task names it and the cleanup is asked for no name", func(id string) func(string) (*bosh.Task, error) { return found(own(id)) }, func(string) string { return "" }, true, nil},
		{"the task names another instance's deployment", func(string) func(string) (*bosh.Task, error) { return found(standalonePlanID + "-another-instance") }, own, false, &nameRefusal{
			deployment: func(string) string { return standalonePlanID + "-another-instance" },
			reason:     "Task 7 with context ID context-7 names deployment " + standalonePlanID + "-another-instance, which was not named for this instance",
		}},
		{"the task is asked for another deployment than it names", func(id string) func(string) (*bosh.Task, error) { return found(own(id)) }, func(id string) string { return "other-plan-" + id }, false, nil},
		{"no task has the context ID", func(string) func(string) (*bosh.Task, error) {
			return func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task is a nil task without an error
		}, own, false, &nameRefusal{deployment: own, reason: "the director has no task with context ID context-7"}},
		{"the lookup fails", func(string) func(string) (*bosh.Task, error) {
			return func(string) (*bosh.Task, error) { return nil, errDirectorUnreachable }
		}, own, false, &nameRefusal{deployment: own, reason: "the director could not be asked for the provision's task with context ID context-7"}},
	}

	for index, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-recover-" + strconv.Itoa(index)

			err := recordFailedProvisionNaming(ctx, vaultClient, instanceID, "")
			if err != nil {
				t.Fatalf("failed to write the record: %v", err)
			}

			director := cleanupGuardDirector()
			director.FindTaskByContextIDFn = entry.lookup(instanceID)
			cleaner := cleanupGuardBroker(director, vaultClient)

			cleaner.CleanupFailedDeployment(ctx, instanceID, entry.asked(instanceID))

			requireNameRecovery(ctx, t, cleaner, director, instanceID, own(instanceID), entry.deletes)
			requireNameRefusal(t, cleaner, instanceID, entry.refusal)
		})
	}
}

// nameRecoveryCase is how the director answers the lookup of an unnamed
// record's task, the deployment the cleanup is asked for, whether it
// deletes, and the row a refusal leaves when the case checks it.
type nameRecoveryCase struct {
	name    string
	lookup  func(instanceID string) func(string) (*bosh.Task, error)
	asked   func(instanceID string) string
	deletes bool
	refusal *nameRefusal
}

// nameRefusal is the deployment a refusal's row names, and a part of its
// reason.
type nameRefusal struct {
	deployment func(instanceID string) string
	reason     string
}

// requireNameRefusal checks that the one row the refusal left names the
// deployment want gives, with a reason that holds want's reason.
func requireNameRefusal(t *testing.T, cleaner *broker.Broker, instanceID string, want *nameRefusal) {
	t.Helper()

	if want == nil {
		return
	}

	attention := cleaner.CleanupAttention()

	switch {
	case len(attention) != 1:
		t.Fatalf("the refusal left %+v for an operator, want one entry", attention)
	case attention[0].DeploymentName != want.deployment(instanceID):
		t.Errorf("the row names deployment %q, want %q", attention[0].DeploymentName, want.deployment(instanceID))
	case !strings.Contains(attention[0].Reason, want.reason):
		t.Errorf("the row's reason is %q, want it to say %q", attention[0].Reason, want.reason)
	}
}

// requireNameRecovery checks that a cleanup of an unnamed record either
// deleted want, wrote it into the record, and took the mark off, or, when
// deletes is false, deleted nothing, left one attention entry naming the
// instance, and kept the mark.
func requireNameRecovery(ctx context.Context, t *testing.T, cleaner *broker.Broker, director *testutil.ScriptedBOSHDirector, instanceID, want string, deletes bool) {
	t.Helper()

	deleted := director.Calls(directorDeleteDeployment)
	attention := cleaner.CleanupAttention()

	if !deletes {
		if len(deleted) != 0 || len(attention) != 1 || attention[0].InstanceID != instanceID {
			t.Fatalf("the cleanup deleted %v and left %+v for an operator, want nothing deleted and one entry", deleted, attention)
		}

		requireMarkKept(ctx, t, cleaner, instanceID)

		return
	}

	if len(deleted) != 1 || deleted[0] != want || len(attention) != 0 {
		t.Fatalf("the cleanup deleted %v and left %+v for an operator, want %s deleted and nothing left", deleted, attention, want)
	}

	recorded, err := cleaner.RecordedOperation(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	if recorded.DeploymentName != want || recorded.CleanupPending {
		t.Fatalf("the record names %q with the mark %v, want %s and no mark", recorded.DeploymentName, recorded.CleanupPending, want)
	}
}

// A cleanup that cannot read the record knows nothing about the refusal
// already on the overview, so the row stays. Only a record read and found
// to owe nothing takes it off.
func TestCleanupFailedDeployment_KeepsTheRowWhenTheRecordCannotBeRead(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()
	instanceID := "cleanup-row-unreadable"
	other := standalonePlanID + "-another-instance"

	err = recordFailedProvisionNaming(ctx, vaultClient, instanceID, other)
	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	cleaner := cleanupGuardBroker(director, vaultClient)

	cleaner.CleanupFailedDeployment(ctx, instanceID, other)

	before := cleaner.CleanupAttention()
	if len(before) != 1 {
		t.Fatalf("the refusal left %+v for an operator, want one entry", before)
	}

	cleaner.Vault = internalVault.New("http://127.0.0.1:1", server.RootToken, true)
	cleaner.CleanupFailedDeployment(ctx, instanceID, other)

	after := cleaner.CleanupAttention()
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("a cleanup that could not read the record left %+v, want the row %+v kept as it was", after, before[0])
	}

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("the cleanup deleted %v", deleted)
	}
}

// Each write of a failed provision with the mark makes the instance a
// suspect, so the sweep's passes between full scans read its record even
// when the cleanup that follows the write never runs. Neither write runs
// the cleanup itself, so the suspect can come only from the write.
func TestFailureWrites_MakeTheInstanceASuspect(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	writes := []struct {
		name  string
		write func(cleaner *broker.Broker, instanceID string, recorded broker.RecordedOperation) bool
	}{
		{"a poll records the failed task", func(cleaner *broker.Broker, instanceID string, recorded broker.RecordedOperation) bool {
			return cleaner.RecordProvisionFailed(ctx, instanceID, recorded, "BOSH deployment task 7 is error", 7)
		}},
		{"re-adoption records the failure", func(cleaner *broker.Broker, instanceID string, recorded broker.RecordedOperation) bool {
			return cleaner.WriteReadoptedFailure(ctx, instanceID, recorded, "The index entry is gone, so the provision cannot finish")
		}},
	}

	for index, write := range writes {
		t.Run(write.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "failure-suspect-" + strconv.Itoa(index)

			err := vaultClient.TrackProgressWithDeployment(ctx, instanceID, outcomeProvision, "Creating BOSH deployment", "context-7", standalonePlanID+"-"+instanceID, 0, nil)
			if err == nil {
				err = vaultClient.TrackProgress(ctx, instanceID, outcomeProvision, "BOSH deployment in progress (task 7)", 7, nil)
			}

			if err != nil {
				t.Fatalf("failed to record the provision: %v", err)
			}

			cleaner := cleanupGuardBroker(cleanupGuardDirector(), vaultClient)

			recorded, err := cleaner.RecordedOperation(ctx, instanceID)
			if err != nil {
				t.Fatalf("failed to read the record: %v", err)
			}

			if !write.write(cleaner, instanceID, recorded) {
				t.Fatal("the failure was not recorded")
			}

			requireMarkKept(ctx, t, cleaner, instanceID)

			if suspects := cleaner.OwedCleanupSuspects(); len(suspects) != 1 || suspects[0] != instanceID {
				t.Fatalf("the sweep suspects %v after the failure was recorded, want only %s", suspects, instanceID)
			}
		})
	}
}

// A record that says which plan its name was minted under owns exactly
// that plan's ID, a dash, and the instance ID. A record from before the
// plan was recorded owns a name of that shape under any plan ID, in or
// out of the catalog. Names that hold the instance ID inside another
// instance's ID, or another plan's name for a record that says its plan,
// are impostors.
func TestMintedFor_ChecksTheRecordedPlanOrTheNamesShape(t *testing.T) {
	t.Parallel()

	const instanceID = "abc"

	for _, check := range []struct {
		name   string
		plan   string
		minted bool
	}{
		{standalonePlanID + "-abc", standalonePlanID, true},
		{clusterPlanID + "-abc", standalonePlanID, false},
		{standalonePlanID + "-x-abc", standalonePlanID, false},
		{"x-" + standalonePlanID + "-abc", standalonePlanID, false},
		{standalonePlanID + "-abc-x", standalonePlanID, false},
		{standalonePlanID + "-abcx", standalonePlanID, false},
		{"abc", standalonePlanID, false},
		{"", standalonePlanID, false},
		{standalonePlanID + "-abc", "", true},
		{clusterPlanID + "-abc", "", true},
		{"other-abc", "", true},
		{standalonePlanID + "-abc-x", "", false},
		{standalonePlanID + "-abcx", "", false},
		{"-abc", "", false},
		{".hidden-abc", "", false},
		{"a/b-abc", "", false},
		{"a b-abc", "", false},
		{"abc", "", false},
		{standalonePlanID + "abc", "", false},
		{standalonePlanID + "-", "", false},
		{"", "", false},
	} {
		if got := broker.MintedFor(check.name, instanceID, check.plan); got != check.minted {
			t.Errorf("MintedFor(%q, %q, %q) = %v, want %v", check.name, instanceID, check.plan, got, check.minted)
		}
	}

	if broker.MintedFor(standalonePlanID+"-", "", "") || broker.MintedFor(standalonePlanID+"-", "", standalonePlanID) {
		t.Error("MintedFor took a name as minted for an empty instance ID")
	}
}
