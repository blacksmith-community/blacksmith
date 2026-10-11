package vmmonitor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/inflight"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

// Static errors for err113 compliance.
var (
	ErrVMStatusNotFound = errors.New("VM status not found")
)

const (
	vmStatusRunning = "running"

	// vmStatusProvisioning is the VM status of an instance whose deployment
	// the director says is gone, kept because it belongs to work in flight.
	vmStatusProvisioning = "provisioning"

	// vmStatusError is the VM status of an instance whose VMs could not be
	// listed, kept because nothing proved its deployment gone or because the
	// monitor could not tell whether it is in flight.
	vmStatusError = "error"
)

// keepReason says why the monitor keeps an instance whose deployment the
// director says is gone. It keeps none when reason is "". inFlight is true
// when something shows that the instance belongs to work in flight, and
// false when a read, a check, or the tombstone write failed, so the monitor
// cannot tell whether it does.
type keepReason struct {
	reason   string
	inFlight bool
}

// tombstoneOutcome says what the monitor found in the index when it marked
// an instance deleted.
type tombstoneOutcome int

const (
	// tombstoneMarked is an entry the monitor marked deleted.
	tombstoneMarked tombstoneOutcome = iota

	// tombstoneUnindexed is an instance the index no longer has, so there
	// was no entry to mark.
	tombstoneUnindexed

	// tombstoneAlreadyMarked is an entry that was already marked deleted,
	// which the monitor left as it was.
	tombstoneAlreadyMarked
)

// statusDeleted is the status of an index entry marked deleted, and the VM
// status the monitor records for it.
const statusDeleted = "deleted"

// defaultCheckTimeout is the deadline of each check's VM listing when the
// config sets none.
const defaultCheckTimeout = 5 * time.Minute

