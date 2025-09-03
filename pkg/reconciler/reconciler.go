package reconciler

import (
	"context"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/bosh"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

// ReconcilerManager is a production-ready reconciler with full load management
type ReconcilerManager struct {
	config ReconcilerConfig

	// Core components
	scanner      Scanner
	matcher      Matcher
	updater      Updater
	synchronizer Synchronizer
	broker       interface{}
	vault        interface{}
	bosh         bosh.Director
	logger       Logger
	cfManager    interface{}

	// Status tracking
	status   Status
	statusMu sync.RWMutex

	// Multiple run prevention
	isReconciling  atomic.Bool
	runCounter     atomic.Uint64
	lastRunID      uint64
	activeRunMutex sync.Mutex

	// Rate limiters for each API
	boshLimiter  *rate.Limiter
	cfLimiter    *rate.Limiter
	vaultLimiter *rate.Limiter

	// Circuit breakers for each API
	boshBreaker  *gobreaker.CircuitBreaker
	cfBreaker    *gobreaker.CircuitBreaker
	vaultBreaker *gobreaker.CircuitBreaker

	// Worker pool for batch processing
	workerPool  *WorkerPool
	workQueue   chan WorkItem
	resultQueue chan WorkResult

	// Context and lifecycle management
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	shutdownOnce sync.Once

	// Metrics and monitoring
	metrics     MetricsCollector
	performance *PerformanceTracker
}

// WorkItem represents a unit of work
type WorkItem struct {
	ID         string
	Type       WorkType
	Data       interface{}
	RetryCount int
	Priority   int
}

// WorkType defines the type of work
type WorkType int

const (
	WorkTypeScanDeployment WorkType = iota
	WorkTypeDiscoverCF
	WorkTypeUpdateVault
	WorkTypeSyncIndex
)

// WorkResult represents the result of processing a work item
type WorkResult struct {
	Item     WorkItem
	Success  bool
	Error    error
	Duration time.Duration
	Data     interface{}
}

// PerformanceTracker tracks reconciler performance metrics
type PerformanceTracker struct {
	mu                   sync.RWMutex
	lastBatchDuration    time.Duration
	averageBatchDuration time.Duration
	successRate          float64
	apiLatencies         map[string]time.Duration
	errorCounts          map[string]int
	samples              int
}

// NewReconcilerManager creates a new reconciler with all safety features
func NewReconcilerManager(
	config ReconcilerConfig,
	broker interface{},
	vault interface{},
	boshDir bosh.Director,
	logger Logger,
	cfManager interface{},
) *ReconcilerManager {

	// Load defaults and validate
	config.LoadDefaults()
	if err := config.Validate(); err != nil {
		logger.Error("Invalid configuration: %v", err)
		// Use defaults anyway but log the error
	}

	ctx, cancel := context.WithCancel(context.Background())

	r := &ReconcilerManager{
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
		synchronizer: NewIndexSynchronizer(vault, logger),
		metrics:      NewMetricsCollector(),
		performance:  newPerformanceTracker(),
		workQueue:    make(chan WorkItem, config.Concurrency.QueueSize),
		resultQueue:  make(chan WorkResult, config.Concurrency.QueueSize),
	}

	// Initialize rate limiters
	r.initializeRateLimiters()

	// Initialize circuit breakers
	r.initializeCircuitBreakers()

	// Initialize worker pool
	r.workerPool = NewWorkerPool(config.Concurrency.WorkerPoolSize, r.processWorkItem)

	// Create updater with enhanced configuration
	r.updater = r.createEnhancedUpdater()

	return r
}

// initializeRateLimiters sets up rate limiting for each API
func (r *ReconcilerManager) initializeRateLimiters() {
	r.boshLimiter = rate.NewLimiter(
		rate.Limit(r.config.APIs.BOSH.RateLimit.RequestsPerSecond),
		r.config.APIs.BOSH.RateLimit.Burst,
	)

	r.cfLimiter = rate.NewLimiter(
		rate.Limit(r.config.APIs.CF.RateLimit.RequestsPerSecond),
		r.config.APIs.CF.RateLimit.Burst,
	)

	r.vaultLimiter = rate.NewLimiter(
		rate.Limit(r.config.APIs.Vault.RateLimit.RequestsPerSecond),
		r.config.APIs.Vault.RateLimit.Burst,
	)
}

// initializeCircuitBreakers sets up circuit breakers for each API
func (r *ReconcilerManager) initializeCircuitBreakers() {
	// Safe conversion with bounds checking
	maxConcurrentBOSH := r.config.APIs.BOSH.CircuitBreaker.MaxConcurrent
	if maxConcurrentBOSH < 0 {
		maxConcurrentBOSH = 0
	}
	if maxConcurrentBOSH > int(^uint32(0)) {
		maxConcurrentBOSH = int(^uint32(0))
	}

	failureThresholdBOSH := r.config.APIs.BOSH.CircuitBreaker.FailureThreshold
	if failureThresholdBOSH < 0 {
		failureThresholdBOSH = 0
	}
	if failureThresholdBOSH > int(^uint32(0)) {
		failureThresholdBOSH = int(^uint32(0))
	}

	boshSettings := gobreaker.Settings{
		Name:        "BOSH-API",
		MaxRequests: uint32(maxConcurrentBOSH), // #nosec G115
		Interval:    r.config.APIs.BOSH.CircuitBreaker.Timeout,
		Timeout:     r.config.APIs.BOSH.CircuitBreaker.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= uint32(failureThresholdBOSH) && // #nosec G115
				failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			r.logger.Info("Circuit breaker %s state changed from %v to %v", name, from, to)
		},
	}
	r.boshBreaker = gobreaker.NewCircuitBreaker(boshSettings)

	// Similar setup for CF and Vault breakers
	// Safe conversion for CF
	maxConcurrentCF := r.config.APIs.CF.CircuitBreaker.MaxConcurrent
	if maxConcurrentCF < 0 {
		maxConcurrentCF = 0
	}
	if maxConcurrentCF > int(^uint32(0)) {
		maxConcurrentCF = int(^uint32(0))
	}

	cfSettings := boshSettings
	cfSettings.Name = "CF-API"
	cfSettings.MaxRequests = uint32(maxConcurrentCF) // #nosec G115
	cfSettings.Interval = r.config.APIs.CF.CircuitBreaker.Timeout
	cfSettings.Timeout = r.config.APIs.CF.CircuitBreaker.Timeout
	r.cfBreaker = gobreaker.NewCircuitBreaker(cfSettings)

	// Safe conversion for Vault
	maxConcurrentVault := r.config.APIs.Vault.CircuitBreaker.MaxConcurrent
	if maxConcurrentVault < 0 {
		maxConcurrentVault = 0
	}
	if maxConcurrentVault > int(^uint32(0)) {
		maxConcurrentVault = int(^uint32(0))
	}

	vaultSettings := boshSettings
	vaultSettings.Name = "Vault-API"
	vaultSettings.MaxRequests = uint32(maxConcurrentVault) // #nosec G115
	vaultSettings.Interval = r.config.APIs.Vault.CircuitBreaker.Timeout
	vaultSettings.Timeout = r.config.APIs.Vault.CircuitBreaker.Timeout
	r.vaultBreaker = gobreaker.NewCircuitBreaker(vaultSettings)
}

