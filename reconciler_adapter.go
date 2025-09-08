package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrReconcilerNotInitialized = errors.New("reconciler not initialized")
	ErrBrokerNotInitialized     = errors.New("broker not initialized")
	ErrNoCredentialsReturned    = errors.New("no credentials returned")
)

const (
	boolStringTrue = "true"
)

// Reconciler configuration constants.
const (
	// General reconciler defaults.
	DefaultReconcilerInterval   = 5 * time.Minute
	DefaultMaxConcurrent        = 4
	DefaultMaxDeploymentsPerRun = 100
	DefaultQueueSize            = 1000
	DefaultWorkerPoolSize       = 10
	DefaultCooldownPeriod       = 2 * time.Second

	// BOSH API defaults.
	DefaultBOSHRequestsPerSecond = 4
	DefaultBOSHBurst             = 2
	DefaultBOSHWaitTimeout       = 5 * time.Second
	DefaultBOSHFailureThreshold  = 5
	DefaultBOSHSuccessThreshold  = 2
	DefaultBOSHBreakerTimeout    = 60 * time.Second
	DefaultBOSHMaxConnections    = 10
	DefaultBOSHKeepAlive         = 30 * time.Second

	// CF API defaults.
	DefaultCFRequestsPerSecond = 20
	DefaultCFBurst             = 10
	DefaultCFWaitTimeout       = 5 * time.Second
	DefaultCFFailureThreshold  = 3
	DefaultCFSuccessThreshold  = 2
	DefaultCFBreakerTimeout    = 30 * time.Second
	DefaultCFMaxConnections    = 20
	DefaultCFKeepAlive         = 30 * time.Second

	// Vault API defaults.
	DefaultVaultRequestsPerSecond = 50
	DefaultVaultBurst             = 20
	DefaultVaultWaitTimeout       = 3 * time.Second
	DefaultVaultFailureThreshold  = 5
	DefaultVaultSuccessThreshold  = 2
	DefaultVaultBreakerTimeout    = 45 * time.Second
	DefaultVaultMaxConnections    = 30
	DefaultVaultKeepAlive         = 30 * time.Second

	// Retry defaults.
	DefaultRetryAttempts   = 3
	DefaultInitialDelay    = 1 * time.Second
	DefaultMaxDelay        = 30 * time.Second
	DefaultRetryMultiplier = 2.0
	DefaultRetryJitter     = 0.1

	// Batch processing defaults.
	DefaultBatchSize        = 10
	DefaultBatchDelay       = 500 * time.Millisecond
	DefaultMaxParallelBatch = 2
	DefaultMinBatchSize     = 5
	DefaultMaxBatchSize     = 50

	// Timeout defaults.
	DefaultReconciliationRunTimeout = 15 * time.Minute
	DefaultDeploymentScanTimeout    = 5 * time.Minute
	DefaultInstanceDiscoveryTimeout = 3 * time.Minute
	DefaultVaultOperationsTimeout   = 2 * time.Minute
	DefaultHealthCheckTimeout       = 30 * time.Second
	DefaultShutdownGracePeriod      = 30 * time.Second

	// Backup defaults.
	DefaultBackupRetentionCount   = 5
	DefaultBackupRetentionDays    = 0
	DefaultBackupCompressionLevel = 9

	// Metrics defaults.
	DefaultMetricsCollectionInterval = 30 * time.Second
	DefaultMetricsRetentionPeriod    = 24 * time.Hour
	DefaultPrometheusPort            = 9090
)

// ReconcilerAdapter adapts the reconciler to work with the existing Blacksmith types.
type ReconcilerAdapter struct {
	manager   reconciler.Manager
	broker    *Broker
	vault     *Vault
	bosh      bosh.Director
	logger    logger.Logger
	config    reconciler.ReconcilerConfig
	cfManager interface{} // CF connection manager as interface for package compatibility
}

