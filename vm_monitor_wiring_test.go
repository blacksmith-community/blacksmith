package main

import (
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// The vm-monitor asks the broker for the operation claims of this process
// before it marks an instance deleted, and a monitor built without the
// claims to ask keeps every instance, because it cannot tell whether one is
// claimed. So the monitor setupVMMonitor builds marks an old, idle instance
// deleted, whose deployment the director confirms gone, only when it was
// handed the broker's claims.
func TestSetupVMMonitorAsksTheBrokerForClaims(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "idle"
		deployment = "standalone-idle"
	)

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	vault := internalVault.New(server.Addr, server.RootToken, true)
	old := time.Now().Add(-3 * time.Hour)

	err = vault.UpdateIndex(t.Context(), func(entries map[string]interface{}) (bool, error) {
		entries[instanceID] = map[string]interface{}{
			"service_id":      "valkey",
			"plan_id":         "standalone",
			"deployment_name": deployment,
			"requested_at":    old.Format(time.RFC3339),
		}

		return true, nil
	})
	if err != nil {
		t.Fatalf("seeding the index: %v", err)
	}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
		return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
	}
	director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
		return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
	}
	director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		return nil, nil //nolint:nilnil // no running task is a nil task without an error
	}

	silent := loggerPkg.CreateSilentLogger()
	pool := bosh.NewPooledDirector(director, 1, time.Second, silent)

	monitor := setupVMMonitor(vault, pool, &broker.Broker{}, &config.Config{}, silent)
	if monitor == nil {
		t.Fatal("setupVMMonitor built no vm-monitor over a BOSH director")
	}

	err = monitor.AddService(t.Context(), instanceID, deployment)
	if err != nil {
		t.Fatalf("adding the instance: %v", err)
	}

	monitor.TriggerRefreshAll(t.Context())

	index, err := vault.GetIndex(t.Context(), "db")
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	entry, _ := index.Data[instanceID].(map[string]interface{})
	if status, _ := entry["status"].(string); status != "deleted" {
		t.Errorf("the vm-monitor left an old, idle instance whose deployment is gone with status %q, want deleted; it was built without the broker's claims: %v",
			status, entry)
	}
}
