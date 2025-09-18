package reconciler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

// Static errors for err113 compliance.
var (
	ErrMissingCriticalComponents  = errors.New("missing critical components")
	ErrReconciliationPanic        = errors.New("reconciliation panic")
	ErrPanic                      = errors.New("panic")
	ErrVaultUpdaterNotInitialized = errors.New("vault updater not initialized")
	ErrReconciliationInProgress   = errors.New("reconciliation already in progress")
	ErrUnknownWorkType            = errors.New("unknown work type")
	ErrSynchronizerNotInitialized = errors.New("synchronizer not initialized")
)

// ReconcilerManager is a production-ready reconciler with full load management.
type ReconcilerManager struct {
	config ReconcilerConfig

	// Core components
	Scanner      Scanner
	Matcher      Matcher
	Updater      Updater
	Synchronizer Synchronizer
	broker       interface{}
	vault        interface{}
	bosh         bosh.Director
	logger       Logger
	cfManager    interface{}

	// Status tracking
	status   Status
	statusMu sync.RWMutex

	// Multiple run prevention
	IsReconciling  atomic.Bool
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

	// Lifecycle management (context removed)
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	shutdownOnce sync.Once

	// Metrics and monitoring
	metrics     MetricsCollector
	performance *PerformanceTracker
}

// WorkItem represents a unit of work.
type WorkItem struct {
	ID         string
	Type       WorkType
	Data       interface{}
	RetryCount int
	Priority   int
}

// WorkType defines the type of work.
type WorkType int

const (
	WorkTypeScanDeployment WorkType = iota
	WorkTypeDiscoverCF
	WorkTypeUpdateVault
	WorkTypeSyncIndex
)

// WorkResult represents the result of processing a work item.
type WorkResult struct {
	Item     WorkItem
	Success  bool
	Error    error
	Duration time.Duration
	Data     interface{}
}

// PerformanceTracker tracks reconciler performance metrics.
type PerformanceTracker struct {
	mu                   sync.RWMutex
	lastBatchDuration    time.Duration
	averageBatchDuration time.Duration
	successRate          float64
	apiLatencies         map[string]time.Duration
	errorCounts          map[string]int
	samples              int
}

// NewReconcilerManager creates a new reconciler with all safety features.
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

	err := config.Validate()
	if err != nil {
		logger.Errorf("Invalid configuration: %v", err)
		// Use defaults anyway but log the error
	}

	manager := &ReconcilerManager{
		config:         config,
		Scanner:        NewBOSHScanner(boshDir, logger),
		Matcher:        NewServiceMatcher(broker, logger),
		Updater:        nil,
		Synchronizer:   NewIndexSynchronizer(vault, logger),
		broker:         broker,
		vault:          vault,
		bosh:           boshDir,
		logger:         logger,
		cfManager:      cfManager,
		status:         Status{},
		statusMu:       sync.RWMutex{},
		IsReconciling:  atomic.Bool{},
		runCounter:     atomic.Uint64{},
		lastRunID:      0,
		activeRunMutex: sync.Mutex{},
		boshLimiter:    nil,
		cfLimiter:      nil,
		vaultLimiter:   nil,
		boshBreaker:    nil,
		cfBreaker:      nil,
		vaultBreaker:   nil,
		workerPool:     nil,
		workQueue:      make(chan WorkItem, config.Concurrency.QueueSize),
		resultQueue:    make(chan WorkResult, config.Concurrency.QueueSize),
		cancel:         nil, // Will be set in Start()
		wg:             sync.WaitGroup{},
		shutdownOnce:   sync.Once{},
		metrics:        NewMetricsCollector(logger),
		performance:    newPerformanceTracker(),
	}

	// Initialize rate limiters
	manager.initializeRateLimiters()

	// Initialize circuit breakers
	manager.initializeCircuitBreakers()

	// Initialize worker pool
	manager.workerPool = NewWorkerPool(config.Concurrency.WorkerPoolSize, manager.processWorkItem)

	// Create updater with enhanced configuration
	manager.Updater = manager.createEnhancedUpdater()

	return manager
}

