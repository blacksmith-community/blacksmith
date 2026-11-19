package broker_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"
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

	name := indexDoubleClaimOwner(ctx, t, vaultClient)

	err := recordFailedMintedProvision(ctx, vaultClient, doubleClaimDebtor, collidingShortPlan, name)
	if err != nil {
		t.Fatalf("failed to write the two claims: %v", err)
	}

	return name
}

// indexDoubleClaimOwner indexes abc under plan redis-small-x with the
// name redis-small-x-abc, and returns that name.
func indexDoubleClaimOwner(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := collidingLongPlan + "-" + doubleClaimOwner

	err := vaultClient.Index(ctx, doubleClaimOwner, map[string]interface{}{
		serviceIDKey: recordedNameService, recordedPlanKey: collidingLongPlan, recordedDeploymentKey: name,
		createdAtKey: time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("failed to index %s: %v", doubleClaimOwner, err)
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

// A delete of x-abc, which the index does not have, is not answered 410
// Gone while its own record owes the cleanup of the name abc claims,
// because the cleanup would then still delete the deployment the 410
// gave to abc. The delete is answered 500 with a description that names
// the double claim and not the other instance, nothing is written or
// deleted, and the overview lists x-abc with a reason that says the
// platform's delete was refused.
// When its own record cannot be read, the delete is answered 500 as well.
func TestDeprovision_RefusesWhileItsOwnRecordClaimsAnotherInstancesName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		recordRead  bool
		description string
		row         bool
	}{
		{name: "the record owes the cleanup of the name", description: "two service instances claim deployment redis-small-x-abc", row: true},
		{name: "the record cannot be read", recordRead: true, description: "the record of instance x-abc could not be read"},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, proxy := claimFaultVault(t)
			ctx := context.Background()
			name := writeDoubleClaim(ctx, t, vaultClient)

			// The delete reads the record twice before the claim check,
			// so the third read is the claim check's own.
			if entry.recordRead {
				proxy.FailReadsAfter("/data/"+doubleClaimDebtor+"/task", 2, http.StatusForbidden, "permission denied", 1)
			}

			director := cleanupGuardDirector()
			deprovisioner := cleanupGuardBroker(director, vaultClient, collidingShortPlan, collidingLongPlan)
			deprovisioner.Shield = &shield.NoopClient{}
			deprovisioner.Config = &config.Config{}
			deprovisioner.Guard = mutation.NewGuard(time.Now)

			written := len(proxy.Writes())

			status, body := deleteInstance(ctx, t, deprovisioner, doubleClaimDebtor, collidingShortPlan)
			if status != http.StatusInternalServerError || !strings.Contains(body, entry.description) {
				t.Fatalf("the delete of %s was answered %d %s, want 500 saying %q", doubleClaimDebtor, status, body, entry.description)
			}

			requireNoOtherInstanceNamed(t, body, name, doubleClaimDebtor, doubleClaimOwner)

			if writes := proxy.Writes()[written:]; len(writes) != 0 {
				t.Fatalf("the refused delete wrote %v", writes)
			}

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
				t.Fatalf("the delete of %s deleted %v", doubleClaimDebtor, deleted)
			}

			requireCleanupStillOwed(ctx, t, vaultClient, doubleClaimDebtor)

			attention := deprovisioner.CleanupAttention()

			switch {
			case !entry.row && len(attention) != 0:
				t.Fatalf("the refused delete left %+v for an operator, want nothing", attention)
			case entry.row && (len(attention) != 1 || attention[0].InstanceID != doubleClaimDebtor || attention[0].DeploymentName != name ||
				!strings.Contains(attention[0].Reason, "The platform's delete of this instance was refused, because instance abc also claims deployment redis-small-x-abc, through its index entry, so two instances claim one name")):
				t.Fatalf("the refused delete left %+v for an operator, want one row for %s that names the double claim", attention, doubleClaimDebtor)
			}
		})
	}
}

// doubleClaimStep is one try of a refused delete or cleanup of x-abc, and
// the reason the row must give after it. keeps says the row must keep the
// time it had before the step, so the step neither dropped nor replaced it.
type doubleClaimStep struct {
	deletes   bool
	indexRead bool
	reason    string
	keeps     bool
}