// Start starts the reconciler
func (r *ReconcilerManager) Start(ctx context.Context) error {
	if !r.config.Enabled {
		r.logger.Info("Enhanced reconciler is disabled")
		return nil
	}

	// Log startup configuration for debugging
	r.logger.Info("Starting reconciler with configuration:")
	r.logger.Info("  Interval: %v", r.config.Interval)
	r.logger.Info("  Max Concurrent: %d", r.config.Concurrency.MaxConcurrent)
	r.logger.Info("  Worker Pool Size: %d", r.config.Concurrency.WorkerPoolSize)
	r.logger.Info("  Queue Size: %d", r.config.Concurrency.QueueSize)
	r.logger.Info("  Batch Size: %d", r.config.Batch.Size)
	r.logger.Info("  BOSH Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.BOSH.RateLimit.RequestsPerSecond, r.config.APIs.BOSH.RateLimit.Burst)
	r.logger.Info("  CF Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.CF.RateLimit.RequestsPerSecond, r.config.APIs.CF.RateLimit.Burst)
	r.logger.Info("  Vault Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.Vault.RateLimit.RequestsPerSecond, r.config.APIs.Vault.RateLimit.Burst)
	r.logger.Info("  Backup Enabled: %v", r.config.Backup.Enabled)
	r.logger.Info("  Debug Mode: %v", r.config.Debug)

	// Validate critical components are initialized
	if err := r.validateComponents(); err != nil {
		r.logger.Error("Component validation warning: %v", err)
		r.logger.Info("Reconciler will run in degraded mode with available components")
		// Continue with degraded functionality rather than failing
	}

	// Start worker pool
	r.workerPool.Start(ctx)

	// Start result processor
	r.wg.Add(1)
	go r.processResults()

	// Start reconciliation loop
	r.wg.Add(1)
	go r.reconciliationLoop()

	// Start metrics collector if enabled
	if r.config.Metrics.Enabled {
		r.wg.Add(1)
		go r.collectMetrics()
	}

	// Run initial reconciliation
	go r.runReconciliation()

	r.setStatus(Status{Running: true, LastRunTime: time.Now()})
	return nil
}

// validateComponents checks if critical components are initialized
func (r *ReconcilerManager) validateComponents() error {
	var errors []string

	// Check Vault
	if r.vault == nil {
		errors = append(errors, "Vault not initialized")
		r.logger.Error("Vault is not initialized - credential operations will fail")
	}

	// Check BOSH
	if r.bosh == nil {
		errors = append(errors, "BOSH director not initialized")
		r.logger.Error("BOSH director is not initialized - deployment scanning will be disabled")
	}

	// Check scanner
	if r.scanner == nil {
		errors = append(errors, "BOSH scanner not initialized")
		r.logger.Error("BOSH scanner is not initialized - deployment scanning will be disabled")
	}

	// Check updater
	if r.updater == nil {
		errors = append(errors, "Updater not initialized")
		r.logger.Error("Updater is not initialized - instance updates will be disabled")
	}

	// Log CF manager status (not critical)
	if r.cfManager == nil {
		r.logger.Info("CF manager not configured - CF discovery will be skipped")
	} else {
		r.logger.Info("CF manager configured - CF discovery enabled")
	}

	// Check circuit breakers (not critical, can run without them)
	if r.boshBreaker == nil {
		r.logger.Info("BOSH circuit breaker not initialized - running without circuit protection")
	}
	if r.vaultBreaker == nil {
		r.logger.Info("Vault circuit breaker not initialized - running without circuit protection")
	}
	if r.cfBreaker == nil && r.cfManager != nil {
		r.logger.Info("CF circuit breaker not initialized - running without circuit protection")
	}

	if len(errors) > 0 {
		return fmt.Errorf("missing critical components: %s", strings.Join(errors, ", "))
	}

	return nil
}

// reconciliationLoop runs periodic reconciliation with multiple run prevention
func (r *ReconcilerManager) reconciliationLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.runReconciliation()
		case <-r.ctx.Done():
			r.logger.Info("Reconciliation loop stopping")
			return
		}
	}
}

