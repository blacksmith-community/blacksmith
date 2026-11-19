package broker_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"
)

// claimListPath is in the path of the LIST that names the instances in
// Vault, and of no read the claim check makes.
const claimListPath = "/metadata"

// claimCheckFailed begins the description of every request the claim
// check fails, before the text of the read that failed.
const claimCheckFailed = "could not check whether another instance claims deployment redis-small-x-abc"

// claimReadFault is one read of the claim check that fails. The claim it
// writes is one that read would find, so a check that took the failure for
// "no claim" would let the request reach the other instance's deployment.
// failed is the text the answer's description gives for that read.
type claimReadFault struct {
	name   string
	claim  func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error
	path   string
	failed string
	// skip counts the reads of path that the provision and the delete make
	// before the claim check's own. Every index read goes to one path, so
	// only the count can pick the claim check's out. The broker has a
	// Guard, as it does in production, so the delete reads the index
	// three times before the check, and the provision reads it once. The
	// description the answer must give is what shows that the fault landed
	// on the claim check's read and not on one before it.
	provisionSkip, deleteSkip int
}

func claimReadFaults() []claimReadFault {
	owing := func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
		return recordFailedMintedProvision(ctx, vaultClient, holder, collidingLongPlan, deploymentName)
	}

	return []claimReadFault{
		{name: "the index read", path: "/data/db", provisionSkip: 1, deleteSkip: 3, failed: "could not read the index", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
			return vaultClient.Index(ctx, holder, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: collidingLongPlan, recordedDeploymentKey: deploymentName})
		}},
		{name: "the list of instances", path: claimListPath, failed: "could not list the instances", claim: owing},
		{name: "the record read", path: "/data/abc/task", failed: "could not read the record of instance", claim: owing},
		{name: "the request details read", path: "/data/abc/redis-small-x-abc", failed: "failed to read the request details stored under", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, deploymentName string) error {
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
// provision or a delete of an instance the index does not have asked. The
// answer's description says the claim check failed and which read did.
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

				if status != http.StatusInternalServerError || !strings.Contains(body, claimCheckFailed) || !strings.Contains(body, entry.failed) {
					t.Fatalf("the %s of %s was answered %d %s, want 500 saying %q and %q", request, intruder, status, body, claimCheckFailed, entry.failed)
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
// The catalog holds both colliding plans, and the director says no
// deployment exists to a provision, so only the claim check can stop it
// from deploying. The director says the deployment does exist to a
// delete, so a delete that got past the check would send it.
func sendWithFailedRead(ctx context.Context, t *testing.T, entry claimReadFault, deleting bool, director *testutil.ScriptedBOSHDirector,
	vaultClient *internalVault.Vault, proxy *testutil.VaultFaultProxy, instanceID string,
) (int, string) {
	t.Helper()

	requester := cleanupGuardBroker(director, vaultClient)
	requester.Plans = deployablePlans(t, collidingShortPlan, collidingLongPlan)
	requester.Shield = &shield.NoopClient{}
	requester.Config = &config.Config{}
	requester.Guard = mutation.NewGuard(time.Now)
	requester.InstanceLocks = make(map[string]*sync.Mutex)

	// A provision that got past the check would start its deploy, which
	// stops with the spec.
	serviceCtx, stopService := context.WithCancel(context.WithoutCancel(ctx))
	t.Cleanup(stopService)

	requester.ServiceContext = serviceCtx

	if deleting {
		proxy.FailReadsAfter(entry.path, entry.deleteSkip, http.StatusForbidden, "permission denied", 1)

		return deleteInstance(ctx, t, requester, instanceID, collidingShortPlan)
	}

	director.GetDeploymentFn = deploymentNotFound

	proxy.FailReadsAfter(entry.path, entry.provisionSkip, http.StatusForbidden, "permission denied", 1)

	return putProvision(ctx, t, requester, instanceID, collidingShortPlan)
}

// deployablePlans returns a catalog of planIDs under the service the specs
// provision, with what a provision needs to get through validation and
// start its deploy.
func deployablePlans(t *testing.T, planIDs ...string) map[string]services.Plan {
	t.Helper()

	initScript := filepath.Join(t.TempDir(), "init")

	err := os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)
	if err != nil {
		t.Fatalf("failed to write the init script: %v", err)
	}

	plans := make(map[string]services.Plan, len(planIDs))
	for _, planID := range planIDs {
		plans[recordedNameService+"/"+planID] = services.Plan{
			ID:             planID,
			Name:           planID,
			Type:           recordedNameService,
			InitScriptPath: initScript,
			Manifest: map[string]interface{}{
				manifestNameKey:           outcomeManifestName,
				manifestInstanceGroupsKey: []interface{}{},
			},
			Service: &services.Service{ID: recordedNameService, Name: recordedNameService},
		}
	}

	return plans
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
