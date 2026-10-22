package main

import (
	"errors"
	"testing"
	"time"

	internalCF "blacksmith/internal/cf"
	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// TestBrokerAndManagerShareOneGuard builds the broker and the API the way
// runService does, and checks that the broker's provisions and the upgrade
// manager reserve instances in the same guard.
//
//nolint:paralleltest // the in-process vault and the upgrade manager are heavy enough to run alone
func TestBrokerAndManagerShareOneGuard(t *testing.T) {
	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	vault := internalVault.New(server.Addr, server.RootToken, true)
	logger := newShutdownLog()
	cfg := &config.Config{}
	guard := mutation.NewGuard(time.Now)

	brokerInstance := newBroker(cfg, vault, nil, nil, guard)

	_, upgradeHandler := createAPIHandler(cfg, brokerInstance, vault, nil, nil, internalCF.NewManagerFromExternal(nil, logger),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, logger)
	t.Cleanup(upgradeHandler.Stop)

	manager := upgradeHandler.GetManager()

	if brokerInstance.Guard != guard {
		t.Fatal("the broker was built with a guard other than the one it was handed")
	}

	if manager.Guard() != guard {
		t.Fatal("the upgrade manager uses a guard other than the broker's")
	}

	// A reservation the upgrade manager's guard holds refuses a provision
	// of the same instance in the broker.
	release, err := manager.Guard().Acquire("shared-1", "redis-shared-1", mutation.Holder{
		Kind:        mutation.KindUpgrade,
		TaskID:      "upgrade-1",
		Description: `upgrade "noble"`,
	})
	if err != nil {
		t.Fatalf("reserving through the manager's guard: %v", err)
	}

	_, async, err := brokerInstance.Provision(t.Context(), "shared-1", osbapi.ProvisionRequest{ServiceID: "guard-service", PlanID: "guard-plan"}, true)
	if !errors.Is(err, osbapi.ErrConcurrencyError) || async {
		t.Fatalf("Provision = async %v, err %v, want a ConcurrencyError while the upgrade holds the instance", async, err)
	}

	release()

	// A reservation the broker's guard holds is visible to the manager.
	release, err = brokerInstance.Guard.Acquire("shared-2", "redis-shared-2", mutation.Holder{Kind: mutation.KindProvision, Description: "provision of shared-2"})
	if err != nil {
		t.Fatalf("reserving through the broker's guard: %v", err)
	}
	defer release()

	holder, held := manager.Guard().Holder("shared-2", "")
	if !held || holder.Kind != mutation.KindProvision {
		t.Fatalf("manager's guard Holder = %+v, %v, want the broker's provision", holder, held)
	}
}
