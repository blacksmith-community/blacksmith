package broker_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	osbapibroker "github.com/fivetwenty-io/osbapi/v2/pkg/broker"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The plans of the two instances whose minted names collide. Instance
// <id> under redis-small-x and instance x-<id> under redis-small are both
// named redis-small-x-<id>.
const (
	collidingLongPlan  = "redis-small-x"
	collidingShortPlan = "redis-small"
)

// nameClaimCase is a claim another instance may hold on the name a
// provision would mint, and whether the provision is refused over it.
// The holder is the instance <id> under collidingLongPlan, and the
// provision is of x-<id> under collidingShortPlan, unless longer is set,
// when the holder is x-<id> under collidingShortPlan and the provision is
// of <id> under collidingLongPlan.
type nameClaimCase struct {
	name    string
	claim   func(ctx context.Context, vaultClient *internalVault.Vault, holder, planID string) error
	longer  bool
	refused bool
}

// A provision must never take a deployment name another instance already
// claims, because the cleanup that instance owes would then delete the new
// instance's deployment. An index entry claims the name it records and
// the name its plan mints, and so does a deleted one, whose delete may
// still run. A record
// that owes a cleanup claims the name it holds, or, when it names none,
// the name its request details are stored under. Nothing else does, and
// the instance's own record never refuses its own provision.
func TestProvision_RefusesADeploymentNameAnotherInstanceClaims(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	for index, entry := range nameClaimCases() {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			short := "claim-" + strconv.Itoa(index)
			long := "x-" + short
			holder, holderPlan, provisioned, provisionedPlan := short, collidingLongPlan, long, collidingShortPlan

			if entry.longer {
				holder, holderPlan, provisioned, provisionedPlan = long, collidingShortPlan, short, collidingLongPlan
			}

			err := entry.claim(ctx, vaultClient, holder, holderPlan)
			if err != nil {
				t.Fatalf("failed to write the claim: %v", err)
			}

			err = cleanupGuardBroker(cleanupGuardDirector(), vaultClient).RefuseProvisionOverClaimedName(ctx, provisioned, provisionedPlan)

			name := collidingLongPlan + "-" + short

			switch {
			case entry.refused && (!errors.Is(err, osbapi.ErrBadRequest) || !strings.Contains(err.Error(), name)):
				t.Fatalf("the provision of %s was answered %v, want a bad request that names %s", provisioned, err, name)
			case entry.refused && strings.Contains(err.Error(), holder+" "):
				t.Fatalf("the refusal %q names the other instance", err)
			case !entry.refused && err != nil:
				t.Fatalf("the provision of %s was refused: %v", provisioned, err)
			}
		})
	}
}

// nameClaimCases returns the claims the name-claim spec checks.
func nameClaimCases() []nameClaimCase {
	indexed := func(fields map[string]interface{}) func(context.Context, *internalVault.Vault, string, string) error {
		return func(ctx context.Context, vaultClient *internalVault.Vault, holder, planID string) error {
			entry := map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: planID}
			for key, value := range fields {
				entry[key] = value
			}

			if name, ok := entry[recordedDeploymentKey].(string); ok && name == "" {
				entry[recordedDeploymentKey] = planID + "-" + holder
			}

			return vaultClient.Index(ctx, holder, entry)
		}
	}
	owing := func(ctx context.Context, vaultClient *internalVault.Vault, holder, planID string) error {
		return recordFailedMintedProvision(ctx, vaultClient, holder, planID, planID+"-"+holder)
	}

	return []nameClaimCase{
		{name: "an index entry mints the name under its plan", claim: indexed(nil), refused: true},
		{name: "an index entry records the name", claim: indexed(map[string]interface{}{recordedDeploymentKey: "", recordedPlanKey: clusterPlanID}), refused: true},
		{name: "a deleted instance's index entry mints the name", claim: indexed(map[string]interface{}{backfillStatusKey: indexStatusDeleted}), refused: true},
		{name: "an index entry of a longer instance ID mints the name", claim: indexed(nil), longer: true, refused: true},
		{name: "an unindexed record owing a cleanup names it", claim: owing, refused: true},
		{name: "an unindexed record of a longer instance ID owing a cleanup names it", claim: owing, longer: true, refused: true},
		{name: "an unnamed record owing a cleanup has request details stored under the name", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, planID string) error {
			err := recordFailedProvisionNaming(ctx, vaultClient, holder, "")
			if err != nil {
				return err
			}

			return storeRequestDetailsAt(ctx, vaultClient, holder, planID+"-"+holder, planID)
		}, refused: true},
		{name: "an unnamed record owing a cleanup has no request details under the name", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, _ string) error {
			return recordFailedProvisionNaming(ctx, vaultClient, holder, "")
		}},
		{name: "an unindexed record that owes no cleanup names it", claim: func(ctx context.Context, vaultClient *internalVault.Vault, holder, planID string) error {
			err := vaultClient.TrackProgressWithMintedDeployment(ctx, holder, outcomeProvision, "Creating BOSH deployment", "context-7", planID, 0, nil)
			if err != nil {
				return fmt.Errorf("recording the provision: %w", err)
			}

			return nil
		}},
		{name: "an index entry records another deployment under a plan that mints the name", claim: indexed(map[string]interface{}{recordedDeploymentKey: "other-deployment"}), refused: true},
		{name: "an index entry records another deployment under another plan", claim: indexed(map[string]interface{}{recordedDeploymentKey: "other-deployment", recordedPlanKey: clusterPlanID})},
		{name: "nothing is held for the other instance", claim: func(context.Context, *internalVault.Vault, string, string) error { return nil }},
	}
}