// initializeRateLimiters initializes rate limiters for API calls.
// Start starts the reconciler.
func (r *ReconcilerManager) Start(ctx context.Context) error {
	if !r.config.Enabled {
		r.logger.Infof("Enhanced reconciler is disabled")

		return nil
	}

	// Create cancellable context for this reconciler instance
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	// Log startup configuration for debugging
	r.logger.Infof("Starting reconciler with configuration:")
	r.logger.Infof("  Interval: %v", r.config.Interval)
	r.logger.Infof("  Max Concurrent: %d", r.config.Concurrency.MaxConcurrent)
	r.logger.Infof("  Worker Pool Size: %d", r.config.Concurrency.WorkerPoolSize)
	r.logger.Infof("  Queue Size: %d", r.config.Concurrency.QueueSize)
	r.logger.Infof("  Batch Size: %d", r.config.Batch.Size)
	r.logger.Infof("  BOSH Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.BOSH.RateLimit.RequestsPerSecond, r.config.APIs.BOSH.RateLimit.Burst)
	r.logger.Infof("  CF Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.CF.RateLimit.RequestsPerSecond, r.config.APIs.CF.RateLimit.Burst)
	r.logger.Infof("  Vault Rate Limit: %.1f req/s (burst: %d)",
		r.config.APIs.Vault.RateLimit.RequestsPerSecond, r.config.APIs.Vault.RateLimit.Burst)
	r.logger.Infof("  Backup Enabled: %v", r.config.Backup.Enabled)
	r.logger.Infof("  Debug Mode: %v", r.config.Debug)

	// Validate critical components are initialized
	err := r.validateComponents()
	if err != nil {
		r.logger.Errorf("Component validation warning: %v", err)
		r.logger.Infof("Reconciler will run in degraded mode with available components")
		// Continue with degraded functionality rather than failing
	}

	// Start worker pool
	r.workerPool.Start(ctx)

	// Start result processor
	r.wg.Add(1)

	go r.processResults(ctx)

	// Start reconciliation loop
	r.wg.Add(1)

	go r.reconciliationLoop(ctx)

	// Start metrics collector if enabled
	if r.config.Metrics.Enabled {
		r.wg.Add(1)

		go r.collectMetrics(ctx)
	}

	// Run initial reconciliation
	go r.RunReconciliation(ctx)

	r.setStatus(Status{
		Running:         true,
		LastRunTime:     time.Now(),
		LastRunDuration: 0,
		InstancesFound:  0,
		InstancesSynced: 0,
		Errors:          nil,
	})

	return nil
}

// RunReconciliation performs a single reconciliation run with safety checks.
func (r *ReconcilerManager) RunReconciliation(ctx context.Context) {
	// Panic recovery to prevent crashes in production
	defer func() {
		if err := recover(); err != nil {
			r.logger.Errorf("PANIC in reconciliation: %v", err)
			r.logger.Errorf("Stack trace: %s", debug.Stack())

			// Update metrics and status
			r.metrics.ReconciliationError(fmt.Errorf("%w: %v", ErrPanic, err))
			r.updateStatusError(fmt.Errorf("%w: %v", ErrReconciliationPanic, err))

			// Ensure we release the reconciliation lock
			r.IsReconciling.Store(false)
		}
	}()

	// Prevent multiple concurrent runs
	if !r.IsReconciling.CompareAndSwap(false, true) {
		r.logger.Infof("Reconciliation already in progress, skipping new run")
		r.metrics.ReconciliationSkipped()

		return
	}
	defer r.IsReconciling.Store(false)

	// Generate unique run ID
	runID := r.runCounter.Add(1)
	r.activeRunMutex.Lock()
	r.lastRunID = runID
	r.activeRunMutex.Unlock()

	startTime := time.Now()

	r.logger.Infof("Starting reconciliation run #%d", runID)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeouts.ReconciliationRun)
	defer cancel()

	// Track metrics
	r.metrics.ReconciliationStarted()

	defer func() {
		duration := time.Since(startTime)
		r.metrics.ReconciliationCompleted(duration)
		r.updatePerformanceMetrics(duration)
		r.logger.Infof("Reconciliation run #%d completed in %v", runID, duration)
	}()

	// Execute reconciliation phases with enhanced error handling
	err := r.executeReconciliationPhases(ctx, runID)
	if err != nil {
		r.logger.Errorf("Reconciliation run #%d failed: %v", runID, err)
		r.metrics.ReconciliationError(err)
		r.updateStatusError(err)

		return
	}

	// Update status
	r.setStatus(Status{
		Running:         true,
		LastRunTime:     startTime,
		LastRunDuration: time.Since(startTime),
		InstancesFound:  0,
		InstancesSynced: 0,
		Errors:          nil,
	})
}

// ProcessBatchWithConcurrency processes a batch with controlled concurrency.
func (r *ReconcilerManager) ProcessBatchWithConcurrency(
	ctx context.Context,
	batch []DeploymentInfo,
	cfInstances []CFServiceInstanceDetails,
) ([]InstanceData, error) {
	channels := r.createProcessingChannels(len(batch))

	r.startDeploymentProcessing(ctx, batch, cfInstances, channels)
	r.waitForProcessingCompletion(channels.waitGroup, channels.resultChan, channels.errorChan)

	return r.collectProcessingResults(ctx, channels)
}

// ProcessingChannels holds channels used for batch processing.
type ProcessingChannels struct {
	semaphore  chan struct{}
	resultChan chan InstanceData
	errorChan  chan error
	waitGroup  *sync.WaitGroup
}

// ForceReconcile forces an immediate reconciliation with safety checks.
func (r *ReconcilerManager) ForceReconcile() error {
	r.logger.Infof("Force reconciliation requested")

	// Check if already running
	if r.IsReconciling.Load() {
		return ErrReconciliationInProgress
	}

	// Start reconciliation in background with background context
	go r.RunReconciliation(context.Background())

	return nil
}

// Stop gracefully stops the reconciler.
func (r *ReconcilerManager) Stop() error {
	r.shutdownOnce.Do(func() {
		r.logger.Infof("Stopping reconciler")

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
			r.logger.Infof("Enhanced reconciler stopped gracefully")
		case <-time.After(r.config.Timeouts.ShutdownGracePeriod):
			r.logger.Warningf("Enhanced reconciler shutdown timeout exceeded")
		}

		// Stop worker pool
		r.workerPool.Stop()

		// Update status
		r.setStatus(Status{
			Running:         false,
			LastRunTime:     time.Time{},
			LastRunDuration: 0,
			InstancesFound:  0,
			InstancesSynced: 0,
			Errors:          nil,
		})
	})

	return nil
}

