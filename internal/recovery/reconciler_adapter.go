package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrReconcilerNotInitialized = errors.New("reconciler not initialized")
	ErrBrokerNotInitialized     = errors.New("broker not initialized")
	ErrNoCredentialsReturned    = errors.New("no credentials returned")
	ErrVaultKeyNotFound         = errors.New("vault key not found")
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
	broker    *broker.Broker
	vault     *internalVault.Vault
	bosh      bosh.Director
	logger    logger.Logger
	config    reconciler.ReconcilerConfig
	cfManager interface{} // CF connection manager as interface for package compatibility
}

// NewReconcilerAdapter creates a new reconciler adapter.
func buildReconcilerConfig(cfg *config.Config) reconciler.ReconcilerConfig {
	reconcilerConfig := reconciler.ReconcilerConfig{
		Enabled:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ENABLED", cfg.Reconciler.Enabled, true),
		Interval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_INTERVAL", parseDuration(cfg.Reconciler.Interval), DefaultReconcilerInterval),
		Debug:    getEnvBoolWithDefault("BLACKSMITH_RECONCILER_DEBUG", cfg.Reconciler.Debug, cfg.Debug),
	}

	reconcilerConfig.Concurrency = buildConcurrencyConfig(cfg)
	reconcilerConfig.APIs = buildAPIConfigs(cfg)
	reconcilerConfig.Retry = buildRetryConfig(cfg)
	reconcilerConfig.Batch = buildBatchConfig(cfg)
	reconcilerConfig.Timeouts = buildTimeoutConfig()
	reconcilerConfig.Backup = buildBackupConfig(cfg)
	reconcilerConfig.Metrics = buildMetricsConfig()

	return reconcilerConfig
}

func buildConcurrencyConfig(cfg *config.Config) reconciler.ConcurrencyConfig {
	return reconciler.ConcurrencyConfig{
		MaxConcurrent:        getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_CONCURRENT", cfg.Reconciler.MaxConcurrency, DefaultMaxConcurrent),
		MaxDeploymentsPerRun: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_DEPLOYMENTS_PER_RUN", 0, DefaultMaxDeploymentsPerRun),
		QueueSize:            getEnvIntWithDefault("BLACKSMITH_RECONCILER_QUEUE_SIZE", 0, DefaultQueueSize),
		WorkerPoolSize:       getEnvIntWithDefault("BLACKSMITH_RECONCILER_WORKER_POOL_SIZE", 0, DefaultWorkerPoolSize),
		CooldownPeriod:       getEnvDurationWithDefault("BLACKSMITH_RECONCILER_COOLDOWN_PERIOD", 0, DefaultCooldownPeriod),
	}
}

func buildAPIConfigs(cfg *config.Config) reconciler.APIConfigs {
	return reconciler.APIConfigs{
		BOSH:  buildBOSHAPIConfig(cfg),
		CF:    buildCFAPIConfig(),
		Vault: buildVaultAPIConfig(),
	}
}

func buildBOSHAPIConfig(cfg *config.Config) reconciler.APIConfig {
	return reconciler.APIConfig{
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
		MaxConnections: getEnvIntWithDefault("BLACKSMITH_BOSH_MAX_CONNECTIONS", cfg.BOSH.MaxConnections, DefaultBOSHMaxConnections),
		KeepAlive:      getEnvDurationWithDefault("BLACKSMITH_BOSH_KEEP_ALIVE", 0, DefaultBOSHKeepAlive),
	}
}

