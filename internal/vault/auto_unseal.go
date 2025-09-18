package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrInvalidFilePath = errors.New("invalid file path")
)

// withAutoUnseal wraps an operation with automatic unsealing on vault sealed errors.
func (vault *Vault) withAutoUnseal(ctx context.Context, operation func() error) error {
	err := operation()
	if err != nil {
		if !vault.isSealedOrUnavailable(err) {
			return err
		}
		// Try to auto-unseal, then retry once
		if !vault.autoUnsealEnabled {
			return err
		}

		var uerr error
		if vault.autoUnsealHook != nil {
			uerr = vault.autoUnsealHook(ctx)
		} else {
			uerr = vault.AutoUnsealIfSealed(ctx)
		}

		if uerr != nil {
			return err // preserve original error; auto-unseal failed
		}
		// retry once
		return operation()
	}

	return nil
}

// AutoUnsealIfSealed attempts to automatically unseal vault if it's sealed.
func (vault *Vault) AutoUnsealIfSealed(_ context.Context) error {
	logger := logger.Get().Named("vault auto-unseal")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	health, err := vault.checkVaultHealth()
	if err != nil {
		return err
	}

	if !health.Sealed {
		return nil
	}

	err = vault.validateAutoUnsealConfig(logger)
	if err != nil {
		return err
	}

	if !vault.acquireUnsealLock() {
		return nil
	}
	defer vault.releaseUnsealLock()

	if vault.shouldSkipDueToCooldown() {
		return nil
	}

	return vault.performUnseal(logger)
}

func (vault *Vault) checkVaultHealth() (*api.HealthResponse, error) {
	health, err := vault.client.Sys().Health()
	if err != nil {
		return nil, fmt.Errorf("failed to check vault health during auto-unseal: %w", err)
	}

	return health, nil
}

func (vault *Vault) validateAutoUnsealConfig(logger logger.Logger) error {
	if vault.credentialsPath == "" {
		logger.Debug("vault is sealed but no credentials path configured; skipping auto-unseal")

		return vaultPkg.ErrSealedNoCredentials
	}

	return nil
}

func (vault *Vault) acquireUnsealLock() bool {
	return atomic.CompareAndSwapInt32(&vault.unsealInProgress, 0, 1)
}

func (vault *Vault) releaseUnsealLock() {
	atomic.StoreInt32(&vault.unsealInProgress, 0)
}

func (vault *Vault) shouldSkipDueToCooldown() bool {
	vault.mu.Lock()
	defer vault.mu.Unlock()

	cooldown := vault.unsealCooldown
	if cooldown <= 0 {
		const defaultUnsealCooldownSeconds = 30

		cooldown = defaultUnsealCooldownSeconds * time.Second
	}

	if time.Since(vault.lastUnsealAttempt) < cooldown {
		return true
	}

	vault.lastUnsealAttempt = time.Now()

	return false
}

func (vault *Vault) performUnseal(logger logger.Logger) error {
	key, err := vault.loadSealKey()
	if err != nil {
		logger.Error("failed to load seal key for auto-unseal: %s", err)

		return err
	}

	logger.Info("vault is sealed; attempting auto-unseal")

	err = vault.client.UnsealVault(key)
	if err != nil {
		logger.Error("auto-unseal failed: %s", err)

		return fmt.Errorf("failed to unseal vault: %w", err)
	}

	logger.Info("vault auto-unseal successful")

	return nil
}

// loadSealKey loads the vault seal key from the credentials file.
func (vault *Vault) loadSealKey() (string, error) {
	bytes, err := safeReadFile(vault.credentialsPath)
	if err != nil {
		return "", err
	}

	creds := vaultPkg.VaultCreds{}

	err = json.Unmarshal(bytes, &creds)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal vault credentials: %w", err)
	}

	if creds.SealKey == "" {
		return "", vaultPkg.ErrSealKeyNotFound
	}

	return creds.SealKey, nil
}

// isSealedOrUnavailable checks if an error indicates vault is sealed or unavailable.
func (vault *Vault) isSealedOrUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var respErr *api.ResponseError
	if errors.As(err, &respErr) {
		// 503 is returned for sealed/standby/unavailable conditions
		if respErr.StatusCode == http.StatusServiceUnavailable {
			return true
		}
	}

	statusStr := strings.ToLower(err.Error())
	switch {
	case strings.Contains(statusStr, "sealed"):
		return true
	case strings.Contains(statusStr, "service unavailable"):
		return true
	case strings.Contains(statusStr, "connection refused"):
		return true
	case strings.Contains(statusStr, "connection reset"):
		return true
	case strings.Contains(statusStr, "broken pipe"):
		return true
	case strings.Contains(statusStr, "i/o timeout"):
		return true
	case strings.Contains(statusStr, "eof"):
		return true
	default:
		return false
	}
}

// IsSealedOrUnavailable is a public wrapper for testing purposes.
func (vault *Vault) IsSealedOrUnavailable(err error) bool {
	return vault.isSealedOrUnavailable(err)
}

// safeReadFile safely reads a file with path validation.
func safeReadFile(path string) ([]byte, error) {
	// Basic path validation - ensure it doesn't contain dangerous patterns
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("%w: %s", ErrInvalidFilePath, path)
	}

	data, err := os.ReadFile(path) // #nosec G304 - Path has been validated
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return data, nil
}

// SetAutoUnsealEnabled sets the auto-unseal enabled flag for testing purposes.
func (vault *Vault) SetAutoUnsealEnabled(enabled bool) {
	vault.autoUnsealEnabled = enabled
}

// SetAutoUnsealHook sets the auto-unseal hook for testing purposes.
func (vault *Vault) SetAutoUnsealHook(hook func(context.Context) error) {
	vault.autoUnsealHook = hook
}

// WithAutoUnseal is a public wrapper for testing purposes.
func (vault *Vault) WithAutoUnseal(ctx context.Context, operation func() error) error {
	return vault.withAutoUnseal(ctx, operation)
}
