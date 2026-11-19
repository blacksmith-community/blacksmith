package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
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

// indexDebtor indexes x-abc under plan redis-small with the name
// redis-small-x-abc, which abc's entry names as well, as instances named
// before the claim check were indexed.
func indexDebtor(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, name string) {
	t.Helper()

	err := vaultClient.Index(ctx, doubleClaimDebtor, map[string]interface{}{
		serviceIDKey: recordedNameService, recordedPlanKey: collidingShortPlan, recordedDeploymentKey: name,
		createdAtKey: time.Now().Add(-time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("failed to index %s: %v", doubleClaimDebtor, err)
	}
}

// doubleClaimDeprovisioner returns a broker over director and vaultClient
// that can accept a deprovision, as the production broker can, with a
// service context that ends with the spec.
func doubleClaimDeprovisioner(ctx context.Context, t *testing.T, director *testutil.ScriptedBOSHDirector, vaultClient *internalVault.Vault) *broker.Broker {
	t.Helper()

	deprovisioner := cleanupGuardBroker(director, vaultClient, collidingShortPlan, collidingLongPlan)
	deprovisioner.Shield = &shield.NoopClient{}
	deprovisioner.Config = &config.Config{}
	deprovisioner.Guard = mutation.NewGuard(time.Now)

	serviceCtx, stopService := context.WithCancel(context.WithoutCancel(ctx))
	t.Cleanup(stopService)

	deprovisioner.ServiceContext = serviceCtx

	return deprovisioner
}

// indexedDoubleClaim is a delete of the indexed instance asked, while
// other claims the deployment it would delete, and what it must leave.
// write writes the claims and returns the deployment name. owes says the
// record of asked owes the cleanup of that name. A read of failPath fails
// after failSkip reads that pass. row is the reason the overview's row
// must give, and no row is wanted when it is empty.
type indexedDoubleClaim struct {
	name        string
	asked       string
	other       string
	write       func(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string
	owes        bool
	failPath    string
	failSkip    int
	description string
	row         string
}

// writeIndexedDoubleClaim writes the double claim of writeDoubleClaim and
// indexes x-abc with the name too.
func writeIndexedDoubleClaim(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := writeDoubleClaim(ctx, t, vaultClient)
	indexDebtor(ctx, t, vaultClient, name)

	return name
}

// writeTwoLiveClaims indexes abc and x-abc with one name, and writes no
// record for either.
func writeTwoLiveClaims(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := indexDoubleClaimOwner(ctx, t, vaultClient)
	indexDebtor(ctx, t, vaultClient, name)

	return name
}

// writeRecordedOwnerClaim indexes x-abc with the name, and has abc claim it
// only through its record, which owes the cleanup of the name.
func writeRecordedOwnerClaim(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string {
	t.Helper()

	name := collidingLongPlan + "-" + doubleClaimOwner

	err := recordFailedMintedProvision(ctx, vaultClient, doubleClaimOwner, collidingLongPlan, name)
	if err != nil {
		t.Fatalf("failed to record the claim of %s: %v", doubleClaimOwner, err)
	}

	indexDebtor(ctx, t, vaultClient, name)

	return name
}

func indexedDoubleClaims() []indexedDoubleClaim {
	const (
		claimedTwice = "two service instances claim deployment redis-small-x-abc"
		refusedRow   = "The platform's delete of this instance was refused, because instance %s also claims deployment redis-small-x-abc, through its index entry"
		recordRow    = "The platform's delete of this instance was refused, because instance abc also claims deployment redis-small-x-abc, through its record, which owes the cleanup of a failed provision"
		refusedCheck = claimCheckFailed + ", so the deprovision of instance x-abc was refused: "
	)

	return []indexedDoubleClaim{
		{
			name: "its own record owes the cleanup of the name", asked: doubleClaimDebtor, other: doubleClaimOwner, owes: true,
			write: writeIndexedDoubleClaim, description: claimedTwice, row: fmt.Sprintf(refusedRow, doubleClaimOwner),
		},
		{
			name: "two live instances name it", asked: doubleClaimDebtor, other: doubleClaimOwner,
			write: writeTwoLiveClaims, description: claimedTwice, row: fmt.Sprintf(refusedRow, doubleClaimOwner),
		},
		{
			name: "the other of two live instances is deleted", asked: doubleClaimOwner, other: doubleClaimDebtor,
			write: writeTwoLiveClaims, description: claimedTwice, row: fmt.Sprintf(refusedRow, doubleClaimDebtor),
		},
		{
			name: "the other claims it only through its record", asked: doubleClaimDebtor, other: doubleClaimOwner,
			write: writeRecordedOwnerClaim, description: claimedTwice, row: recordRow,
		},
		{
			// The delete reads the index three times before the claim
			// check, so the fourth read is the claim check's own.
			name: "the index read of the claim check fails", asked: doubleClaimDebtor, other: doubleClaimOwner, owes: true,
			write: writeIndexedDoubleClaim, failPath: "/data/db", failSkip: 3,
			description: refusedCheck + "could not read the index",
		},
		{
			name: "the claim check cannot read the other record", asked: doubleClaimDebtor, other: doubleClaimOwner,
			write: writeRecordedOwnerClaim, failPath: "/data/" + doubleClaimOwner + "/task",
			description: refusedCheck + "could not read the record of another instance that can claim deployment redis-small-x-abc",
		},
	}
}

// A delete of an indexed instance never deletes a deployment that another
// instance claims too, since nothing shows which of the two it belongs
// to. That holds for an instance whose own record owes the cleanup of the
// name, for two live instances whose entries both name it, for the delete
// of either one, and for another instance that claims the name only
// through its record, which owes the cleanup of it. The delete is answered 500 with a description
// that names the double claim and not the other instance, nothing is
// written or deleted, and the overview lists the instance asked for with
// a reason that says the platform's delete was refused. When the claim
// check cannot read what it needs, the delete is answered 500 with
// nothing written or deleted.
func TestDeprovision_RefusesAnIndexedInstanceWhoseNameAnotherClaims(t *testing.T) {
	t.Parallel()

	for _, entry := range indexedDoubleClaims() {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, proxy := claimFaultVault(t)
			ctx := context.Background()
			name := entry.write(ctx, t, vaultClient)

			if entry.failPath != "" {
				proxy.FailReadsAfter(entry.failPath, entry.failSkip, http.StatusForbidden, "permission denied", 1)
			}

			director := cleanupGuardDirector()
			deprovisioner := doubleClaimDeprovisioner(ctx, t, director, vaultClient)

			written := len(proxy.Writes())

			status, body := deleteInstance(ctx, t, deprovisioner, entry.asked, collidingShortPlan)
			if status != http.StatusInternalServerError || !strings.Contains(body, entry.description) {
				t.Fatalf("the delete of %s was answered %d %s, want 500 saying %q", entry.asked, status, body, entry.description)
			}

			requireNoOtherInstanceNamed(t, body, name, entry.asked, entry.other)

			if writes := proxy.Writes()[written:]; len(writes) != 0 {
				t.Fatalf("the refused delete wrote %v", writes)
			}

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
				t.Fatalf("the delete of %s deleted %v", entry.asked, deleted)
			}

			if entry.owes {
				requireCleanupStillOwed(ctx, t, vaultClient, entry.asked)
			}

			requireDeleteRow(t, deprovisioner, entry.asked, name, entry.row)
		})
	}
}

// requireDeleteRow checks that the overview of b lists instanceID for
// deploymentName with a reason that says reason, or lists nothing when
// reason is empty.
func requireDeleteRow(t *testing.T, b *broker.Broker, instanceID, deploymentName, reason string) {
	t.Helper()

	attention := b.CleanupAttention()

	switch {
	case reason == "" && len(attention) != 0:
		t.Fatalf("the refused delete left %+v for an operator, want nothing", attention)
	case reason != "" && (len(attention) != 1 || attention[0].InstanceID != instanceID || attention[0].DeploymentName != deploymentName || !strings.Contains(attention[0].Reason, reason)):
		t.Fatalf("the refused delete left %+v for an operator, want one row for %s that says %q", attention, instanceID, reason)
	}
}

// goneDoubleClaim is a cleanup or a delete of a double claim, made while
// the director says the deployment is gone, or cannot say, as unknown
// sets. indexed says x-abc is indexed with the name as well.
type goneDoubleClaim struct {
	name    string
	deletes bool
	indexed bool
	unknown bool
	status  int
}

// A double claim on a deployment the director says is gone takes nothing
// from either instance, so it no longer refuses anything. The cleanup x-abc
// owes deletes nothing, its mark comes off, and the row goes. A delete of
// x-abc that the index does not have is answered 410 Gone with nothing
// written, and a delete of an indexed x-abc is accepted, as each was before
// the double claim refused it. When the director cannot say whether the
// deployment exists, the cleanup keeps the mark and the row, and the delete
// is answered 500 with nothing written and the delete's own row.
func TestDoubleClaim_SettlesOnceTheDeploymentIsGone(t *testing.T) {
	t.Parallel()

	cases := []goneDoubleClaim{
		{name: "the cleanup"},
		{name: "the cleanup when the director cannot say", unknown: true},
		{name: "a delete of an instance the index does not have", deletes: true, status: http.StatusGone},
		{name: "a delete of an instance the index does not have when the director cannot say", deletes: true, unknown: true, status: http.StatusInternalServerError},
		{name: "a delete of an indexed instance", deletes: true, indexed: true, status: http.StatusAccepted},
		{name: "a delete of an indexed instance when the director cannot say", deletes: true, indexed: true, unknown: true, status: http.StatusInternalServerError},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, proxy := claimFaultVault(t)
			ctx := context.Background()

			name := writeDoubleClaim(ctx, t, vaultClient)
			if entry.indexed {
				indexDebtor(ctx, t, vaultClient, name)
			}

			director := cleanupGuardDirector()
			director.GetDeploymentFn = deploymentNotFound

			if entry.unknown {
				director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnreachable }
			}

			refuser := doubleClaimDeprovisioner(ctx, t, director, vaultClient)

			if entry.deletes {
				requireGoneDelete(ctx, t, refuser, proxy, entry)
			} else {
				requireGoneCleanup(ctx, t, refuser, vaultClient, name, entry.unknown)
			}

			if asked := director.Calls("GetDeployment"); len(asked) == 0 {
				t.Fatalf("in %q nothing asked the director whether %s exists", entry.name, name)
			}

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 && !entry.indexed {
				t.Fatalf("in %q the director was told to delete %v", entry.name, deleted)
			}
		})
	}
}

