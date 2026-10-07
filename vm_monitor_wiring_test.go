package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// errTasksUnavailable is a director that cannot list its tasks.
var errTasksUnavailable = errors.New("director responded with non-successful status code '503' listing tasks")

// startWiringVault starts the in-process vault and seeds the index with one
// entry for instanceID, requested three hours ago.
func startWiringVault(t *testing.T, instanceID, deployment string) *internalVault.Vault {
	t.Helper()

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

	return vault
}

// goneMonitorDirector is the director the vm-monitor talks to. It confirms
// every deployment gone and has no task running.
func goneMonitorDirector() *bosh.PooledDirector {
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

	return bosh.NewPooledDirector(director, 1, time.Second, loggerPkg.CreateSilentLogger())
}

// indexStatus reads the status an instance's index entry records.
func indexStatus(t *testing.T, vault *internalVault.Vault, instanceID string) string {
	t.Helper()

	index, err := vault.GetIndex(t.Context(), "db")
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	entry, _ := index.Data[instanceID].(map[string]interface{})
	status, _ := entry["status"].(string)

	return status
}

// The vm-monitor asks the broker for the operation claims of this process
// before it marks an instance deleted. So the monitor setupVMMonitor builds
// keeps an old, idle instance, whose deployment the director confirms gone,
// while a deprovision on the broker holds its claim, and marks it deleted
// once the deprovision gives the claim up. A monitor handed anything other
// than the broker's claims cannot see the deprovision's claim.
func TestSetupVMMonitorAsksTheBrokerForClaims(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "idle"
		deployment = "standalone-idle"
	)

	vault := startWiringVault(t, instanceID, deployment)

	// The broker's director holds the deprovision in its running-task
	// check, after the deprovision claimed the instance, until the test
	// releases it, and then fails the check, so the deprovision ends and
	// gives its claim up without writing anything.
	claimed := make(chan struct{})
	release := make(chan struct{})

	brokerDirector := testutil.NewScriptedBOSHDirector()
	brokerDirector.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
		close(claimed)
		<-release

		return nil, errTasksUnavailable
	}

	brokerInstance := &broker.Broker{Vault: vault, BOSH: brokerDirector}

	monitor := setupVMMonitor(vault, goneMonitorDirector(), brokerInstance, &config.Config{}, loggerPkg.CreateSilentLogger())
	if monitor == nil {
		t.Fatal("setupVMMonitor built no vm-monitor over a BOSH director")
	}

	deprovisionErr := make(chan error, 1)

	go func() {
		_, _, err := brokerInstance.Deprovision(t.Context(), instanceID, osbapi.DeprovisionRequest{ServiceID: "valkey", PlanID: "standalone"}, true)
		deprovisionErr <- err
	}()

	<-claimed

	err := monitor.AddService(t.Context(), instanceID, deployment)
	if err != nil {
		t.Fatalf("adding the instance: %v", err)
	}

	monitor.TriggerRefreshAll(t.Context())

	whileClaimed := indexStatus(t, vault, instanceID)

	close(release)

	err = <-deprovisionErr
	if !errors.Is(err, errTasksUnavailable) {
		t.Fatalf("the held deprovision ended with %v, want the failed running-task check", err)
	}

	if whileClaimed == "deleted" {
		t.Errorf("the vm-monitor marked an instance deleted while a deprovision on the broker held its claim; it was not handed the broker's claims")
	}

	monitor.TriggerRefreshAll(t.Context())

	if status := indexStatus(t, vault, instanceID); status != "deleted" {
		t.Errorf("the vm-monitor left an old, idle instance whose deployment is gone with status %q once no claim was held, want deleted", status)
	}
}

// The broker tells the vm-monitor about each instance whose provision
// completes, so the monitor setupVMMonitor builds starts watching an
// instance as soon as the broker's provision hook runs for it.
func TestSetupVMMonitorConnectsTheBrokerToTheMonitor(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "provisioned"
		deployment = "standalone-provisioned"
	)

	vault := startWiringVault(t, instanceID, deployment)

	err := vault.Put(t.Context(), instanceID+"/"+deployment, map[string]interface{}{
		"details": map[string]interface{}{"service_id": "valkey", "plan_id": "standalone"},
	})
	if err != nil {
		t.Fatalf("seeding the provision details: %v", err)
	}

	// The broker's director has no VMs to list, so the provision hook ends
	// after it tells the monitor about the instance.
	brokerDirector := testutil.NewScriptedBOSHDirector()
	brokerDirector.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return nil, nil }

	brokerInstance := &broker.Broker{Vault: vault, BOSH: brokerDirector}
	silent := loggerPkg.CreateSilentLogger()

	monitor := setupVMMonitor(vault, goneMonitorDirector(), brokerInstance, &config.Config{}, silent)
	if monitor == nil {
		t.Fatal("setupVMMonitor built no vm-monitor over a BOSH director")
	}

	_ = brokerInstance.OnProvisionCompleted(t.Context(), silent, instanceID)

	for _, watched := range monitor.GetStatus().Services {
		if watched.ServiceID == instanceID {
			if watched.DeploymentName != deployment {
				t.Errorf("the vm-monitor watches instance %s as deployment %q, want %q", instanceID, watched.DeploymentName, deployment)
			}

			return
		}
	}

	t.Errorf("the vm-monitor does not watch instance %s after the broker's provision hook ran; the broker was not handed the monitor", instanceID)
}
