package vault

import (
	"context"
	"fmt"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// WaitForVaultReady waits for vault to become available and responsive.
func (vault *Vault) WaitForVaultReady() error {
	vaultLogger := logger.Get().Named("vault readiness")

	// Log that we're waiting for Vault to be available
	vaultLogger.Infof("waiting for Vault to become available at %s", vault.URL)

	// Retry for up to 20 seconds with 1-second intervals
	maxRetries := 20
	for attempt := 1; attempt <= maxRetries; attempt++ {
		vaultLogger.Debugf("checking Vault availability (attempt %d/%d)", attempt, maxRetries)

		// Try to create a vault client for this check
		client, err := vaultPkg.NewClient(vault.URL, "", vault.Insecure) // No token needed for health check
		if err != nil {
			vaultLogger.Debugf("failed to create vault client (attempt %d/%d): %s", attempt, maxRetries, err)
		} else {
			// Use the official Vault API health check
			health, healthErr := client.Sys().Health()
			if healthErr != nil {
				vaultLogger.Debugf("vault health check failed (attempt %d/%d): %s", attempt, maxRetries, healthErr)
			} else {
				// Vault is responding and we got health info
				vaultLogger.Debugf("vault health check successful - initialized: %t, sealed: %t", health.Initialized, health.Sealed)
				vaultLogger.Info("Vault is ready and available")

				return nil
			}
		}

		// Don't sleep after the last attempt
		if attempt < maxRetries {
			time.Sleep(1 * time.Second)
		}
	}

	return fmt.Errorf("%w after %d seconds", vaultPkg.ErrNotAvailable, maxRetries)
}

// HealthCheck performs a health check against vault.
func (vault *Vault) HealthCheck() (*api.HealthResponse, error) {
	logger := logger.Get().Named("vault health")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Errorf("failed to ensure vault client: %s", err)

		return nil, err
	}

	logger.Debug("performing health check")

	health, err := vault.client.Sys().Health()
	if err != nil {
		logger.Debugf("health check failed: %s", err)

		return nil, fmt.Errorf("vault health check failed: %w", err)
	}

	logger.Debugf("health check results - initialized: %t, sealed: %t, standby: %t",
		health.Initialized, health.Sealed, health.Standby)

	return health, nil
}

// IsInitialized checks if vault is initialized.
func (vault *Vault) IsInitialized() (bool, error) {
	logger := logger.Get().Named("vault init status")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Errorf("failed to ensure vault client: %s", err)

		return false, err
	}

	logger.Debug("checking if vault is initialized")

	initStatus, err := vault.client.Sys().InitStatus()
	if err != nil {
		logger.Errorf("failed to check vault initialization status: %s", err)

		return false, fmt.Errorf("failed to check vault init status: %w", err)
	}

	logger.Debugf("vault initialization status: %t", initStatus)

	return initStatus, nil
}

// GetSealStatus retrieves the seal status from vault.
func (vault *Vault) GetSealStatus() (*api.SealStatusResponse, error) {
	logger := logger.Get().Named("vault seal status")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Errorf("failed to ensure vault client: %s", err)

		return nil, err
	}

	logger.Debug("checking vault seal status")

	sealStatus, err := vault.client.Sys().SealStatus()
	if err != nil {
		logger.Errorf("failed to get vault seal status: %s", err)

		return nil, fmt.Errorf("failed to get vault seal status: %w", err)
	}

	logger.Debugf("vault seal status - sealed: %t, threshold: %d, shares: %d",
		sealStatus.Sealed, sealStatus.T, sealStatus.N)

	return sealStatus, nil
}

// IsReady checks if vault is ready for operations (initialized and unsealed).
func (vault *Vault) IsReady() (bool, error) {
	logger := logger.Get().Named("vault readiness check")

	health, err := vault.HealthCheck()
	if err != nil {
		logger.Debugf("health check failed during readiness check: %s", err)

		return false, err
	}

	ready := health.Initialized && !health.Sealed
	logger.Debugf("vault readiness status: %t (initialized: %t, sealed: %t)",
		ready, health.Initialized, health.Sealed)

	return ready, nil
}

// StartHealthWatcher starts a background health watcher that auto-unseals vault.
func (vault *Vault) StartHealthWatcher(ctx context.Context, interval time.Duration) {
	logger := logger.Get().Named("vault watcher")
	if !vault.autoUnsealEnabled {
		logger.Debug("auto-unseal disabled; vault watcher not started")

		return
	}

	if vault.credentialsPath == "" {
		logger.Debug("no credentials path configured; vault watcher disabled")

		return
	}

	if interval <= 0 {
		interval = vaultPkg.UnsealCheckInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Infof("starting vault health watcher with %v interval", interval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("stopping vault health watcher")

			return
		case <-ticker.C:
			_ = vault.AutoUnsealIfSealed(ctx)
		}
	}
}

// EnableAutoUnseal enables automatic unsealing with the specified credentials path.
func (vault *Vault) EnableAutoUnseal(credentialsPath string) {
	vault.autoUnsealEnabled = true
	vault.SetCredentialsPath(credentialsPath)
}