// NewReconcilerAdapter creates a new reconciler adapter.
func NewReconcilerAdapter(config *Config, broker *Broker, vault *Vault, boshDir bosh.Director, cfManager interface{}) *ReconcilerAdapter {
	logger := logger.Get().Named("reconciler")

	// Build production reconciler config from main config
	reconcilerConfig := reconciler.ReconcilerConfig{
		Enabled:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ENABLED", config.Reconciler.Enabled, true),
		Interval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_INTERVAL", parseDuration(config.Reconciler.Interval), DefaultReconcilerInterval),
		Debug:    getEnvBoolWithDefault("BLACKSMITH_RECONCILER_DEBUG", config.Reconciler.Debug, config.Debug),
	}

	// Configure concurrency controls
	reconcilerConfig.Concurrency = reconciler.ConcurrencyConfig{
		MaxConcurrent:        getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_CONCURRENT", config.Reconciler.MaxConcurrency, DefaultMaxConcurrent),
		MaxDeploymentsPerRun: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_DEPLOYMENTS_PER_RUN", 0, DefaultMaxDeploymentsPerRun),
		QueueSize:            getEnvIntWithDefault("BLACKSMITH_RECONCILER_QUEUE_SIZE", 0, DefaultQueueSize),
		WorkerPoolSize:       getEnvIntWithDefault("BLACKSMITH_RECONCILER_WORKER_POOL_SIZE", 0, DefaultWorkerPoolSize),
		CooldownPeriod:       getEnvDurationWithDefault("BLACKSMITH_RECONCILER_COOLDOWN_PERIOD", 0, DefaultCooldownPeriod),
	}

	// Configure BOSH API limits (default to 4 requests/second as requested)
	reconcilerConfig.APIs.BOSH = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_BOSH_REQUESTS_PER_SECOND", 0, DefaultBOSHRequestsPerSecond)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_BOSH_BURST", 0, DefaultBOSHBurst),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_BOSH_WAIT_TIMEOUT", 0, DefaultBOSHWaitTimeout),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_BOSH_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_BOSH_FAILURE_THRESHOLD", 0, DefaultBOSHFailureThreshold),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_BOSH_SUCCESS_THRESHOLD", 0, DefaultBOSHSuccessThreshold),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_BOSH_BREAKER_TIMEOUT", 0, DefaultBOSHBreakerTimeout),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_BOSH_BREAKER_MAX_CONCURRENT", 0, reconciler.DefaultBOSHMaxConcurrent),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_BOSH_TIMEOUT", 0, reconciler.DefaultBOSHTimeout),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_BOSH_MAX_CONNECTIONS", config.BOSH.MaxConnections, DefaultBOSHMaxConnections),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_BOSH_KEEP_ALIVE", 0, DefaultBOSHKeepAlive),
	}

	// Configure CF API limits
	reconcilerConfig.APIs.CF = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_CF_REQUESTS_PER_SECOND", 0, DefaultCFRequestsPerSecond)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_CF_BURST", 0, DefaultCFBurst),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_CF_WAIT_TIMEOUT", 0, DefaultCFWaitTimeout),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_CF_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_CF_FAILURE_THRESHOLD", 0, DefaultCFFailureThreshold),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_CF_SUCCESS_THRESHOLD", 0, DefaultCFSuccessThreshold),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_CF_BREAKER_TIMEOUT", 0, DefaultCFBreakerTimeout),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_CF_BREAKER_MAX_CONCURRENT", 0, reconciler.DefaultCFMaxConcurrent),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_CF_TIMEOUT", 0, reconciler.DefaultCFTimeout),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_CF_MAX_CONNECTIONS", 0, DefaultCFMaxConnections),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_CF_KEEP_ALIVE", 0, DefaultCFKeepAlive),
	}

	// Configure Vault API limits
	reconcilerConfig.APIs.Vault = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_VAULT_REQUESTS_PER_SECOND", 0, DefaultVaultRequestsPerSecond)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_VAULT_BURST", 0, DefaultVaultBurst),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_VAULT_WAIT_TIMEOUT", 0, DefaultVaultWaitTimeout),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_VAULT_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_VAULT_FAILURE_THRESHOLD", 0, DefaultVaultFailureThreshold),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_VAULT_SUCCESS_THRESHOLD", 0, DefaultVaultSuccessThreshold),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_VAULT_BREAKER_TIMEOUT", 0, DefaultVaultBreakerTimeout),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_VAULT_BREAKER_MAX_CONCURRENT", 0, reconciler.DefaultVaultMaxConcurrent),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_VAULT_TIMEOUT", 0, reconciler.DefaultVaultTimeout),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_VAULT_MAX_CONNECTIONS", 0, DefaultVaultMaxConnections),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_VAULT_KEEP_ALIVE", 0, DefaultVaultKeepAlive),
	}

	// Configure retry settings
	reconcilerConfig.Retry = reconciler.RetryConfig{
		MaxAttempts:  getEnvIntWithDefault("BLACKSMITH_RECONCILER_RETRY_ATTEMPTS", config.Reconciler.RetryAttempts, DefaultRetryAttempts),
		InitialDelay: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_INITIAL_DELAY", 0, DefaultInitialDelay),
		MaxDelay:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_MAX_DELAY", 0, DefaultMaxDelay),
		Multiplier:   DefaultRetryMultiplier,
		Jitter:       DefaultRetryJitter,
	}

	// Configure batch processing
	reconcilerConfig.Batch = reconciler.BatchConfig{
		Size:             getEnvIntWithDefault("BLACKSMITH_RECONCILER_BATCH_SIZE", config.Reconciler.BatchSize, DefaultBatchSize),
		Delay:            getEnvDurationWithDefault("BLACKSMITH_RECONCILER_BATCH_DELAY", 0, DefaultBatchDelay),
		MaxParallelBatch: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_PARALLEL_BATCH", 0, DefaultMaxParallelBatch),
		AdaptiveScaling:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ADAPTIVE_SCALING", false, true),
		MinSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MIN_BATCH_SIZE", 0, DefaultMinBatchSize),
		MaxSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_BATCH_SIZE", 0, DefaultMaxBatchSize),
	}

	// Configure timeouts
	reconcilerConfig.Timeouts = reconciler.TimeoutConfig{
		ReconciliationRun:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RUN_TIMEOUT", 0, DefaultReconciliationRunTimeout),
		DeploymentScan:      getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SCAN_TIMEOUT", 0, DefaultDeploymentScanTimeout),
		InstanceDiscovery:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_DISCOVERY_TIMEOUT", 0, DefaultInstanceDiscoveryTimeout),
		VaultOperations:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_VAULT_TIMEOUT", 0, DefaultVaultOperationsTimeout),
		HealthCheck:         getEnvDurationWithDefault("BLACKSMITH_RECONCILER_HEALTH_TIMEOUT", 0, DefaultHealthCheckTimeout),
		ShutdownGracePeriod: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SHUTDOWN_TIMEOUT", 0, DefaultShutdownGracePeriod),
	}

	// Configure backup settings
	reconcilerConfig.Backup = reconciler.BackupConfig{
		Enabled:          getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ENABLED", config.Reconciler.Backup.Enabled, true),
		RetentionCount:   getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION", config.Reconciler.Backup.RetentionCount, DefaultBackupRetentionCount),
		RetentionDays:    getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION_DAYS", config.Reconciler.Backup.RetentionDays, DefaultBackupRetentionDays),
		CompressionLevel: getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_COMPRESSION", config.Reconciler.Backup.CompressionLevel, DefaultBackupCompressionLevel),
		CleanupEnabled:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_CLEANUP", config.Reconciler.Backup.CleanupEnabled, true),
		BackupOnUpdate:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_UPDATE", config.Reconciler.Backup.BackupOnUpdate, true),
		BackupOnDelete:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_DELETE", config.Reconciler.Backup.BackupOnDelete, true),
	}

	// Configure metrics
	reconcilerConfig.Metrics = reconciler.MetricsConfig{
		Enabled:            getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_ENABLED", false, true),
		CollectionInterval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_INTERVAL", 0, DefaultMetricsCollectionInterval),
		RetentionPeriod:    getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_RETENTION", 0, DefaultMetricsRetentionPeriod),
		ExportPrometheus:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_PROMETHEUS", false, false),
		PrometheusPort:     getEnvIntWithDefault("BLACKSMITH_RECONCILER_METRICS_PORT", 0, DefaultPrometheusPort),
	}

	logger.Info("Reconciler configured with production safety features")
	logger.Info("BOSH rate limit: %d req/s, CF rate limit: %d req/s, Vault rate limit: %d req/s",
		int(reconcilerConfig.APIs.BOSH.RateLimit.RequestsPerSecond),
		int(reconcilerConfig.APIs.CF.RateLimit.RequestsPerSecond),
		int(reconcilerConfig.APIs.Vault.RateLimit.RequestsPerSecond))

	return &ReconcilerAdapter{
		broker:    broker,
		vault:     vault,
		bosh:      boshDir,
		logger:    logger,
		config:    reconcilerConfig,
		cfManager: cfManager,
	}
}