func (r *ReconcilerManager) FilterServiceDeployments(deployments []DeploymentInfo) []DeploymentInfo {
	var filtered []DeploymentInfo

	for _, dep := range deployments {
		if r.IsServiceDeployment(dep.Name) {
			filtered = append(filtered, dep)
		}
	}

	return filtered
}

// IsServiceDeployment checks if a deployment name indicates it's a service deployment.
func (r *ReconcilerManager) IsServiceDeployment(deploymentName string) bool {
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

func (r *ReconcilerManager) initializeRateLimiters() {
	r.boshLimiter = rate.NewLimiter(rate.Limit(r.config.APIs.BOSH.RateLimit.RequestsPerSecond), r.config.APIs.BOSH.RateLimit.Burst)
	r.cfLimiter = rate.NewLimiter(rate.Limit(r.config.APIs.CF.RateLimit.RequestsPerSecond), r.config.APIs.CF.RateLimit.Burst)
	r.vaultLimiter = rate.NewLimiter(rate.Limit(r.config.APIs.Vault.RateLimit.RequestsPerSecond), r.config.APIs.Vault.RateLimit.Burst)
}

// initializeCircuitBreakers initializes circuit breakers for API calls.
func (r *ReconcilerManager) initializeCircuitBreakers() {
	r.boshBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "bosh",
		MaxRequests: r.safeUint32(r.config.APIs.BOSH.CircuitBreaker.MaxConcurrent),
		Interval:    0, // Use default
		Timeout:     r.config.APIs.BOSH.CircuitBreaker.Timeout,
	})

	r.cfBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "cf",
		MaxRequests: r.safeUint32(r.config.APIs.CF.CircuitBreaker.MaxConcurrent),
		Interval:    0, // Use default
		Timeout:     r.config.APIs.CF.CircuitBreaker.Timeout,
	})

	r.vaultBreaker = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "vault",
		MaxRequests: r.safeUint32(r.config.APIs.Vault.CircuitBreaker.MaxConcurrent),
		Interval:    0, // Use default
		Timeout:     r.config.APIs.Vault.CircuitBreaker.Timeout,
	})
}

