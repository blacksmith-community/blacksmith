package reconciler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"blacksmith/bosh"
)

type reconcilerManager struct {
	config       ReconcilerConfig
	scanner      Scanner
	matcher      Matcher
	updater      Updater
	synchronizer Synchronizer
	broker       interface{} // Will be replaced with actual Broker type
	vault        interface{} // Will be replaced with actual Vault type
	bosh         bosh.Director
	logger       Logger
	services     []Service   // Cached service catalog
	cfManager    interface{} // CF connection manager for service enrichment

	status   Status
	statusMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	metrics MetricsCollector
}

// NewReconcilerManager creates a new reconciler manager
func NewReconcilerManager(config ReconcilerConfig, broker interface{}, vault interface{}, boshDir bosh.Director, logger Logger, cfManager interface{}) Manager {
	ctx, cancel := context.WithCancel(context.Background())

	// Convert ReconcilerConfig backup settings to BackupConfig
	backupConfig := BackupConfig{
		Enabled:          config.BackupEnabled,
		RetentionCount:   config.BackupRetention,
		RetentionDays:    config.BackupRetentionDays,
		CompressionLevel: config.BackupCompressionLevel,
		CleanupEnabled:   config.BackupCleanup,
		BackupOnUpdate:   config.BackupOnUpdate,
		BackupOnDelete:   config.BackupOnDelete,
	}

	// Create updater with CF manager if available
	var updater Updater
	if cfMgr, ok := cfManager.(CFManagerInterface); ok && cfMgr != nil {
		updater = NewVaultUpdaterWithCF(vault, logger, backupConfig, cfMgr)
	} else {
		updater = NewVaultUpdater(vault, logger, backupConfig)
	}

	return &reconcilerManager{
		config:       config,
		broker:       broker,
		vault:        vault,
		bosh:         boshDir,
		logger:       logger,
		cfManager:    cfManager,
		ctx:          ctx,
		cancel:       cancel,
		scanner:      NewBOSHScanner(boshDir, logger),
		matcher:      NewServiceMatcher(broker, logger),
		updater:      updater,
		synchronizer: NewIndexSynchronizer(vault, logger),
		metrics:      NewMetricsCollector(),
	}
}

// Start starts the reconciler
func (r *reconcilerManager) Start(ctx context.Context) error {
	if r.config.Debug {
		r.logDebug("Starting deployment reconciler with config: %+v", r.config)
	}

	if !r.config.Enabled {
		r.logInfo("Reconciler is disabled, not starting")
		return nil
	}

	r.logInfo("Starting deployment reconciler with interval %v", r.config.Interval)

	// Update status
	r.setStatus(Status{Running: true, LastRunTime: time.Now()})

	// Start background reconciliation loop
	r.wg.Add(1)
	go r.reconciliationLoop()

	// Run initial reconciliation
	go r.runReconciliation()

	return nil
}

// reconciliationLoop runs the periodic reconciliation
func (r *reconcilerManager) reconciliationLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.runReconciliation()
		case <-r.ctx.Done():
			r.logInfo("Reconciliation loop stopped")
			return
		}
	}
}