// runReconciliation performs a single reconciliation run with safety checks
func (r *ReconcilerManager) runReconciliation() {
	// Panic recovery to prevent crashes in production
	defer func() {
		if err := recover(); err != nil {
			r.logger.Error("PANIC in reconciliation: %v", err)
			r.logger.Error("Stack trace: %s", debug.Stack())

			// Update metrics and status
			r.metrics.ReconciliationError(fmt.Errorf("panic: %v", err))
			r.updateStatusError(fmt.Errorf("reconciliation panic: %v", err))

			// Ensure we release the reconciliation lock
			r.isReconciling.Store(false)
		}
	}()

	// Prevent multiple concurrent runs
	if !r.isReconciling.CompareAndSwap(false, true) {
		r.logger.Info("Reconciliation already in progress, skipping new run")
		r.metrics.ReconciliationSkipped()
		return
	}
	defer r.isReconciling.Store(false)

	// Generate unique run ID
	runID := r.runCounter.Add(1)
	r.activeRunMutex.Lock()
	r.lastRunID = runID
	r.activeRunMutex.Unlock()

	startTime := time.Now()
	r.logger.Info("Starting reconciliation run #%d", runID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.ctx, r.config.Timeouts.ReconciliationRun)
	defer cancel()

	// Track metrics
	r.metrics.ReconciliationStarted()
	defer func() {
		duration := time.Since(startTime)
		r.metrics.ReconciliationCompleted(duration)
		r.updatePerformanceMetrics(duration)
		r.logger.Info("Reconciliation run #%d completed in %v", runID, duration)
	}()

	// Execute reconciliation phases with enhanced error handling
	if err := r.executeReconciliationPhases(ctx, runID); err != nil {
		r.logger.Error("Reconciliation run #%d failed: %v", runID, err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)
		return
	}

	// Update status
	r.setStatus(Status{
		Running:         true,
		LastRunTime:     startTime,
		LastRunDuration: time.Since(startTime),
	})
}

