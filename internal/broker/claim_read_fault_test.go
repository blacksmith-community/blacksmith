package broker_test

import (
	"context"
	"net/http"
	"testing"

	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"
)

// claimListPath is in the path of the LIST that names the instances in
// Vault, and of no read the claim check makes.
const claimListPath = "/metadata"

// claimReadFault is one read of the claim check that fails. The claim it
// writes is one that read would find, so a check that took the failure for
// "no claim" would let the request reach the other instance's deployment.
type claimReadFault struct {
	name  string
	claim func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error
	path  string
	// skip counts the reads of path that the provision and the delete make
	// before the claim check's own.
	provisionSkip, deleteSkip int
}

func claimReadFaults() []claimReadFault {
	owing := func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
		return recordFailedMintedProvision(ctx, vaultClient, holder, collidingLongPlan, deploymentName)
	}

	return []claimReadFault{
		{name: "the index read", path: "/data/db", provisionSkip: 1, deleteSkip: 2, claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
			return vaultClient.Index(ctx, holder, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: collidingLongPlan, recordedDeploymentKey: deploymentName})
		}},
		{name: "the list of instances", path: claimListPath, claim: owing},
		{name: "the record read", path: "/data/abc/task", claim: owing},
		{name: "the request details read", path: "/data/abc/redis-small-x-abc", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
			err := recordFailedProvisionNaming(ctx, vaultClient, holder, "")
			if err != nil {
				return err
			}

			return storeRequestDetailsAt(ctx, vaultClient, holder, deploymentName, collidingLongPlan)
		}},
	}
}

// A read of the claim check that fails answers 500, with nothing written
// for the instance asked for and nothing deployed or deleted, whether a
// provision or a delete of an instance the index does not have asked.
func TestClaimCheck_FailsClosedOnAFailedRead(t *testing.T) {
	t.Parallel()

	const owner = "abc"

	intruder := "x-" + owner
	name := collidingLongPlan + "-" + owner

	for _, entry := range claimReadFaults() {
		for _, deleting := range []bool{false, true} {
			request := "a provision"
			if deleting {
				request = "a delete"
			}

			t.Run(request+" when "+entry.name+" fails", func(t *testing.T) {
				t.Parallel()

				vaultClient, proxy := claimFaultVault(t)
				ctx := context.Background()

				err := entry.claim(ctx, vaultClient, owner, name)
				if err != nil {
					t.Fatalf("failed to write the claim: %v", err)
				}

				director := cleanupGuardDirector()
				status, body := sendWithFailedRead(ctx, t, entry, deleting, director, vaultClient, proxy, intruder)

				if status != http.StatusInternalServerError {
					t.Fatalf("the %s of %s was answered %d %s, want 500", request, intruder, status, body)
				}

				requireNothingWritten(ctx, t, vaultClient, intruder)

				if sent := append(director.Calls("CreateDeployment"), director.Calls(directorDeleteDeployment)...); len(sent) != 0 {
					t.Fatalf("the %s of %s sent %v to the director", request, intruder, sent)
				}
			})
		}
	}
}

// sendWithFailedRead fails the read entry names and sends a delete, or
// a provision, of instanceID under the plan that collides with the claim.
// The director says no deployment exists to a provision, so only the
// claim check can stop it, and says it does exist to a delete, so a delete
// that got past the check would send it.
func sendWithFailedRead(ctx context.Context, t *testing.T, entry claimReadFault, deleting bool, director *testutil.ScriptedBOSHDirector,
	vaultClient *internalVault.Vault, proxy *testutil.VaultFaultProxy, instanceID string,
) (int, string) {
	t.Helper()

	requester := cleanupGuardBroker(director, vaultClient)
	requester.Shield = &shield.NoopClient{}
	requester.Config = &config.Config{}

	if deleting {
		proxy.FailReadsAfter(entry.path, entry.deleteSkip, http.StatusForbidden, "permission denied", 1)

		return deleteInstance(ctx, t, requester, instanceID, collidingShortPlan)
	}

	director.GetDeploymentFn = deploymentNotFound

	proxy.FailReadsAfter(entry.path, entry.provisionSkip, http.StatusForbidden, "permission denied", 1)

	return putProvision(ctx, t, requester, instanceID, collidingShortPlan)
}

// claimFaultVault starts a Vault dev server behind a fault proxy and
// returns a client that talks to it through the proxy.
func claimFaultVault(t *testing.T) (*internalVault.Vault, *testutil.VaultFaultProxy) {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	proxy, err := testutil.NewVaultFaultProxy(server.Addr)
	if err != nil {
		t.Fatalf("failed to start the fault proxy: %v", err)
	}

	t.Cleanup(proxy.Close)

	return internalVault.New(proxy.Addr, server.RootToken, true), proxy
}
