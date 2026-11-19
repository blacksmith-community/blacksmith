package broker_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	internalVault "blacksmith/internal/vault"
)

// doubleClaimOwner is the instance whose index entry claims the colliding
// name, and doubleClaimDebtor is the instance the index does not have,
// whose own record owes the cleanup of the same name.
const (
	doubleClaimOwner  = "abc"
	doubleClaimDebtor = "x-abc"
)

// writeDoubleClaim writes two claims on redis-small-x-abc, as instances
// named before the claim check, or an index entry that was lost, can leave
// them. Instance abc is indexed under plan redis-small-x with that name,
// and instance x-abc, which the index does not have, holds a provision
// under plan redis-small that failed and still owes the cleanup of it.
func writeDoubleClaim(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := collidingLongPlan + "-" + doubleClaimOwner

	err := vaultClient.Index(ctx, doubleClaimOwner, map[string]interface{}{
		serviceIDKey: recordedNameService, recordedPlanKey: collidingLongPlan, recordedDeploymentKey: name,
		createdAtKey: time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err == nil {
		err = recordFailedMintedProvision(ctx, vaultClient, doubleClaimDebtor, collidingShortPlan, name)
	}

	if err != nil {
		t.Fatalf("failed to write the two claims: %v", err)
	}

	return name
}

// requireCleanupStillOwed checks that the record of instanceID still owes
// its cleanup.
func requireCleanupStillOwed(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, instanceID string) {
	t.Helper()

	recorded, err := cleanupGuardBroker(cleanupGuardDirector(), vaultClient).RecordedOperation(ctx, instanceID)
	if err != nil || !recorded.CleanupPending {
		t.Fatalf("the record of %s is %+v, %v, want it still owing its cleanup", instanceID, recorded, err)
	}
}

// A cleanup never deletes a deployment that another instance claims too,
// because nothing shows which instance it belongs to. The cleanup x-abc
// owes deletes nothing, the record still owes it, and the overview lists
// x-abc with a reason that names the double claim. When the other claims
// cannot be read, the cleanup deletes nothing either, and the row says so.
func TestCleanupFailedDeployment_RefusesANameAnotherInstanceClaims(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		indexRead bool
		reason    string
	}{
		{name: "another instance claims the name", reason: "Instance abc also claims deployment redis-small-x-abc, through its index entry, so two instances claim one name"},
		{name: "the other claims cannot be read", indexRead: true, reason: "Whether another instance also claims deployment redis-small-x-abc could not be checked"},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, proxy := claimFaultVault(t)
			ctx := context.Background()
			name := writeDoubleClaim(ctx, t, vaultClient)

			if entry.indexRead {
				proxy.FailReads("/data/db", http.StatusForbidden, "permission denied", 1)
			}

			director := cleanupGuardDirector()
			cleaner := cleanupGuardBroker(director, vaultClient)

			cleaner.CleanupFailedDeployment(ctx, doubleClaimDebtor, name)

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
				t.Fatalf("the cleanup of %s deleted %v", doubleClaimDebtor, deleted)
			}

			requireCleanupStillOwed(ctx, t, vaultClient, doubleClaimDebtor)

			attention := cleaner.CleanupAttention()
			if len(attention) != 1 || attention[0].InstanceID != doubleClaimDebtor || attention[0].DeploymentName != name || !strings.Contains(attention[0].Reason, entry.reason) {
				t.Fatalf("the refusal left %+v for an operator, want one row for %s and %s that says %q", attention, doubleClaimDebtor, name, entry.reason)
			}
		})
	}
}
