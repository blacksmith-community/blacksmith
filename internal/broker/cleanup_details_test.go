package broker_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// A record from before the plan was recorded is owned only through the
// request details stored under its name. When Vault can't answer for
// them, ownership is unknown, so nothing is deleted. The refusal gets a
// row of its own when none is held, and a row already held stays as it
// was, so a passing fault never replaces what an operator is looking at.
// Once the details can be read, the cleanup proceeds.
func TestCleanupFailedDeployment_RefusesWhileTheRequestDetailsCannotBeRead(t *testing.T) {
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
	proxied := internalVault.New(proxy.Addr, server.RootToken, true)

	t.Run("with no row held", func(t *testing.T) {
		t.Parallel()

		refuseUnreadDetailsWithNoRow(ctx, t, proxy, direct, proxied)
	})

	t.Run("with a row held", func(t *testing.T) {
		t.Parallel()

		refuseUnreadDetailsKeepingTheRow(ctx, t, proxy, direct, proxied)
	})
}

// refuseUnreadDetailsWithNoRow fails the read of the request details of a
// record that has them, and checks that the refusal leaves a row that
// says so.
func refuseUnreadDetailsWithNoRow(ctx context.Context, t *testing.T, proxy *testutil.VaultFaultProxy, direct, proxied *internalVault.Vault) {
	t.Helper()

	instanceID := "details-unreadable-new"
	own := standalonePlanID + "-" + instanceID

	err := recordFailedProvisionOwingCleanup(ctx, direct, instanceID)
	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	cleaner := cleanupGuardBroker(director, proxied)

	proxy.FailReads(instanceID+"/"+own, http.StatusForbidden, "permission denied", 1)
	cleaner.CleanupFailedDeployment(ctx, instanceID, own)

	requireDetailsRefusal(ctx, t, cleaner, director, instanceID, own, "could not be read")
}

// refuseUnreadDetailsKeepingTheRow refuses a record with no request
// details, stores them, fails their read and checks that the row stays as
// it was, and then checks that the cleanup proceeds once they can be read.
func refuseUnreadDetailsKeepingTheRow(ctx context.Context, t *testing.T, proxy *testutil.VaultFaultProxy, direct, proxied *internalVault.Vault) {
	t.Helper()

	instanceID := "details-unreadable-held"
	own := standalonePlanID + "-" + instanceID

	err := recordFailedProvisionNaming(ctx, direct, instanceID, own)
	if err != nil {
		t.Fatalf("failed to write the record: %v", err)
	}

	director := cleanupGuardDirector()
	cleaner := cleanupGuardBroker(director, proxied)

	cleaner.CleanupFailedDeployment(ctx, instanceID, own)
	requireDetailsRefusal(ctx, t, cleaner, director, instanceID, own, "no request details with a plan are stored")

	before := cleaner.CleanupAttention()[0]

	err = storeRequestDetails(ctx, direct, instanceID)
	if err != nil {
		t.Fatal(err)
	}

	proxy.FailReads(instanceID+"/"+own, http.StatusForbidden, "permission denied", 1)
	cleaner.CleanupFailedDeployment(ctx, instanceID, own)

	if after := cleaner.CleanupAttention(); len(after) != 1 || after[0] != before {
		t.Fatalf("a cleanup that could not read the request details left %+v, want the row %+v kept as it was", after, before)
	}

	requireDetailsRefusal(ctx, t, cleaner, director, instanceID, own, "no request details with a plan are stored")

	cleaner.CleanupFailedDeployment(ctx, instanceID, own)

	recorded, err := cleaner.RecordedOperation(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 1 || deleted[0] != own || recorded.CleanupPending {
		t.Fatalf("once the request details could be read, the cleanup deleted %v and left the mark %v, want %s deleted once and no mark", deleted, recorded.CleanupPending, own)
	}

	if attention := cleaner.CleanupAttention(); len(attention) != 0 {
		t.Fatalf("the cleanup that deleted its own deployment left %+v for an operator", attention)
	}
}

// requireDetailsRefusal checks that the cleanup deleted nothing, kept the
// mark, and left one row naming deploymentName with a reason that holds
// reason.
func requireDetailsRefusal(ctx context.Context, t *testing.T, cleaner *broker.Broker, director *testutil.ScriptedBOSHDirector, instanceID, deploymentName, reason string) {
	t.Helper()

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("the cleanup deleted %v", deleted)
	}

	attention := cleaner.CleanupAttention()
	if len(attention) != 1 || attention[0].DeploymentName != deploymentName || !strings.Contains(attention[0].Reason, reason) {
		t.Fatalf("the refusal left %+v for an operator, want one row for %s that says %q", attention, deploymentName, reason)
	}

	requireMarkKept(ctx, t, cleaner, instanceID)
}