// executeReconciliationPhases runs all reconciliation phases with proper rate limiting
func (r *ReconcilerManager) executeReconciliationPhases(ctx context.Context, runID uint64) error {
	// Phase 1: Discover CF instances with rate limiting
	cfInstances, err := r.discoverCFInstancesWithRateLimit(ctx)
	if err != nil {
		r.logger.Error("Phase 1 failed: %v", err)
		// Continue anyway with BOSH-only data
	}

	// Phase 2: Scan BOSH deployments with rate limiting and batching
	deployments, err := r.scanDeploymentsWithRateLimit(ctx)
	if err != nil {
		r.logger.Error("Phase 2 failed: %v", err)
		// Continue with available data
	}

	// Phase 3: Process in batches with adaptive sizing
	instances, err := r.processBatchesAdaptive(ctx, cfInstances, deployments)
	if err != nil {
		return fmt.Errorf("phase 3 failed: %w", err)
	}

	// Phase 4: Update Vault with rate limiting
	updatedInstances, err := r.updateVaultWithRateLimit(ctx, instances)
	if err != nil {
		return fmt.Errorf("phase 4 failed: %w", err)
	}

	// Phase 5: Synchronize index
	if err := r.synchronizeIndex(ctx, updatedInstances); err != nil {
		return fmt.Errorf("phase 5 failed: %w", err)
	}

	r.logger.Info("Run #%d processed %d instances successfully", runID, len(updatedInstances))
	return nil
}

// discoverCFInstancesWithRateLimit discovers CF instances with rate limiting
func (r *ReconcilerManager) discoverCFInstancesWithRateLimit(ctx context.Context) ([]CFServiceInstanceDetails, error) {
	// Skip if CF is not configured
	if r.cfManager == nil {
		r.logger.Debug("CF manager not configured, skipping CF discovery")
		return []CFServiceInstanceDetails{}, nil
	}

	// Check if rate limiter is available
	if r.cfLimiter != nil {
		if err := r.cfLimiter.Wait(ctx); err != nil {
			r.logger.Error("CF rate limit wait failed: %v", err)
			return []CFServiceInstanceDetails{}, nil // Continue without CF discovery
		}
	}

	// Execute with or without circuit breaker
	var result interface{}
	var err error

	if r.cfBreaker != nil {
		result, err = r.cfBreaker.Execute(func() (interface{}, error) {
			return r.discoverCFServiceInstances(ctx, nil)
		})
	} else {
		// Execute directly without circuit breaker
		result, err = r.discoverCFServiceInstances(ctx, nil)
	}

	if err != nil {
		r.logger.Error("CF discovery failed: %v", err)
		return []CFServiceInstanceDetails{}, nil // Continue without CF instances
	}

	if instances, ok := result.([]CFServiceInstanceDetails); ok {
		return instances, nil
	}

	r.logger.Error("Unexpected CF discovery result type")
	return []CFServiceInstanceDetails{}, nil
}

// scanDeploymentsWithRateLimit scans BOSH deployments with rate limiting
func (r *ReconcilerManager) scanDeploymentsWithRateLimit(ctx context.Context) ([]DeploymentInfo, error) {
	// Skip if BOSH scanner is not configured
	if r.scanner == nil {
		r.logger.Error("BOSH scanner not configured, cannot scan deployments")
		return []DeploymentInfo{}, nil
	}

	// Check if rate limiter is available
	if r.boshLimiter != nil {
		if err := r.boshLimiter.Wait(ctx); err != nil {
			r.logger.Error("BOSH rate limit wait failed: %v", err)
			return []DeploymentInfo{}, nil // Continue without BOSH scan
		}
	}

	// Execute with or without circuit breaker
	var result interface{}
	var err error

	if r.boshBreaker != nil {
		result, err = r.boshBreaker.Execute(func() (interface{}, error) {
			return r.scanner.ScanDeployments(ctx)
		})
	} else {
		// Execute directly without circuit breaker
		result, err = r.scanner.ScanDeployments(ctx)
	}

	if err != nil {
		r.logger.Error("BOSH scan failed: %v", err)
		return []DeploymentInfo{}, nil // Continue without deployments
	}

	if deployments, ok := result.([]DeploymentInfo); ok {
		return r.filterServiceDeployments(deployments), nil
	}

	r.logger.Error("Unexpected BOSH scan result type")
	return []DeploymentInfo{}, nil
}