// runReconciliation performs a single reconciliation run
func (r *reconcilerManager) runReconciliation() {
	startTime := time.Now()
	r.logInfo("Starting CF-first reconciliation run")

	// Create a context with timeout for this run
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Minute) // Increased timeout for CF queries
	defer cancel()

	// Track metrics
	r.metrics.ReconciliationStarted()
	defer func() {
		r.metrics.ReconciliationCompleted(time.Since(startTime))
	}()

	// Get service catalog from broker
	var brokerServiceNames []string
	if broker, ok := r.broker.(BrokerInterface); ok {
		r.services = broker.GetServices()
		r.logDebug("Loaded %d services from broker", len(r.services))

		// Extract service names for CF discovery
		for _, svc := range r.services {
			brokerServiceNames = append(brokerServiceNames, svc.Name)
		}
	} else {
		r.logWarning("Broker does not implement GetServices, proceeding without service catalog")
		r.services = []Service{}
	}

	// Phase 1: Discover service instances from CF (PRIMARY SOURCE)
	r.logDebug("Phase 1: Discovering service instances from CF")
	cfInstances, err := r.discoverCFServiceInstances(ctx, brokerServiceNames)
	if err != nil {
		r.logError("Failed to discover CF service instances: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		// Don't return - continue with BOSH-only reconciliation as fallback
	}
	r.logInfo("Discovered %d service instances from CF", len(cfInstances))

	// Phase 2: Scan BOSH deployments for infrastructure details
	r.logDebug("Phase 2: Scanning BOSH deployments for infrastructure details")
	deployments, err := r.scanDeployments(ctx)
	if err != nil {
		r.logError("Failed to scan BOSH deployments: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		// Continue - we might still have CF data to process
	} else {
		r.logInfo("Found %d deployments in BOSH", len(deployments))
		r.metrics.DeploymentsScanned(len(deployments))
	}

	// Phase 3: Cross-reference and build  instance data
	r.logDebug("Phase 3: Cross-referencing CF instances with BOSH deployments")
	instances, err := r.buildInstanceData(ctx, cfInstances, deployments)
	if err != nil {
		r.logError("Failed to build  instance data: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}
	r.logInfo("Built  data for %d service instances", len(instances))
	r.metrics.InstancesMatched(len(instances))

	// Phase 4: Update Vault with  data
	r.logDebug("Phase 4: Updating Vault with  service instance data")
	updatedInstances, err := r.updateVault(ctx, instances)
	if err != nil {
		r.logError("Failed to update vault with  data: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}
	r.logInfo("Updated %d instances in Vault with  data", len(updatedInstances))
	r.metrics.InstancesUpdated(len(updatedInstances))

	// Phase 5: Synchronize index
	r.logDebug("Phase 5: Synchronizing service index")
	err = r.synchronizeIndex(ctx, updatedInstances)
	if err != nil {
		r.logError("Failed to synchronize index: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}

	// Phase 6: Handle orphaned instances (optional)
	r.logDebug("Phase 6: Checking for orphaned instances")
	err = r.handleOrphanedInstances(ctx, cfInstances, deployments)
	if err != nil {
		r.logWarning("Failed to handle orphaned instances: %s", err)
		// Don't fail reconciliation for orphan handling issues
	}

	// Update status with  metrics
	totalFound := len(cfInstances)
	if totalFound == 0 {
		totalFound = len(deployments) // Fallback to BOSH count if no CF instances
	}

	// Preserve any errors that were collected during reconciliation
	currentStatus := r.GetStatus()
	r.setStatus(Status{
		Running:         true,
		LastRunTime:     startTime,
		LastRunDuration: time.Since(startTime),
		InstancesFound:  totalFound,
		InstancesSynced: len(updatedInstances),
		Errors:          currentStatus.Errors, // Preserve accumulated errors
	})

	r.logInfo("CF-first reconciliation completed successfully in %v (CF: %d, BOSH: %d, Updated: %d)",
		time.Since(startTime), len(cfInstances), len(updatedInstances), len(updatedInstances))
}

// scanDeployments scans BOSH for deployments
func (r *reconcilerManager) scanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	deployments, err := r.scanner.ScanDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("scanner failed: %w", err)
	}

	r.logDebug("Scanner returned %d total deployments", len(deployments))

	// Log all deployment names for debugging
	for _, dep := range deployments {
		r.logDebug("Checking deployment: %s", dep.Name)
	}

	// Filter deployments based on naming convention
	var serviceDeployments []DeploymentInfo
	for _, dep := range deployments {
		if r.isServiceDeployment(dep.Name) {
			r.logDebug("✓ Identified as service deployment: %s", dep.Name)
			serviceDeployments = append(serviceDeployments, dep)
		} else {
			r.logDebug("✗ Not a service deployment: %s", dep.Name)
		}
	}

	r.logInfo("Filtered %d service deployments from %d total deployments", len(serviceDeployments), len(deployments))
	return serviceDeployments, nil
}

// isServiceDeployment checks if a deployment is a service deployment
func (r *reconcilerManager) isServiceDeployment(name string) bool {
	// Exclude platform deployments - these follow the pattern: environment-platform-component
	platformDeployments := []string{
		"blacksmith",
		"cf",
		"cf-app-autoscaler",
		"prometheus",
		"scheduler",
		"shield",
		"vault",
		"concourse",
		"bosh",
		"credhub",
		"uaa",
	}

	// Check if this is a platform deployment by looking for platform components
	for _, platform := range platformDeployments {
		if strings.HasSuffix(name, "-"+platform) || strings.Contains(name, "-"+platform+"-") || name == platform {
			return false
		}
	}

	// Service deployments follow the pattern: {plan-id}-{instance-id}
	// Where instance-id is a UUID with format: 8-4-4-4-12 hex digits
	// Examples:
	//   - rabbitmq-single-node-65c0db3d-e63b-4b87-912c-07173486cc94
	//   - rabbitmq-three-node-c425ddf5-af93-4830-a1dc-d34ce1942a40
	//   - redis-cache-small-1510f7aa-919b-4b71-b756-36d5932ad617

	parts := strings.Split(name, "-")

	// Need at least 6 parts: minimum 1 for plan-id + 5 for UUID
	if len(parts) < 6 {
		return false
	}

	// Check if the last 5 parts form a valid UUID when joined
	if len(parts) >= 5 {
		potentialUUID := strings.Join(parts[len(parts)-5:], "-")
		if r.isValidUUID(potentialUUID) {
			r.logDebug("Identified service deployment: %s (UUID: %s)", name, potentialUUID)
			return true
		}
	}

	return false
}

// processBatch processes a batch of deployments
func (r *reconcilerManager) processBatch(ctx context.Context, deployments []DeploymentInfo) ([]MatchedDeployment, error) {
	var matches []MatchedDeployment
	var mu sync.Mutex
	var wg sync.WaitGroup

	semaphore := make(chan struct{}, r.config.MaxConcurrency)

	for _, dep := range deployments {
		wg.Add(1)
		go func(deployment DeploymentInfo) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Try to match even without details first
			matchResult, err := r.matcher.MatchDeployment(deployment, r.services)
			if err != nil {
				r.logError("Failed to match deployment %s: %s", deployment.Name, err)
				return
			}

			// If no match, no need to get details
			if matchResult == nil {
				r.logDebug("No match found for deployment %s", deployment.Name)
				return
			}

			// Get detailed information
			details, err := r.scanner.GetDeploymentDetails(ctx, deployment.Name)
			if err != nil {
				r.logError("Failed to get details for %s: %s", deployment.Name, err)
				// Still record the match with basic deployment info
				details = &DeploymentDetail{
					DeploymentInfo: deployment,
				}
			}

			mu.Lock()
			matches = append(matches, MatchedDeployment{
				Deployment: *details,
				Match:      *matchResult,
			})
			mu.Unlock()
			r.logDebug("Matched deployment %s to service %s plan %s instance %s",
				deployment.Name, matchResult.ServiceID, matchResult.PlanID, matchResult.InstanceID)
		}(dep)
	}

	wg.Wait()
	return matches, nil
}

// mergeInstanceData merges existing and new instance data
func (r *reconcilerManager) mergeInstanceData(existing, new *InstanceData) *InstanceData {
	// Keep existing creation time
	new.CreatedAt = existing.CreatedAt

	// List of fields to always preserve from existing
	preserveFields := []string{
		"history",
		"dashboard_url",
		"context",
		"maintenance_info",
		"bindings_count",
		"binding_ids",
		"provision_params",
		"update_params",
		"organization_id",
		"space_id",
		"parameters",
		"has_credentials",
		"has_bindings",
		"original_request",
		"created_by",
	}

	// Merge metadata
	if existing.Metadata != nil {
		if new.Metadata == nil {
			new.Metadata = make(map[string]interface{})
		}

		// Preserve listed fields
		for _, field := range preserveFields {
			if v, ok := existing.Metadata[field]; ok && v != nil {
				new.Metadata[field] = v
			}
		}

		// Also preserve any fields starting with "original_" or "provision_"
		for k, v := range existing.Metadata {
			if v != nil && (strings.HasPrefix(k, "original_") || strings.HasPrefix(k, "provision_")) {
				new.Metadata[k] = v
			}
		}
	}

	return new
}

// synchronizeIndex synchronizes the vault index
func (r *reconcilerManager) synchronizeIndex(ctx context.Context, instances []InstanceData) error {
	return r.synchronizer.SyncIndex(ctx, instances)
}

// Stop stops the reconciler
func (r *reconcilerManager) Stop() error {
	r.logInfo("Stopping deployment reconciler")
	r.cancel()
	r.wg.Wait()
	r.setStatus(Status{Running: false})
	return nil
}

// ForceReconcile forces an immediate reconciliation
func (r *reconcilerManager) ForceReconcile() error {
	r.logInfo("Force reconciliation requested")
	go r.runReconciliation()
	return nil
}

// GetStatus returns the current status
func (r *reconcilerManager) GetStatus() Status {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.status
}

// setStatus sets the status
func (r *reconcilerManager) setStatus(status Status) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.status = status
}

// updateStatusError updates the status with an error
func (r *reconcilerManager) updateStatusError(err error) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.status.Errors = append(r.status.Errors, err)
}

// Logging helper methods - these will be replaced with actual logger calls
func (r *reconcilerManager) logDebug(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Debug(format, args...)
	} else {
		fmt.Printf("[DEBUG] reconciler: "+format+"\n", args...)
	}
}

func (r *reconcilerManager) logInfo(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Info(format, args...)
	} else {
		fmt.Printf("[INFO] reconciler: "+format+"\n", args...)
	}
}

