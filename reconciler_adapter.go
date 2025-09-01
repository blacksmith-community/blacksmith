package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/reconciler"
	"github.com/hashicorp/vault/api"
)

// ReconcilerAdapter adapts the reconciler to work with the existing Blacksmith types
type ReconcilerAdapter struct {
	manager   reconciler.Manager
	broker    *Broker
	vault     *Vault
	bosh      bosh.Director
	logger    *Log
	config    reconciler.ReconcilerConfig
	cfManager interface{} // CF connection manager as interface for package compatibility
}

// NewReconcilerAdapter creates a new reconciler adapter
func NewReconcilerAdapter(config *Config, broker *Broker, vault *Vault, boshDir bosh.Director, cfManager interface{}) *ReconcilerAdapter {
	logger := Logger.Wrap("reconciler")

	// Build production reconciler config from main config
	reconcilerConfig := reconciler.ReconcilerConfig{
		Enabled:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ENABLED", config.Reconciler.Enabled, true),
		Interval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_INTERVAL", parseDuration(config.Reconciler.Interval), 5*time.Minute),
		Debug:    getEnvBoolWithDefault("BLACKSMITH_RECONCILER_DEBUG", config.Reconciler.Debug, config.Debug || Debugging),
	}

	// Configure concurrency controls
	reconcilerConfig.Concurrency = reconciler.ConcurrencyConfig{
		MaxConcurrent:        getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_CONCURRENT", config.Reconciler.MaxConcurrency, 4),
		MaxDeploymentsPerRun: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_DEPLOYMENTS_PER_RUN", 0, 100),
		QueueSize:            getEnvIntWithDefault("BLACKSMITH_RECONCILER_QUEUE_SIZE", 0, 1000),
		WorkerPoolSize:       getEnvIntWithDefault("BLACKSMITH_RECONCILER_WORKER_POOL_SIZE", 0, 10),
		CooldownPeriod:       getEnvDurationWithDefault("BLACKSMITH_RECONCILER_COOLDOWN_PERIOD", 0, 2*time.Second),
	}

	// Configure BOSH API limits (default to 4 requests/second as requested)
	reconcilerConfig.APIs.BOSH = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_BOSH_REQUESTS_PER_SECOND", 0, 4)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_BOSH_BURST", 0, 2),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_BOSH_WAIT_TIMEOUT", 0, 5*time.Second),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_BOSH_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_BOSH_FAILURE_THRESHOLD", 0, 5),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_BOSH_SUCCESS_THRESHOLD", 0, 2),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_BOSH_BREAKER_TIMEOUT", 0, 60*time.Second),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_BOSH_BREAKER_MAX_CONCURRENT", 0, 1),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_BOSH_TIMEOUT", 0, 30*time.Second),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_BOSH_MAX_CONNECTIONS", config.BOSH.MaxConnections, 10),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_BOSH_KEEP_ALIVE", 0, 30*time.Second),
	}

	// Configure CF API limits
	reconcilerConfig.APIs.CF = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_CF_REQUESTS_PER_SECOND", 0, 20)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_CF_BURST", 0, 10),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_CF_WAIT_TIMEOUT", 0, 5*time.Second),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_CF_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_CF_FAILURE_THRESHOLD", 0, 3),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_CF_SUCCESS_THRESHOLD", 0, 2),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_CF_BREAKER_TIMEOUT", 0, 30*time.Second),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_CF_BREAKER_MAX_CONCURRENT", 0, 2),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_CF_TIMEOUT", 0, 15*time.Second),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_CF_MAX_CONNECTIONS", 0, 20),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_CF_KEEP_ALIVE", 0, 30*time.Second),
	}

	// Configure Vault API limits
	reconcilerConfig.APIs.Vault = reconciler.APIConfig{
		RateLimit: reconciler.RateLimitConfig{
			RequestsPerSecond: float64(getEnvIntWithDefault("BLACKSMITH_VAULT_REQUESTS_PER_SECOND", 0, 50)),
			Burst:             getEnvIntWithDefault("BLACKSMITH_VAULT_BURST", 0, 20),
			WaitTimeout:       getEnvDurationWithDefault("BLACKSMITH_VAULT_WAIT_TIMEOUT", 0, 3*time.Second),
		},
		CircuitBreaker: reconciler.CircuitBreakerConfig{
			Enabled:          getEnvBoolWithDefault("BLACKSMITH_VAULT_CIRCUIT_BREAKER", false, true),
			FailureThreshold: getEnvIntWithDefault("BLACKSMITH_VAULT_FAILURE_THRESHOLD", 0, 5),
			SuccessThreshold: getEnvIntWithDefault("BLACKSMITH_VAULT_SUCCESS_THRESHOLD", 0, 2),
			Timeout:          getEnvDurationWithDefault("BLACKSMITH_VAULT_BREAKER_TIMEOUT", 0, 45*time.Second),
			MaxConcurrent:    getEnvIntWithDefault("BLACKSMITH_VAULT_BREAKER_MAX_CONCURRENT", 0, 3),
		},
		Timeout:        getEnvDurationWithDefault("BLACKSMITH_VAULT_TIMEOUT", 0, 10*time.Second),
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_VAULT_MAX_CONNECTIONS", 0, 30),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_VAULT_KEEP_ALIVE", 0, 30*time.Second),
	}

	// Configure retry settings
	reconcilerConfig.Retry = reconciler.RetryConfig{
		MaxAttempts:  getEnvIntWithDefault("BLACKSMITH_RECONCILER_RETRY_ATTEMPTS", config.Reconciler.RetryAttempts, 3),
		InitialDelay: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_INITIAL_DELAY", 0, 1*time.Second),
		MaxDelay:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_MAX_DELAY", 0, 30*time.Second),
		Multiplier:   2.0,
		Jitter:       0.1,
	}

	// Configure batch processing
	reconcilerConfig.Batch = reconciler.BatchConfig{
		Size:             getEnvIntWithDefault("BLACKSMITH_RECONCILER_BATCH_SIZE", config.Reconciler.BatchSize, 10),
		Delay:            getEnvDurationWithDefault("BLACKSMITH_RECONCILER_BATCH_DELAY", 0, 500*time.Millisecond),
		MaxParallelBatch: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_PARALLEL_BATCH", 0, 2),
		AdaptiveScaling:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ADAPTIVE_SCALING", false, true),
		MinSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MIN_BATCH_SIZE", 0, 5),
		MaxSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_BATCH_SIZE", 0, 50),
	}

	// Configure timeouts
	reconcilerConfig.Timeouts = reconciler.TimeoutConfig{
		ReconciliationRun:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RUN_TIMEOUT", 0, 15*time.Minute),
		DeploymentScan:      getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SCAN_TIMEOUT", 0, 5*time.Minute),
		InstanceDiscovery:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_DISCOVERY_TIMEOUT", 0, 3*time.Minute),
		VaultOperations:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_VAULT_TIMEOUT", 0, 2*time.Minute),
		HealthCheck:         getEnvDurationWithDefault("BLACKSMITH_RECONCILER_HEALTH_TIMEOUT", 0, 30*time.Second),
		ShutdownGracePeriod: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SHUTDOWN_TIMEOUT", 0, 30*time.Second),
	}

	// Configure backup settings
	reconcilerConfig.Backup = reconciler.BackupConfig{
		Enabled:          getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ENABLED", config.Reconciler.Backup.Enabled, true),
		RetentionCount:   getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION", config.Reconciler.Backup.RetentionCount, 5),
		RetentionDays:    getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION_DAYS", config.Reconciler.Backup.RetentionDays, 0),
		CompressionLevel: getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_COMPRESSION", config.Reconciler.Backup.CompressionLevel, 9),
		CleanupEnabled:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_CLEANUP", config.Reconciler.Backup.CleanupEnabled, true),
		BackupOnUpdate:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_UPDATE", config.Reconciler.Backup.BackupOnUpdate, true),
		BackupOnDelete:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_DELETE", config.Reconciler.Backup.BackupOnDelete, true),
	}

	// Configure metrics
	reconcilerConfig.Metrics = reconciler.MetricsConfig{
		Enabled:            getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_ENABLED", false, true),
		CollectionInterval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_INTERVAL", 0, 30*time.Second),
		RetentionPeriod:    getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_RETENTION", 0, 24*time.Hour),
		ExportPrometheus:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_PROMETHEUS", false, false),
		PrometheusPort:     getEnvIntWithDefault("BLACKSMITH_RECONCILER_METRICS_PORT", 0, 9090),
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

// Start starts the reconciler
func (r *ReconcilerAdapter) Start(ctx context.Context) error {
	if !r.config.Enabled {
		r.logger.Info("Deployment reconciler is disabled")
		return nil
	}

	r.logger.Info("Initializing deployment reconciler")

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
		return err
	}

	r.logger.Info("Deployment reconciler started successfully")
	return nil
}