// Monitor handles scheduled VM monitoring for service instances.
type Monitor struct {
	vault        vmVault
	boshDirector bosh.Director
	claims       OperationClaims
	config       interface{}

	normalInterval time.Duration
	failedInterval time.Duration

	// checkTimeout is the deadline the monitor gives the VM listing of
	// each check, so one listing whose BOSH task waits behind a busy
	// director cannot hold a worker for longer.
	checkTimeout time.Duration

	services map[string]*ServiceMonitor

	// checking holds the IDs of the instances a check cycle is checking.
	// Another cycle skips an instance until that check ends, so two cycles
	// never check one instance at the same time. It is keyed by instance
	// ID, not by the watched record, because a rescan can drop the record
	// of an instance that is being checked and AddService can then build a
	// new one. It is guarded by mu.
	checking map[string]bool

	mu sync.RWMutex

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// vmVault abstracts the subset of Vault used by VMMonitor.
type vmVault interface {
	GetIndex(ctx context.Context, name string) (*vaultPkg.Index, error)
	Get(ctx context.Context, path string, out interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
	UpdateIndex(ctx context.Context, change func(entries map[string]interface{}) (bool, error)) error
}

// OperationClaims reports whether a provision or a deprovision running in
// this process holds the claim on an instance. The broker holds those claims
// in memory, so only a claim this process holds is seen.
type OperationClaims interface {
	OperationClaimed(instanceID string) bool
}

// ServiceMonitor tracks the monitoring state of a service instance. Its
// ServiceID and DeploymentName never change once it is built. Monitor.mu
// guards every other field, because GetStatus reads them from request
// goroutines while a check cycle updates them.
type ServiceMonitor struct {
	ServiceID      string
	DeploymentName string
	LastStatus     string
	LastCheck      time.Time
	NextCheck      time.Time
	FailureCount   int
	IsHealthy      bool
	IsProvisioning bool // true until status is "running" or "failing", uses 5-min interval; a failed check leaves it alone
}

// StemcellInfo contains basic stemcell information for a service instance.
type StemcellInfo struct {
	Name    string `json:"name"`    // e.g., "bosh-google-kvm-ubuntu-jammy-go_agent"
	OS      string `json:"os"`      // e.g., "ubuntu-jammy"
	Version string `json:"version"` // e.g., "1.73"
}

// VMStatus represents the aggregated status of VMs for a service.
type VMStatus struct {
	Status      string                 `json:"status"`
	VMCount     int                    `json:"vm_count"`
	HealthyVMs  int                    `json:"healthy_vms"`
	LastUpdated time.Time              `json:"last_updated"`
	NextUpdate  time.Time              `json:"next_update"`
	VMs         []bosh.VM              `json:"vms,omitempty"`
	Stemcell    *StemcellInfo          `json:"stemcell,omitempty"`
	Details     map[string]interface{} `json:"details,omitempty"`
}

// New creates a new VM monitor service. claims is the broker of this process,
// which the monitor asks before it marks an instance deleted.
func New(vault *internalVault.Vault, boshDirector bosh.Director, claims OperationClaims, cfg interface{}) *Monitor {
	// Default intervals if not configured
	normalInterval := 1 * time.Hour

	const defaultFailedIntervalMinutes = 5

	failedInterval := defaultFailedIntervalMinutes * time.Minute

	checkTimeout := defaultCheckTimeout

	// Access config fields through type assertion
	type configGetter interface {
		GetVMMonitoringNormalInterval() int
		GetVMMonitoringFailedInterval() int
	}

	if c, ok := cfg.(configGetter); ok {
		if interval := c.GetVMMonitoringNormalInterval(); interval > 0 {
			normalInterval = time.Duration(interval) * time.Second
		}

		if interval := c.GetVMMonitoringFailedInterval(); interval > 0 {
			failedInterval = time.Duration(interval) * time.Second
		}
	}

	// The check deadline has its own interface, so a config that offers
	// only the intervals still works and gets the default deadline.
	type timeoutGetter interface {
		GetVMMonitoringTimeout() int
	}

	if c, ok := cfg.(timeoutGetter); ok {
		if timeout := c.GetVMMonitoringTimeout(); timeout > 0 {
			checkTimeout = time.Duration(timeout) * time.Second
		}
	}

	return &Monitor{
		vault:          vault,
		boshDirector:   boshDirector,
		claims:         claims,
		config:         cfg,
		normalInterval: normalInterval,
		failedInterval: failedInterval,
		checkTimeout:   checkTimeout,
		services:       make(map[string]*ServiceMonitor),
		checking:       make(map[string]bool),
		cancel:         nil, // Will be set in Start()
	}
}

// Start begins the VM monitoring process.
func (m *Monitor) Start(ctx context.Context) error {
	// Check if monitoring is enabled through config interface
	type configChecker interface {
		IsVMMonitoringEnabled() bool
	}

	if checker, ok := m.config.(configChecker); ok && !checker.IsVMMonitoringEnabled() {
		logger.Get().Named("vm-monitor").Info("VM monitoring is disabled")

		return nil
	}

	logger := logger.Get().Named("vm-monitor")
	logger.Info("Starting VM monitor with intervals: normal=%v, failed=%v",
		m.normalInterval, m.failedInterval)

	// Create cancellable context for this VM monitor instance
	monitorCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	// Initial scan of services
	err := m.scanAllServices(monitorCtx)
	if err != nil {
		logger.Error("Failed to scan services: %s", err)

		return err
	}

	m.wg.Add(1)

	go m.monitorLoop(monitorCtx)

	// Trigger initial check for all services
	go func() {
		const initialDelaySeconds = 2
		time.Sleep(initialDelaySeconds * time.Second) // Give the monitor loop time to start
		logger.Info("Triggering initial VM check for all services")
		m.TriggerRefreshAll(monitorCtx)
	}()

	return nil
}

// Stop gracefully shuts down the VM monitor. A monitor that never started,
// such as one with VM monitoring disabled, has nothing to stop.
func (m *Monitor) Stop() {
	if m.cancel == nil {
		return
	}

	logger := logger.Get().Named("vm-monitor")
	logger.Info("Stopping VM monitor...")

	m.cancel()
	m.wg.Wait()

	logger.Info("VM monitor stopped")
}

// IsVMMonitor implements the interfaces.VMMonitor interface.
func (m *Monitor) IsVMMonitor() bool {
	return true
}

// AddService adds a new service to monitoring (called by broker on provision).
// deploymentName is the name the instance's index entry records, which the
// broker reads through Instance.DeploymentNameFor. The monitor watches it as
// it is and never rebuilds it from a plan ID, because a rebuilt name can name
// a deployment the director does not have, and the instance would then be
// taken for deleted. The service is added with IsProvisioning=true and will
// be checked every 5 minutes until it reaches "running" or "failing" status.
func (m *Monitor) AddService(ctx context.Context, instanceID, deploymentName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.services[instanceID]; exists {
		return nil // Already monitoring
	}

	m.services[instanceID] = &ServiceMonitor{
		ServiceID:      instanceID,
		DeploymentName: deploymentName,
		LastStatus:     "unknown",
		NextCheck:      time.Now(), // Check immediately
		IsHealthy:      true,
		IsProvisioning: true, // Start in provisioning mode (5-min interval)
	}

	logger.Get().Named("vm-monitor").Info("Added service %s to monitoring (provisioning mode)", instanceID)

	return nil
}

// GetServiceVMStatus retrieves VM status for a service.
func (m *Monitor) GetServiceVMStatus(ctx context.Context, serviceID string) (*VMStatus, error) {
	var statusData map[string]interface{}

	exists, err := m.vault.Get(ctx, serviceID+"/vm_status", &statusData)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM status: %w", err)
	}

	if !exists {
		return nil, ErrVMStatusNotFound
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

	// Extract stemcell info from the first VM if available
	status.Stemcell = extractStemcellFromVMs(statusData["vms"])

	return status, nil
}

// extractStemcellFromVMs extracts stemcell info from the first VM in the list.
func extractStemcellFromVMs(vmsData interface{}) *StemcellInfo {
	vms, ok := vmsData.([]interface{})
	if !ok || len(vms) == 0 {
		return nil
	}

	// Get first VM
	firstVM, ok := vms[0].(map[string]interface{})
	if !ok {
		return nil
	}

	// Extract stemcell data
	stemcellData, ok := firstVM["stemcell"].(map[string]interface{})
	if !ok {
		return nil
	}

	stemcell := &StemcellInfo{}
	if name, ok := stemcellData["name"].(string); ok {
		stemcell.Name = name
		// Extract OS from name (e.g., "bosh-google-kvm-ubuntu-jammy-go_agent" -> "ubuntu-jammy")
		stemcell.OS = extractOSFromStemcellName(name)
	}
	if version, ok := stemcellData["version"].(string); ok {
		stemcell.Version = version
	}

	// Only return if we have at least version
	if stemcell.Version == "" {
		return nil
	}

	return stemcell
}

// extractOSFromStemcellName extracts the OS name from a full stemcell name.
// e.g., "bosh-google-kvm-ubuntu-jammy-go_agent" -> "ubuntu-jammy"
func extractOSFromStemcellName(name string) string {
	// Common OS patterns in stemcell names
	osPatterns := []string{
		"ubuntu-jammy",
		"ubuntu-bionic",
		"ubuntu-xenial",
		"ubuntu-trusty",
		"centos-7",
		"windows2019",
		"windows2016",
	}

	for _, pattern := range osPatterns {
		if strings.Contains(name, pattern) {
			return pattern
		}
	}

	return ""
}

// TriggerRefresh forces an immediate refresh of a service's VMs.
func (m *Monitor) TriggerRefresh(serviceID string) error {
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
func (m *Monitor) TriggerRefreshAll(ctx context.Context) {
	logger := logger.Get().Named("vm-monitor")
	logger.Info("Triggering refresh for all services")

	m.mu.Lock()

	for _, svc := range m.services {
		svc.NextCheck = time.Now()
	}

	m.mu.Unlock()

	// Force an immediate check
	m.checkScheduledServices(ctx)
}

// scanAllServices scans all services for health status.
func (m *Monitor) scanAllServices(ctx context.Context) error {
	logger := logger.Get().Named("vm-monitor")

	// Get service index from Vault
	idx, err := m.vault.GetIndex(ctx, "db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	count := 0

	for instanceID, instanceData := range idx.Data {
		if instanceMap, ok := instanceData.(map[string]interface{}); ok {
			// Skip deleted instances
			if status, _ := instanceMap["status"].(string); status == "deleted" {
				continue
			}

			if _, hasPlan := instanceMap["plan_id"].(string); !hasPlan {
				continue
			}

			deploymentName := indexEntryDeploymentName(instanceID, instanceMap)

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

	logger.Info("Discovered %d service instances to monitor", count)

	return nil
}

// indexEntryDeploymentName returns the deployment name an index entry
// records, and builds it from the entry's plan ID and the instance ID only
// when the entry records none. The reconciler can rewrite the plan ID after
// the deployment was created, and a name built from it then names a
// deployment that does not exist, which the monitor would take for a deleted
// instance.
func indexEntryDeploymentName(instanceID string, entry map[string]interface{}) string {
	if name, _ := entry["deployment_name"].(string); name != "" {
		return name
	}

	planID, _ := entry["plan_id"].(string)

	return planID + "-" + instanceID
}

// rescanServices periodically rescans Vault index to discover new services and remove deleted ones.
// This is a safety net for edge cases (missed broker notifications, manual Vault changes, recovery).
func (m *Monitor) rescanServices(ctx context.Context) {
	logger := logger.Get().Named("vm-monitor")

	idx, err := m.vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Error("Failed to get vault index for rescan: %v", err)

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	added, removed := 0, 0

	// Add new services from index
	for instanceID, instanceData := range idx.Data {
		if _, exists := m.services[instanceID]; exists {
			continue // Already monitoring
		}

		instanceMap, ok := instanceData.(map[string]interface{})
		if !ok {
			continue
		}

		// Skip deleted instances
		if status, _ := instanceMap["status"].(string); status == "deleted" {
			continue
		}

		if _, hasPlan := instanceMap["plan_id"].(string); !hasPlan {
			continue
		}

		deploymentName := indexEntryDeploymentName(instanceID, instanceMap)
		m.services[instanceID] = &ServiceMonitor{
			ServiceID:      instanceID,
			DeploymentName: deploymentName,
			LastStatus:     "unknown",
			NextCheck:      time.Now(),
			IsHealthy:      true,
			IsProvisioning: false, // Not provisioning (discovered via rescan)
		}
		added++
	}

	// Remove deleted services from m.services
	for instanceID := range m.services {
		instanceData, exists := idx.Data[instanceID]
		if !exists {
			delete(m.services, instanceID)
			removed++

			continue
		}

		if instanceMap, ok := instanceData.(map[string]interface{}); ok {
			if status, _ := instanceMap["status"].(string); status == "deleted" {
				delete(m.services, instanceID)
				removed++
			}
		}
	}

	if added > 0 || removed > 0 {
		logger.Info("Rescan complete: added %d, removed %d services", added, removed)
	} else {
		logger.Debug("Rescan complete: no changes")
	}
}

// monitorLoop runs the continuous monitoring loop.
func (m *Monitor) monitorLoop(ctx context.Context) {
	defer m.wg.Done()

	logger := logger.Get().Named("vm-monitor")
	logger.Info("VM monitor loop started")

	ticker := time.NewTicker(1 * time.Minute)        // Check every minute for services due
	rescanTicker := time.NewTicker(m.normalInterval) // Rescan Vault index every normalInterval (1 hour)
	defer ticker.Stop()
	defer rescanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("VM monitor loop stopping (context cancelled)")

			return
		case <-ticker.C:
			// Log state before each check cycle
			m.mu.RLock()
			serviceCount := len(m.services)
			m.mu.RUnlock()
			logger.Debug("Monitor tick: %d services tracked", serviceCount)

			if serviceCount == 0 {
				logger.Warning("VM monitor has no services to track - services map is empty")
			}

			m.checkScheduledServices(ctx)
		case <-rescanTicker.C:
			logger.Info("Periodic rescan of Vault index")
			m.rescanServices(ctx)
		}
	}
}

// checkScheduledServices checks the services that are due. Two cycles can
// overlap, because the monitor loop's tick and TriggerRefreshAll both run
// one, so a cycle marks the ID of each instance it takes as being checked
// and skips an instance another cycle is still checking, even when a rescan
// and AddService rebuilt its watched record in the meantime. Every worker
// releases its instance in a deferred call, so a check that panics, returns
// early, or runs with a cancelled context still releases it.
func (m *Monitor) checkScheduledServices(ctx context.Context) {
	logger := logger.Get().Named("vm-monitor")

	m.mu.Lock()

	servicesDue := make([]*ServiceMonitor, 0)
	totalServices := len(m.services)
	now := time.Now()
	inProgress := 0

	for _, svc := range m.services {
		if m.checking[svc.ServiceID] {
			inProgress++

			continue
		}

		if now.After(svc.NextCheck) || svc.NextCheck.IsZero() {
			m.checking[svc.ServiceID] = true
			servicesDue = append(servicesDue, svc)
		}
	}

	m.mu.Unlock()

	if inProgress > 0 {
		logger.Debug("Skipping %d services that another check cycle is still checking", inProgress)
	}

	if len(servicesDue) == 0 {
		logger.Debug("No services due for check (total tracked: %d)", totalServices)

		return
	}

	logger.Info("Starting check cycle: %d services due out of %d total", len(servicesDue), totalServices)

	// Process services with concurrency limit
	const maxConcurrentChecks = 3

	sem := make(chan struct{}, maxConcurrentChecks) // Max 3 concurrent checks

	var waitGroup sync.WaitGroup

	var completed int32

	startTime := time.Now()

	for _, svc := range servicesDue {
		waitGroup.Add(1)

		sem <- struct{}{}

		go func(s *ServiceMonitor) {
			defer waitGroup.Done()
			defer func() { <-sem }()
			defer m.finishCheck(s.ServiceID)
			defer func() {
				// Track worker completion
				count := atomic.AddInt32(&completed, 1)
				logger.Debug("Worker completed check for %s (%d/%d done)", s.ServiceID, count, len(servicesDue))
			}()
			// Recover from panics to prevent goroutine death
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic in checkService for %s: %v", s.ServiceID, r)
				}
			}()

			m.checkService(ctx, s)
		}(svc)
	}

	waitGroup.Wait()

	elapsed := time.Since(startTime)
	logger.Info("Check cycle completed: %d services checked in %v", len(servicesDue), elapsed)
}

// checkService performs health check for a specific service.
func (m *Monitor) checkService(ctx context.Context, svc *ServiceMonitor) {
	logger := logger.Get().Named("vm-monitor")
	logger.Debug("Checking VMs for deployment %s (service %s)", svc.DeploymentName, svc.ServiceID)

	// The deadline bounds only the listing. When it passes, the director
	// adapter cancels the listing's BOSH task, and the check below still
	// confirms the deployment and stores the result on the service context,
	// so an expired listing is recorded as a failed check.
	listCtx, cancelList := context.WithTimeout(ctx, m.checkTimeout)
	vms, err := m.boshDirector.GetDeploymentVMs(listCtx, svc.DeploymentName)

	cancelList()

	if err != nil {
		m.handleCheckError(ctx, svc, err)

		return
	}

	// Calculate overall status
	status := m.calculateOverallStatus(vms)
	healthyCount := m.countHealthyVMs(vms)
	checkedAt := time.Now()

	nextCheck, provisioningDone := m.recordListing(svc, status, checkedAt)
	if provisioningDone {
		logger.Info("Service %s provisioning complete, switching to normal interval", svc.ServiceID)
	}

	vmStatus := VMStatus{
		Status:      status,
		VMCount:     len(vms),
		HealthyVMs:  healthyCount,
		LastUpdated: checkedAt,
		NextUpdate:  nextCheck,
		VMs:         vms,
	}

	// Store in Vault
	err = m.storeVMStatus(ctx, svc.ServiceID, vmStatus)
	if err != nil {
		logger.Error("Failed to store VM status: %s", err)
	} else {
		logger.Info("Stored VM status for %s: status=%s, healthy=%d, total=%d",
			svc.ServiceID, status, healthyCount, len(vms))
	}
}

// handleCheckError handles a failed VM listing for a service. The listing's
// error never proves the deployment gone. The director adapter wraps only
// the director's own deployment-not-found answer, its code 70000, in
// bosh.ErrDeploymentNotFound, and a plain 404 reaches the monitor as an
// error from the director's API, but the monitor does not act on the
// listing alone. The instance is marked deleted only when
// deploymentConfirmedGone says the director itself answered with its
// deployment-not-found code, and tombstoneUnlessInFlight finds nothing
// to show that the instance belongs to work in flight. Any other answer
// keeps monitoring the instance. An instance kept because it is in flight is
// held as a fresh provision is, healthy and provisioning with no failure
// counted, and its check is stored with the VM status "provisioning". Any
// other keep records a failed check, ends the instance's provisioning, and
// stores its check with "error".
func (m *Monitor) handleCheckError(ctx context.Context, svc *ServiceMonitor, err error) {
	logger := logger.Get().Named("vm-monitor")

	gone, confirmErr := m.deploymentConfirmedGone(ctx, svc.DeploymentName)

	var kept keepReason

	switch {
	case gone:
		kept = m.tombstoneUnlessInFlight(ctx, svc, err)
		if kept.reason == "" {
			return
		}

		logger.Info("Listing the VMs of deployment %s failed (%s), and the director confirms the deployment does not exist, but instance %s is kept, because %s", svc.DeploymentName, err, svc.ServiceID, kept.reason)
	case confirmErr != nil:
		logger.Error("Failed to check VMs for service %s: %s; the instance is kept, because the director %s", svc.ServiceID, err, confirmErr)
	default:
		logger.Error("Failed to check VMs for service %s: %s; the director still has deployment %s, so the instance is kept", svc.ServiceID, err, svc.DeploymentName)
	}

	status := vmStatusError
	if kept.inFlight {
		status = vmStatusProvisioning
	}

	checkedAt := time.Now()
	failureCount, nextCheck := m.recordKeep(svc, kept.inFlight, checkedAt)

	vmStatus := VMStatus{
		Status:      status,
		VMCount:     0,
		HealthyVMs:  0,
		LastUpdated: checkedAt,
		NextUpdate:  nextCheck,
		Details: map[string]interface{}{
			"error":         err.Error(),
			"failure_count": failureCount,
		},
	}

	err = m.storeVMStatus(ctx, svc.ServiceID, vmStatus)
	if err != nil {
		logger.Error("Failed to store %s status for service %s: %s", status, svc.ServiceID, err)
	}
}

// calculateOverallStatus determines the overall status from VM states.
func (m *Monitor) calculateOverallStatus(vms []bosh.VM) string {
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

// countHealthyVMs counts the number of healthy VMs.
func (m *Monitor) countHealthyVMs(vms []bosh.VM) int {
	logger := logger.Get().Named("vm-monitor")
	count := 0

	for _, vm := range vms {
		// Check JobState which represents the aggregate state of the job
		// This is what indicates if the job/service is "running" properly
		logger.Debug("VM %s/%d - JobState: %s, State: %s", vm.Job, vm.Index, vm.JobState, vm.State)

		if vm.JobState == "running" {
			count++
		}
	}

	logger.Debug("Counted %d healthy VMs out of %d total", count, len(vms))

	return count
}

// storeVMStatus stores VM status information in vault.
func (m *Monitor) storeVMStatus(ctx context.Context, serviceID string, status VMStatus) error {
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

	err := m.vault.Put(ctx, serviceID+"/vm_status", statusData)
	if err != nil {
		return fmt.Errorf("failed to store VM status: %w", err)
	}

	return nil
}

// MonitorStatus represents the current state of the VM monitor for debugging.
type MonitorStatus struct {
	Running        bool                   `json:"running"`
	ServiceCount   int                    `json:"service_count"`
	NormalInterval string                 `json:"normal_interval"`
	FailedInterval string                 `json:"failed_interval"`
	Services       []ServiceStatus        `json:"services"`
	Summary        map[string]interface{} `json:"summary"`
}

// ServiceStatus represents the monitoring state of a single service.
type ServiceStatus struct {
	ServiceID      string    `json:"service_id"`
	DeploymentName string    `json:"deployment_name"`
	LastStatus     string    `json:"last_status"`
	LastCheck      time.Time `json:"last_check"`
	NextCheck      time.Time `json:"next_check"`
	IsDue          bool      `json:"is_due"`
	FailureCount   int       `json:"failure_count"`
	IsHealthy      bool      `json:"is_healthy"`
	IsProvisioning bool      `json:"is_provisioning"`
}

// GetStatus returns current vm-monitor state for debugging and monitoring.
func (m *Monitor) GetStatus() MonitorStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	services := make([]ServiceStatus, 0, len(m.services))

	var dueCount, healthyCount, failingCount, provisioningCount int

	for _, svc := range m.services {
		isDue := now.After(svc.NextCheck) || svc.NextCheck.IsZero()
		if isDue {
			dueCount++
		}

		// An instance that is still provisioning counts as provisioning
		// only, whatever its failure count or health say.
		switch {
		case svc.IsProvisioning:
			provisioningCount++
		case svc.IsHealthy:
			healthyCount++
		default:
			failingCount++
		}

		services = append(services, ServiceStatus{
			ServiceID:      svc.ServiceID,
			DeploymentName: svc.DeploymentName,
			LastStatus:     svc.LastStatus,
			LastCheck:      svc.LastCheck,
			NextCheck:      svc.NextCheck,
			IsDue:          isDue,
			FailureCount:   svc.FailureCount,
			IsHealthy:      svc.IsHealthy,
			IsProvisioning: svc.IsProvisioning,
		})
	}

	return MonitorStatus{
		Running:        m.cancel != nil,
		ServiceCount:   len(m.services),
		NormalInterval: m.normalInterval.String(),
		FailedInterval: m.failedInterval.String(),
		Services:       services,
		Summary: map[string]interface{}{
			"total":         len(m.services),
			"due_for_check": dueCount,
			"healthy":       healthyCount,
			"failing":       failingCount,
			"provisioning":  provisioningCount,
		},
	}
}

// finishCheck marks an instance as no longer being checked, so a later
// cycle can check it again.
func (m *Monitor) finishCheck(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.checking, instanceID)
}

// recordListing records a check whose VM listing succeeded in the
// instance's state, under the monitor's lock, and returns when to check the
// instance next and whether the check ended its provisioning. A provision
// ends when the instance reaches "running" or "failing". Until then it is
// checked at the failed interval with no failure counted. After that, any
// status but "running" counts a failure and is checked at the failed
// interval, and "running" clears the failures.
func (m *Monitor) recordListing(svc *ServiceMonitor, status string, checkedAt time.Time) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	provisioningDone := false

	if svc.IsProvisioning && (status == vmStatusRunning || status == "failing") {
		svc.IsProvisioning = false
		provisioningDone = true
	}

	next := checkedAt.Add(m.failedInterval)

	switch {
	case svc.IsProvisioning:
		// A provision still in progress counts no failure.
	case status != vmStatusRunning:
		svc.IsHealthy = false
		svc.FailureCount++
	default:
		svc.IsHealthy = true
		svc.FailureCount = 0
		next = checkedAt.Add(m.normalInterval)
	}

	svc.LastStatus = status
	svc.LastCheck = checkedAt
	svc.NextCheck = next

	return next, provisioningDone
}