// processBatchesAdaptive processes deployments in adaptive batches
func (r *ReconcilerManager) processBatchesAdaptive(
	ctx context.Context,
	cfInstances []CFServiceInstanceDetails,
	deployments []DeploymentInfo,
) ([]InstanceData, error) {

	// Get effective batch size based on performance
	performanceScore := r.performance.GetScore()
	batchSize := r.config.GetEffectiveBatchSize(performanceScore)

	r.logger.Debug("Using batch size %d based on performance score %.2f", batchSize, performanceScore)

	var allInstances []InstanceData
	var mu sync.Mutex

	// Process deployments in batches
	for i := 0; i < len(deployments); i += batchSize {
		end := min(i+batchSize, len(deployments))
		batch := deployments[i:end]

		r.logger.Debug("Processing batch %d-%d of %d deployments", i, end, len(deployments))

		// Process batch with controlled concurrency
		instances, err := r.processBatchWithConcurrency(ctx, batch, cfInstances)
		if err != nil {
			r.logger.Error("Batch processing failed: %v", err)
			// Continue with other batches
			continue
		}

		mu.Lock()
		allInstances = append(allInstances, instances...)
		mu.Unlock()

		// Cooldown period between batches to prevent API overload
		if i+batchSize < len(deployments) {
			select {
			case <-time.After(r.config.Concurrency.CooldownPeriod):
			case <-ctx.Done():
				return allInstances, ctx.Err()
			}
		}
	}

	return allInstances, nil
}

// processBatchWithConcurrency processes a batch with controlled concurrency
func (r *ReconcilerManager) processBatchWithConcurrency(
	ctx context.Context,
	batch []DeploymentInfo,
	cfInstances []CFServiceInstanceDetails,
) ([]InstanceData, error) {
	// Ensure configuration is valid to prevent channel panics
	maxConcurrent := r.config.Concurrency.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1 // Safe fallback
	}

	semaphore := make(chan struct{}, maxConcurrent)
	resultChan := make(chan InstanceData, len(batch))
	errorChan := make(chan error, len(batch))

	var wg sync.WaitGroup

	for _, deployment := range batch {
		wg.Add(1)
		go func(dep DeploymentInfo) {
			defer wg.Done()

			// Panic recovery for goroutine
			defer func() {
				if err := recover(); err != nil {
					r.logger.Error("PANIC in deployment processing goroutine: %v", err)
					errorChan <- fmt.Errorf("panic processing deployment %s: %v", dep.Name, err)
				}
			}()

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				errorChan <- ctx.Err()
				return
			}

			// Rate limit BOSH API calls (with nil check)
			if r.boshLimiter != nil {
				if err := r.boshLimiter.Wait(ctx); err != nil {
					errorChan <- err
					return
				}
			}

			// Process deployment
			instance, err := r.processDeployment(ctx, dep, cfInstances)
			if err != nil {
				errorChan <- err
				return
			}

			resultChan <- instance
		}(deployment)
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(resultChan)
		close(errorChan)
	}()

	// Collect results
	var instances []InstanceData
	var errors []error

	for {
		select {
		case instance, ok := <-resultChan:
			if !ok {
				// Check for errors
				if len(errors) > 0 {
					r.logger.Warning("Batch processing completed with %d errors", len(errors))
				}
				return instances, nil
			}
			instances = append(instances, instance)
		case err := <-errorChan:
			if err != nil {
				errors = append(errors, err)
			}
		case <-ctx.Done():
			return instances, ctx.Err()
		}
	}
}

// updateVaultWithRateLimit updates Vault with rate limiting
func (r *ReconcilerManager) updateVaultWithRateLimit(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	// Check if updater is available
	if r.updater == nil {
		return nil, fmt.Errorf("vault updater not initialized")
	}

	var updated []InstanceData
	var mu sync.Mutex

	// Process updates with rate limiting
	for _, instance := range instances {
		// Wait for rate limit (with nil check)
		if r.vaultLimiter != nil {
			if err := r.vaultLimiter.Wait(ctx); err != nil {
				return updated, fmt.Errorf("vault rate limit wait failed: %w", err)
			}
		}

		// Execute with or without circuit breaker
		var result interface{}
		var err error

		if r.vaultBreaker != nil {
			result, err = r.vaultBreaker.Execute(func() (interface{}, error) {
				return r.updater.UpdateInstance(ctx, instance)
			})
		} else {
			// Execute directly without circuit breaker
			result, err = r.updater.UpdateInstance(ctx, instance)
		}

		if err != nil {
			r.logger.Error("Failed to update instance %s: %v", instance.ID, err)
			continue
		}

		if updatedInstance, ok := result.(*InstanceData); ok {
			mu.Lock()
			updated = append(updated, *updatedInstance)
			mu.Unlock()
		}
	}

	return updated, nil
}