// requireGoneDelete sends the delete of x-abc entry describes and checks
// its status, and that a delete that is not accepted wrote nothing. A
// delete refused because the director could not say must leave the
// delete's row, and one answered 410 Gone must leave none.
func requireGoneDelete(ctx context.Context, t *testing.T, refuser *broker.Broker, proxy *testutil.VaultFaultProxy, entry goneDoubleClaim) {
	t.Helper()

	written := len(proxy.Writes())

	status, body := deleteInstance(ctx, t, refuser, doubleClaimDebtor, collidingShortPlan)
	if status != entry.status {
		t.Fatalf("in %q the delete was answered %d %s, want %d", entry.name, status, body, entry.status)
	}

	if status == http.StatusAccepted {
		return
	}

	if writes := proxy.Writes()[written:]; len(writes) != 0 {
		t.Fatalf("in %q the delete wrote %v", entry.name, writes)
	}

	attention := refuser.CleanupAttention()

	switch {
	case !entry.unknown && len(attention) != 0:
		t.Fatalf("in %q the delete left %+v for an operator, want nothing", entry.name, attention)
	case entry.unknown && (len(attention) != 1 || attention[0].InstanceID != doubleClaimDebtor || attention[0].DeploymentName != collidingLongPlan+"-"+doubleClaimOwner ||
		!strings.Contains(attention[0].Reason, "The platform's delete of this instance was refused, because instance abc also claims deployment redis-small-x-abc, through its index entry")):
		t.Fatalf("in %q the delete left %+v for an operator, want one row for %s that says the platform's delete was refused", entry.name, attention, doubleClaimDebtor)
	}
}