const (
	defaultCircuitBreakerMaxRequests = 10
	maxUint32Value                   = 4294967295
)

// safeUint32 safely converts an int to uint32, preventing integer overflow.
func (r *ReconcilerManager) safeUint32(value int) uint32 {
	if value < 0 {
		return defaultCircuitBreakerMaxRequests // Default fallback for negative values
	}

	if value > maxUint32Value {
		return maxUint32Value // Max uint32 value
	}

	return uint32(value)
}

// validateComponents checks if critical components are initialized.
func (r *ReconcilerManager) validateComponents() error {
	var errors []string

	// Check Vault
	if r.vault == nil {
		errors = append(errors, "Vault not initialized")

		r.logger.Errorf("Vault is not initialized - credential operations will fail")
	}

	// Check BOSH
	if r.bosh == nil {
		errors = append(errors, "BOSH director not initialized")

		r.logger.Errorf("BOSH director is not initialized - deployment scanning will be disabled")
	}

	// Check scanner
	if r.Scanner == nil {
		errors = append(errors, "BOSH scanner not initialized")

		r.logger.Errorf("BOSH scanner is not initialized - deployment scanning will be disabled")
	}

	// Check updater
	if r.Updater == nil {
		errors = append(errors, "Updater not initialized")

		r.logger.Errorf("Updater is not initialized - instance updates will be disabled")
	}

	// Log CF manager status (not critical)
	if r.cfManager == nil {
		r.logger.Infof("CF manager not configured - CF discovery will be skipped")
	} else {
		r.logger.Infof("CF manager configured - CF discovery enabled")
	}

	// Check circuit breakers (not critical, can run without them)
	if r.boshBreaker == nil {
		r.logger.Infof("BOSH circuit breaker not initialized - running without circuit protection")
	}

	if r.vaultBreaker == nil {
		r.logger.Infof("Vault circuit breaker not initialized - running without circuit protection")
	}

	if r.cfBreaker == nil && r.cfManager != nil {
		r.logger.Infof("CF circuit breaker not initialized - running without circuit protection")
	}

	if len(errors) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingCriticalComponents, strings.Join(errors, ", "))
	}

	return nil
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

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func randomFloat() float64 {
	// Implementation would use crypto/rand for better randomness
	return deploymentConfidenceLow // Placeholder
}

// reconciliationLoop runs periodic reconciliation with multiple run prevention.
func (r *ReconcilerManager) reconciliationLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.RunReconciliation(ctx)
		case <-ctx.Done():
			r.logger.Infof("Reconciliation loop stopping")

			return
		}
	}
}

// executeReconciliationPhases runs all reconciliation phases with proper rate limiting.
func (r *ReconcilerManager) executeReconciliationPhases(ctx context.Context, runID uint64) error {
	// Phase 1: Discover CF instances with rate limiting
	cfInstances := r.discoverCFInstancesWithRateLimit(ctx)

	// Phase 2: Scan BOSH deployments with rate limiting and batching
	deployments := r.scanDeploymentsWithRateLimit(ctx)

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
	err = r.synchronizeIndex(ctx, updatedInstances)
	if err != nil {
		return fmt.Errorf("phase 5 failed: %w", err)
	}

	r.logger.Infof("Run #%d processed %d instances successfully", runID, len(updatedInstances))

	return nil
}

// discoverCFInstancesWithRateLimit discovers CF instances with rate limiting.
func (r *ReconcilerManager) discoverCFInstancesWithRateLimit(ctx context.Context) []CFServiceInstanceDetails {
	// Skip if CF is not configured
	if r.cfManager == nil {
		r.logger.Debugf("CF manager not configured, skipping CF discovery")

		return []CFServiceInstanceDetails{}
	}

	// Check if rate limiter is available
	if r.cfLimiter != nil {
		err := r.cfLimiter.Wait(ctx)
		if err != nil {
			r.logger.Errorf("CF rate limit wait failed: %v", err)

			return []CFServiceInstanceDetails{} // Continue without CF discovery
		}
	}

	// Execute with or without circuit breaker
	var (
		result interface{}
		err    error
	)

	if r.cfBreaker != nil {
		result, err = r.cfBreaker.Execute(func() (interface{}, error) {
			return r.discoverCFServiceInstances(ctx)
		})
	} else {
		// Execute directly without circuit breaker
		result, err = r.discoverCFServiceInstances(ctx)
	}

	if err != nil {
		r.logger.Errorf("CF discovery failed: %v", err)

		return []CFServiceInstanceDetails{} // Continue without CF instances
	}

	if instances, ok := result.([]CFServiceInstanceDetails); ok {
		return instances
	}

	r.logger.Errorf("Unexpected CF discovery result type")

	return []CFServiceInstanceDetails{}
}