// Stop stops the reconciler
func (r *ReconcilerAdapter) Stop() error {
	if r.manager == nil {
		return nil
	}

	r.logger.Info("Stopping deployment reconciler")
	return r.manager.Stop()
}

// GetStatus returns the reconciler status
func (r *ReconcilerAdapter) GetStatus() reconciler.Status {
	if r.manager == nil {
		return reconciler.Status{Running: false}
	}
	return r.manager.GetStatus()
}

// ForceReconcile forces an immediate reconciliation
func (r *ReconcilerAdapter) ForceReconcile() error {
	if r.manager == nil {
		return fmt.Errorf("reconciler not initialized")
	}
	return r.manager.ForceReconcile()
}

// Wrapper types to adapt between reconciler interfaces and existing types

// brokerWrapper wraps the Broker for use by the reconciler
type brokerWrapper struct {
	broker *Broker
}

func (b *brokerWrapper) GetServices() []reconciler.Service {
	var services []reconciler.Service

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

// vaultWrapper wraps the Vault for use by the reconciler
type vaultWrapper struct {
	vault *Vault
}

func (v *vaultWrapper) Put(path string, data interface{}) error {
	return v.vault.Put(path, data)
}

func (v *vaultWrapper) Get(path string) (map[string]interface{}, error) {
	var data map[string]interface{}
	exists, err := v.vault.Get(path, &data)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return data, nil
}

func (v *vaultWrapper) Delete(path string) error {
	return v.vault.Delete(path)
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
		return nil, err
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

// GetClient exposes the underlying Vault API client for backup operations
func (v *vaultWrapper) GetClient() *api.Client {
	client, err := v.vault.GetAPIClient()
	if err != nil {
		return nil
	}
	return client
}

// loggerWrapper wraps the Log for use by the reconciler
type loggerWrapper struct {
	logger *Log
}

func (l *loggerWrapper) Debug(format string, args ...interface{}) {
	l.logger.Debug(format, args...)
}

func (l *loggerWrapper) Info(format string, args ...interface{}) {
	l.logger.Info(format, args...)
}

func (l *loggerWrapper) Warning(format string, args ...interface{}) {
	// Use Info with [WARN] prefix since Log doesn't have Warning method
	l.logger.Info("[WARN] "+format, args...)
}

func (l *loggerWrapper) Error(format string, args ...interface{}) {
	l.logger.Error(format, args...)
}

// Helper functions for environment variable parsing with config file fallback

func getEnvBoolWithDefault(key string, configValue bool, defaultValue bool) bool {
	val := os.Getenv(key)
	if val != "" {
		return val == "true" || val == "1" || val == "yes"
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