// requireGoneCleanup runs the cleanup x-abc owes, and checks that its mark
// came off and its row went, or, when the director could not say whether
// the deployment exists, that both stayed.
func requireGoneCleanup(ctx context.Context, t *testing.T, refuser *broker.Broker, vaultClient *internalVault.Vault, name string, unknown bool) {
	t.Helper()

	refuser.CleanupFailedDeployment(ctx, doubleClaimDebtor, name)

	attention := refuser.CleanupAttention()

	if unknown {
		requireCleanupStillOwed(ctx, t, vaultClient, doubleClaimDebtor)

		if len(attention) != 1 || !strings.Contains(attention[0].Reason, "Instance abc also claims deployment redis-small-x-abc") {
			t.Fatalf("the refused cleanup left %+v for an operator, want one row that names the double claim", attention)
		}

		return
	}

	recorded, err := refuser.RecordedOperation(ctx, doubleClaimDebtor)
	if err != nil || recorded.CleanupPending {
		t.Fatalf("the record of %s is %+v, %v, want it owing no cleanup", doubleClaimDebtor, recorded, err)
	}

	if len(attention) != 0 {
		t.Fatalf("the settled cleanup left %+v for an operator, want nothing", attention)
	}
}

// goneRowsCase is a delete of x-abc made once the director says the
// deployment two instances claim is gone, after the deletes of refused
// were refused while it still existed. other has abc hold a row a cleanup
// noted, and status is the answer the delete must get.
type goneRowsCase struct {
	name    string
	write   func(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault) string
	refused []string
	other   bool
	status  int
}