func parseDuration(s string) time.Duration {
	if s == "" {
		return 0
	}

	d, _ := time.ParseDuration(s)

	return d
}

// Start starts the reconciler.
func (r *ReconcilerAdapter) Start(ctx context.Context) error {
	if !r.config.Enabled {
		r.logger.Info("Deployment reconciler is disabled")

		return nil
	}

	r.logger.Info("Initializing deployment reconciler")

	// Wait for Vault to be ready before starting
	if r.vault != nil {
		r.logger.Info("Waiting for Vault to be ready before starting reconciler...")

		retries := 0

		maxRetries := 30 // Wait up to 30 seconds
		for retries < maxRetries {
			if r.isVaultReady() {
				r.logger.Info("Vault is ready")

				break
			}

			time.Sleep(1 * time.Second)

			retries++
			if retries%5 == 0 {
				r.logger.Debug("Still waiting for Vault to be ready... (%d/%d)", retries, maxRetries)
			}
		}

		if retries >= maxRetries {
			r.logger.Error("Vault not ready after %d seconds - reconciler will run with degraded functionality", maxRetries)
			// Continue anyway with degraded functionality
		}
	} else {
		r.logger.Error("Vault is nil - reconciler will run with severely limited functionality")
	}

	// Create wrapped components
	wrappedBroker := &brokerWrapper{broker: r.broker}
	wrappedVault := &vaultWrapper{vault: r.vault}
	wrappedLogger := &loggerWrapper{logger: r.logger}

	// Create the reconciler manager with CF manager (as interface{})
	r.manager = reconciler.NewReconcilerManager(
		r.config,
		wrappedBroker,
		wrappedVault,
		r.bosh,
		wrappedLogger,
		r.cfManager,
	)

	// Start the reconciler
	err := r.manager.Start(ctx)
	if err != nil {
		r.logger.Error("Failed to start reconciler: %s", err)

		return fmt.Errorf("failed to start reconciler: %w", err)
	}

	r.logger.Info("Deployment reconciler started successfully")

	return nil
}

