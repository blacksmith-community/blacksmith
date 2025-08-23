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
		Enabled:   config.BackupEnabled,
		Retention: config.BackupRetention,
		Cleanup:   config.BackupCleanup,
		Path:      config.BackupPath,
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
		updater:      NewVaultUpdater(vault, logger, backupConfig),
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
	r.logInfo("Starting reconciliation run")

	// Create a context with timeout for this run
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Minute)
	defer cancel()

	// Track metrics
	r.metrics.ReconciliationStarted()
	defer func() {
		r.metrics.ReconciliationCompleted(time.Since(startTime))
	}()

	// Get service catalog from broker
	if broker, ok := r.broker.(BrokerInterface); ok {
		r.services = broker.GetServices()
		r.logDebug("Loaded %d services from broker", len(r.services))
	} else {
		r.logWarning("Broker does not implement GetServices, proceeding without service catalog")
		r.services = []Service{}
	}

	// Phase 1: Scan BOSH deployments
	r.logDebug("Phase 1: Scanning BOSH deployments")
	deployments, err := r.scanDeployments(ctx)
	if err != nil {
		r.logError("Failed to scan deployments: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}
	r.logInfo("Found %d deployments in BOSH", len(deployments))
	r.metrics.DeploymentsScanned(len(deployments))

	// Phase 2: Match deployments to services
	r.logDebug("Phase 2: Matching deployments to services")
	matches, err := r.matchDeployments(ctx, deployments)
	if err != nil {
		r.logError("Failed to match deployments: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}
	r.logInfo("Matched %d deployments to service instances", len(matches))
	r.metrics.InstancesMatched(len(matches))

	// Phase 3: Update Vault
	r.logDebug("Phase 3: Updating Vault with deployment data")
	instances, err := r.updateVault(ctx, matches)
	if err != nil {
		r.logError("Failed to update vault: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}
	r.logInfo("Updated %d instances in Vault", len(instances))
	r.metrics.InstancesUpdated(len(instances))

	// Phase 4: Synchronize index
	r.logDebug("Phase 4: Synchronizing service index")
	err = r.synchronizeIndex(ctx, instances)
	if err != nil {
		r.logError("Failed to synchronize index: %s", err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}

	// Update status
	r.setStatus(Status{
		Running:         true,
		LastRunTime:     startTime,
		LastRunDuration: time.Since(startTime),
		InstancesFound:  len(deployments),
		InstancesSynced: len(instances),
		Errors:          nil,
	})

	r.logInfo("Reconciliation completed successfully in %v", time.Since(startTime))
}

// scanDeployments scans BOSH for deployments
func (r *reconcilerManager) scanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	deployments, err := r.scanner.ScanDeployments(ctx)
	if err != nil {
		return nil, fmt.Errorf("scanner failed: %w", err)
	}

	// Filter deployments based on naming convention
	var serviceDeployments []DeploymentInfo
	for _, dep := range deployments {
		if r.isServiceDeployment(dep.Name) {
			r.logDebug("Found service deployment: %s", dep.Name)
			serviceDeployments = append(serviceDeployments, dep)
		}
	}

	return serviceDeployments, nil
}

// isServiceDeployment checks if a deployment is a service deployment
func (r *reconcilerManager) isServiceDeployment(name string) bool {
	// Check if deployment name matches service pattern: planID-instanceID
	// This will need to be updated to use actual broker services
	return strings.Contains(name, "-") && len(strings.Split(name, "-")) >= 2
}

// matchDeployments matches deployments to services
func (r *reconcilerManager) matchDeployments(ctx context.Context, deployments []DeploymentInfo) ([]MatchedDeployment, error) {
	var matches []MatchedDeployment

	// Process in batches for efficiency
	batchSize := r.config.BatchSize
	if batchSize == 0 {
		batchSize = 10
	}

	for i := 0; i < len(deployments); i += batchSize {
		end := i + batchSize
		if end > len(deployments) {
			end = len(deployments)
		}

		batch := deployments[i:end]
		batchMatches, err := r.processBatch(ctx, batch)
		if err != nil {
			r.logError("Failed to process batch %d-%d: %s", i, end, err)
			// Continue with other batches
			continue
		}
		matches = append(matches, batchMatches...)
	}

	return matches, nil
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

// updateVault updates vault with deployment information
func (r *reconcilerManager) updateVault(ctx context.Context, matches []MatchedDeployment) ([]InstanceData, error) {
	var instances []InstanceData

	for _, match := range matches {
		instance := r.buildInstanceData(match)

		// Check if instance exists in vault
		existing, err := r.updater.GetInstance(ctx, instance.ID)
		if err != nil && !IsNotFoundError(err) {
			r.logError("Failed to get instance %s: %s", instance.ID, err)
			continue
		}

		// Update or create instance
		if existing != nil {
			r.logDebug("Updating existing instance %s", instance.ID)
			instance = r.mergeInstanceData(existing, instance)
		} else {
			r.logInfo("Adding new instance %s to vault", instance.ID)
		}

		// Use binding repair-aware update if broker is available
		if broker, ok := r.broker.(BrokerInterface); ok {
			// Try to use the enhanced update method that includes binding repair
			if updaterWithRepair, ok := r.updater.(interface {
				UpdateInstanceWithBindingRepair(ctx context.Context, instance *InstanceData, broker BrokerInterface) error
			}); ok {
				err = updaterWithRepair.UpdateInstanceWithBindingRepair(ctx, instance, broker)
			} else {
				// Fallback to regular update
				r.logDebug("Enhanced updater not available, using standard update for instance %s", instance.ID)
				err = r.updater.UpdateInstance(ctx, instance)
			}
		} else {
			// Broker not available or doesn't implement interface, use standard update
			r.logDebug("Broker not available for binding repair, using standard update for instance %s", instance.ID)
			err = r.updater.UpdateInstance(ctx, instance)
		}

		if err != nil {
			r.logError("Failed to update instance %s: %s", instance.ID, err)
			continue
		}

		instances = append(instances, *instance)
	}

	return instances, nil
}

// buildInstanceData builds instance data from a matched deployment
func (r *reconcilerManager) buildInstanceData(match MatchedDeployment) *InstanceData {
	// Find the service and plan names from the catalog
	var serviceName, planName, serviceType string
	for _, svc := range r.services {
		if svc.ID == match.Match.ServiceID {
			serviceName = svc.Name
			// Try to get service type from metadata if available
			if svc.Metadata != nil {
				if stype, ok := svc.Metadata["type"].(string); ok {
					serviceType = stype
				}
			}
			// Fallback: infer service type from service name for common services
			if serviceType == "" {
				switch serviceName {
				case "redis":
					serviceType = "redis"
				case "rabbitmq":
					serviceType = "rabbitmq"
				default:
					serviceType = serviceName // Use service name as fallback
				}
			}
			for _, plan := range svc.Plans {
				if plan.ID == match.Match.PlanID {
					planName = plan.Name
					break
				}
			}
			break
		}
	}

	metadata := map[string]interface{}{
		"service_name":     serviceName,
		"service_type":     serviceType,
		"plan_name":        planName,
		"releases":         match.Deployment.Releases,
		"stemcells":        match.Deployment.Stemcells,
		"vms":              match.Deployment.VMs,
		"teams":            match.Deployment.Teams,
		"variables":        match.Deployment.Variables,
		"properties":       match.Deployment.Properties,
		"match_confidence": match.Match.Confidence,
		"match_reason":     match.Match.MatchReason,
		"reconciled_at":    time.Now().Format(time.RFC3339),
	}

	// Add latest task ID if available from deployment properties
	if match.Deployment.Properties != nil {
		if taskID, ok := match.Deployment.Properties["latest_task_id"].(string); ok && taskID != "" {
			metadata["latest_task_id"] = taskID
		}
	}

	// Enrich with CF metadata if CF manager is available and configured
	if r.cfManager != nil {
		// Type assert to get the actual CF manager interface
		if cfMgr, ok := r.cfManager.(interface {
			EnrichServiceInstanceWithCF(instanceID, serviceName string) interface{}
		}); ok {
			cfMetadata := cfMgr.EnrichServiceInstanceWithCF(match.Match.InstanceID, serviceName)
			if cfMetadata != nil {
				metadata["cf"] = cfMetadata
				r.logInfo("successfully enriched service instance %s with CF metadata", match.Match.InstanceID)
			} else {
				r.logDebug("no CF metadata found for service instance %s", match.Match.InstanceID)
			}
		} else {
			r.logDebug("CF manager does not support service enrichment")
		}
	} else {
		r.logDebug("CF manager not available for service instance enrichment")
	}

	return &InstanceData{
		ID:             match.Match.InstanceID,
		ServiceID:      match.Match.ServiceID,
		PlanID:         match.Match.PlanID,
		DeploymentName: match.Deployment.Name,
		Manifest:       match.Deployment.Manifest,
		Metadata:       metadata,
		CreatedAt:      match.Deployment.CreatedAt,
		UpdatedAt:      match.Deployment.UpdatedAt,
		LastSyncedAt:   time.Now(),
	}
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
