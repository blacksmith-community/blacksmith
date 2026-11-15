package bosh_test

import (
	"context"
	"testing"
	"time"

	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/logger"
)

// addressMonitor serves the cached VM status of each instance it holds. A
// call it does not script panics through the nil embedded VMMonitor.
type addressMonitor struct {
	interfaces.VMMonitor

	statuses map[string]*vmmonitor.VMStatus
}

func (m addressMonitor) GetServiceVMStatus(_ context.Context, id string) (*vmmonitor.VMStatus, error) {
	status, ok := m.statuses[id]
	if !ok {
		return nil, vmmonitor.ErrVMStatusNotFound
	}

	return status, nil
}

func orgSpaceHandler(monitor interfaces.VMMonitor) *boshhandler.Handler {
	return boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:    &logger.NoOpLogger{},
		Config:    statusConfig{},
		Director:  &blockingDirector{},
		Broker:    planBroker{},
		VMMonitor: monitor,
		Vault: &statusVault{records: map[string]map[string]interface{}{
			redisInstance: {
				planIDField:         redisPlan,
				"organization_name": "payments",
				"space_name":        "prod",
			},
			valkeyInstance: {planIDField: redisPlan},
		}},
	})
}

func TestStatusReportsTheOrgAndSpaceOfEachInstance(t *testing.T) {
	t.Parallel()

	answer := status(t, orgSpaceHandler(nil))

	redis := answer.Instances[redisInstance]
	if redis["organization_name"] != "payments" || redis["space_name"] != "prod" {
		t.Errorf("the instance reports org %v and space %v, want payments and prod",
			redis["organization_name"], redis["space_name"])
	}

	valkey := answer.Instances[valkeyInstance]
	if _, has := valkey["organization_name"]; has {
		t.Errorf("an instance with no stored org reports one: %v", valkey["organization_name"])
	}
}

func TestStatusReportsTheCachedVMAddressesOfEachInstance(t *testing.T) {
	t.Parallel()

	monitor := addressMonitor{statuses: map[string]*vmmonitor.VMStatus{
		redisInstance: {Status: "running", IPs: []string{"10.0.0.5", "fd00::5"}, LastUpdated: time.Now()},
	}}

	answer := status(t, orgSpaceHandler(monitor))

	ips, _ := answer.Instances[redisInstance]["vm_ips"].([]interface{})
	if len(ips) != 2 || ips[0] != "10.0.0.5" || ips[1] != "fd00::5" {
		t.Errorf("the instance reports the addresses %v, want 10.0.0.5 and fd00::5", ips)
	}

	if _, has := answer.Instances[valkeyInstance]["vm_ips"]; has {
		t.Errorf("an instance with no cached status reports addresses")
	}
}
