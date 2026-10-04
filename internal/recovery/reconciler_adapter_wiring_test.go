//nolint:testpackage // the test reads the adapter's unexported manager to check what it wired
package recovery

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
)

const (
	wiringZombieID   = "5c0ffee0-1111-4222-8333-444455556666"
	wiringZombieName = "valkey-standalone-" + wiringZombieID
	wiringLiveName   = "valkey-standalone-00000000-0000-4000-8000-0000000000aa"
)

// wiringCleaner records the deployments the broker hands to its cleaner.
type wiringCleaner struct {
	mu      sync.Mutex
	targets []credhub.Target
}

func (c *wiringCleaner) CleanupDeployment(_ context.Context, target credhub.Target) credhub.Result {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.targets = append(c.targets, target)

	return credhub.Result{Target: target}
}

func (c *wiringCleaner) Targets() []credhub.Target {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]credhub.Target(nil), c.targets...)
}

// The real adapter around a real broker hands the broker itself to the
// reconciler as both CredHub cleanup hooks, and an orphan index sweep that
// removes an entry Cloud Foundry asked to deprovision reaches the broker's
// cleaner with that deployment.
func TestReconcilerAdapterWiresCredentialHooksIntoTheBroker(t *testing.T) {
	t.Setenv("BLACKSMITH_RECONCILER_ENABLED", "true")

	ctx := context.Background()
	vaultClient := seedWiringVault(t)
	director := wiringDirector()

	cleaner := &wiringCleaner{}
	brokerInstance := &broker.Broker{
		BOSH:              director,
		Vault:             vaultClient,
		Config:            &config.Config{CredHub: config.CredHubConfig{DirectorName: "lab-bosh"}},
		InstanceLocks:     make(map[string]*sync.Mutex),
		CredentialCleaner: cleaner,
	}

	cfg := &config.Config{Reconciler: config.ReconcilerConfig{Enabled: true, Interval: "1h"}}
	adapter := NewReconcilerAdapter(cfg, brokerInstance, vaultClient, director, nil)

	err := adapter.Start(ctx)
	if err != nil {
		t.Fatalf("adapter start failed: %v", err)
	}

	t.Cleanup(func() { _ = adapter.Stop() })

	manager, ok := adapter.manager.(*reconciler.ReconcilerManager)
	if !ok {
		t.Fatalf("expected the adapter to hold a *reconciler.ReconcilerManager, got %T", adapter.manager)
	}

	hookCleaner, hookSweeper := manager.CredentialHooks()

	wiredBroker, isBroker := hookCleaner.(*broker.Broker)
	if !isBroker || wiredBroker != brokerInstance {
		t.Fatalf("expected the cleanup hook to be the broker itself, got %T", hookCleaner)
	}

	sweepBroker, isBroker := hookSweeper.(*broker.Broker)
	if !isBroker || sweepBroker != brokerInstance {
		t.Fatalf("expected the orphan sweep hook to be the broker itself, got %T", hookSweeper)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(cleaner.Targets()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}

	want := credhub.Target{InstanceID: wiringZombieID, DeploymentName: wiringZombieName}
	if got := cleaner.Targets(); len(got) != 1 || got[0] != want {
		t.Fatalf("expected the broker's cleaner to receive exactly %v, got %v", want, got)
	}
}

// seedWiringVault starts a vault holding one old index entry whose metadata
// records Cloud Foundry's deprovision request.
func seedWiringVault(t *testing.T) *internalVault.Vault {
	t.Helper()

	ctx := t.Context()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	stamp := time.Now().Add(-3 * time.Hour).Format(time.RFC3339)

	err = vaultClient.Index(ctx, wiringZombieID, map[string]interface{}{
		"service_id": "valkey", "plan_id": "standalone", "deployment_name": wiringZombieName,
		"reconciled": true, "reconciled_at": stamp, "discovered_at": stamp,
	})
	if err != nil {
		t.Fatalf("failed to seed the index: %v", err)
	}

	err = vaultClient.Put(ctx, wiringZombieID+"/metadata", map[string]interface{}{"delete_requested_at": stamp})
	if err != nil {
		t.Fatalf("failed to seed the metadata: %v", err)
	}

	return vaultClient
}

// wiringDirector reports one live deployment and answers 404 for every other.
func wiringDirector() *testutil.ScriptedBOSHDirector {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentsFn = func() ([]bosh.Deployment, error) {
		return []bosh.Deployment{{Name: wiringLiveName}}, nil
	}
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		if name == wiringLiveName {
			return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
		}

		return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
	}
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

	return director
}