// scanDeploymentsWithRateLimit scans BOSH deployments with rate limiting.
func (r *ReconcilerManager) scanDeploymentsWithRateLimit(ctx context.Context) []DeploymentInfo {
	// Skip if BOSH scanner is not configured
	if r.Scanner == nil {
		r.logger.Errorf("BOSH scanner not configured, cannot scan deployments")

		return []DeploymentInfo{}
	}

	// Check if rate limiter is available
	if r.boshLimiter != nil {
		err := r.boshLimiter.Wait(ctx)
		if err != nil {
			r.logger.Errorf("BOSH rate limit wait failed: %v", err)

			return []DeploymentInfo{} // Continue without BOSH scan
		}
	}

	// Execute with or without circuit breaker
	var (
		result interface{}
		err    error
	)

	if r.boshBreaker != nil {
		result, err = r.boshBreaker.Execute(func() (interface{}, error) {
			return r.Scanner.ScanDeployments(ctx)
		})
	} else {
		// Execute directly without circuit breaker
		result, err = r.Scanner.ScanDeployments(ctx)
	}

	if err != nil {
		r.logger.Errorf("BOSH scan failed: %v", err)

		return []DeploymentInfo{} // Continue without deployments
	}

	if deployments, ok := result.([]DeploymentInfo); ok {
		return r.FilterServiceDeployments(deployments)
	}

	r.logger.Errorf("Unexpected BOSH scan result type")

	return []DeploymentInfo{}
}

// processBatchesAdaptive processes deployments in adaptive batches.
func (r *ReconcilerManager) processBatchesAdaptive(
	ctx context.Context,
	cfInstances []CFServiceInstanceDetails,
	deployments []DeploymentInfo,
) ([]InstanceData, error) {
	// Get effective batch size based on performance
	performanceScore := r.performance.GetScore()
	batchSize := r.config.GetEffectiveBatchSize(performanceScore)

	r.logger.Debugf("Using batch size %d based on performance score %.2f", batchSize, performanceScore)

	var (
		allInstances []InstanceData
		mutex        sync.Mutex
	)

	// Process deployments in batches

	for index := 0; index < len(deployments); index += batchSize {
		end := minInt(index+batchSize, len(deployments))
		batch := deployments[index:end]

		r.logger.Debugf("Processing batch %d-%d of %d deployments", index, end, len(deployments))

		// Process batch with controlled concurrency
		instances, err := r.ProcessBatchWithConcurrency(ctx, batch, cfInstances)
		if err != nil {
			r.logger.Errorf("Batch processing failed: %v", err)
			// Continue with other batches
			continue
		}

		mutex.Lock()

		allInstances = append(allInstances, instances...)

		mutex.Unlock()

		// Cooldown period between batches to prevent API overload
		if index+batchSize < len(deployments) {
			select {
			case <-time.After(r.config.Concurrency.CooldownPeriod):
			case <-ctx.Done():
				return allInstances, fmt.Errorf("context cancelled during deployment processing cooldown: %w", ctx.Err())
			}
		}
	}

	return allInstances, nil
}

// getValidConcurrency ensures concurrency configuration is valid.
func (r *ReconcilerManager) getValidConcurrency() int {
	maxConcurrent := r.config.Concurrency.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1 // Safe fallback
	}

	return maxConcurrent
}

// createProcessingChannels creates and initializes processing channels.
func (r *ReconcilerManager) createProcessingChannels(batchSize int) *ProcessingChannels {
	return &ProcessingChannels{
		semaphore:  make(chan struct{}, r.getValidConcurrency()),
		resultChan: make(chan InstanceData, batchSize),
		errorChan:  make(chan error, batchSize),
		waitGroup:  &sync.WaitGroup{},
	}
}