func (r *reconcilerManager) logWarning(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Warning(format, args...)
	} else {
		fmt.Printf("[WARN] reconciler: "+format+"\n", args...)
	}
}

func (r *reconcilerManager) logError(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Error(format, args...)
	} else {
		fmt.Printf("[ERROR] reconciler: "+format+"\n", args...)
	}
}

// discoverCFServiceInstances discovers service instances from CF API
func (r *reconcilerManager) discoverCFServiceInstances(ctx context.Context, brokerServiceNames []string) ([]CFServiceInstanceDetails, error) {
	if r.cfManager == nil {
		r.logDebug("CF manager not available for service instance discovery")
		return nil, nil
	}

	// Type assert to get the CF discovery interface
	if cfMgr, ok := r.cfManager.(interface {
		DiscoverAllServiceInstances(brokerServices []string) ([]CFServiceInstanceDetails, error)
	}); ok {
		r.logDebug("Using CF manager to discover service instances for services: %v", brokerServiceNames)
		instances, err := cfMgr.DiscoverAllServiceInstances(brokerServiceNames)
		if err != nil {
			r.logError("Failed to discover service instances from CF: %v", err)
			return nil, err
		}
		r.logDebug("CF manager returned %d service instances", len(instances))
		return instances, nil
	} else {
		r.logDebug("CF manager does not support service instance discovery")
		return nil, nil
	}
}

