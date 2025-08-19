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

		// Convert brokerapi.ServiceMetadata to map
		if svc.Metadata != nil {
			service.Metadata = map[string]interface{}{
				"displayName":         svc.Metadata.DisplayName,
				"imageUrl":            svc.Metadata.ImageUrl,
				"longDescription":     svc.Metadata.LongDescription,
				"providerDisplayName": svc.Metadata.ProviderDisplayName,
				"documentationUrl":    svc.Metadata.DocumentationUrl,
				"supportUrl":          svc.Metadata.SupportUrl,
			}
			// Add shareable if set
			if svc.Metadata.Shareable != nil {
				service.Metadata["shareable"] = *svc.Metadata.Shareable
			}
			// Add any additional metadata
			for k, v := range svc.Metadata.AdditionalMetadata {
				service.Metadata[k] = v
			}
		}

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

			// Convert brokerapi.ServicePlanMetadata to map
			if p.Metadata != nil {
				plan.Metadata = map[string]interface{}{
					"displayName": p.Metadata.DisplayName,
					"bullets":     p.Metadata.Bullets,
				}
				// Convert costs if present
				if len(p.Metadata.Costs) > 0 {
					costs := make([]map[string]interface{}, len(p.Metadata.Costs))
					for i, cost := range p.Metadata.Costs {
						costs[i] = map[string]interface{}{
							"amount": cost.Amount,
							"unit":   cost.Unit,
						}
					}
					plan.Metadata["costs"] = costs
				}
				// Add any additional metadata
				for k, v := range p.Metadata.AdditionalMetadata {
					plan.Metadata[k] = v
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

func (v *vaultWrapper) Get(path string, out interface{}) (bool, error) {
	return v.vault.Get(path, out)
}

func (v *vaultWrapper) Delete(path string) error {
	return v.vault.Delete(path)
}

func (v *vaultWrapper) GetIndex(name string) (*reconciler.VaultIndex, error) {
	// Get the actual vault index
	vaultIdx, err := v.vault.GetIndex(name)
	if err != nil {
		return nil, err
	}

	// Convert to reconciler.VaultIndex with a SaveFunc closure
	return &reconciler.VaultIndex{
		Data: vaultIdx.Data,
		SaveFunc: func() error {
			return vaultIdx.Save()
		},
	}, nil
}

func (v *vaultWrapper) UpdateIndex(name string, instanceID string, data interface{}) error {
	// Get the vault index
	idx, err := v.vault.GetIndex(name)
	if err != nil {
		return err
	}

	// Update the data
	idx.Data[instanceID] = data

	// Save the index
	return idx.Save()
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