// The instance's own record never claims against its own provision, so a
// provision that is tried again for the same instance goes on.
func TestProvision_IsNotRefusedOverItsOwnClaim(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()
	instanceID := "claim-own"

	err = recordFailedMintedProvision(ctx, vaultClient, instanceID, standalonePlanID, standalonePlanID+"-"+instanceID)
	if err == nil {
		err = vaultClient.Index(ctx, instanceID, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: standalonePlanID})
	}

	if err != nil {
		t.Fatalf("failed to write the instance: %v", err)
	}

	err = cleanupGuardBroker(cleanupGuardDirector(), vaultClient).RefuseProvisionOverClaimedName(ctx, instanceID, standalonePlanID)
	if err != nil {
		t.Fatalf("the instance's own claim refused its provision: %v", err)
	}
}

// A provision of an instance the index does not have deploys only when the
// deployment it would create does not exist, or when the instance's own
// record names it for a provision that owes no cleanup. A deployment the
// broker cannot show is the instance's is refused as a bad request, and a
// director that cannot answer fails the request.
func TestProvision_RefusesADeploymentItCannotShowIsItsOwn(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	exists := func(name string) (*bosh.DeploymentDetail, error) { return &bosh.DeploymentDetail{Name: name}, nil }
	unreachable := func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnreachable }
	inProgress, owing, cleaned := unownedRecordWriters(vaultClient)

	cases := []struct {
		name       string
		deployment func(string) (*bosh.DeploymentDetail, error)
		record     func(context.Context, string) error
		want       error
		fails      bool
	}{
		{name: "the deployment does not exist", deployment: deploymentNotFound},
		{name: "the deployment exists and nothing names it", deployment: exists, want: osbapi.ErrBadRequest, fails: true},
		{name: "the deployment exists and the instance's provision names it", deployment: exists, record: inProgress},
		{name: "the deployment exists and the instance's failed provision still owes its cleanup", deployment: exists, record: owing, want: osbapi.ErrInstanceAlreadyExists, fails: true},
		{name: "the deployment exists and the instance's failed provision owes no cleanup", deployment: exists, record: cleaned, want: osbapi.ErrInstanceAlreadyExists, fails: true},
		{name: "the director cannot say whether it exists", deployment: unreachable, fails: true},
	}

	for index, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()

			instanceID := "unowned-" + strconv.Itoa(index)
			name := standalonePlanID + "-" + instanceID

			if entry.record != nil {
				err := entry.record(ctx, instanceID)
				if err != nil {
					t.Fatalf("failed to write the record: %v", err)
				}
			}

			director := cleanupGuardDirector()
			director.GetDeploymentFn = entry.deployment

			err := cleanupGuardBroker(director, vaultClient).RefuseProvisionOverUnownedDeployment(ctx, instanceID, name)

			requireUnownedAnswer(t, err, entry.want, entry.fails)
		})
	}
}

// unownedRecordWriters returns writers for the records of an instance
// whose provision is in progress, failed and still owes its cleanup, and
// failed with its cleanup done.
func unownedRecordWriters(vaultClient *internalVault.Vault) (func(context.Context, string) error, func(context.Context, string) error, func(context.Context, string) error) {
	inProgress := func(ctx context.Context, instanceID string) error {
		err := vaultClient.TrackProgressWithMintedDeployment(ctx, instanceID, outcomeProvision, "Creating BOSH deployment", "context-7", standalonePlanID, 0, nil)
		if err != nil {
			return fmt.Errorf("recording the provision: %w", err)
		}

		return nil
	}
	owing := func(ctx context.Context, instanceID string) error {
		return recordFailedMintedProvision(ctx, vaultClient, instanceID, standalonePlanID, standalonePlanID+"-"+instanceID)
	}
	cleaned := func(ctx context.Context, instanceID string) error {
		err := owing(ctx, instanceID)
		if err == nil {
			err = vaultClient.ClearCleanupPending(ctx, instanceID, outcomeProvision, "context-7")
		}

		if err != nil {
			return fmt.Errorf("clearing the owed cleanup: %w", err)
		}

		return nil
	}

	return inProgress, owing, cleaned
}