// recordKeep records a check that kept an instance after its VM listing
// failed in the instance's state, under the monitor's lock, and returns the
// failure count and when to check the instance next, which is at the failed
// interval. An instance in flight is held as a fresh provision is, healthy
// and provisioning with no failure counted. Any other keep counts a failure
// and marks the instance unhealthy. It leaves IsProvisioning as it was, and
// GetStatus counts an instance that is still provisioning under
// provisioning only.
func (m *Monitor) recordKeep(svc *ServiceMonitor, inFlight bool, checkedAt time.Time) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inFlight {
		svc.FailureCount = 0
		svc.IsHealthy = true
		svc.IsProvisioning = true
	} else {
		svc.FailureCount++
		svc.IsHealthy = false
	}

	svc.LastCheck = checkedAt
	svc.NextCheck = checkedAt.Add(m.failedInterval)

	return svc.FailureCount, svc.NextCheck
}

// tombstoneUnlessInFlight marks an instance whose deployment the director
// confirmed gone as deleted, stores its VM status as deleted, and stops
// monitoring it. It returns no reason when it did. It returns why the
// instance is kept when inFlightReason or writeTombstone finds that the
// instance may belong to work in flight, or when the tombstone could not be
// written, and then it writes nothing. An instance that is no longer indexed
// has no entry to mark, and an entry already marked deleted needs no mark,
// so the monitor writes nothing to the index for either, and it stops
// watching the instance all the same.
func (m *Monitor) tombstoneUnlessInFlight(ctx context.Context, svc *ServiceMonitor, listErr error) keepReason {
	logger := logger.Get().Named("vm-monitor")

	judged, alreadyMarked, kept := m.inFlightReason(ctx, svc)
	if kept.reason != "" {
		return kept
	}

	outcome := tombstoneAlreadyMarked

	if !alreadyMarked {
		var err error

		outcome, kept, err = m.writeTombstone(ctx, svc, judged)
		if err != nil {
			return keepReason{reason: fmt.Sprintf("its tombstone could not be written (%s)", err)}
		}

		if kept.reason != "" {
			return kept
		}
	}

	switch outcome {
	case tombstoneMarked:
		logger.Info("Listing the VMs of deployment %s failed (%s), and the director confirms the deployment does not exist. Marked instance %s as deleted and stopping monitoring.", svc.DeploymentName, listErr, svc.ServiceID)
	case tombstoneUnindexed:
		logger.Info("Listing the VMs of deployment %s failed (%s), and the director confirms the deployment does not exist. Instance %s is no longer in the index, so there is nothing to mark deleted, and monitoring stops.", svc.DeploymentName, listErr, svc.ServiceID)
	case tombstoneAlreadyMarked:
		logger.Info("Listing the VMs of deployment %s failed (%s), and the director confirms the deployment does not exist. Instance %s is already marked deleted, so the monitor leaves its index entry as it is and stops monitoring it.", svc.DeploymentName, listErr, svc.ServiceID)
	}

	deletedStatus := VMStatus{
		Status:      statusDeleted,
		VMCount:     0,
		HealthyVMs:  0,
		LastUpdated: time.Now(),
		NextUpdate:  time.Time{},
		Details: map[string]interface{}{
			"error":           listErr.Error(),
			"deleted":         true,
			"deleted_at":      time.Now().Format(time.RFC3339),
			"deployment_name": svc.DeploymentName,
		},
	}

	err := m.storeVMStatus(ctx, svc.ServiceID, deletedStatus)
	if err != nil {
		logger.Error("Failed to store deleted status for service %s: %s", svc.ServiceID, err)
	}

	// Remove from monitoring to avoid further BOSH queries
	m.mu.Lock()
	delete(m.services, svc.ServiceID)
	m.mu.Unlock()

	return keepReason{}
}