// The row that lists x-abc under Attention required holds still while the
// sweep retries its cleanup and while a delete from the platform and the
// sweep take turns. A cleanup that repeats its refusal keeps the row's
// time and reason, and so does one whose check of the other claims fails.
// A delete leaves a row only when none is held, with a reason of its own
// that says the platform's delete was refused, so the cleanup's refusal
// replaces it once and no later try changes it, even when that refusal is
// the cleanup's own because the record names no deployment.
func TestDoubleClaimRow_HoldsStillAcrossRetries(t *testing.T) {
	t.Parallel()

	const (
		doubleClaim  = "Instance abc also claims deployment redis-small-x-abc, through its index entry, so two instances claim one name"
		deleteRefuse = "The platform's delete of this instance was refused, because instance abc also claims deployment redis-small-x-abc, through its index entry"
		unnamed      = "Neither the record nor an index entry names a deployment for this instance, and the director has no task with context ID context-7"
	)

	cases := []struct {
		name    string
		unnamed bool
		steps   []doubleClaimStep
	}{
		{name: "the sweep retries the cleanup", steps: []doubleClaimStep{
			{reason: doubleClaim},
			{reason: doubleClaim, keeps: true},
			{indexRead: true, reason: doubleClaim, keeps: true},
		}},
		{name: "a delete and the sweep take turns", steps: []doubleClaimStep{
			{deletes: true, reason: deleteRefuse},
			{reason: doubleClaim},
			{deletes: true, reason: doubleClaim, keeps: true},
			{reason: doubleClaim, keeps: true},
		}},
		{name: "a delete and the sweep take turns on a record that names no deployment", unnamed: true, steps: []doubleClaimStep{
			{deletes: true, reason: deleteRefuse},
			{reason: unnamed},
			{deletes: true, reason: unnamed, keeps: true},
			{reason: unnamed, keeps: true},
		}},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, proxy := claimFaultVault(t)
			ctx := context.Background()

			var name string
			if entry.unnamed {
				name = writeUnnamedDoubleClaim(ctx, t, vaultClient)
			} else {
				name = writeDoubleClaim(ctx, t, vaultClient)
			}

			director := cleanupGuardDirector()
			director.FindTaskByContextIDFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no task with the context ID is a nil task without an error

			refuser := cleanupGuardBroker(director, vaultClient, collidingShortPlan, collidingLongPlan)
			refuser.Shield = &shield.NoopClient{}
			refuser.Config = &config.Config{}
			refuser.Guard = mutation.NewGuard(time.Now)

			requireRowHoldsStill(ctx, t, refuser, proxy, name, entry.steps)

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
				t.Fatalf("the refused tries deleted %v", deleted)
			}
		})
	}
}

// writeUnnamedDoubleClaim writes the double claim of writeDoubleClaim,
// except that the record of x-abc names no deployment, as a record
// written before names were recorded does. Its request details are stored
// under redis-small-x-abc, which is how such a record claims the name.
func writeUnnamedDoubleClaim(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := indexDoubleClaimOwner(ctx, t, vaultClient)

	err := recordFailedProvisionNaming(ctx, vaultClient, doubleClaimDebtor, "")
	if err == nil {
		err = storeRequestDetailsAt(ctx, vaultClient, doubleClaimDebtor, name, collidingShortPlan)
	}

	if err != nil {
		t.Fatalf("failed to write the unnamed debt: %v", err)
	}

	return name
}

// requireRowHoldsStill runs each of steps against refuser, and after each
// one checks that the overview holds one row for x-abc with the step's
// reason, and the time it had before the step when the step keeps it.
func requireRowHoldsStill(ctx context.Context, t *testing.T, refuser *broker.Broker, proxy *testutil.VaultFaultProxy, name string, steps []doubleClaimStep) {
	t.Helper()

	var since time.Time

	for index, step := range steps {
		runDoubleClaimStep(ctx, t, refuser, proxy, name, step)

		row := refuser.CleanupAttention()
		if len(row) != 1 || row[0].InstanceID != doubleClaimDebtor || !strings.Contains(row[0].Reason, step.reason) {
			t.Fatalf("after step %d the overview lists %+v, want one row for %s that says %q", index+1, row, doubleClaimDebtor, step.reason)
		}

		if step.keeps && !row[0].Since.Equal(since) {
			t.Fatalf("step %d moved the row's time from %s to %s, so it dropped or replaced the row", index+1, since, row[0].Since)
		}

		since = row[0].Since
	}
}

// runDoubleClaimStep runs one try of step against refuser, which must
// refuse it, a delete with 500.
func runDoubleClaimStep(ctx context.Context, t *testing.T, refuser *broker.Broker, proxy *testutil.VaultFaultProxy, name string, step doubleClaimStep) {
	t.Helper()

	if step.indexRead {
		proxy.FailReads("/data/db", http.StatusForbidden, "permission denied", 1)
	}

	if !step.deletes {
		refuser.CleanupFailedDeployment(ctx, doubleClaimDebtor, name)

		return
	}

	status, body := deleteInstance(ctx, t, refuser, doubleClaimDebtor, collidingShortPlan)
	if status != http.StatusInternalServerError {
		t.Fatalf("the delete of %s was answered %d %s, want 500", doubleClaimDebtor, status, body)
	}
}