// ForceReconcile forces an immediate reconciliation with safety checks
func (r *ReconcilerManager) ForceReconcile() error {
	r.logger.Info("Force reconciliation requested")

	// Check if already running
	if r.isReconciling.Load() {
		return fmt.Errorf("reconciliation already in progress")
	}

	// Start reconciliation in background
	go r.runReconciliation()
	return nil
}

// Stop gracefully stops the reconciler
func (r *ReconcilerManager) Stop() error {
	r.shutdownOnce.Do(func() {
		r.logger.Info("Stopping reconciler")

		// Cancel context to signal shutdown
		r.cancel()

		// Wait for graceful shutdown with timeout
		done := make(chan struct{})
		go func() {
			r.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			r.logger.Info("Enhanced reconciler stopped gracefully")
		case <-time.After(r.config.Timeouts.ShutdownGracePeriod):
			r.logger.Warning("Enhanced reconciler shutdown timeout exceeded")
		}

		// Stop worker pool
		r.workerPool.Stop()

		// Update status
		r.setStatus(Status{Running: false})
	})

	return nil
}

// Helper functions

func (r *ReconcilerManager) createEnhancedUpdater() Updater {
	// Create updater with backup configuration from enhanced config
	backupConfig := BackupConfig{
		Enabled:          r.config.Backup.Enabled,
		RetentionCount:   r.config.Backup.RetentionCount,
		RetentionDays:    r.config.Backup.RetentionDays,
		CompressionLevel: r.config.Backup.CompressionLevel,
		CleanupEnabled:   r.config.Backup.CleanupEnabled,
		BackupOnUpdate:   r.config.Backup.BackupOnUpdate,
		BackupOnDelete:   r.config.Backup.BackupOnDelete,
	}

	if cfMgr, ok := r.cfManager.(CFManagerInterface); ok && cfMgr != nil {
		return NewVaultUpdaterWithCF(r.vault, r.logger, backupConfig, cfMgr)
	}
	return NewVaultUpdater(r.vault, r.logger, backupConfig)
}

func (r *ReconcilerManager) processWorkItem(ctx context.Context, item WorkItem) WorkResult {
	startTime := time.Now()
	result := WorkResult{Item: item}

	switch item.Type {
	case WorkTypeScanDeployment:
		// Process deployment scan work
		result.Success = true
	case WorkTypeDiscoverCF:
		// Process CF discovery work
		result.Success = true
	case WorkTypeUpdateVault:
		// Process Vault update work
		result.Success = true
	case WorkTypeSyncIndex:
		// Process index sync work
		result.Success = true
	default:
		result.Error = fmt.Errorf("unknown work type: %v", item.Type)
	}

	result.Duration = time.Since(startTime)
	return result
}