// buildInstanceData combines CF and BOSH data to build complete instance information
// Uses BOSH deployments as the source of truth since they contain the correct naming and GUID structure
func (r *reconcilerManager) buildInstanceData(ctx context.Context, cfInstances []CFServiceInstanceDetails, boshDeployments []DeploymentInfo) ([]InstanceData, error) {
	var instances []InstanceData

	// Create a map of CF instances by GUID for quick lookup
	cfInstanceMap := make(map[string]CFServiceInstanceDetails)
	for _, cfInstance := range cfInstances {
		cfInstanceMap[cfInstance.GUID] = cfInstance
	}

	// Build  data starting with BOSH deployments as the source of truth
	for _, boshDeployment := range boshDeployments {
		// Parse the deployment name to extract service info
		serviceID, planID, instanceID := r.extractServiceInfoFromDeployment(boshDeployment.Name)

		if instanceID == "" {
			r.logDebug("Could not extract instance ID from deployment %s, skipping", boshDeployment.Name)
			continue
		}

		// Find matching CF service instance by GUID
		cfInstance := r.findCFInstanceByGUID(instanceID, cfInstances)

		// Build instance data using BOSH as primary source
		instance := r.buildInstanceFromBOSH(boshDeployment, serviceID, planID, instanceID)

		// Enrich with CF data if available
		if cfInstance != nil {
			r.logDebug("Found matching CF service instance %s for BOSH deployment %s", instanceID, boshDeployment.Name)
			r.enrichInstanceWithCF(instance, *cfInstance)
		} else {
			r.logDebug("No corresponding CF service instance found for BOSH deployment %s (instance-id: %s)", boshDeployment.Name, instanceID)
			instance.Metadata["cf_instance_missing"] = true
		}

		instances = append(instances, *instance)
	}

	// Handle CF instances that don't have corresponding BOSH deployments (orphaned CF instances)
	for _, cfInstance := range cfInstances {
		// Check if we already processed this CF instance via BOSH deployment
		found := false
		for _, instance := range instances {
			if instance.ID == cfInstance.GUID {
				found = true
				break
			}
		}

		if !found {
			r.logWarning("Found orphaned CF service instance %s with no corresponding BOSH deployment", cfInstance.GUID)
			// Create instance data from CF only (should be rare)
			instance := r.buildInstanceFromCFOnly(cfInstance)
			instance.Metadata["orphaned_cf_instance"] = true
			instance.Metadata["reconciliation_source"] = "cf_only"
			instances = append(instances, *instance)
		}
	}

	return instances, nil
}

