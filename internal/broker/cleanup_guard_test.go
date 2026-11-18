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
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
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

			(&broker.Broker{BOSH: director, Vault: vaultClient}).CleanupFailedDeployment(ctx, instanceID, deploymentName)

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

		(&broker.Broker{BOSH: director, Vault: unreachable}).CleanupFailedDeployment(ctx, "cleanup-guard-unreadable", standalonePlanID+"-cleanup-guard-unreadable")

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
			cleaner := &broker.Broker{BOSH: director, Vault: vaultClient}

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

	cleaner := &broker.Broker{BOSH: director, Vault: vaultClient}
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
	name     string
	recorded func(instanceID string) string
	asked    func(instanceID string) string
	indexed  bool
	deletes  bool
}

// The cleanup deletes only the instance's own deployment. The name it is
// asked to delete must be the one recorded for the instance, which is the
// record's, or the index entry's when the record holds none, and it must
// be a name minted for the instance. Otherwise it deletes nothing, asks
// the director nothing, and leaves the refusal where an operator sees it.
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
	otherPlan := func(instanceID string) string { return "other-plan-" + instanceID }
	none := func(string) string { return "" }

	cases := []cleanupOwnership{
		{"the record names the instance's deployment", own, own, false, true},
		{"the record names another instance's deployment", other, other, false, false},
		{"the cleanup is asked for another instance's deployment", own, other, false, false},
		{"the cleanup is asked for a deployment the record does not name", own, otherPlan, false, false},
		{"an unnamed record of an indexed instance", none, own, true, true},
		{"an unnamed record of an indexed instance asked for another name", none, otherPlan, true, false},
		{"an unnamed record with no index entry", none, own, false, false},
	}

	for index, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "cleanup-owner-" + strconv.Itoa(index)

			err := recordFailedProvisionNaming(ctx, vaultClient, instanceID, entry.recorded(instanceID))
			if err != nil {
				t.Fatalf("failed to write the record: %v", err)
			}

			if entry.indexed {
				err = vaultClient.Index(ctx, instanceID, map[string]interface{}{recordedPlanKey: standalonePlanID, serviceIDKey: recordedNameService})
				if err != nil {
					t.Fatalf("failed to index the instance: %v", err)
				}
			}

			director := cleanupGuardDirector()
			cleaner := &broker.Broker{BOSH: director, Vault: vaultClient}
			asked := entry.asked(instanceID)

			cleaner.CleanupFailedDeployment(ctx, instanceID, asked)

			requireCleanupOwnership(ctx, t, cleaner, director, instanceID, asked, entry.deletes)
		})
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
			cleaner := &broker.Broker{BOSH: director, Vault: vaultClient}

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
	cleaner := &broker.Broker{BOSH: director, Vault: vaultClient}

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