// The rows refused deletes noted for a double claim say that the
// platform's delete was refused. Once the director says the deployment is
// gone, neither refusal holds, so the delete that then goes ahead drops
// the rows refused deletes noted for that deployment, for the instance
// asked and for the other claimant. A row of the other claimant that a
// cleanup noted stays, because that cleanup settles it.
func TestDoubleClaim_DropsTheDeleteRowsOnceTheDeploymentIsGone(t *testing.T) {
	t.Parallel()

	const cleanupReason = "Request details stored under redis-small-x-abc could not be read"

	cases := []goneRowsCase{
		{name: "both live instances were refused", write: writeTwoLiveClaims, refused: []string{doubleClaimDebtor, doubleClaimOwner}, status: http.StatusAccepted},
		{name: "the other claimant holds a cleanup's row", write: writeTwoLiveClaims, refused: []string{doubleClaimDebtor}, other: true, status: http.StatusAccepted},
		{name: "the instance the index does not have was refused", write: writeDoubleClaim, refused: []string{doubleClaimDebtor}, status: http.StatusGone},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, _ := claimFaultVault(t)
			ctx := context.Background()
			name := entry.write(ctx, t, vaultClient)

			director := cleanupGuardDirector()
			deleter := doubleClaimDeprovisioner(ctx, t, director, vaultClient)

			for _, asked := range entry.refused {
				status, body := deleteInstance(ctx, t, deleter, asked, collidingShortPlan)
				if status != http.StatusInternalServerError {
					t.Fatalf("the delete of %s was answered %d %s, want 500", asked, status, body)
				}
			}

			if entry.other {
				deleter.NoteCleanupAttention(doubleClaimOwner, name, cleanupReason)
			}

			held := len(entry.refused)
			if entry.other {
				held++
			}

			if rows := deleter.CleanupAttention(); len(rows) != held {
				t.Fatalf("the refused deletes left %+v for an operator, want %d rows", rows, held)
			}

			director.GetDeploymentFn = deploymentNotFound

			status, body := deleteInstance(ctx, t, deleter, doubleClaimDebtor, collidingShortPlan)
			if status != entry.status {
				t.Fatalf("the delete of %s was answered %d %s, want %d", doubleClaimDebtor, status, body, entry.status)
			}

			rows := deleter.CleanupAttention()

			switch {
			case entry.other && (len(rows) != 1 || rows[0].InstanceID != doubleClaimOwner || rows[0].Reason != cleanupReason):
				t.Fatalf("the delete left %+v for an operator, want only the cleanup's row for %s", rows, doubleClaimOwner)
			case !entry.other && len(rows) != 0:
				t.Fatalf("the delete left %+v for an operator, want nothing", rows)
			}
		})
	}
}

