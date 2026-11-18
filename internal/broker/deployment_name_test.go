package broker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/mutation"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// errStopAtVMs ends a credential read once it has named the deployment.
var errStopAtVMs = errors.New("the test stops the credential read here")

// The plans of an instance whose plan changed after it was created.
const (
	recordedNameService = "redis"
	recordedNameCreated = "redis-small"
	recordedNameCurrent = "redis-large"
	recordedPlanKey     = "plan_id"
)

// recordedNameInstance names the instance a credential reader's subtest
// reads, and the deployment its index entry records.
func recordedNameInstance(reader string) (string, string) {
	instanceID := "recorded-name-" + strings.ReplaceAll(reader, " ", "-")

	return instanceID, recordedNameCreated + "-" + instanceID
}

// recordedNamePaths names every broker path the test runs: each
// credential reader and each other path that names a deployment.
func recordedNamePaths() []string {
	readers := (&broker.Broker{}).CredentialReaders()
	sites := (&broker.Broker{}).DeploymentNameSites()
	paths := make([]string, 0, len(readers)+len(sites))

	for reader := range readers {
		paths = append(paths, reader)
	}

	for site := range sites {
		paths = append(paths, site)
	}

	return paths
}

// seedRecordedNames indexes, for every path the test runs, an instance
// whose entry records the current plan and the deployment of the plan it
// was created under. One Vault value seeds them all, because the index
// lock belongs to a Vault value and writers through separate values race.
func seedRecordedNames(t *testing.T, vaultClient *internalVault.Vault) {
	t.Helper()

	for _, path := range recordedNamePaths() {
		instanceID, recorded := recordedNameInstance(path)

		err := vaultClient.Index(context.Background(), instanceID, map[string]interface{}{
			serviceIDKey:      recordedNameService,
			recordedPlanKey:   recordedNameCurrent,
			deploymentNameKey: recorded,
		})
		if err != nil {
			t.Fatalf("failed to index the instance: %v", err)
		}
	}
}

// requireRecordedNameRead runs one credential reader and fails the test
// unless every deployment it asked the director for is the recorded one.
func requireRecordedNameRead(t *testing.T, reader string, vaultClient *internalVault.Vault, plan services.Plan) {
	t.Helper()

	instanceID, recorded := recordedNameInstance(reader)

	requireCredentialReadOf(t, reader, instanceID, recorded, vaultClient, plan)
}

// requireCredentialReadOf runs one credential reader for instanceID and
// fails the test unless every deployment it asked the director for is
// want.
func requireCredentialReadOf(t *testing.T, reader, instanceID, want string, vaultClient *internalVault.Vault, plan services.Plan) {
	t.Helper()

	var (
		askedLock sync.Mutex
		asked     []string
	)

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(deployment string) ([]bosh.VM, error) {
		askedLock.Lock()
		defer askedLock.Unlock()

		asked = append(asked, deployment)

		return nil, errStopAtVMs
	}

	brokerInstance := &broker.Broker{
		Plans: map[string]services.Plan{recordedNameService + "/" + recordedNameCurrent: plan},
		BOSH:  director,
		Vault: vaultClient,
	}

	_ = brokerInstance.CredentialReaders()[reader](context.Background(), instanceID, plan)

	askedLock.Lock()
	defer askedLock.Unlock()

	if len(asked) == 0 {
		t.Fatalf("the %s path never asked the director for the deployment's VMs", reader)
	}

	for _, deployment := range asked {
		if deployment != want {
			t.Errorf("the %s path asked for the VMs of %q, want deployment %q", reader, deployment, want)
		}
	}
}

// requireRecordedNameSite runs one path that names a deployment and fails
// the test unless the name it chose and every deployment it asked the
// director about are the recorded one.
func requireRecordedNameSite(t *testing.T, site string, vaultClient *internalVault.Vault) {
	t.Helper()

	instanceID, recorded := recordedNameInstance(site)

	director := testutil.NewScriptedBOSHDirector()
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient, Guard: mutation.NewGuard(time.Now)}

	chosen, err := brokerInstance.DeploymentNameSites()[site](context.Background(), instanceID, recordedNameCurrent)
	if err != nil {
		t.Fatalf("the %s path failed: %v", site, err)
	}

	named := director.Calls("FindRunningTaskForDeployment")
	if chosen != "" {
		named = append(named, chosen)
	}

	if len(named) == 0 {
		t.Fatalf("the %s path named no deployment", site)
	}

	for _, deployment := range named {
		if deployment != recorded {
			t.Errorf("the %s path named deployment %q, want the recorded deployment %q", site, deployment, recorded)
		}
	}
}

// After a plan change, an instance's deployment keeps the name it was
// created under, so the name starts with the old plan while the index
// records the new one. Every broker path that names the deployment, the
// credential readers and the deprovision, last_operation, and re-adoption
// paths, must name the deployment the index records, not one named from
// the new plan, which doesn't exist.
func TestDeploymentName_ReadsRecordedName(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	seedRecordedNames(t, vaultClient)

	service := &services.Service{ID: recordedNameService, Name: recordedNameService}
	plan := services.Plan{ID: recordedNameCurrent, Name: recordedNameCurrent, Service: service}

	for reader := range (&broker.Broker{}).CredentialReaders() {
		t.Run(reader, func(t *testing.T) {
			t.Parallel()

			requireRecordedNameRead(t, reader, vaultClient, plan)
		})
	}

	for site := range (&broker.Broker{}).DeploymentNameSites() {
		t.Run(site, func(t *testing.T) {
			t.Parallel()

			requireRecordedNameSite(t, site, vaultClient)
		})
	}
}

// An index entry that records neither a deployment name nor a plan gives
// no name to read, so every credential reader names the deployment from
// the plan of the request, as it did before it read the index, and not
// "-<instance>", which no deployment is named.
func TestDeploymentName_FallsBackToTheRequestPlan(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	service := &services.Service{ID: recordedNameService, Name: recordedNameService}
	plan := services.Plan{ID: recordedNameCurrent, Name: recordedNameCurrent, Service: service}

	for reader := range (&broker.Broker{}).CredentialReaders() {
		instanceID := "unnamed-" + strings.ReplaceAll(reader, " ", "-")

		err = vaultClient.Index(context.Background(), instanceID, map[string]interface{}{serviceIDKey: recordedNameService})
		if err != nil {
			t.Fatalf("failed to index the instance: %v", err)
		}
	}

	for reader := range (&broker.Broker{}).CredentialReaders() {
		t.Run(reader, func(t *testing.T) {
			t.Parallel()

			instanceID := "unnamed-" + strings.ReplaceAll(reader, " ", "-")

			requireCredentialReadOf(t, reader, instanceID, recordedNameCurrent+"-"+instanceID, vaultClient, plan)
		})
	}
}