// Stop stops the reconciler.
func (r *ReconcilerAdapter) Stop() error {
	if r.manager == nil {
		return nil
	}

	r.logger.Info("Stopping deployment reconciler")

	if err := r.manager.Stop(); err != nil {
		return fmt.Errorf("failed to stop reconciler: %w", err)
	}

	return nil
}

// GetStatus returns the reconciler status.
func (r *ReconcilerAdapter) GetStatus() reconciler.Status {
	if r.manager == nil {
		return reconciler.Status{Running: false}
	}

	return r.manager.GetStatus()
}

// ForceReconcile forces an immediate reconciliation.
func (r *ReconcilerAdapter) ForceReconcile() error {
	if r.manager == nil {
		return ErrReconcilerNotInitialized
	}

	if err := r.manager.ForceReconcile(); err != nil {
		return fmt.Errorf("failed to force reconcile: %w", err)
	}

	return nil
}

// isVaultReady checks if Vault is ready for operations.
func (r *ReconcilerAdapter) isVaultReady() bool {
	if r.vault == nil {
		return false
	}

	// Try to check Vault health
	client := r.vault.client
	if client == nil {
		return false
	}

	// Try a simple operation to verify Vault is responsive
	health, err := client.Sys().Health()
	if err != nil {
		r.logger.Debug("Vault health check failed: %s", err)

		return false
	}

	// Check if Vault is initialized and unsealed
	if !health.Initialized || health.Sealed {
		r.logger.Debug("Vault not ready: initialized=%v, sealed=%v", health.Initialized, health.Sealed)

		return false
	}

	return true
}

