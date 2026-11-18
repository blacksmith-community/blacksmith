package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// directorFindTaskByContextID is the director call the cleanup of an
// unnamed record recovers its name with.
const directorFindTaskByContextID = "FindTaskByContextID"

// A record that names no deployment is judged by its index entry, so a
// cleanup that cannot read the index knows nothing about the instance. It
// asks the director nothing, deletes nothing, keeps the mark, and leaves
// the row an operator already sees as it was, and the failed read is
// logged at Error.
func TestCleanupFailedDeployment_RefusesWhileTheIndexCannotBeRead(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	proxy, err := testutil.NewVaultFaultProxy(server.Addr)
	if err != nil {
		t.Fatalf("failed to start the Vault proxy: %v", err)
	}

	t.Cleanup(proxy.Close)

	ctx := context.Background()
	direct := internalVault.New(server.Addr, server.RootToken, true)
	instanceID := "cleanup-index-unreadable"
	own := standalonePlanID + "-" + instanceID

	err = recordFailedProvisionNaming(ctx, direct, instanceID, "")
	if err == nil {
		err = storeRequestDetails(ctx, direct, instanceID)
	}

	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task is a nil task without an error
	cleaner := cleanupGuardBroker(director, internalVault.New(proxy.Addr, server.RootToken, true))

	cleaner.CleanupFailedDeployment(ctx, instanceID, own)

	before := cleaner.CleanupAttention()
	if len(before) != 1 || !strings.Contains(before[0].Reason, "the director has no task") {
		t.Fatalf("the first refusal left %+v for an operator, want one row that says the director has no task", before)
	}

	log := newLevelLogger()

	proxy.FailReads("data/db", http.StatusForbidden, "permission denied", 1)
	cleaner.CleanupFailedDeploymentLogged(ctx, instanceID, own, log)

	if lookups := director.Calls(directorFindTaskByContextID); len(lookups) != 1 {
		t.Fatalf("the director was asked for the provision's task %d times, want once, before the index read failed", len(lookups))
	}

	if after := cleaner.CleanupAttention(); len(after) != 1 || after[0] != before[0] {
		t.Fatalf("a cleanup that could not read the index left %+v, want the row %+v kept as it was", after, before[0])
	}

	if got := log.count("ERROR", "its index entry could not be read"); got != 1 {
		t.Fatalf("the failed index read was logged at Error %d times, want once", got)
	}

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("the cleanup deleted %v", deleted)
	}

	requireMarkKept(ctx, t, cleaner, instanceID)
}

// A record from before context IDs were kept, which names no deployment
// and whose index entry names none either, gives the cleanup no way to
// find the provision's task. The cleanup refuses with the reason that
// says so, without asking the director, and keeps the mark.
func TestCleanupFailedDeployment_RefusesAnUnnamedRecordWithNoContextID(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	ctx := context.Background()
	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	instanceID := "cleanup-no-context-id"
	own := standalonePlanID + "-" + instanceID

	err = recordFailedProvisionNaming(ctx, vaultClient, instanceID, "")
	if err == nil {
		err = storeRequestDetails(ctx, vaultClient, instanceID)
	}

	if err == nil {
		err = dropRecordedContextID(ctx, vaultClient, instanceID)
	}

	if err == nil {
		err = vaultClient.Index(ctx, instanceID, map[string]interface{}{recordedPlanKey: standalonePlanID, serviceIDKey: recordedNameService})
	}

	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task is a nil task without an error
	cleaner := cleanupGuardBroker(director, vaultClient, standalonePlanID)

	cleaner.CleanupFailedDeployment(ctx, instanceID, own)

	if lookups := director.Calls(directorFindTaskByContextID); len(lookups) != 0 {
		t.Fatalf("the director was asked for tasks with context IDs %q, want no lookup without a context ID", lookups)
	}

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("the cleanup deleted %v", deleted)
	}

	attention := cleaner.CleanupAttention()
	if len(attention) != 1 || !strings.Contains(attention[0].Reason, "the record holds no context ID to find the provision's task by") {
		t.Fatalf("the refusal left %+v for an operator, want one row that says the record holds no context ID", attention)
	}

	requireMarkKept(ctx, t, cleaner, instanceID)
}

// dropRecordedContextID takes the context ID off the record of instanceID,
// as a record written before the broker chose context IDs has none.
func dropRecordedContextID(ctx context.Context, vaultClient *internalVault.Vault, instanceID string) error {
	var record map[string]interface{}

	_, err := vaultClient.Get(ctx, instanceID+"/task", &record)
	if err == nil {
		delete(record, "context_id")
		err = vaultClient.Put(ctx, instanceID+"/task", record)
	}

	if err != nil {
		return fmt.Errorf("taking the context ID off the record: %w", err)
	}

	return nil
}