func (r *ReconcilerManager) processResults() {
	defer r.wg.Done()

	for {
		select {
		case result := <-r.resultQueue:
			if result.Error != nil {
				r.logger.Error("Work item %s failed: %v", result.Item.ID, result.Error)
				r.handleWorkItemFailure(result)
			} else {
				r.logger.Debug("Work item %s completed in %v", result.Item.ID, result.Duration)
			}
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *ReconcilerManager) handleWorkItemFailure(result WorkResult) {
	// Implement retry logic with exponential backoff
	if result.Item.RetryCount < r.config.Retry.MaxAttempts {
		delay := r.calculateRetryDelay(result.Item.RetryCount)
		r.logger.Info("Retrying work item %s after %v (attempt %d/%d)",
			result.Item.ID, delay, result.Item.RetryCount+1, r.config.Retry.MaxAttempts)

		result.Item.RetryCount++

		// Schedule retry
		go func() {
			select {
			case <-time.After(delay):
				r.workQueue <- result.Item
			case <-r.ctx.Done():
			}
		}()
	} else {
		r.logger.Error("Work item %s failed after %d attempts", result.Item.ID, r.config.Retry.MaxAttempts)
		r.metrics.ReconciliationError(result.Error)
	}
}

func (r *ReconcilerManager) calculateRetryDelay(retryCount int) time.Duration {
	delay := r.config.Retry.InitialDelay * time.Duration(r.config.Retry.Multiplier*float64(retryCount))

	// Add jitter
	if r.config.Retry.Jitter > 0 {
		jitter := time.Duration(float64(delay) * r.config.Retry.Jitter * (0.5 - randomFloat()))
		delay += jitter
	}

	// Cap at max delay
	if delay > r.config.Retry.MaxDelay {
		delay = r.config.Retry.MaxDelay
	}

	return delay
}

func (r *ReconcilerManager) collectMetrics() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.Metrics.CollectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.metrics.Collect()
			if r.config.Metrics.ExportPrometheus {
				r.exportPrometheusMetrics()
			}
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *ReconcilerManager) exportPrometheusMetrics() {
	// Export metrics to Prometheus
	// Implementation would depend on Prometheus client library
}

func (r *ReconcilerManager) updatePerformanceMetrics(duration time.Duration) {
	r.performance.Update(duration, true)
}

func (r *ReconcilerManager) filterServiceDeployments(deployments []DeploymentInfo) []DeploymentInfo {
	var filtered []DeploymentInfo
	for _, dep := range deployments {
		if r.isServiceDeployment(dep.Name) {
			filtered = append(filtered, dep)
		}
	}
	return filtered
}

func (r *ReconcilerManager) processDeployment(ctx context.Context, deployment DeploymentInfo, cfInstances []CFServiceInstanceDetails) (InstanceData, error) {
	r.logger.Debug("Processing deployment: %s", deployment.Name)

	// Try to get full deployment details (manifest etc.) for better matching
	detail := DeploymentDetail{DeploymentInfo: deployment}
	if r.scanner != nil {
		d, err := r.scanner.GetDeploymentDetails(ctx, deployment.Name)
		if err != nil {
			r.logger.Debug("Failed to get deployment details for %s: %v", deployment.Name, err)
		} else if d != nil {
			detail = *d
			r.logger.Debug("Got deployment details for %s: %d releases, %d stemcells, %d VMs",
				deployment.Name, len(detail.Releases), len(detail.Stemcells), len(detail.VMs))
		}
	} else {
		r.logger.Debug("Scanner not available, using basic deployment info")
	}

	// Derive instance ID from deployment name; allow lenient fallback for legacy/tests
	uuidPattern := regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	matches := uuidPattern.FindStringSubmatch(deployment.Name)

	var instanceID string
	if len(matches) > 0 {
		instanceID = matches[0]
		r.logger.Debug("Extracted instance ID %s from deployment name", instanceID)
	} else {
		fallback := regexp.MustCompile(`([0-9a-f-]{11,36})$`).FindStringSubmatch(deployment.Name)
		if len(fallback) > 0 {
			instanceID = fallback[0]
			r.logger.Debug("Using fallback instance ID %s from deployment name", instanceID)
		} else {
			r.logger.Warning("Could not extract instance ID from deployment name: %s", deployment.Name)
		}
	}

	inst := InstanceData{
		ID:         instanceID,
		Deployment: detail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Metadata: map[string]interface{}{
			"deployment_name": deployment.Name,
		},
	}

	// Attempt to match service/plan via matcher
	if r.matcher != nil {
		if match, err := r.matcher.MatchDeployment(detail, nil); err == nil && match != nil {
			if match.InstanceID != "" {
				inst.ID = match.InstanceID
			}
			inst.ServiceID = match.ServiceID
			inst.PlanID = match.PlanID
			inst.Metadata["match_confidence"] = match.Confidence
			inst.Metadata["match_reason"] = match.MatchReason
		}
	}

	// Enrich from CF discovery data if available and matching
	if inst.ID != "" {
		for _, c := range cfInstances {
			if c.GUID == inst.ID {
				if inst.ServiceID == "" {
					inst.ServiceID = c.ServiceID
				}
				if inst.PlanID == "" {
					inst.PlanID = c.PlanID
				}
				inst.Metadata["cf_org_id"] = c.OrganizationID
				inst.Metadata["cf_space_id"] = c.SpaceID
				inst.Metadata["cf_name"] = c.Name
				break
			}
		}
	}

	if inst.ServiceID == "" || inst.PlanID == "" {
		inst.Metadata["unmatched_service"] = true
	}

	return inst, nil
}

// synchronizeIndex synchronizes the vault index with the given instances
func (r *ReconcilerManager) synchronizeIndex(ctx context.Context, instances []InstanceData) error {
	if r.synchronizer == nil {
		return fmt.Errorf("synchronizer not initialized")
	}
	return r.synchronizer.SyncIndex(ctx, instances)
}

// discoverCFServiceInstances discovers service instances from Cloud Foundry
func (r *ReconcilerManager) discoverCFServiceInstances(ctx context.Context, filter interface{}) ([]CFServiceInstanceDetails, error) {
	if r.cfManager == nil {
		r.logger.Debug("CF manager not configured, skipping CF service discovery")
		return []CFServiceInstanceDetails{}, nil
	}

	// Narrow interface for CF discovery supported by CFConnectionManager in main package
	type cfDiscovery interface {
		DiscoverAllServiceInstances(brokerServices []string) ([]CFServiceInstanceDetails, error)
	}

	// Build the list of broker services to guide discovery (optional)
	var brokerServices []string
	if b, ok := r.broker.(BrokerInterface); ok && b != nil {
		for _, svc := range b.GetServices() {
			if svc.Name != "" {
				brokerServices = append(brokerServices, svc.Name)
			}
		}
	}

	if cfMgr, ok := r.cfManager.(cfDiscovery); ok && cfMgr != nil {
		r.logger.Info("Discovering CF service instances via CF manager")
		instances, err := cfMgr.DiscoverAllServiceInstances(brokerServices)
		if err != nil {
			r.logger.Error("CF discovery failed: %v", err)
			return []CFServiceInstanceDetails{}, nil
		}
		r.logger.Info("CF discovery returned %d instances", len(instances))
		return instances, nil
	}

	r.logger.Info("CF manager present but does not support discovery; skipping")
	return []CFServiceInstanceDetails{}, nil
}

// isServiceDeployment checks if a deployment name indicates it's a service deployment
func (r *ReconcilerManager) isServiceDeployment(deploymentName string) bool {
	// Check if deployment name matches service deployment pattern
	// Format is typically: service-plan-instanceID

	// Quick checks for non-service deployments
	if deploymentName == "" {
		return false
	}

	// System deployments to exclude
	systemDeployments := []string{
		"bosh",
		"cf",
		"concourse",
		"prometheus",
		"grafana",
		"shield",
		"vault",
	}

	for _, sys := range systemDeployments {
		if deploymentName == sys || strings.HasPrefix(deploymentName, sys+"-") {
			return false
		}
	}

	// Check if it matches the service-plan-uuid pattern
	// UUID pattern: 8-4-4-4-12 hexadecimal characters
	uuidPattern := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	matched, _ := regexp.MatchString(uuidPattern, deploymentName)

	return matched
}

func (r *ReconcilerManager) GetStatus() Status {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return r.status
}

func (r *ReconcilerManager) setStatus(status Status) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.status = status
}