// requireUnownedAnswer checks the answer of the unowned-deployment check
// against the error it should wrap, or a plain failure when want is nil.
func requireUnownedAnswer(t *testing.T, err, want error, fails bool) {
	t.Helper()

	switch {
	case fails != (err != nil):
		t.Fatalf("the provision was answered %v, want a refusal %v", err, fails)
	case want != nil && !errors.Is(err, want):
		t.Fatalf("the provision was answered %v, want %v", err, want)
	case want == nil && (errors.Is(err, osbapi.ErrBadRequest) || errors.Is(err, osbapi.ErrInstanceAlreadyExists)):
		t.Fatalf("the provision was answered %v, want a plain failure", err)
	case errors.Is(err, osbapi.ErrInstanceAlreadyExists) && !strings.Contains(err.Error(), "must be deleted before a provision with the same ID"):
		t.Fatalf("the provision was answered %v, want the indexed path's wording", err)
	}
}

// The case the guard exists for, end to end. Instance <id> under plan
// redis-small-x failed before its deployment was created, and its cleanup
// is still owed because the director could not be asked. Instance x-<id>
// under plan redis-small would be named the same, so its provision is
// answered 400 before anything is written for it, and the cleanup owed by
// <id> still finds only its own deployment to delete.
func TestProvision_RefusedOverAnOwedCleanupKeepsTheOtherInstanceSafe(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	const owner = "abc"

	intruder := "x-" + owner
	name := collidingLongPlan + "-" + owner

	err = recordFailedMintedProvision(ctx, vaultClient, owner, collidingLongPlan, name)
	if err == nil {
		err = storeRequestDetailsAt(ctx, vaultClient, owner, name, collidingLongPlan)
	}

	if err == nil {
		err = vaultClient.Index(ctx, owner, map[string]interface{}{serviceIDKey: recordedNameService, recordedPlanKey: collidingLongPlan, recordedDeploymentKey: name})
	}

	if err != nil {
		t.Fatalf("failed to write the first instance: %v", err)
	}

	director := cleanupGuardDirector()
	director.GetDeploymentFn = deploymentNotFound
	provisioner := cleanupGuardBroker(director, vaultClient, collidingShortPlan)

	status, body := putProvision(ctx, t, provisioner, intruder, collidingShortPlan)
	if status != http.StatusBadRequest || !strings.Contains(body, name) {
		t.Fatalf("the provision of %s was answered %d %s, want 400 naming %s", intruder, status, body, name)
	}

	requireNothingWritten(ctx, t, vaultClient, intruder)

	if created := director.Calls("CreateDeployment"); len(created) != 0 {
		t.Fatalf("the refused provision deployed %v", created)
	}

	// The deployment exists by the time the owed cleanup is tried again,
	// and it is still the first instance's own.
	director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: deployment}, nil
	}

	provisioner.CleanupFailedDeployment(ctx, owner, name)

	if deleted := director.Calls(directorDeleteDeployment); len(deleted) != 1 || deleted[0] != name {
		t.Fatalf("the owed cleanup deleted %v, want %s once", deleted, name)
	}
}

// A deployment can carry a new instance's name without any record of it,
// such as one an operator made by hand. The provision is answered 400
// before anything is written for the instance, and nothing is deployed
// over the deployment.
func TestProvision_RefusedOverADeploymentNothingNamesDeploysNothing(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	ctx := context.Background()

	const instanceID = "unrecorded"

	name := standalonePlanID + "-" + instanceID

	director := cleanupGuardDirector()
	director.GetDeploymentFn = func(deployment string) (*bosh.DeploymentDetail, error) {
		return &bosh.DeploymentDetail{Name: deployment}, nil
	}

	status, body := putProvision(ctx, t, cleanupGuardBroker(director, vaultClient), instanceID, standalonePlanID)
	if status != http.StatusBadRequest || !strings.Contains(body, name) {
		t.Fatalf("the provision of %s was answered %d %s, want 400 naming %s", instanceID, status, body, name)
	}

	requireNothingWritten(ctx, t, vaultClient, instanceID)

	if created := director.Calls("CreateDeployment"); len(created) != 0 {
		t.Fatalf("the refused provision deployed %v", created)
	}
}

// putProvision sends the provision of instanceID under planID through the
// OSB library's HTTP handler and returns the status and body of the answer.
func putProvision(ctx context.Context, t *testing.T, b *broker.Broker, instanceID, planID string) (int, string) {
	t.Helper()

	server := httptest.NewServer(osbapibroker.NewHandler(b))
	t.Cleanup(server.Close)

	body := fmt.Sprintf(`{"service_id":%q,"plan_id":%q,"organization_guid":"org-guid","space_guid":"space-guid"}`, recordedNameService, planID)

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, server.URL+"/v2/service_instances/"+instanceID+"?accepts_incomplete=true", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set(osbapi.HeaderAPIVersion, "2.17")
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}

	return response.StatusCode, strings.TrimSpace(string(answer))
}

// requireNothingWritten checks that Vault holds no index entry and no
// record for instanceID.
func requireNothingWritten(ctx context.Context, t *testing.T, vaultClient *internalVault.Vault, instanceID string) {
	t.Helper()

	_, indexed, err := vaultClient.FindInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the index: %v", err)
	}

	state, _, _, err := vaultClient.State(ctx, instanceID)
	if err != nil {
		t.Fatalf("failed to read the record: %v", err)
	}

	if indexed || state != "not_found" {
		t.Fatalf("the refused provision left an index entry %v and a record in state %q", indexed, state)
	}
}