// startDeploymentProcessing starts goroutines to process deployments.
func (r *ReconcilerManager) startDeploymentProcessing(ctx context.Context, batch []DeploymentInfo, cfInstances []CFServiceInstanceDetails, channels *ProcessingChannels) {
	for _, deployment := range batch {
		channels.waitGroup.Add(1)

		go r.processDeploymentConcurrently(ctx, deployment, cfInstances, channels)
	}
}

// processDeploymentConcurrently processes a single deployment in a goroutine.
func (r *ReconcilerManager) processDeploymentConcurrently(ctx context.Context, deployment DeploymentInfo, cfInstances []CFServiceInstanceDetails, channels *ProcessingChannels) {
	defer channels.waitGroup.Done()
	defer r.handleDeploymentPanic(deployment, channels.errorChan)

	if !r.acquireSemaphore(ctx, channels.semaphore, channels.errorChan) {
		return
	}

	defer func() { <-channels.semaphore }()

	if !r.waitForRateLimit(ctx, channels.errorChan) {
		return
	}

	instance := r.processDeployment(ctx, deployment, cfInstances)
	channels.resultChan <- instance
}

// handleDeploymentPanic handles panic recovery for deployment processing.
func (r *ReconcilerManager) handleDeploymentPanic(deployment DeploymentInfo, errorChan chan<- error) {
	if err := recover(); err != nil {
		r.logger.Errorf("PANIC in deployment processing goroutine: %v", err)

		errorChan <- fmt.Errorf("%w processing deployment %s: %v", ErrPanic, deployment.Name, err)
	}
}

// acquireSemaphore attempts to acquire a semaphore slot.
func (r *ReconcilerManager) acquireSemaphore(ctx context.Context, semaphore chan struct{}, errorChan chan<- error) bool {
	select {
	case semaphore <- struct{}{}:
		return true
	case <-ctx.Done():
		errorChan <- ctx.Err()

		return false
	}
}

// waitForRateLimit waits for rate limiter if configured.
func (r *ReconcilerManager) waitForRateLimit(ctx context.Context, errorChan chan<- error) bool {
	if r.boshLimiter == nil {
		return true
	}

	err := r.boshLimiter.Wait(ctx)
	if err != nil {
		errorChan <- err

		return false
	}

	return true
}

// waitForProcessingCompletion waits for all processing to complete and closes channels.
func (r *ReconcilerManager) waitForProcessingCompletion(waitGroup *sync.WaitGroup, resultChan chan InstanceData, errorChan chan error) {
	go func() {
		waitGroup.Wait()
		close(resultChan)
		close(errorChan)
	}()
}

// collectProcessingResults collects results from processing channels.
func (r *ReconcilerManager) collectProcessingResults(ctx context.Context, channels *ProcessingChannels) ([]InstanceData, error) {
	var (
		instances []InstanceData
		errors    []error
	)

	for {
		select {
		case instance, ok := <-channels.resultChan:
			if !ok {
				if len(errors) > 0 {
					r.logger.Warningf("Batch processing completed with %d errors", len(errors))
				}

				return instances, nil
			}

			instances = append(instances, instance)
		case err := <-channels.errorChan:
			if err != nil {
				errors = append(errors, err)
			}
		case <-ctx.Done():
			return instances, fmt.Errorf("context cancelled during batch processing: %w", ctx.Err())
		}
	}
}

// updateVaultWithRateLimit updates Vault with rate limiting.
func (r *ReconcilerManager) updateVaultWithRateLimit(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	// Check if updater is available
	if r.Updater == nil {
		return nil, ErrVaultUpdaterNotInitialized
	}

	var (
		updated     []InstanceData
		updateMutex sync.Mutex
	)

	// Process updates with rate limiting

	for _, instance := range instances {
		// Wait for rate limit (with nil check)
		if r.vaultLimiter != nil {
			err := r.vaultLimiter.Wait(ctx)
			if err != nil {
				return updated, fmt.Errorf("vault rate limit wait failed: %w", err)
			}
		}

		// Execute with or without circuit breaker
		var (
			result interface{}
			err    error
		)

		if r.vaultBreaker != nil {
			result, err = r.vaultBreaker.Execute(func() (interface{}, error) {
				return r.Updater.UpdateInstance(ctx, instance)
			})
		} else {
			// Execute directly without circuit breaker
			result, err = r.Updater.UpdateInstance(ctx, instance)
		}

		if err != nil {
			r.logger.Errorf("Failed to update instance %s: %v", instance.ID, err)

			continue
		}

		if updatedInstance, ok := result.(*InstanceData); ok {
			updateMutex.Lock()

			updated = append(updated, *updatedInstance)

			updateMutex.Unlock()
		}
	}

	return updated, nil
}

