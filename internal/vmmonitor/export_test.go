package vmmonitor

import (
	"context"
	"time"

	"blacksmith/internal/bosh"
)

// NewWithVault builds a Monitor over any vault that offers the calls the
// monitor makes, so a test can script the index it reads, and over the
// operation claims a test scripts.
func NewWithVault(vault vmVault, director bosh.Director, claims OperationClaims) *Monitor {
	return &Monitor{
		vault:          vault,
		boshDirector:   director,
		claims:         claims,
		normalInterval: time.Hour,
		failedInterval: 5 * time.Minute,
		checkTimeout:   defaultCheckTimeout,
		services:       make(map[string]*ServiceMonitor),
		checking:       make(map[string]bool),
	}
}

// UseVault replaces the vault a monitor reads and writes, so a test can
// build the monitor with New and still script the index it reads.
func (m *Monitor) UseVault(vault vmVault) {
	m.vault = vault
}

// SetCheckTimeout sets the deadline the monitor gives the VM listing of
// each check.
func (m *Monitor) SetCheckTimeout(timeout time.Duration) {
	m.checkTimeout = timeout
}

// OverallStatus exposes the overall status and the healthy count a check
// derives from a deployment's VMs.
func (m *Monitor) OverallStatus(vms []bosh.VM) (string, int) {
	return m.calculateOverallStatus(vms), m.countHealthyVMs(vms)
}

// ScanAllServices exposes the scan of the index that Start makes.
func (m *Monitor) ScanAllServices(ctx context.Context) error {
	return m.scanAllServices(ctx)
}

// RescanServices exposes the periodic rescan of the index.
func (m *Monitor) RescanServices(ctx context.Context) {
	m.rescanServices(ctx)
}

// CheckScheduledServices exposes one check cycle of the monitor loop.
func (m *Monitor) CheckScheduledServices(ctx context.Context) {
	m.checkScheduledServices(ctx)
}

// MonitoredDeployment returns the deployment the monitor checks for an
// instance, and false when it does not monitor the instance.
func (m *Monitor) MonitoredDeployment(instanceID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svc, monitored := m.services[instanceID]
	if !monitored {
		return "", false
	}

	return svc.DeploymentName, true
}