// sweptRowCase is a delete of x-abc refused while abc and x-abc are both
// indexed with one name, followed by settle, which changes what the
// sweep's full scans then find, and kept, which says the row must stay.
type sweptRowCase struct {
	name   string
	settle func(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, director *testutil.ScriptedBOSHDirector)
	kept   bool
}

// The row a refused delete notes for two live instances does not depend
// on a cleanup, since neither owes one. So the sweep's full scans keep
// it, with its reason and its time, while x-abc is still indexed and the
// double claim still holds, and a later refused delete leaves it as it
// is. The first full scan that finds x-abc gone from the index, abc no
// longer claiming the name, or the director saying the deployment is
// gone drops it.
func TestDeleteRow_StaysWhileTheDoubleClaimHolds(t *testing.T) {
	t.Parallel()

	unindex := func(instanceID string) func(context.Context, *testing.T, *internalVault.Vault, *testutil.ScriptedBOSHDirector) {
		return func(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, _ *testutil.ScriptedBOSHDirector) {
			t.Helper()

			err := vaultClient.Index(ctx, instanceID, nil)
			if err != nil {
				t.Fatalf("failed to take %s out of the index: %v", instanceID, err)
			}
		}
	}

	cases := []sweptRowCase{
		{name: "the double claim holds", kept: true},
		{name: "the other instance leaves the index", settle: unindex(doubleClaimOwner)},
		{name: "the instance leaves the index", settle: unindex(doubleClaimDebtor)},
		{name: "the director says the deployment is gone", settle: func(_ context.Context, _ *testing.T, _ *internalVault.Vault, director *testutil.ScriptedBOSHDirector) {
			director.GetDeploymentFn = deploymentNotFound
		}},
	}

	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			vaultClient, _ := claimFaultVault(t)
			ctx := context.Background()
			writeTwoLiveClaims(ctx, t, vaultClient)

			director := cleanupGuardDirector()
			deleter := doubleClaimDeprovisioner(ctx, t, director, vaultClient)

			now := time.Now()

			deleter.SetSweepClock(func() time.Time { return now })

			requireRefusedDelete(ctx, t, deleter)

			row := deleter.CleanupAttention()
			if len(row) != 1 {
				t.Fatalf("the refused delete left %+v for an operator, want one row", row)
			}

			if entry.settle != nil {
				entry.settle(ctx, t, vaultClient, director)
			}

			// The first pass is a full scan, and so is one a full scan's
			// interval later.
			for scan := range 2 {
				deleter.SweepOwedCleanupsOnce(ctx, time.Minute)

				now = now.Add(7 * time.Hour)

				requireSweptRow(t, deleter, row[0], entry.kept, scan+1)
			}

			if entry.kept {
				requireRefusedDelete(ctx, t, deleter)
				requireSweptRow(t, deleter, row[0], true, 3)
			}

			if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
				t.Fatalf("the sweep or the delete deleted %v", deleted)
			}
		})
	}
}