// extractServiceInfoFromDeployment parses a BOSH deployment name to extract service info
// Deployment pattern: {plan-id}-{instance-id}
// Examples:
//   - rabbitmq-three-node-c425ddf5-af93-4830-a1dc-d34ce1942a40 (plan-id=rabbitmq-three-node, instance-id=c425ddf5-af93-4830-a1dc-d34ce1942a40)
//   - rabbitmq-single-node-65c0db3d-e63b-4b87-912c-07173486cc94 (plan-id=rabbitmq-single-node, instance-id=65c0db3d-e63b-4b87-912c-07173486cc94)
//   - redis-cache-small-1510f7aa-919b-4b71-b756-36d5932ad617 (plan-id=redis-cache-small, instance-id=1510f7aa-919b-4b71-b756-36d5932ad617)
//
// The plan-id is stored in vault as created by broker: {service-name}-{plan-name}
func (r *reconcilerManager) extractServiceInfoFromDeployment(deploymentName string) (serviceID, planID, instanceID string) {
	parts := strings.Split(deploymentName, "-")
	if len(parts) < 6 { // Need at least 6 parts: minimum 1 for plan-id + 5 for UUID (8-4-4-4-12)
		r.logDebug("Deployment name %s has insufficient parts (%d)", deploymentName, len(parts))
		return "", "", ""
	}

	// UUID format is 8-4-4-4-12 hex digits, so we need exactly 5 parts for the UUID
	// Work backwards from the end to find where the UUID starts
	if len(parts) >= 5 {
		// Check if the last 5 parts form a valid UUID
		potentialUUID := strings.Join(parts[len(parts)-5:], "-")
		if r.isValidUUID(potentialUUID) {
			instanceID = potentialUUID

			// Everything before the UUID is the plan-id
			planParts := parts[:len(parts)-5]
			if len(planParts) >= 1 {
				planID = strings.Join(planParts, "-")

				// Extract service-id from plan-id by finding it in broker catalog
				serviceID = r.extractServiceIDFromPlanID(planID)

				r.logDebug("Parsed deployment %s: plan-id=%s, instance-id=%s, extracted-service-id=%s", deploymentName, planID, instanceID, serviceID)
				return serviceID, planID, instanceID
			}
		}
	}

	r.logDebug("Could not parse deployment name %s into plan-id-instance-id format", deploymentName)
	return "", "", ""
}

// extractServiceIDFromPlanID finds the service ID by looking up the plan in broker catalog
func (r *reconcilerManager) extractServiceIDFromPlanID(planID string) string {
	for _, svc := range r.services {
		for _, plan := range svc.Plans {
			if plan.ID == planID {
				r.logDebug("Found service-id %s for plan-id %s", svc.ID, planID)
				return svc.ID
			}
		}
	}

	// Fallback: try to infer service ID from plan ID (plan ID often starts with service name)
	parts := strings.Split(planID, "-")
	if len(parts) > 0 {
		potentialServiceID := parts[0]
		r.logDebug("Could not find plan-id %s in catalog, inferring service-id as %s", planID, potentialServiceID)
		return potentialServiceID
	}

	r.logDebug("Could not extract service-id from plan-id %s", planID)
	return ""
}

// isValidUUID checks if a string matches the UUID format (8-4-4-4-12)
func (r *reconcilerManager) isValidUUID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}

	// Check each part has the correct length and contains only hex characters
	expectedLengths := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != expectedLengths[i] || !isHexString(part) {
			return false
		}
	}

	return true
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// findCFInstanceByGUID finds a CF service instance by GUID
func (r *reconcilerManager) findCFInstanceByGUID(guid string, cfInstances []CFServiceInstanceDetails) *CFServiceInstanceDetails {
	for _, cfInstance := range cfInstances {
		if cfInstance.GUID == guid {
			return &cfInstance
		}
	}
	return nil
}