// writeTombstone marks an instance deleted in the index in one locked
// write, the way the broker's orphan check removes an entry. Inside that
// write it reads the entry again, and it marks the entry only when the entry
// still equals judged, the entry inFlightReason judged, and no operation in
// this process holds the claim on the instance. Otherwise it writes nothing
// and returns why the instance is kept. It writes nothing, and reports
// tombstoneUnindexed, when the instance is no longer indexed, and it writes
// nothing, and reports tombstoneAlreadyMarked, when the entry was marked
// deleted after inFlightReason read it, as an overlapping check of the same
// instance does. A claim taken after the recheck is not seen here, but that
// operation's own index write waits for the lock and lands after the mark.
func (m *Monitor) writeTombstone(ctx context.Context, svc *ServiceMonitor, judged interface{}) (tombstoneOutcome, keepReason, error) {
	var (
		outcome tombstoneOutcome
		kept    keepReason
	)

	err := m.vault.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		kept = keepReason{}

		current, exists := entries[svc.ServiceID]
		if !exists {
			outcome = tombstoneUnindexed

			return false, nil
		}

		if entryMarkedDeleted(current) {
			outcome = tombstoneAlreadyMarked

			return false, nil
		}

		outcome = tombstoneMarked

		if !reflect.DeepEqual(current, judged) {
			kept = keepReason{reason: "its index entry changed after the monitor checked whether it is in flight", inFlight: true}

			return false, nil
		}

		// This guard is defensive. inFlightReason already keeps an instance
		// when the monitor has no claims to ask, so the write is never
		// reached without them, and the guard only keeps a later change from
		// dereferencing nil here.
		if m.claims == nil {
			kept = keepReason{reason: "the monitor has no way to ask for the operation claims of this process"}

			return false, nil
		}

		if m.claims.OperationClaimed(svc.ServiceID) {
			kept = keepReason{reason: "an operation in this process claimed it after the monitor checked whether it is in flight", inFlight: true}

			return false, nil
		}

		entries[svc.ServiceID] = DeletedEntry(current)

		return true, nil
	})
	if err != nil {
		return tombstoneMarked, keepReason{}, fmt.Errorf("failed to mark instance %s as deleted: %w", svc.ServiceID, err)
	}

	return outcome, kept, nil
}