// requireRefusedDelete sends the delete of x-abc to deleter and requires
// a 500.
func requireRefusedDelete(ctx context.Context, t *testing.T, deleter *broker.Broker) {
	t.Helper()

	status, body := deleteInstance(ctx, t, deleter, doubleClaimDebtor, collidingShortPlan)
	if status != http.StatusInternalServerError {
		t.Fatalf("the delete of %s was answered %d %s, want 500", doubleClaimDebtor, status, body)
	}
}

// requireSweptRow checks that the overview of deleter still holds want,
// with its reason and its time, when kept is set, and holds nothing when
// it is not.
func requireSweptRow(t *testing.T, deleter *broker.Broker, want interfaces.CleanupAttention, kept bool, step int) {
	t.Helper()

	rows := deleter.CleanupAttention()

	switch {
	case kept && (len(rows) != 1 || rows[0].InstanceID != want.InstanceID || rows[0].Reason != want.Reason || !rows[0].Since.Equal(want.Since)):
		t.Fatalf("after step %d the overview lists %+v, want only %+v as it was", step, rows, want)
	case !kept && len(rows) != 0:
		t.Fatalf("after step %d the overview lists %+v, want nothing", step, rows)
	}
}

// A delete of an indexed instance goes ahead over a double claim when the
// director says the deployment is gone. If the director has the
// deployment again when the deprovision would send its delete, the other
// claimant may have deployed it, so nothing is deleted. The deprovision
// fails with a description that names the deployment and not the other
// instance, the index entry stays, and the overview lists the instance
// with the delete's reason.
func TestDeprovision_KeepsADoubleClaimedDeploymentThatReappears(t *testing.T) {
	t.Parallel()

	vaultClient, _ := claimFaultVault(t)
	ctx := context.Background()
	name := writeTwoLiveClaims(ctx, t, vaultClient)

	// The claim check's read is the first, and the director has the
	// deployment again on every later read.
	var reads atomic.Int32

	director := cleanupGuardDirector()
	director.GetDeploymentFn = func(asked string) (*bosh.DeploymentDetail, error) {
		if reads.Add(1) == 1 {
			return deploymentNotFound(asked)
		}

		return &bosh.DeploymentDetail{Name: asked}, nil
	}

	deleter := doubleClaimDeprovisioner(ctx, t, director, vaultClient)

	status, body := deleteInstance(ctx, t, deleter, doubleClaimDebtor, collidingShortPlan)
	if status != http.StatusAccepted {
		t.Fatalf("the delete of %s was answered %d %s, want 202", doubleClaimDebtor, status, body)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	deleter.WaitForBackground(waitCtx)

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 0 {
		t.Fatalf("the deprovision deleted %v, a deployment the director had again", deleted)
	}

	if reads.Load() < 2 {
		t.Fatalf("the director was asked for %s %d times, want the deprovision to ask again", name, reads.Load())
	}

	_, indexed, err := vaultClient.FindInstance(ctx, doubleClaimDebtor)
	if err != nil || !indexed {
		t.Fatalf("the index entry of %s is %v, %v, want it kept", doubleClaimDebtor, indexed, err)
	}

	recorded, err := deleter.RecordedOperation(ctx, doubleClaimDebtor)
	if err != nil || recorded.Action != outcomeDeprovision || recorded.State != outcomeStateFailed || !strings.Contains(recorded.Description, "Deployment "+name+", which two service instances claim, exists again") {
		t.Fatalf("the record of %s is %+v, %v, want a failed deprovision that says the deployment exists again", doubleClaimDebtor, recorded, err)
	}

	requireNoOtherInstanceNamed(t, recorded.Description, name, doubleClaimDebtor, doubleClaimOwner)
	requireDeleteRow(t, deleter, doubleClaimDebtor, name, "The platform's delete of this instance was refused, because instance abc also claims deployment redis-small-x-abc, through its index entry")
}