// buildInstanceFromBOSH creates instance data primarily from BOSH deployment information
func (r *reconcilerManager) buildInstanceFromBOSH(boshDeployment DeploymentInfo, serviceID, planID, instanceID string) *InstanceData {
	// Get detailed BOSH information
	deploymentDetails, err := r.scanner.GetDeploymentDetails(context.Background(), boshDeployment.Name)
	var detail DeploymentDetail
	if err != nil {
		r.logDebug("Could not get detailed BOSH info for deployment %s: %s", boshDeployment.Name, err)
		detail = DeploymentDetail{DeploymentInfo: boshDeployment}
	} else {
		detail = *deploymentDetails
	}

	// Find service and plan names from broker catalog using the IDs from deployment
	var serviceName, planName, serviceType string
	for _, svc := range r.services {
		if svc.ID == serviceID {
			serviceName = svc.Name
			serviceType = svc.Name // Default to service name

			// Get service type from metadata if available
			if svc.Metadata != nil {
				if stype, ok := svc.Metadata["type"].(string); ok {
					serviceType = stype
				}
			}

			// Find plan name by plan ID
			for _, plan := range svc.Plans {
				if plan.ID == planID {
					planName = plan.Name
					break
				}
			}
			break
		}
	}

	// Fallback: if we couldn't find in broker catalog, use IDs as names
	if serviceName == "" {
		serviceName = serviceID
	}
	if planName == "" {
		planName = planID
	}
	if serviceType == "" {
		serviceType = serviceID
	}

	// Build  metadata with BOSH as primary source
	metadata := map[string]interface{}{
		// Service identification
		"service_name": serviceName,
		"service_type": serviceType,
		"plan_name":    planName,

		// BOSH-specific data (primary source)
		"bosh_deployment_name": boshDeployment.Name,
		"bosh_releases":        detail.Releases,
		"bosh_stemcells":       detail.Stemcells,
		"bosh_vms":             detail.VMs,
		"bosh_teams":           detail.Teams,
		"bosh_variables":       detail.Variables,
		"bosh_properties":      detail.Properties,
		"bosh_created_at":      boshDeployment.CreatedAt.Format(time.RFC3339),
		"bosh_updated_at":      boshDeployment.UpdatedAt.Format(time.RFC3339),

		// Reconciliation metadata
		"reconciliation_source": "bosh_primary",
		"reconciled_at":         time.Now().Format(time.RFC3339),
		"instance_id":           instanceID,
		"extracted_service_id":  serviceID,
		"extracted_plan_id":     planID,
	}

	// Add latest task ID if available from BOSH deployment properties
	if detail.Properties != nil {
		if taskID, ok := detail.Properties["latest_task_id"].(string); ok && taskID != "" {
			metadata["latest_task_id"] = taskID
		}
	}

	// For RabbitMQ services, try to get dashboard URL from credentials
	if strings.Contains(strings.ToLower(serviceType), "rabbitmq") || strings.Contains(strings.ToLower(serviceName), "rabbitmq") {
		r.logDebug("Detected RabbitMQ service, attempting to retrieve dashboard URL from credentials")

		// Try to get credentials from vault if vault is available
		if vaultInterface, ok := r.vault.(interface {
			Get(path string, out interface{}) (bool, error)
		}); ok {
			credsPath := fmt.Sprintf("%s/credentials", instanceID)
			var creds map[string]interface{}
			exists, err := vaultInterface.Get(credsPath, &creds)
			if err != nil {
				r.logDebug("Failed to retrieve credentials for RabbitMQ instance %s: %s", instanceID, err)
			} else if !exists {
				r.logDebug("No credentials found for RabbitMQ instance %s", instanceID)
			} else if apiURL, ok := creds["api_url"].(string); ok {
				// Extract dashboard URL from API URL
				// API URL format: https://hostname:15672/api
				// Dashboard URL format: https://hostname:15672/
				dashboardURL := strings.TrimSuffix(apiURL, "/api")
				if !strings.HasSuffix(dashboardURL, "/") {
					dashboardURL = dashboardURL + "/"
				}
				metadata["dashboard_url"] = dashboardURL
				metadata["rabbitmq_dashboard_url"] = dashboardURL
				r.logInfo("Added RabbitMQ dashboard URL for instance %s: %s", instanceID, dashboardURL)
			} else {
				r.logDebug("No api_url found in credentials for RabbitMQ instance %s", instanceID)
			}
		} else {
			r.logDebug("Vault interface not available for retrieving RabbitMQ credentials")
		}
	}

	return &InstanceData{
		ID:             instanceID, // Use instance ID from deployment name
		ServiceID:      serviceID,
		PlanID:         planID,
		DeploymentName: boshDeployment.Name,
		Manifest:       detail.Manifest,
		Metadata:       metadata,
		CreatedAt:      boshDeployment.CreatedAt,
		UpdatedAt:      boshDeployment.UpdatedAt,
		LastSyncedAt:   time.Now(),
	}
}

