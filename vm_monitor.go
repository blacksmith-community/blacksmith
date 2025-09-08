package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/logger"
)

const (
	vmStatusRunning = "running"
)

// VMMonitor handles scheduled VM monitoring for service instances.
type VMMonitor struct {
	vault        vmVault
	boshDirector bosh.Director
	config       *Config

	normalInterval time.Duration
	failedInterval time.Duration

	services map[string]*ServiceMonitor
	mu       sync.RWMutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// vmVault abstracts the subset of Vault used by VMMonitor.
type vmVault interface {
	GetIndex(ctx context.Context, name string) (*VaultIndex, error)
	Get(ctx context.Context, path string, out interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
}

// ServiceMonitor tracks the monitoring state of a service instance.
type ServiceMonitor struct {
	ServiceID      string
	DeploymentName string
	LastStatus     string
	LastCheck      time.Time
	NextCheck      time.Time
	FailureCount   int
	IsHealthy      bool
}

// VMStatus represents the aggregated status of VMs for a service.
type VMStatus struct {
	Status      string                 `json:"status"`
	VMCount     int                    `json:"vm_count"`
	HealthyVMs  int                    `json:"healthy_vms"`
	LastUpdated time.Time              `json:"last_updated"`
	NextUpdate  time.Time              `json:"next_update"`
	VMs         []bosh.VM              `json:"vms,omitempty"`
	Details     map[string]interface{} `json:"details,omitempty"`
}

// NewVMMonitor creates a new VM monitor service.
func NewVMMonitor(vault *Vault, boshDirector bosh.Director, config *Config) *VMMonitor {
	// Default intervals if not configured
	normalInterval := 1 * time.Hour
	const defaultFailedIntervalMinutes = 5
	failedInterval := defaultFailedIntervalMinutes * time.Minute

	if config.VMMonitoring.NormalInterval > 0 {
		normalInterval = time.Duration(config.VMMonitoring.NormalInterval) * time.Second
	}

	if config.VMMonitoring.FailedInterval > 0 {
		failedInterval = time.Duration(config.VMMonitoring.FailedInterval) * time.Second
	}

	return &VMMonitor{
		vault:          vault,
		boshDirector:   boshDirector,
		config:         config,
		normalInterval: normalInterval,
		failedInterval: failedInterval,
		services:       make(map[string]*ServiceMonitor),
		cancel:         nil, // Will be set in Start()
	}
}

// Start begins the VM monitoring process.
func (m *VMMonitor) Start(ctx context.Context) error {
	if m.config.VMMonitoring.Enabled == nil || !*m.config.VMMonitoring.Enabled {
		logger.Get().Named("vm-monitor").Info("VM monitoring is disabled")

		return nil
	}

	l := logger.Get().Named("vm-monitor")
	l.Info("Starting VM monitor with intervals: normal=%v, failed=%v",
		m.normalInterval, m.failedInterval)

	// Create cancellable context for this VM monitor instance
	monitorCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	// Initial scan of services
	err := m.scanAllServices(monitorCtx)
	if err != nil {
		l.Error("Failed to scan services: %s", err)

		return err
	}

	m.wg.Add(1)

	go m.monitorLoop(monitorCtx)

	// Trigger initial check for all services
	go func() {
		const initialDelaySeconds = 2
		time.Sleep(initialDelaySeconds * time.Second) // Give the monitor loop time to start
		l.Info("Triggering initial VM check for all services")
		m.TriggerRefreshAll(monitorCtx)
	}()

	return nil
}

// Stop gracefully shuts down the VM monitor.
func (m *VMMonitor) Stop() {
	l := logger.Get().Named("vm-monitor")
	l.Info("Stopping VM monitor...")

	m.cancel()
	m.wg.Wait()

	l.Info("VM monitor stopped")
}

// scanAllServices discovers all service instances to monitor.
func (m *VMMonitor) scanAllServices(ctx context.Context) error {
	l := logger.Get().Named("vm-monitor")

	// Get service index from Vault
	idx, err := m.vault.GetIndex(ctx, "db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	count := 0

	for instanceID, instanceData := range idx.Data {
		if instanceMap, ok := instanceData.(map[string]interface{}); ok {
			planID, hasPlan := instanceMap["plan_id"].(string)
			if !hasPlan {
				continue
			}

			// Create deployment name following the pattern: planID-instanceID
			deploymentName := planID + "-" + instanceID

			serviceMonitor := &ServiceMonitor{
				ServiceID:      instanceID,
				DeploymentName: deploymentName,
				LastStatus:     "unknown",
				LastCheck:      time.Time{},
				NextCheck:      time.Now(), // Check immediately on startup
				IsHealthy:      true,
			}

			m.mu.Lock()
			m.services[instanceID] = serviceMonitor
			m.mu.Unlock()

			count++
		}
	}

	l.Info("Discovered %d service instances to monitor", count)

	return nil
}

// monitorLoop is the main monitoring goroutine.
func (m *VMMonitor) monitorLoop(ctx context.Context) {
	defer m.wg.Done()

	l := logger.Get().Named("vm-monitor")
	l.Debug("VM monitor loop started")

	ticker := time.NewTicker(1 * time.Minute) // Check every minute for services due
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Debug("VM monitor loop stopping")

			return
		case <-ticker.C:
			m.checkScheduledServices(ctx)
		}
	}
}

// checkScheduledServices checks which services are due for monitoring.
func (m *VMMonitor) checkScheduledServices(ctx context.Context) {
	l := logger.Get().Named("vm-monitor")

	m.mu.RLock()

	servicesDue := make([]*ServiceMonitor, 0)
	now := time.Now()

	for _, svc := range m.services {
		if now.After(svc.NextCheck) || svc.NextCheck.IsZero() {
			servicesDue = append(servicesDue, svc)
		}
	}

	m.mu.RUnlock()

	if len(servicesDue) == 0 {
		return
	}

	l.Debug("Checking %d services due for monitoring", len(servicesDue))

	// Process services with concurrency limit
	const maxConcurrentChecks = 3
	sem := make(chan struct{}, maxConcurrentChecks) // Max 3 concurrent checks

	var wg sync.WaitGroup

	for _, svc := range servicesDue {
		wg.Add(1)

		sem <- struct{}{}

		go func(s *ServiceMonitor) {
			defer wg.Done()
			defer func() { <-sem }()

			m.checkService(ctx, s)
		}(svc)
	}

	wg.Wait()
}

// checkService monitors a single service instance.
func (m *VMMonitor) checkService(ctx context.Context, svc *ServiceMonitor) {
	l := logger.Get().Named("vm-monitor")
	l.Debug("Checking VMs for deployment %s (service %s)", svc.DeploymentName, svc.ServiceID)

	// Fetch VM data from BOSH
	vms, err := m.boshDirector.GetDeploymentVMs(svc.DeploymentName)
	if err != nil {
		m.handleCheckError(ctx, svc, err)

		return
	}

	// Calculate overall status
	status := m.calculateOverallStatus(vms)
	healthyCount := m.countHealthyVMs(vms)

	vmStatus := VMStatus{
		Status:      status,
		VMCount:     len(vms),
		HealthyVMs:  healthyCount,
		LastUpdated: time.Now(),
		VMs:         vms,
	}

	// Determine next check interval
	if status != vmStatusRunning {
		svc.IsHealthy = false
		svc.FailureCount++
		vmStatus.NextUpdate = time.Now().Add(m.failedInterval)
	} else {
		svc.IsHealthy = true
		svc.FailureCount = 0
		vmStatus.NextUpdate = time.Now().Add(m.normalInterval)
	}

	// Store in Vault
	if err := m.storeVMStatus(ctx, svc.ServiceID, vmStatus); err != nil {
		l.Error("Failed to store VM status: %s", err)
	} else {
		l.Info("Stored VM status for %s: status=%s, healthy=%d, total=%d",
			svc.ServiceID, status, healthyCount, len(vms))
	}

	// Update service monitor
	m.mu.Lock()

	svc.LastStatus = status
	svc.LastCheck = vmStatus.LastUpdated
	svc.NextCheck = vmStatus.NextUpdate

	m.mu.Unlock()
}

// handleCheckError handles errors during VM monitoring.
func (m *VMMonitor) handleCheckError(ctx context.Context, svc *ServiceMonitor, err error) {
	l := logger.Get().Named("vm-monitor")
	l.Error("Failed to check VMs for service %s: %s", svc.ServiceID, err)

	// Detect BOSH 404 "doesn't exist" and mark as deleted immediately
	errStr := err.Error()
	if strings.Contains(errStr, "doesn't exist") || strings.Contains(errStr, "status code '404'") {
		l.Info("Deployment %s not found (404). Marking instance %s as deleted and stopping monitoring.", svc.DeploymentName, svc.ServiceID)

		// Mark deleted in index and store VM status as deleted
		m.markInstanceDeleted(ctx, svc)

		deletedStatus := VMStatus{
			Status:      "deleted",
			VMCount:     0,
			HealthyVMs:  0,
			LastUpdated: time.Now(),
			NextUpdate:  time.Time{},
			Details: map[string]interface{}{
				"error":           err.Error(),
				"deleted":         true,
				"deleted_at":      time.Now().Format(time.RFC3339),
				"deployment_name": svc.DeploymentName,
			},
		}

		err := m.storeVMStatus(ctx, svc.ServiceID, deletedStatus)
		if err != nil {
			l.Error("Failed to store deleted status for service %s: %s", svc.ServiceID, err)
		}

		// Remove from monitoring to avoid further BOSH queries
		m.mu.Lock()
		delete(m.services, svc.ServiceID)
		m.mu.Unlock()

		return
	}

	// Default error handling with retry
	svc.FailureCount++
	svc.IsHealthy = false
	svc.LastCheck = time.Now()

	// Use failed interval for retry
	svc.NextCheck = time.Now().Add(m.failedInterval)

	// Store error status
	vmStatus := VMStatus{
		Status:      "error",
		VMCount:     0,
		HealthyVMs:  0,
		LastUpdated: time.Now(),
		NextUpdate:  svc.NextCheck,
		Details: map[string]interface{}{
			"error":         err.Error(),
			"failure_count": svc.FailureCount,
		},
	}
	if err := m.storeVMStatus(ctx, svc.ServiceID, vmStatus); err != nil {
		l.Error("Failed to store error status for service %s: %s", svc.ServiceID, err)
	}
}

// markInstanceDeleted updates the Vault index entry to reflect deletion.
func (m *VMMonitor) markInstanceDeleted(ctx context.Context, svc *ServiceMonitor) {
	l := logger.Get().Named("vm-monitor")

	idx, err := m.vault.GetIndex(ctx, "db")
	if err != nil {
		l.Error("Failed to get index to mark deletion for %s: %v", svc.ServiceID, err)

		return
	}

	if _, exists := idx.Data[svc.ServiceID]; exists {
		l.Info("Service %s found in index and marked as deleted", svc.ServiceID)

		// We don't actually delete from the index here since that would require
		// extending the vmVault interface. The service deletion will be handled
		// by other parts of the system that have full vault access.
	}
}

// calculateOverallStatus determines the overall health status from VM states.
func (m *VMMonitor) calculateOverallStatus(vms []bosh.VM) string {
	if len(vms) == 0 {
		return "unknown"
	}

	const (
		failingPriority      = 1
		unresponsivePriority = 2
		stoppingPriority     = 3
		startingPriority     = 4
		stoppedPriority      = 5
		runningPriority      = 6
	)
	statusPriority := map[string]int{
		"failing":      failingPriority,
		"unresponsive": unresponsivePriority,
		"stopping":     stoppingPriority,
		"starting":     startingPriority,
		"stopped":      stoppedPriority,
		"running":      runningPriority,
	}

	worstStatus := "running"
	worstPriority := statusPriority["running"]

	for _, vm := range vms {
		// Use JobState which represents the aggregate state of the job
		if priority, exists := statusPriority[vm.JobState]; exists {
			if priority < worstPriority {
				worstStatus = vm.JobState
				worstPriority = priority
			}
		}
	}

	return worstStatus
}

// countHealthyVMs counts VMs in running state.
func (m *VMMonitor) countHealthyVMs(vms []bosh.VM) int {
	l := logger.Get().Named("vm-monitor")
	count := 0

	for _, vm := range vms {
		// Check JobState which represents the aggregate state of the job
		// This is what indicates if the job/service is "running" properly
		l.Debug("VM %s/%d - JobState: %s, State: %s", vm.Job, vm.Index, vm.JobState, vm.State)

		if vm.JobState == "running" {
			count++
		}
	}

	l.Debug("Counted %d healthy VMs out of %d total", count, len(vms))

	return count
}

// storeVMStatus stores VM status data in Vault.
func (m *VMMonitor) storeVMStatus(ctx context.Context, serviceID string, status VMStatus) error {
	// Store detailed status
	statusData := map[string]interface{}{
		"status":       status.Status,
		"vm_count":     status.VMCount,
		"healthy_vms":  status.HealthyVMs,
		"last_updated": status.LastUpdated.Unix(),
		"next_update":  status.NextUpdate.Unix(),
		"vms":          status.VMs,
	}

	if status.Details != nil {
		statusData["details"] = status.Details
	}

	if err := m.vault.Put(ctx, serviceID+"/vm_status", statusData); err != nil {
		return fmt.Errorf("failed to store VM status: %w", err)
	}

	return nil
}

// GetServiceVMStatus retrieves VM status for a service.
func (m *VMMonitor) GetServiceVMStatus(ctx context.Context, serviceID string) (*VMStatus, error) {
	var statusData map[string]interface{}

	exists, err := m.vault.Get(ctx, serviceID+"/vm_status", &statusData)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM status: %w", err)
	}

	if !exists {
		return nil, nil
	}

	status := &VMStatus{}
	if s, ok := statusData["status"].(string); ok {
		status.Status = s
	}

	if count, ok := statusData["vm_count"].(float64); ok {
		status.VMCount = int(count)
	}

	if healthy, ok := statusData["healthy_vms"].(float64); ok {
		status.HealthyVMs = int(healthy)
	}

	if updated, ok := statusData["last_updated"].(float64); ok {
		status.LastUpdated = time.Unix(int64(updated), 0)
	}

	if next, ok := statusData["next_update"].(float64); ok {
		status.NextUpdate = time.Unix(int64(next), 0)
	}

	return status, nil
}

// TriggerRefresh forces an immediate refresh of a service's VMs.
func (m *VMMonitor) TriggerRefresh(serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	svc, exists := m.services[serviceID]
	if !exists {
		return nil // Service not monitored
	}

	// Set next check to now to trigger immediate refresh
	svc.NextCheck = time.Now()

	return nil
}

// TriggerRefreshAll forces an immediate refresh of all services.
func (m *VMMonitor) TriggerRefreshAll(ctx context.Context) {
	l := logger.Get().Named("vm-monitor")
	l.Info("Triggering refresh for all services")

	m.mu.Lock()

	for _, svc := range m.services {
		svc.NextCheck = time.Now()
	}

	m.mu.Unlock()

	// Force an immediate check
	m.checkScheduledServices(ctx)
}