// Wrapper types to adapt between reconciler interfaces and existing types

// brokerWrapper wraps the Broker for use by the reconciler.
type brokerWrapper struct {
	broker *Broker
}

func (b *brokerWrapper) GetServices() []reconciler.Service {
	services := make([]reconciler.Service, 0, len(b.broker.Catalog))

	// Use the Catalog field which contains brokerapi.Service entries
	for _, svc := range b.broker.Catalog {
		service := reconciler.Service{
			ID:          svc.ID,
			Name:        svc.Name,
			Description: svc.Description,
		}

		// Convert plans
		for _, p := range svc.Plans {
			plan := reconciler.Plan{
				ID:          p.ID,
				Name:        p.Name,
				Description: p.Description,
				Properties:  make(map[string]interface{}),
			}

			// Handle Free pointer field
			if p.Free != nil {
				plan.Properties["free"] = *p.Free
			}

			// Convert brokerapi.ServicePlanMetadata to Properties
			if p.Metadata != nil {
				plan.Properties["displayName"] = p.Metadata.DisplayName
				plan.Properties["bullets"] = p.Metadata.Bullets

				// Convert costs if present
				if len(p.Metadata.Costs) > 0 {
					costs := make([]map[string]interface{}, len(p.Metadata.Costs))
					for i, cost := range p.Metadata.Costs {
						costs[i] = map[string]interface{}{
							"amount": cost.Amount,
							"unit":   cost.Unit,
						}
					}

					plan.Properties["costs"] = costs
				}
				// Add any additional metadata
				for k, v := range p.Metadata.AdditionalMetadata {
					plan.Properties[k] = v
				}
			}

			service.Plans = append(service.Plans, plan)
		}

		services = append(services, service)
	}

	return services
}

// GetBindingCredentials reconstructs binding credentials via the real broker and maps them.
func (b *brokerWrapper) GetBindingCredentials(instanceID, bindingID string) (*reconciler.BindingCredentials, error) {
	if b.broker == nil {
		return nil, ErrBrokerNotInitialized
	}

	bc, err := b.broker.GetBindingCredentials(context.Background(), instanceID, bindingID)
	if err != nil {
		return nil, err
	}

	if bc == nil {
		return nil, ErrNoCredentialsReturned
	}
	// Map to reconciler.BindingCredentials
	rbc := &reconciler.BindingCredentials{
		Host:            bc.Host,
		Port:            bc.Port,
		Username:        bc.Username,
		Password:        bc.Password,
		CredentialType:  bc.CredentialType,
		ReconstructedAt: bc.ReconstructedAt,
		Raw:             make(map[string]interface{}),
	}
	// Optional service-specific fields: include in Raw and keep structured where applicable
	if bc.URI != "" {
		rbc.Raw["uri"] = bc.URI
	}

	if bc.APIURL != "" {
		rbc.Raw["api_url"] = bc.APIURL
	}

	if bc.Vhost != "" {
		rbc.Raw["vhost"] = bc.Vhost
	}

	if bc.Database != "" {
		rbc.Raw["database"] = bc.Database
	}

	if bc.Scheme != "" {
		rbc.Raw["scheme"] = bc.Scheme
	}
	// Copy all raw fields if provided
	if bc.Raw != nil {
		for k, v := range bc.Raw {
			rbc.Raw[k] = v
		}
	}
	// Ensure standard fields are reflected in Raw as well
	if rbc.Host != "" {
		rbc.Raw["host"] = rbc.Host
	}

	if rbc.Port != 0 {
		rbc.Raw["port"] = rbc.Port
	}

	if rbc.Username != "" {
		rbc.Raw["username"] = rbc.Username
	}

	if rbc.Password != "" {
		rbc.Raw["password"] = rbc.Password
	}

	if rbc.CredentialType != "" {
		rbc.Raw["credential_type"] = rbc.CredentialType
	}

	return rbc, nil
}