// entryMarkedDeleted reports whether an index entry is already marked
// deleted, by the monitor or by anything else that writes the mark.
func entryMarkedDeleted(entry interface{}) bool {
	fields, _ := entry.(map[string]interface{})
	status, _ := fields["status"].(string)

	return status == statusDeleted
}

// DeletedEntry returns a copy of an index entry marked deleted by the
// vm-monitor, which is the entry its tombstone writes. The mark sets status,
// deleted_at, deleted_by, and deletion_reason, and the copy keeps every
// other field of the entry. An entry that is not a map is replaced by one
// that holds only the mark. Tests that race other index writers against the
// tombstone write it through vmmonitortest.MarkDeleted.
func DeletedEntry(entry interface{}) map[string]interface{} {
	fields, _ := entry.(map[string]interface{})

	marked := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		marked[key] = value
	}

	marked["status"] = statusDeleted
	marked["deleted_at"] = time.Now().Format(time.RFC3339)
	marked["deleted_by"] = "vm-monitor"
	marked["deletion_reason"] = "deployment not found in BOSH director"

	return marked
}

// inFlightReason returns why an instance whose deployment the director says
// is gone may belong to work in flight, or no reason when nothing shows that
// it does, together with the index entry it judged, which is nil when the
// instance is not indexed. It reads the instance's index entry afresh. An
// entry already marked deleted is a tombstone, not work in flight, so for
// one it reports true with no reason and checks nothing more, whatever its
// deleted_at, which inflight.NewestTime would otherwise take for a young
// entry. For any other entry it applies the broker orphan check's rules. It
// judges the entry, and its task record, by inflight.Reason. Then it asks the director whether a task is
// running on the deployment the monitor watches, because a deploy may be
// creating it. Last, it asks whether an operation in this process holds the
// claim on the instance. A read or a check that fails keeps the instance,
// and so does a monitor built without the claims to ask, but they do not
// show that it is in flight. writeTombstone checks the entry and the claim
// again under the index lock.
func (m *Monitor) inFlightReason(ctx context.Context, svc *ServiceMonitor) (interface{}, bool, keepReason) {
	idx, err := m.vault.GetIndex(ctx, "db")
	if err != nil {
		return nil, false, keepReason{reason: fmt.Sprintf("its index entry could not be read (%s)", err)}
	}

	judged := idx.Data[svc.ServiceID]
	if entryMarkedDeleted(judged) {
		return judged, true, keepReason{}
	}

	entry, _ := judged.(map[string]interface{})

	taskReadFailed := false

	reason := inflight.Reason(entry, func() (map[string]interface{}, bool, error) {
		var task map[string]interface{}

		exists, err := m.vault.Get(ctx, svc.ServiceID+"/task", &task)
		if err != nil {
			taskReadFailed = true

			return nil, false, fmt.Errorf("read %s/task: %w", svc.ServiceID, err)
		}

		return task, exists, nil
	})
	if reason != "" {
		return judged, false, keepReason{reason: reason, inFlight: !taskReadFailed}
	}

	task, err := m.boshDirector.FindRunningTaskForDeployment(ctx, svc.DeploymentName)
	if err != nil {
		return judged, false, keepReason{reason: fmt.Sprintf("the director could not say whether a task is running on deployment %s (%s)", svc.DeploymentName, err)}
	}

	if task != nil {
		return judged, false, keepReason{reason: fmt.Sprintf("BOSH task %d (%s) is %s on deployment %s", task.ID, task.Description, task.State, svc.DeploymentName), inFlight: true}
	}

	if m.claims == nil {
		return judged, false, keepReason{reason: "the monitor has no way to ask for the operation claims of this process"}
	}

	if m.claims.OperationClaimed(svc.ServiceID) {
		return judged, false, keepReason{reason: "an operation in this process holds its claim", inFlight: true}
	}

	return judged, false, keepReason{}
}

// deploymentConfirmedGone asks the director for a deployment, and reports
// true only when the director answers with its deployment-not-found code.
// The director adapter's GetDeployment maps only that code, 70000, to
// bosh.ErrDeploymentNotFound. A deployment the director still has reports
// false with no error, and any other answer reports false with that answer,
// because it leaves the deployment's state unknown.
func (m *Monitor) deploymentConfirmedGone(ctx context.Context, deploymentName string) (bool, error) {
	_, err := m.boshDirector.GetDeployment(ctx, deploymentName)

	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, bosh.ErrDeploymentNotFound):
		return true, nil
	default:
		return false, fmt.Errorf("could not confirm whether deployment %s exists: %w", deploymentName, err)
	}
}