// enrichInstanceWithCF adds CF service instance data to an existing BOSH-based instance
func (r *reconcilerManager) enrichInstanceWithCF(instance *InstanceData, cfInstance CFServiceInstanceDetails) {
	if instance.Metadata == nil {
		instance.Metadata = make(map[string]interface{})
	}

	// Add CF instance name (the actual name given to the instance like "rmq-3n-a")
	instance.Metadata["instance_name"] = cfInstance.Name

	// Add CF-specific metadata as enrichment
	instance.Metadata["cf_instance_name"] = cfInstance.Name       // Also store with cf_ prefix for clarity
	instance.Metadata["cf_service_name"] = cfInstance.ServiceName // This is the service type (e.g., "rabbitmq")
	instance.Metadata["cf_plan_name"] = cfInstance.PlanName
	instance.Metadata["cf_service_guid"] = cfInstance.ServiceGUID
	instance.Metadata["cf_plan_guid"] = cfInstance.PlanGUID
	instance.Metadata["org_name"] = cfInstance.OrgName
	instance.Metadata["org_guid"] = cfInstance.OrgGUID
	instance.Metadata["space_name"] = cfInstance.SpaceName
	instance.Metadata["space_guid"] = cfInstance.SpaceGUID
	instance.Metadata["dashboard_url"] = cfInstance.DashboardURL
	instance.Metadata["maintenance_info"] = cfInstance.MaintenanceInfo
	instance.Metadata["cf_parameters"] = cfInstance.Parameters
	instance.Metadata["cf_tags"] = cfInstance.Tags
	instance.Metadata["cf_state"] = cfInstance.State
	instance.Metadata["cf_last_operation"] = cfInstance.LastOperation
	instance.Metadata["cf_created_at"] = cfInstance.CreatedAt.Format(time.RFC3339)
	instance.Metadata["cf_updated_at"] = cfInstance.UpdatedAt.Format(time.RFC3339)
	instance.Metadata["reconciliation_source"] = "bosh_and_cf"

	// Add CF binding information if available
	if len(cfInstance.Bindings) > 0 {
		var bindingIDs []string
		for _, binding := range cfInstance.Bindings {
			bindingIDs = append(bindingIDs, binding.GUID)
		}
		instance.Metadata["has_bindings"] = true
		instance.Metadata["bindings_count"] = len(cfInstance.Bindings)
		instance.Metadata["binding_ids"] = bindingIDs
		instance.Metadata["cf_bindings"] = cfInstance.Bindings
	} else {
		instance.Metadata["has_bindings"] = false
		instance.Metadata["bindings_count"] = 0
	}

	// Update timestamps if CF has more recent data
	if cfInstance.UpdatedAt.After(instance.UpdatedAt) {
		instance.UpdatedAt = cfInstance.UpdatedAt
	}

	r.logDebug("Enriched BOSH deployment %s (GUID: %s) with CF service instance data (name: %s)", instance.DeploymentName, instance.ID, cfInstance.Name)
}

// buildInstanceFromCFOnly creates instance data from CF service instance only (for orphaned instances)
func (r *reconcilerManager) buildInstanceFromCFOnly(cfInstance CFServiceInstanceDetails) *InstanceData {
	// Find service and plan information from broker catalog using CF names
	var serviceID, planID, serviceType string
	for _, svc := range r.services {
		if svc.Name == cfInstance.ServiceName {
			serviceID = svc.ID
			serviceType = svc.Name

			if svc.Metadata != nil {
				if stype, ok := svc.Metadata["type"].(string); ok {
					serviceType = stype
				}
			}

			for _, plan := range svc.Plans {
				if plan.Name == cfInstance.PlanName {
					planID = plan.ID
					break
				}
			}
			break
		}
	}

	// Fallback: use CF data directly
	if serviceID == "" {
		serviceID = cfInstance.ServiceGUID
	}
	if planID == "" {
		planID = cfInstance.PlanGUID
	}
	if serviceType == "" {
		serviceType = cfInstance.ServiceName
	}

	metadata := map[string]interface{}{
		// CF instance name (the actual name given to the instance like "rmq-3n-a")
		"instance_name":    cfInstance.Name,
		"cf_instance_name": cfInstance.Name,

		// CF data as primary source
		"service_name":      cfInstance.ServiceName, // This is the service type (e.g., "rabbitmq")
		"service_type":      serviceType,
		"plan_name":         cfInstance.PlanName,
		"org_name":          cfInstance.OrgName,
		"org_guid":          cfInstance.OrgGUID,
		"space_name":        cfInstance.SpaceName,
		"space_guid":        cfInstance.SpaceGUID,
		"dashboard_url":     cfInstance.DashboardURL,
		"maintenance_info":  cfInstance.MaintenanceInfo,
		"cf_parameters":     cfInstance.Parameters,
		"cf_tags":           cfInstance.Tags,
		"cf_state":          cfInstance.State,
		"cf_last_operation": cfInstance.LastOperation,

		// Reconciliation metadata
		"reconciliation_source":   "cf_only",
		"reconciled_at":           time.Now().Format(time.RFC3339),
		"instance_id":             cfInstance.GUID,
		"bosh_deployment_missing": true,

		// Binding information
		"has_bindings":   len(cfInstance.Bindings) > 0,
		"bindings_count": len(cfInstance.Bindings),
	}

	// Add CF binding information if available
	if len(cfInstance.Bindings) > 0 {
		var bindingIDs []string
		for _, binding := range cfInstance.Bindings {
			bindingIDs = append(bindingIDs, binding.GUID)
		}
		metadata["binding_ids"] = bindingIDs
		metadata["cf_bindings"] = cfInstance.Bindings
	}

	return &InstanceData{
		ID:             cfInstance.GUID, // Use CF service instance GUID as the instance ID
		ServiceID:      serviceID,
		PlanID:         planID,
		DeploymentName: "", // No BOSH deployment
		Metadata:       metadata,
		CreatedAt:      cfInstance.CreatedAt,
		UpdatedAt:      cfInstance.UpdatedAt,
		LastSyncedAt:   time.Now(),
	}
}