func (r *ReconcilerManager) updateStatusError(err error) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	r.status.Errors = append(r.status.Errors, err)
}

// PerformanceTracker implementation

func newPerformanceTracker() *PerformanceTracker {
	return &PerformanceTracker{
		apiLatencies: make(map[string]time.Duration),
		errorCounts:  make(map[string]int),
		successRate:  1.0,
	}
}

func (p *PerformanceTracker) Update(duration time.Duration, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.samples++
	p.lastBatchDuration = duration

	// Update moving average
	if p.averageBatchDuration == 0 {
		p.averageBatchDuration = duration
	} else {
		p.averageBatchDuration = (p.averageBatchDuration*time.Duration(p.samples-1) + duration) / time.Duration(p.samples)
	}

	// Update success rate
	if success {
		p.successRate = (p.successRate*float64(p.samples-1) + 1.0) / float64(p.samples)
	} else {
		p.successRate = (p.successRate * float64(p.samples-1)) / float64(p.samples)
	}
}

func (p *PerformanceTracker) GetScore() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Simple scoring based on success rate and performance
	// High success rate and low latency = high score
	score := p.successRate

	// Adjust based on average duration (penalize slow operations)
	if p.averageBatchDuration > 30*time.Second {
		score *= 0.8
	} else if p.averageBatchDuration > 60*time.Second {
		score *= 0.5
	}

	return score
}

// Helper functions

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomFloat() float64 {
	// Implementation would use crypto/rand for better randomness
	return 0.5 // Placeholder
}
