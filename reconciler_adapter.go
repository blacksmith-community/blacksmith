package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/reconciler"
)

// ReconcilerAdapter adapts the reconciler to work with the existing Blacksmith types
type ReconcilerAdapter struct {
	manager reconciler.Manager
	broker  *Broker
	vault   *Vault
	bosh    bosh.Director
	logger  *Log
	config  reconciler.ReconcilerConfig
}

// NewReconcilerAdapter creates a new reconciler adapter
func NewReconcilerAdapter(config *Config, broker *Broker, vault *Vault, boshDir bosh.Director) *ReconcilerAdapter {
	logger := Logger.Wrap("reconciler")

	// Build reconciler config from main config
	reconcilerConfig := reconciler.ReconcilerConfig{
		Enabled:        getEnvBool("BLACKSMITH_RECONCILER_ENABLED", true),
		Interval:       getEnvDuration("BLACKSMITH_RECONCILER_INTERVAL", 1*time.Hour),
		MaxConcurrency: getEnvInt("BLACKSMITH_RECONCILER_MAX_CONCURRENCY", 5),
		BatchSize:      getEnvInt("BLACKSMITH_RECONCILER_BATCH_SIZE", 10),
		RetryAttempts:  getEnvInt("BLACKSMITH_RECONCILER_RETRY_ATTEMPTS", 3),
		RetryDelay:     getEnvDuration("BLACKSMITH_RECONCILER_RETRY_DELAY", 10*time.Second),
		CacheTTL:       getEnvDuration("BLACKSMITH_RECONCILER_CACHE_TTL", 5*time.Minute),
		Debug:          config.Debug || Debugging,
	}

	return &ReconcilerAdapter{
		broker: broker,
		vault:  vault,
		bosh:   boshDir,
		logger: logger,
		config: reconcilerConfig,
	}
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

	// Create the reconciler manager
	r.manager = reconciler.NewReconcilerManager(
		r.config,
		wrappedBroker,
		wrappedVault,
		r.bosh,
		wrappedLogger,
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
			Tags:        svc.Tags,
			Metadata:    make(map[string]interface{}),
		}

		// Convert metadata if present
		// Note: svc.Metadata is likely a struct, not a map
		// For now, we'll just use an empty map
		// TODO: Convert actual metadata fields if needed

		// Convert plans
		for _, p := range svc.Plans {
			// Handle Free pointer field
			free := false
			if p.Free != nil {
				free = *p.Free
			}

			plan := reconciler.Plan{
				ID:          p.ID,
				Name:        p.Name,
				Description: p.Description,
				Free:        free,
				Metadata:    make(map[string]interface{}),
			}

			// Convert plan metadata if present
			// Note: p.Metadata is likely a struct, not a map
			// For now, we'll just use an empty map
			// TODO: Convert actual metadata fields if needed

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

func (v *vaultWrapper) Get(path string, out interface{}) (bool, error) {
	return v.vault.Get(path, out)
}

func (v *vaultWrapper) Delete(path string) error {
	return v.vault.Delete(path)
}

func (v *vaultWrapper) GetIndex(name string) (*VaultIndex, error) {
	return v.vault.GetIndex(name)
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

// Helper functions for environment variable parsing

func getEnvBool(key string, defaultValue bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	return val == "true" || val == "1" || val == "yes"
}

func getEnvInt(key string, defaultValue int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	var intVal int
	if _, err := fmt.Sscanf(val, "%d", &intVal); err == nil {
		return intVal
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	if duration, err := time.ParseDuration(val); err == nil {
		return duration
	}
	return defaultValue
}