// vaultWrapper wraps the Vault for use by the reconciler.
type vaultWrapper struct {
	vault *Vault
}

func (v *vaultWrapper) Put(path string, secret map[string]interface{}) error {
	return v.vault.Put(context.Background(), path, secret)
}

func (v *vaultWrapper) Get(path string) (map[string]interface{}, error) {
	var data map[string]interface{}

	exists, err := v.vault.Get(context.Background(), path, &data)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, nil
	}

	return data, nil
}

func (v *vaultWrapper) Delete(path string) error {
	return v.vault.Delete(context.Background(), path)
}

func (v *vaultWrapper) GetSecret(path string) (map[string]interface{}, error) {
	return v.Get(path)
}

func (v *vaultWrapper) SetSecret(path string, secret map[string]interface{}) error {
	return v.Put(path, secret)
}

func (v *vaultWrapper) DeleteSecret(path string) error {
	return v.Delete(path)
}

func (v *vaultWrapper) ListSecrets(path string) ([]string, error) {
	// Get the vault API client and list using it directly
	client, err := v.vault.GetAPIClient()
	if err != nil {
		return nil, err
	}

	// List secrets at the given path
	secret, err := client.Logical().List(path)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets at path %s: %w", path, err)
	}

	if secret == nil || secret.Data == nil {
		return []string{}, nil
	}

	// Extract keys from the list response
	keys, ok := secret.Data["keys"].([]interface{})
	if !ok {
		return []string{}, nil
	}

	// Convert to string slice
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		if str, ok := key.(string); ok {
			result = append(result, str)
		}
	}

	return result, nil
}

// GetClient exposes the underlying Vault API client for backup operations.
func (v *vaultWrapper) GetClient() *api.Client {
	client, err := v.vault.GetAPIClient()
	if err != nil {
		return nil
	}

	return client
}

// loggerWrapper wraps the Log for use by the reconciler.
type loggerWrapper struct {
	logger logger.Logger
}

func (l *loggerWrapper) Debugf(format string, args ...interface{}) {
	l.logger.Debug(format, args...)
}

func (l *loggerWrapper) Infof(format string, args ...interface{}) {
	l.logger.Info(format, args...)
}

func (l *loggerWrapper) Warningf(format string, args ...interface{}) {
	// Use Info with [WARN] prefix since Log doesn't have Warning method
	l.logger.Info("[WARN] "+format, args...)
}

func (l *loggerWrapper) Errorf(format string, args ...interface{}) {
	l.logger.Error(format, args...)
}

// Helper functions for environment variable parsing with config file fallback

func getEnvBoolWithDefault(key string, configValue bool, defaultValue bool) bool {
	val := os.Getenv(key)
	if val != "" {
		return val == boolStringTrue || val == "1" || val == "yes"
	}

	if configValue {
		return configValue
	}

	return defaultValue
}

func getEnvIntWithDefault(key string, configValue int, defaultValue int) int {
	val := os.Getenv(key)
	if val != "" {
		var intVal int
		if _, err := fmt.Sscanf(val, "%d", &intVal); err == nil {
			return intVal
		}
	}

	if configValue != 0 {
		return configValue
	}

	return defaultValue
}

func getEnvDurationWithDefault(key string, configValue time.Duration, defaultValue time.Duration) time.Duration {
	val := os.Getenv(key)
	if val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			return duration
		}
	}

	if configValue != 0 {
		return configValue
	}

	return defaultValue
}