// Helper functions

func (r *ReconcilerManager) createEnhancedUpdater() *VaultUpdater {
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
	result := WorkResult{
		Item:     item,
		Success:  false,
		Error:    nil,
		Duration: 0,
		Data:     nil,
	}

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
		result.Error = fmt.Errorf("%w: %v", ErrUnknownWorkType, item.Type)
	}

	result.Duration = time.Since(startTime)

	return result
}

func (r *ReconcilerManager) processResults(ctx context.Context) {
	defer r.wg.Done()

	for {
		select {
		case result := <-r.resultQueue:
			if result.Error != nil {
				r.logger.Errorf("Work item %s failed: %v", result.Item.ID, result.Error)
				r.handleWorkItemFailure(ctx, result)
			} else {
				r.logger.Debugf("Work item %s completed in %v", result.Item.ID, result.Duration)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *ReconcilerManager) handleWorkItemFailure(ctx context.Context, result WorkResult) {
	// Implement retry logic with exponential backoff
	if result.Item.RetryCount < r.config.Retry.MaxAttempts {
		delay := r.calculateRetryDelay(result.Item.RetryCount)
		r.logger.Infof("Retrying work item %s after %v (attempt %d/%d)",
			result.Item.ID, delay, result.Item.RetryCount+1, r.config.Retry.MaxAttempts)

		result.Item.RetryCount++

		// Schedule retry
		go func() {
			select {
			case <-time.After(delay):
				r.workQueue <- result.Item
			case <-ctx.Done():
			}
		}()
	} else {
		r.logger.Errorf("Work item %s failed after %d attempts", result.Item.ID, r.config.Retry.MaxAttempts)
		r.metrics.ReconciliationError(result.Error)
	}
}

func (r *ReconcilerManager) calculateRetryDelay(retryCount int) time.Duration {
	delay := r.config.Retry.InitialDelay * time.Duration(r.config.Retry.Multiplier*float64(retryCount))

	// Add jitter
	if r.config.Retry.Jitter > 0 {
		jitter := time.Duration(float64(delay) * r.config.Retry.Jitter * (retryJitterFactor - randomFloat()))
		delay += jitter
	}

	// Cap at max delay
	if delay > r.config.Retry.MaxDelay {
		delay = r.config.Retry.MaxDelay
	}

	return delay
}

func (r *ReconcilerManager) collectMetrics(ctx context.Context) {
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
		case <-ctx.Done():
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

func (r *ReconcilerManager) processDeployment(ctx context.Context, deployment DeploymentInfo, cfInstances []CFServiceInstanceDetails) InstanceData {
	r.logger.Debugf("Processing deployment: %s", deployment.Name)

	detail := r.getDeploymentDetail(ctx, deployment)
	instanceID := r.extractInstanceID(deployment.Name)

	inst := r.createInstanceData(instanceID, detail, deployment.Name)
	r.matchService(detail, &inst)
	r.enrichFromCFData(inst.ID, cfInstances, &inst)
	r.markUnmatchedService(&inst)

	return inst
}

func (r *ReconcilerManager) getDeploymentDetail(ctx context.Context, deployment DeploymentInfo) DeploymentDetail {
	detail := DeploymentDetail{DeploymentInfo: deployment}

	if r.Scanner == nil {
		r.logger.Debugf("Scanner not available, using basic deployment info")

		return detail
	}

	details, err := r.Scanner.GetDeploymentDetails(ctx, deployment.Name)
	if err != nil {
		r.logger.Debugf("Failed to get deployment details for %s: %v", deployment.Name, err)

		return detail
	}

	if details != nil {
		detail = *details
		r.logger.Debugf("Got deployment details for %s: %d releases, %d stemcells, %d VMs",
			deployment.Name, len(detail.Releases), len(detail.Stemcells), len(detail.VMs))
	}

	return detail
}

func (r *ReconcilerManager) extractInstanceID(deploymentName string) string {
	uuidPattern := regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	matches := uuidPattern.FindStringSubmatch(deploymentName)

	if len(matches) > 0 {
		instanceID := matches[0]
		r.logger.Debugf("Extracted instance ID %s from deployment name", instanceID)

		return instanceID
	}

	return r.tryFallbackInstanceID(deploymentName)
}

func (r *ReconcilerManager) tryFallbackInstanceID(deploymentName string) string {
	fallback := regexp.MustCompile(`([0-9a-f-]{11,36})$`).FindStringSubmatch(deploymentName)
	if len(fallback) > 0 {
		instanceID := fallback[0]
		r.logger.Debugf("Using fallback instance ID %s from deployment name", instanceID)

		return instanceID
	}

	r.logger.Warningf("Could not extract instance ID from deployment name: %s", deploymentName)

	return ""
}

func (r *ReconcilerManager) createInstanceData(instanceID string, detail DeploymentDetail, deploymentName string) InstanceData {
	return InstanceData{
		ID:         instanceID,
		Deployment: detail,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Metadata: map[string]interface{}{
			"deployment_name": deploymentName,
		},
	}
}

func (r *ReconcilerManager) matchService(detail DeploymentDetail, inst *InstanceData) {
	if r.Matcher == nil {
		return
	}

	match, err := r.Matcher.MatchDeployment(detail, nil)
	if err != nil || match == nil {
		return
	}

	if match.InstanceID != "" {
		inst.ID = match.InstanceID
	}

	inst.ServiceID = match.ServiceID
	inst.PlanID = match.PlanID
	inst.Metadata["match_confidence"] = match.Confidence
	inst.Metadata["match_reason"] = match.MatchReason
}

func (r *ReconcilerManager) enrichFromCFData(instanceID string, cfInstances []CFServiceInstanceDetails, inst *InstanceData) {
	if instanceID == "" {
		return
	}

	for _, cfInstance := range cfInstances {
		if cfInstance.GUID != instanceID {
			continue
		}

		r.applyCFInstanceData(cfInstance, inst)

		break
	}
}

func (r *ReconcilerManager) applyCFInstanceData(cfInstance CFServiceInstanceDetails, inst *InstanceData) {
	if inst.ServiceID == "" {
		inst.ServiceID = cfInstance.ServiceID
	}

	if inst.PlanID == "" {
		inst.PlanID = cfInstance.PlanID
	}

	inst.Metadata["cf_org_id"] = cfInstance.OrganizationID
	inst.Metadata["cf_space_id"] = cfInstance.SpaceID
	inst.Metadata["cf_name"] = cfInstance.Name
}

func (r *ReconcilerManager) markUnmatchedService(inst *InstanceData) {
	if inst.ServiceID == "" || inst.PlanID == "" {
		inst.Metadata["unmatched_service"] = true
	}
}

// synchronizeIndex synchronizes the vault index with the given instances.
func (r *ReconcilerManager) synchronizeIndex(ctx context.Context, instances []InstanceData) error {
	if r.Synchronizer == nil {
		return ErrSynchronizerNotInitialized
	}

	err := r.Synchronizer.SyncIndex(ctx, instances)
	if err != nil {
		return fmt.Errorf("failed to sync index with synchronizer: %w", err)
	}

	return nil
}

// discoverCFServiceInstances discovers service instances from Cloud Foundry.
func (r *ReconcilerManager) discoverCFServiceInstances(_ context.Context) ([]CFServiceInstanceDetails, error) {
	if r.cfManager == nil {
		r.logger.Debugf("CF manager not configured, skipping CF service discovery")

		return []CFServiceInstanceDetails{}, nil
	}

	// Narrow interface for CF discovery supported by Manager in internal/cf package
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
		r.logger.Infof("Discovering CF service instances via CF manager")

		instances, err := cfMgr.DiscoverAllServiceInstances(brokerServices)
		if err != nil {
			r.logger.Errorf("CF discovery failed: %v", err)

			return []CFServiceInstanceDetails{}, fmt.Errorf("CF discovery failed: %w", err)
		}

		r.logger.Infof("CF discovery returned %d instances", len(instances))

		return instances, nil
	}

	r.logger.Infof("CF manager present but does not support discovery; skipping")

	return []CFServiceInstanceDetails{}, nil
}
