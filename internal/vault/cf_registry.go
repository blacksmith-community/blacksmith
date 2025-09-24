package vault

import (
	"context"
	"fmt"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

// SaveCFRegistration saves a Cloud Foundry registration to vault.
func (vault *Vault) SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error {
	logger := logger.Get().Named("vault cf registration")

	registrationID, ok := registration["id"].(string)
	if !ok {
		return vaultPkg.ErrRegistrationIDRequired
	}

	path := "secret/blacksmith/registrations/" + registrationID
	logger.Debug("saving CF registration to %s", path)

	return vault.Put(ctx, path, registration)
}

// GetCFRegistration retrieves a Cloud Foundry registration from vault.
func (vault *Vault) GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error) {
	path := "secret/blacksmith/registrations/" + registrationID

	return vault.Get(ctx, path, out)
}

// ListCFRegistrations lists all Cloud Foundry registrations.
func (vault *Vault) ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error) {
	logger := logger.Get().Named("vault cf registrations list")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return nil, err
	}

	// List all registration keys
	keys, err := vault.client.ListSecrets("secret/blacksmith/registrations/")
	if err != nil {
		logger.Debug("failed to list CF registrations (may not exist yet): %s", err)

		return []map[string]interface{}{}, nil
	}

	var registrations []map[string]interface{}
	for _, key := range keys {
		var registration map[string]interface{}

		path := "secret/blacksmith/registrations/" + key

		exists, err := vault.Get(ctx, path, &registration)
		if err != nil {
			logger.Error("failed to get registration %s: %s", key, err)

			continue
		}

		if exists {
			registrations = append(registrations, registration)
		}
	}

	return registrations, nil
}

// DeleteCFRegistration deletes a Cloud Foundry registration from vault.
func (vault *Vault) DeleteCFRegistration(ctx context.Context, registrationID string) error {
	logger := logger.Get().Named("vault cf registration delete")

	path := "secret/blacksmith/registrations/" + registrationID
	logger.Debug("deleting CF registration at %s", path)

	return vault.Delete(ctx, path)
}

// UpdateCFRegistrationStatus updates the status of a Cloud Foundry registration.
func (vault *Vault) UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error {
	logger := logger.Get().Named("vault cf registration status")

	// Get existing registration
	var registration map[string]interface{}

	exists, err := vault.GetCFRegistration(ctx, registrationID, &registration)
	if err != nil {
		return fmt.Errorf("failed to get registration: %w", err)
	}

	if !exists {
		return fmt.Errorf("%w: %s", vaultPkg.ErrRegistrationNotFound, registrationID)
	}

	// Update status and timestamp
	registration["status"] = status

	registration["updated_at"] = time.Now().Format(time.RFC3339)
	if errorMsg != "" {
		registration["last_error"] = errorMsg
	} else {
		delete(registration, "last_error")
	}

	logger.Debug("updating CF registration %s status to %s", registrationID, status)

	return vault.SaveCFRegistration(ctx, registration)
}

// SaveCFRegistrationProgress stores the latest progress record for a registration.
func (vault *Vault) SaveCFRegistrationProgress(ctx context.Context, registrationID string, progress map[string]interface{}) error {
	path := "secret/blacksmith/registrations/" + registrationID + "/progress"

	return vault.Put(ctx, path, progress)
}
