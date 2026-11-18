package broker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
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

// seedRecordedNames indexes, for every credential reader, an instance whose
// entry records the current plan and the deployment of the plan it was
// created under. One Vault value seeds them all, because the index lock
// belongs to a Vault value and writers through separate values race.
func seedRecordedNames(t *testing.T, vaultClient *internalVault.Vault) {
	t.Helper()

	for reader := range (&broker.Broker{}).CredentialReaders() {
		instanceID, recorded := recordedNameInstance(reader)

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
		if deployment != recorded {
			t.Errorf("the %s path asked for the VMs of %q, want the recorded deployment %q", reader, deployment, recorded)
		}
	}
}

// After a plan change, an instance's deployment keeps the name it was
// created under, so the name starts with the old plan while the index
// records the new one. Every broker path that reads credentials from the
// deployment's VMs must ask the director for the deployment the index
// records, not for one named from the new plan, which doesn't exist.
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
}