// updateVault updates vault with  instance data (CF + BOSH)
func (r *reconcilerManager) updateVault(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	var updatedInstances []InstanceData

	for _, instance := range instances {
		// Check if instance exists in vault
		existing, err := r.updater.GetInstance(ctx, instance.ID)
		if err != nil && !IsNotFoundError(err) {
			r.logError("Failed to get instance %s: %s", instance.ID, err)
			continue
		}

		// Merge with existing data to preserve important fields
		if existing != nil {
			r.logDebug("Updating existing instance %s with  data", instance.ID)
			mergedInstance := r.mergeInstanceData(existing, &instance)
			instance = *mergedInstance
		} else {
			r.logInfo("Adding new instance %s to vault with  data", instance.ID)
		}

		// Use enhanced update with broker integration if available
		if broker, ok := r.broker.(BrokerInterface); ok {
			// Try to use the enhanced update method that includes binding repair
			if updaterWithRepair, ok := r.updater.(interface {
				UpdateInstanceWithBindingRepair(ctx context.Context, instance *InstanceData, broker BrokerInterface) error
			}); ok {
				err = updaterWithRepair.UpdateInstanceWithBindingRepair(ctx, &instance, broker)
			} else {
				err = r.updater.UpdateInstance(ctx, &instance)
			}
		} else {
			err = r.updater.UpdateInstance(ctx, &instance)
		}

		if err != nil {
			r.logError("Failed to update instance %s: %s", instance.ID, err)
			continue
		}

		updatedInstances = append(updatedInstances, instance)
	}

	return updatedInstances, nil
}

// handleOrphanedInstances identifies and handles instances that exist in only one system
func (r *reconcilerManager) handleOrphanedInstances(ctx context.Context, cfInstances []CFServiceInstanceDetails, boshDeployments []DeploymentInfo) error {
	r.logDebug("Checking for orphaned instances between CF and BOSH")

	// Create maps for quick lookup
	cfInstanceMap := make(map[string]CFServiceInstanceDetails)
	for _, cfInstance := range cfInstances {
		cfInstanceMap[cfInstance.GUID] = cfInstance
	}

	boshInstanceMap := make(map[string]DeploymentInfo)
	for _, deployment := range boshDeployments {
		// Extract instance ID from deployment name
		_, _, instanceID := r.extractServiceInfoFromDeployment(deployment.Name)
		if instanceID != "" {
			boshInstanceMap[instanceID] = deployment
		}
	}

	// Find BOSH deployments without CF instances
	var orphanedBOSH []string
	for instanceID, deployment := range boshInstanceMap {
		if _, found := cfInstanceMap[instanceID]; !found {
			orphanedBOSH = append(orphanedBOSH, deployment.Name)
			r.logDebug("BOSH deployment %s (instance %s) has no corresponding CF instance", deployment.Name, instanceID)
		}
	}

	// Find CF instances without BOSH deployments
	var orphanedCF []string
	for _, cfInstance := range cfInstances {
		if _, found := boshInstanceMap[cfInstance.GUID]; !found {
			orphanedCF = append(orphanedCF, cfInstance.GUID)
			// Try to use the instance GUID directly as deployment name fallback
			r.logDebug("Using instance GUID as deployment name: %s", cfInstance.GUID)
		}
	}

	// Log findings
	if len(orphanedBOSH) > 0 {
		r.logWarning("Found %d orphaned BOSH deployments (no corresponding CF instances): %v", len(orphanedBOSH), orphanedBOSH)
	}
	if len(orphanedCF) > 0 {
		r.logWarning("Found %d orphaned CF service instances (no corresponding BOSH deployments): %v", len(orphanedCF), orphanedCF)
	}

	// Could implement cleanup logic here in the future
	return nil
}