func buildCFAPIConfig() reconciler.APIConfig {
	return reconciler.APIConfig{
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
}

func buildVaultAPIConfig() reconciler.APIConfig {
	return reconciler.APIConfig{
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
}

func buildRetryConfig(cfg *config.Config) reconciler.RetryConfig {
	return reconciler.RetryConfig{
		MaxAttempts:  getEnvIntWithDefault("BLACKSMITH_RECONCILER_RETRY_ATTEMPTS", cfg.Reconciler.RetryAttempts, DefaultRetryAttempts),
		InitialDelay: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_INITIAL_DELAY", 0, DefaultInitialDelay),
		MaxDelay:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RETRY_MAX_DELAY", 0, DefaultMaxDelay),
		Multiplier:   DefaultRetryMultiplier,
		Jitter:       DefaultRetryJitter,
	}
}

func buildBatchConfig(cfg *config.Config) reconciler.BatchConfig {
	return reconciler.BatchConfig{
		Size:             getEnvIntWithDefault("BLACKSMITH_RECONCILER_BATCH_SIZE", cfg.Reconciler.BatchSize, DefaultBatchSize),
		Delay:            getEnvDurationWithDefault("BLACKSMITH_RECONCILER_BATCH_DELAY", 0, DefaultBatchDelay),
		MaxParallelBatch: getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_PARALLEL_BATCH", 0, DefaultMaxParallelBatch),
		AdaptiveScaling:  getEnvBoolWithDefault("BLACKSMITH_RECONCILER_ADAPTIVE_SCALING", false, true),
		MinSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MIN_BATCH_SIZE", 0, DefaultMinBatchSize),
		MaxSize:          getEnvIntWithDefault("BLACKSMITH_RECONCILER_MAX_BATCH_SIZE", 0, DefaultMaxBatchSize),
	}
}

func buildTimeoutConfig() reconciler.TimeoutConfig {
	return reconciler.TimeoutConfig{
		ReconciliationRun:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_RUN_TIMEOUT", 0, DefaultReconciliationRunTimeout),
		DeploymentScan:      getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SCAN_TIMEOUT", 0, DefaultDeploymentScanTimeout),
		InstanceDiscovery:   getEnvDurationWithDefault("BLACKSMITH_RECONCILER_DISCOVERY_TIMEOUT", 0, DefaultInstanceDiscoveryTimeout),
		VaultOperations:     getEnvDurationWithDefault("BLACKSMITH_RECONCILER_VAULT_TIMEOUT", 0, DefaultVaultOperationsTimeout),
		HealthCheck:         getEnvDurationWithDefault("BLACKSMITH_RECONCILER_HEALTH_TIMEOUT", 0, DefaultHealthCheckTimeout),
		ShutdownGracePeriod: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_SHUTDOWN_TIMEOUT", 0, DefaultShutdownGracePeriod),
	}
}

func buildBackupConfig(cfg *config.Config) reconciler.BackupConfig {
	return reconciler.BackupConfig{
		Enabled:          getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ENABLED", cfg.Reconciler.Backup.Enabled, true),
		RetentionCount:   getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION", cfg.Reconciler.Backup.RetentionCount, DefaultBackupRetentionCount),
		RetentionDays:    getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_RETENTION_DAYS", cfg.Reconciler.Backup.RetentionDays, DefaultBackupRetentionDays),
		CompressionLevel: getEnvIntWithDefault("BLACKSMITH_RECONCILER_BACKUP_COMPRESSION", cfg.Reconciler.Backup.CompressionLevel, DefaultBackupCompressionLevel),
		CleanupEnabled:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_CLEANUP", cfg.Reconciler.Backup.CleanupEnabled, true),
		BackupOnUpdate:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_UPDATE", cfg.Reconciler.Backup.BackupOnUpdate, true),
		BackupOnDelete:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_BACKUP_ON_DELETE", cfg.Reconciler.Backup.BackupOnDelete, true),
	}
}

func buildMetricsConfig() reconciler.MetricsConfig {
	return reconciler.MetricsConfig{
		Enabled:            getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_ENABLED", false, true),
		CollectionInterval: getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_INTERVAL", 0, DefaultMetricsCollectionInterval),
		RetentionPeriod:    getEnvDurationWithDefault("BLACKSMITH_RECONCILER_METRICS_RETENTION", 0, DefaultMetricsRetentionPeriod),
		ExportPrometheus:   getEnvBoolWithDefault("BLACKSMITH_RECONCILER_METRICS_PROMETHEUS", false, false),
		PrometheusPort:     getEnvIntWithDefault("BLACKSMITH_RECONCILER_METRICS_PORT", 0, DefaultPrometheusPort),
	}
}

func NewReconcilerAdapter(cfg *config.Config, brokerInstance *broker.Broker, vault *internalVault.Vault, boshDir bosh.Director, cfManager interface{}) *ReconcilerAdapter {
	logger := logger.Get().Named("reconciler")

	reconcilerConfig := buildReconcilerConfig(cfg)

	logger.Info("Reconciler configured with production safety features")
	logger.Info("BOSH rate limit: %d req/s, CF rate limit: %d req/s, Vault rate limit: %d req/s",
		int(reconcilerConfig.APIs.BOSH.RateLimit.RequestsPerSecond),
		int(reconcilerConfig.APIs.CF.RateLimit.RequestsPerSecond),
		int(reconcilerConfig.APIs.Vault.RateLimit.RequestsPerSecond))

	return &ReconcilerAdapter{
		broker:    brokerInstance,
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
			if r.isVaultReady(ctx) {
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

	err := r.manager.Stop()
	if err != nil {
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

	err := r.manager.ForceReconcile()
	if err != nil {
		return fmt.Errorf("failed to force reconcile: %w", err)
	}

	return nil
}

// isVaultReady checks if Vault is ready for operations.
func (r *ReconcilerAdapter) isVaultReady(ctx context.Context) bool {
	if r.vault == nil {
		return false
	}

	// Try to check Vault health
	//nolint:contextcheck // GetAPIClient() doesn't accept context parameter
	client, err := r.vault.GetAPIClient()
	if err != nil {
		return false
	}

	// Try a simple operation to verify Vault is responsive
	health, err := client.Sys().HealthWithContext(ctx)
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
	broker *broker.Broker
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
		for _, servicePlan := range svc.Plans {
			plan := reconciler.Plan{
				ID:          servicePlan.ID,
				Name:        servicePlan.Name,
				Description: servicePlan.Description,
				Properties:  make(map[string]interface{}),
			}

			// Handle Free pointer field
			if servicePlan.Free != nil {
				plan.Properties["free"] = *servicePlan.Free
			}

			// Convert brokerapi.ServicePlanMetadata to Properties
			if servicePlan.Metadata != nil {
				plan.Properties["displayName"] = servicePlan.Metadata.DisplayName
				plan.Properties["bullets"] = servicePlan.Metadata.Bullets

				// Convert costs if present
				if len(servicePlan.Metadata.Costs) > 0 {
					costs := make([]map[string]interface{}, len(servicePlan.Metadata.Costs))
					for i, cost := range servicePlan.Metadata.Costs {
						costs[i] = map[string]interface{}{
							"amount": cost.Amount,
							"unit":   cost.Unit,
						}
					}

					plan.Properties["costs"] = costs
				}
				// Add any additional metadata
				for k, v := range servicePlan.Metadata.AdditionalMetadata {
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

	bindingCredentials, err := b.broker.GetBindingCredentials(context.Background(), instanceID, bindingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding credentials: %w", err)
	}

	if bindingCredentials == nil {
		return nil, ErrNoCredentialsReturned
	}

	return b.mapToReconcilerCredentials(bindingCredentials), nil
}

func (b *brokerWrapper) mapToReconcilerCredentials(bindingCredentials interface{}) *reconciler.BindingCredentials {
	// Extract credentials using type assertion - assuming a specific broker credential type
	creds, valid := bindingCredentials.(interface {
		GetHost() string
		GetPort() int
		GetUsername() string
		GetPassword() string
		GetCredentialType() string
		GetReconstructedAt() time.Time
		GetURI() string
		GetAPIURL() string
		GetVhost() string
		GetDatabase() string
		GetScheme() string
		GetRaw() map[string]interface{}
	})

	if !valid {
		// Fallback to basic field mapping if type assertion fails
		return b.mapCredentialsGeneric()
	}

	rbc := &reconciler.BindingCredentials{
		Host:            creds.GetHost(),
		Port:            creds.GetPort(),
		Username:        creds.GetUsername(),
		Password:        creds.GetPassword(),
		CredentialType:  creds.GetCredentialType(),
		ReconstructedAt: creds.GetReconstructedAt().Format(time.RFC3339),
		Raw:             make(map[string]interface{}),
	}

	b.addServiceSpecificFields(rbc, creds)
	b.copyRawFields(rbc, creds.GetRaw())
	b.ensureStandardFieldsInRaw(rbc)

	return rbc
}

func (b *brokerWrapper) mapCredentialsGeneric() *reconciler.BindingCredentials {
	// Generic mapping - extract what we can from the interface{}
	rbc := &reconciler.BindingCredentials{
		Raw: make(map[string]interface{}),
	}

	// Try to extract basic fields using reflection if needed
	// This would need to be implemented based on the actual credential structure
	// For now, return minimal structure
	return rbc
}

func (b *brokerWrapper) addServiceSpecificFields(rbc *reconciler.BindingCredentials, creds interface {
	GetURI() string
	GetAPIURL() string
	GetVhost() string
	GetDatabase() string
	GetScheme() string
}) {
	if uri := creds.GetURI(); uri != "" {
		rbc.Raw["uri"] = uri
	}

	if apiURL := creds.GetAPIURL(); apiURL != "" {
		rbc.Raw["api_url"] = apiURL
	}

	if vhost := creds.GetVhost(); vhost != "" {
		rbc.Raw["vhost"] = vhost
	}

	if database := creds.GetDatabase(); database != "" {
		rbc.Raw["database"] = database
	}

	if scheme := creds.GetScheme(); scheme != "" {
		rbc.Raw["scheme"] = scheme
	}
}

func (b *brokerWrapper) copyRawFields(rbc *reconciler.BindingCredentials, rawFields map[string]interface{}) {
	for k, v := range rawFields {
		rbc.Raw[k] = v
	}
}

func (b *brokerWrapper) ensureStandardFieldsInRaw(rbc *reconciler.BindingCredentials) {
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
}

// vaultWrapper wraps the Vault for use by the reconciler.
type vaultWrapper struct {
	vault *internalVault.Vault
}

func (v *vaultWrapper) Put(path string, secret map[string]interface{}) error {
	err := v.vault.Put(context.Background(), path, secret)
	if err != nil {
		return fmt.Errorf("vault put operation failed: %w", err)
	}

	return nil
}

func (v *vaultWrapper) Get(path string) (map[string]interface{}, error) {
	var data map[string]interface{}

	exists, err := v.vault.Get(context.Background(), path, &data)
	if err != nil {
		return nil, fmt.Errorf("vault get operation failed: %w", err)
	}

	if !exists {
		return nil, ErrVaultKeyNotFound
	}

	return data, nil
}

func (v *vaultWrapper) Delete(path string) error {
	err := v.vault.Delete(context.Background(), path)
	if err != nil {
		return fmt.Errorf("vault delete operation failed: %w", err)
	}

	return nil
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
		return nil, fmt.Errorf("failed to get vault API client: %w", err)
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

		_, err := fmt.Sscanf(val, "%d", &intVal)
		if err == nil {
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
		duration, err := time.ParseDuration(val)
		if err == nil {
			return duration
		}
	}

	if configValue != 0 {
		return configValue
	}

	return defaultValue
}
