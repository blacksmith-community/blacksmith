package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/pkg/logger"
	"github.com/hashicorp/vault/api"
)

// Constants for Vault operations.
const (
	VaultCredentialsFilePermissions = 0600
	VaultDefaultTimeout             = 30 * time.Second
	VaultHistoryMaxSize             = 50
	VaultUnsealCheckInterval        = 15 * time.Second
)

type Vault struct {
	URL      string
	Token    string
	Insecure bool
	client   *VaultClient // HashiCorp API client
	// auto-unseal management
	credentialsPath   string
	mu                sync.Mutex
	unsealInProgress  int32
	lastUnsealAttempt time.Time
	unsealCooldown    time.Duration
	autoUnsealEnabled bool
	// test hook: if set, used by withAutoUnseal instead of AutoUnsealIfSealed
	autoUnsealHook func(context.Context) error
}

// ensureClient ensures the vault client is initialized.
func (vault *Vault) ensureClient() error {
	if vault.client == nil {
		client, err := NewVaultClient(vault.URL, vault.Token, vault.Insecure)
		if err != nil {
			return err
		}

		vault.client = client
	}

	return nil
}

// GetAPIClient returns the underlying HashiCorp Vault API client.
func (vault *Vault) GetAPIClient() (*api.Client, error) {
	err := vault.ensureClient()
	if err != nil {
		return nil, err
	}
	// Best-effort: proactively auto-unseal if needed before handing out the client
	if vault.autoUnsealEnabled {
		_ = vault.AutoUnsealIfSealed(context.Background())
	}

	return vault.client.Client, nil
}

// WaitForVaultReady checks if Vault is ready and available before proceeding.
func (vault *Vault) WaitForVaultReady() error {
	vaultLogger := logger.Get().Named("vault readiness")

	// Log that we're waiting for Vault to be available
	vaultLogger.Info("waiting for Vault to become available at %s", vault.URL)

	// Retry for up to 20 seconds with 1-second intervals
	maxRetries := 20
	for attempt := 1; attempt <= maxRetries; attempt++ {
		vaultLogger.Debug("checking Vault availability (attempt %d/%d)", attempt, maxRetries)

		// Try to create a vault client for this check
		client, err := NewVaultClient(vault.URL, "", vault.Insecure) // No token needed for health check
		if err != nil {
			vaultLogger.Debug("failed to create vault client (attempt %d/%d): %s", attempt, maxRetries, err)
		} else {
			// Use the official Vault API health check
			health, healthErr := client.Sys().Health()
			if healthErr != nil {
				vaultLogger.Debug("vault health check failed (attempt %d/%d): %s", attempt, maxRetries, healthErr)
			} else {
				// Vault is responding and we got health info
				vaultLogger.Debug("vault health check successful - initialized: %t, sealed: %t", health.Initialized, health.Sealed)
				vaultLogger.Info("Vault is ready and available")

				return nil
			}
		}

		// Don't sleep after the last attempt
		if attempt < maxRetries {
			time.Sleep(1 * time.Second)
		}
	}

	return fmt.Errorf("%w after %d seconds", ErrVaultNotAvailable, maxRetries)
}

// HealthCheck performs a  health check using the official Vault API.
func (vault *Vault) HealthCheck() (*api.HealthResponse, error) {
	logger := logger.Get().Named("vault health")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return nil, err
	}

	logger.Debug("performing health check")

	health, err := vault.client.Sys().Health()
	if err != nil {
		logger.Debug("health check failed: %s", err)

		return nil, fmt.Errorf("vault health check failed: %w", err)
	}

	logger.Debug("health check results - initialized: %t, sealed: %t, standby: %t",
		health.Initialized, health.Sealed, health.Standby)

	return health, nil
}

// IsInitialized checks if Vault is initialized using the official API.
func (vault *Vault) IsInitialized() (bool, error) {
	logger := logger.Get().Named("vault init status")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return false, err
	}

	logger.Debug("checking initialization status")

	initialized, err := vault.client.Sys().InitStatus()
	if err != nil {
		logger.Debug("failed to check initialization status: %s", err)

		return false, fmt.Errorf("failed to check vault init status: %w", err)
	}

	logger.Debug("vault initialized: %t", initialized)

	return initialized, nil
}

// GetSealStatus returns the current seal status using the official API.
func (vault *Vault) GetSealStatus() (*api.SealStatusResponse, error) {
	logger := logger.Get().Named("vault seal status")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return nil, err
	}

	logger.Debug("checking seal status")

	sealStatus, err := vault.client.Sys().SealStatus()
	if err != nil {
		logger.Debug("failed to check seal status: %s", err)

		return nil, fmt.Errorf("failed to check vault seal status: %w", err)
	}

	logger.Debug("vault sealed: %t, progress: %d/%d", sealStatus.Sealed, sealStatus.Progress, sealStatus.T)

	return sealStatus, nil
}

// IsReady checks if Vault is ready for operations (initialized and unsealed).
func (vault *Vault) IsReady() (bool, error) {
	logger := logger.Get().Named("vault readiness check")

	health, err := vault.HealthCheck()
	if err != nil {
		return false, err
	}

	ready := health.Initialized && !health.Sealed
	logger.Debug("vault ready: %t (initialized: %t, sealed: %t)", ready, health.Initialized, health.Sealed)

	return ready, nil
}

type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

func (vault *Vault) Init(store string) error {
	logger := logger.Get().Named("vault init")

	// Initialize the new client if not already done
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return err
	}

	// Remember credentials path for future auto-unseal
	vault.credentialsPath = store

	// Check if vault is already initialized
	initResp, err := vault.client.InitVault(1, 1)
	if err != nil {
		return err
	}

	if initResp == nil {
		// Vault was already initialized, read existing credentials
		logger.Debug("reading credentials files from %s", store)

		credentialsData, err := safeReadFile(store)
		if err != nil {
			logger.Error("failed to read vault credentials from %s: %s", store, err)

			return err
		}

		creds := VaultCreds{}

		err = json.Unmarshal(credentialsData, &creds)
		if err != nil {
			logger.Error("failed to parse vault credentials from %s: %s", store, err)

			return fmt.Errorf("failed to parse vault credentials: %w", err)
		}

		vault.Token = creds.RootToken
		vault.client.Token = creds.RootToken
		vault.client.SetToken(creds.RootToken)

		if err := os.Setenv("VAULT_TOKEN", vault.Token); err != nil {
			logger.Error("failed to set VAULT_TOKEN environment variable: %s", err)
		}

		vault.updateHomeDirs()

		return vault.Unseal(creds.SealKey)
	}

	// Vault was just initialized
	if initResp.RootToken == "" || len(initResp.Keys) != 1 {
		if initResp.RootToken == "" {
			logger.Error("failed to initialize vault: root token was blank")
		}

		if len(initResp.Keys) != 1 {
			logger.Error("failed to initialize vault: incorrect number of seal keys (%d) returned", len(initResp.Keys))
		}

		err = fmt.Errorf("%w: token '%s' and %d keys", ErrVaultInvalidResponse, initResp.RootToken, len(initResp.Keys))

		return err
	}

	creds := VaultCreds{
		SealKey:   initResp.Keys[0],
		RootToken: initResp.RootToken,
	}

	logger.Debug("marshaling credentials for longterm storage")

	b, err := json.Marshal(creds)
	if err != nil {
		logger.Error("failed to marshal vault root token / seal key for longterm storage: %s", err)

		return fmt.Errorf("failed to marshal vault credentials: %w", err)
	}

	logger.Debug("storing credentials at %s (mode 0600)", store)

	err = os.WriteFile(store, b, VaultCredentialsFilePermissions)
	if err != nil {
		logger.Error("failed to write credentials to longterm storage file %s: %s", store, err)

		return fmt.Errorf("failed to write vault credentials to storage: %w", err)
	}

	vault.Token = creds.RootToken
	vault.client.Token = creds.RootToken
	vault.client.SetToken(creds.RootToken)

	if err := os.Setenv("VAULT_TOKEN", vault.Token); err != nil {
		logger.Error("failed to set VAULT_TOKEN environment variable: %s", err)
	}

	vault.updateHomeDirs()

	return vault.Unseal(creds.SealKey)
}

func (vault *Vault) Unseal(key string) error {
	logger := logger.Get().Named("vault unseal")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return err
	}

	// Use the new client to unseal
	return vault.client.UnsealVault(key)
}

func (vault *Vault) VerifyMount(store string, createIfMissing bool) error {
	logger := logger.Get().Named("Verify Mount")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return err
	}

	// Use the new client to verify/create mount
	return vault.client.VerifyMount(store, createIfMissing)
}

func (vault *Vault) NewRequest(method, url string, data interface{}) (*http.Request, error) {
	ctx, cancel := context.WithTimeout(context.Background(), VaultDefaultTimeout)
	defer cancel()

	if data == nil {
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}

		return req, nil
	}

	cooked, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data to JSON: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(cooked)))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request with body: %w", err)
	}

	return req, nil
}

func (vault *Vault) Do(method, url string, data interface{}) (*http.Response, error) {
	req, err := vault.NewRequest(method, fmt.Sprintf("%s%s", vault.URL, url), data)
	if err != nil {
		return nil, err
	}

	req.Header.Add("X-Vault-Token", vault.Token)

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	return resp, nil
}

func (vault *Vault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	logger := logger.Get().Named("vault get")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return false, err
	}

	var (
		data   map[string]interface{}
		exists bool
		err    error
	)

	op := func() error {
		data, exists, err = vault.client.GetSecret(path)

		return err
	}
	if err = vault.withAutoUnseal(ctx, op); err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	if out == nil {
		return true, nil
	}

	// Marshal and unmarshal to populate the output
	dataBytes, err := json.Marshal(data)
	if err != nil {
		logger.Error("could not remarshal vault data: %s", err)

		return true, ErrVaultCouldNotRemarshalData
	}

	err = json.Unmarshal(dataBytes, &out)
	if err != nil {
		return true, fmt.Errorf("failed to unmarshal vault data: %w", err)
	}

	return true, nil
}

func (vault *Vault) Put(ctx context.Context, path string, data interface{}) error {
	logger := logger.Get().Named("vault put")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return err
	}

	// Convert data to map[string]interface{}
	dataMap, err := convertToMap(data)
	if err != nil {
		logger.Error("failed to convert data to map: %s", err)

		return err
	}

	// Put the secret using the new client, with auto-unseal retry
	op := func() error { return vault.client.PutSecret(path, dataMap) }

	return vault.withAutoUnseal(ctx, op)
}

func (vault *Vault) Delete(ctx context.Context, path string) error {
	logger := logger.Get().Named("vault delete")

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return err
	}
	// Delete the secret using the new client, with auto-unseal retry
	op := func() error { return vault.client.DeleteSecret(path) }

	return vault.withAutoUnseal(ctx, op)
}

func (vault *Vault) Clear(instanceID string) {
	logger := logger.Get().Named("vault clear " + instanceID)

	// Ensure client is initialized
	err := vault.ensureClient()
	if err != nil {
		logger.Error("failed to ensure vault client: %s", err)

		return
	}

	var rm func(string)

	rm = func(path string) {
		logger.Debug("removing Vault secrets at/below %s", path)

		if err := vault.client.DeleteSecret(path); err != nil {
			logger.Error("failed to delete %s: %s", path, err)
		}

		keys, err := vault.client.ListSecrets(path)
		if err != nil {
			logger.Error("failed to list secrets at %s: %s", path, err)

			return
		}

		for _, sub := range keys {
			rm(fmt.Sprintf("%s/%s", path, strings.TrimSuffix(sub, "/")))
		}

		logger.Debug("cleared out vault secrets")
	}

	logger.Info("removing secrets under %s", instanceID)
	rm(instanceID)
	logger.Info("completed")
}

func (vault *Vault) Track(ctx context.Context, instanceID, action string, taskID int, params interface{}) error {
	logger := logger.Get().Named("vault track " + instanceID)
	logger.Debug("tracking action '%s', task %d", action, taskID)

	// Note: We're deprecating storing task IDs in vault
	// This function is kept for backward compatibility but will be removed
	// Task IDs are now retrieved from BOSH deployment events

	// Store minimal tracking info for audit purposes
	task := struct {
		Action      string      `json:"action"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		State:       "in_progress",
		Description: fmt.Sprintf("Operation %s in progress", action),
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	return vault.Put(ctx, instanceID+"/task", task)
}

// TrackProgress updates the progress of an async operation with a description.
func (vault *Vault) TrackProgress(ctx context.Context, instanceID, action, description string, taskID int, params interface{}) error {
	logger := logger.Get().Named("vault track progress " + instanceID)
	logger.Debug("tracking progress for '%s': %s (taskID: %d)", action, description, taskID)

	// Determine the state based on task ID
	var state string

	switch taskID {
	case -1:
		state = eventStateFailed
	case 0:
		state = "initializing"
	default:
		state = "in_progress"
	}

	task := struct {
		Action      string      `json:"action"`
		Task        int         `json:"task"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		Task:        taskID,
		State:       state,
		Description: description,
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	// Log the task being stored for debugging
	logger.Debug("storing task with ID %d, state '%s' at path %s/task", taskID, state, instanceID)

	// Store current state
	err := vault.Put(ctx, instanceID+"/task", task)
	if err != nil {
		logger.Error("failed to store task progress: %s", err)

		return err
	}

	logger.Debug("successfully stored task progress with ID %d", taskID)

	// Append to history
	return vault.AppendHistory(ctx, instanceID, action, description)
}

// AppendHistory adds an entry to the operation history for an instance.
func (vault *Vault) AppendHistory(ctx context.Context, instanceID, action, description string) error {
	logger := logger.Get().Named("vault history " + instanceID)
	logger.Debug("appending to history: %s - %s", action, description)

	metadataPath := instanceID + "/metadata"

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := vault.Get(ctx, metadataPath, &metadata)
	if err != nil {
		logger.Error("failed to get metadata: %s", err)
		// Start fresh if there's an error
		metadata = map[string]interface{}{}
	}

	if !exists {
		metadata = map[string]interface{}{}
	}

	// Extract the history array from metadata
	var history []map[string]interface{}
	if historyData, ok := metadata["history"].([]interface{}); ok {
		// Convert []interface{} to []map[string]interface{}
		for _, entry := range historyData {
			if entryMap, ok := entry.(map[string]interface{}); ok {
				history = append(history, entryMap)
			}
		}
	} else if historyData, ok := metadata["history"].([]map[string]interface{}); ok {
		history = historyData
	} else {
		// If history doesn't exist or is wrong type, start fresh
		history = []map[string]interface{}{}
	}

	// Append new entry
	entry := map[string]interface{}{
		"timestamp":   time.Now().Unix(),
		"action":      action,
		"description": description,
	}
	history = append(history, entry)

	// Limit history to last 50 entries
	if len(history) > VaultHistoryMaxSize {
		history = history[len(history)-VaultHistoryMaxSize:]
	}

	// Store history back in metadata
	metadata["history"] = history

	return vault.Put(ctx, metadataPath, metadata)
}

func (vault *Vault) Index(ctx context.Context, instanceID string, data interface{}) error {
	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		return err
	}

	if data != nil {
		idx.Data[instanceID] = data

		return idx.Save(ctx)
	}

	delete(idx.Data, instanceID)
	err = idx.Save(ctx)
	// Note: We intentionally do NOT call vault.Clear(instanceID) here
	// to preserve secrets for auditing purposes
	return err
}

type Instance struct {
	ID        string
	ServiceID string
	PlanID    string
}

func (vault *Vault) FindInstance(ctx context.Context, id string) (*Instance, bool, error) {
	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		return nil, false, err
	}

	raw, err := idx.Lookup(id)
	if err != nil {
		// Index lookup error indicates item not found - this is a valid case
		_ = err // Explicitly ignore the error as "not found" is represented by returning false

		return nil, false, nil //nolint:nilerr // Index lookup failure means "not found", not an error
	}

	inst, ok := raw.(map[string]interface{})
	if !ok {
		return nil, true, fmt.Errorf("%w [%s]", ErrVaultIndexedValueMalformed, id)
	}

	instance := &Instance{}

	if v, ok := inst["service_id"]; ok {
		if s, ok := v.(string); ok {
			instance.ServiceID = s
		}
	}

	if v, ok := inst["plan_id"]; ok {
		if s, ok := v.(string); ok {
			instance.PlanID = s
		}
	}

	return instance, true, nil
}

func (vault *Vault) State(ctx context.Context, instanceID string) (string, int, map[string]interface{}, error) {
	type TaskState struct {
		Action string                 `json:"action"`
		Task   int                    `json:"task"`
		Params map[string]interface{} `json:"params"`
	}

	state := TaskState{}

	exists, err := vault.Get(ctx, instanceID+"/task", &state)
	if err == nil && !exists {
		err = fmt.Errorf("%w: %s", ErrVaultInstanceNotFound, instanceID)
	}

	return state.Action, state.Task, state.Params, err
}

// getVaultDB returns the vault index (some useful vault constructs) that we're using to keep track of service/plan usage data.
func (vault *Vault) getVaultDB(ctx context.Context) (*VaultIndex, error) {
	logger := logger.Get().Named("task.log")
	logger.Debug("retrieving vault 'db' index (for tracking service usage)")

	db, err := vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Error("failed to get 'db' index out of the vault: %s", err)

		return nil, err
	}

	return db, nil
}

func (vault *Vault) updateHomeDirs() {
	home := os.Getenv("BLACKSMITH_OPER_HOME")
	if home == "" {
		return
	}

	logger := logger.Get().Named("update-home")

	/* ~/.saferc */
	path := home + "/.saferc"
	logger.Debug("writing ~/.saferc file to %s", path)

	var saferc struct {
		Version int    `json:"version"`
		Current string `json:"current"`
		Vaults  struct {
			Local struct {
				URL         string `json:"url"`
				Token       string `json:"token"`
				NoStrongbox bool   `json:"no-strongbox"`
			} `json:"blacksmith"`
		} `json:"vaults"`
	}

	saferc.Version = 1
	saferc.Current = "blacksmith"
	saferc.Vaults.Local.URL = vault.URL
	saferc.Vaults.Local.Token = vault.Token
	saferc.Vaults.Local.NoStrongbox = true

	b, err := json.Marshal(saferc)
	if err != nil {
		logger.Error("failed to marshal new ~/.saferc: %s", err)
	} else {
		err = os.WriteFile(path, b, VaultCredentialsFilePermissions)
		if err != nil {
			logger.Error("failed to write new ~/.saferc: %s", err)
		}
	}

	/* ~/.svtoken */
	path = home + "/.svtoken"
	logger.Debug("writing ~/.svtoken file to %s", path)

	var svtoken struct {
		Vault string `json:"vault"`
		Token string `json:"token"`
	}

	svtoken.Vault = vault.URL
	svtoken.Token = vault.Token

	b, err = json.Marshal(svtoken)
	if err != nil {
		logger.Error("failed to marshal new ~/.svtoken: %s", err)
	} else {
		err = os.WriteFile(path, b, VaultCredentialsFilePermissions)
		if err != nil {
			logger.Error("failed to write new ~/.svtoken: %s", err)
		}
	}

	/* ~/.vault-token */
	path = home + "/.vault-token"
	logger.Debug("writing ~/.vault-token file to %s", path)

	err = os.WriteFile(path, []byte(vault.Token), VaultCredentialsFilePermissions)
	if err != nil {
		logger.Error("failed to write new ~/.vault-token: %s", err)
	}
}

// CF Registration Storage Methods

// SaveCFRegistration stores a CF registration in Vault.
func (vault *Vault) SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error {
	logger := logger.Get().Named("vault cf registration")

	registrationID, ok := registration["id"].(string)
	if !ok {
		return ErrVaultRegistrationIDRequired
	}

	path := "secret/blacksmith/registrations/" + registrationID
	logger.Debug("saving CF registration to %s", path)

	return vault.Put(ctx, path, registration)
}

// GetCFRegistration retrieves a CF registration from Vault.
func (vault *Vault) GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error) {
	path := "secret/blacksmith/registrations/" + registrationID

	return vault.Get(ctx, path, out)
}

// ListCFRegistrations retrieves all CF registrations from Vault.
func (vault *Vault) ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error) {
	logger := logger.Get().Named("vault cf registrations list")

	// Ensure client is initialized
	if err := vault.ensureClient(); err != nil {
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

// DeleteCFRegistration removes a CF registration from Vault.
func (vault *Vault) DeleteCFRegistration(ctx context.Context, registrationID string) error {
	logger := logger.Get().Named("vault cf registration delete")

	path := "secret/blacksmith/registrations/" + registrationID
	logger.Debug("deleting CF registration at %s", path)

	return vault.Delete(ctx, path)
}

// UpdateCFRegistrationStatus updates the status of a CF registration.
func (vault *Vault) UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error {
	logger := logger.Get().Named("vault cf registration status")

	// Get existing registration
	var registration map[string]interface{}

	exists, err := vault.GetCFRegistration(ctx, registrationID, &registration)
	if err != nil {
		return fmt.Errorf("failed to get registration: %w", err)
	}

	if !exists {
		return fmt.Errorf("%w: %s", ErrVaultRegistrationNotFound, registrationID)
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

// withAutoUnseal runs an operation, and if it fails due to Vault being sealed or
// temporarily unavailable, attempts to auto-unseal (if we have credentials) and retries once.
func (vault *Vault) withAutoUnseal(ctx context.Context, op func() error) error {
	err := op()
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
		return op()
	}

	return nil
}

// AutoUnsealIfSealed checks Vault health, and if sealed and credentials are available,
// unseals Vault. It avoids concurrent unseal attempts and cools down between attempts.
func (vault *Vault) AutoUnsealIfSealed(_ context.Context) error {
	logger := logger.Get().Named("vault auto-unseal")

	// Fast path: ensure client exists
	if err := vault.ensureClient(); err != nil {
		return err
	}

	// Query health; if this fails we might be unable to reach Vault at all.
	health, err := vault.client.Sys().Health()
	if err != nil {
		// If unreachable, nothing to do here. Caller will handle retries.
		return fmt.Errorf("failed to check vault health during auto-unseal: %w", err)
	}

	if !health.Sealed {
		return nil
	}

	// Require a credentials file to auto-unseal
	if vault.credentialsPath == "" {
		logger.Debug("vault is sealed but no credentials path configured; skipping auto-unseal")

		return ErrVaultSealedNoCredentials
	}

	// Prevent concurrent unseal attempts
	if !atomic.CompareAndSwapInt32(&vault.unsealInProgress, 0, 1) {
		// Someone else is unsealing; nothing to do.
		return nil
	}
	defer atomic.StoreInt32(&vault.unsealInProgress, 0)

	// Basic cooldown to avoid hot loops
	vault.mu.Lock()

	cooldown := vault.unsealCooldown
	if cooldown <= 0 {
		const defaultUnsealCooldownSeconds = 30
		cooldown = defaultUnsealCooldownSeconds * time.Second
	}

	if time.Since(vault.lastUnsealAttempt) < cooldown {
		vault.mu.Unlock()

		return nil
	}

	vault.lastUnsealAttempt = time.Now()
	vault.mu.Unlock()

	// Load seal key and unseal
	key, err := vault.loadSealKey()
	if err != nil {
		logger.Error("failed to load seal key for auto-unseal: %s", err)

		return err
	}

	logger.Info("vault is sealed; attempting auto-unseal")

	if err := vault.client.UnsealVault(key); err != nil {
		logger.Error("auto-unseal failed: %s", err)

		return err
	}

	logger.Info("vault auto-unseal successful")

	return nil
}

// loadSealKey reads the seal key from the configured credentials JSON file.
func (vault *Vault) loadSealKey() (string, error) {
	b, err := safeReadFile(vault.credentialsPath)
	if err != nil {
		return "", err
	}

	creds := VaultCreds{}
	if err := json.Unmarshal(b, &creds); err != nil {
		return "", fmt.Errorf("failed to unmarshal vault credentials: %w", err)
	}

	if creds.SealKey == "" {
		return "", ErrVaultSealKeyNotFound
	}

	return creds.SealKey, nil
}

// isSealedOrUnavailable attempts to classify an error as being caused by Vault being sealed
// or temporarily unavailable. It errs on the side of allowing the retry path.
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

	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "sealed"):
		return true
	case strings.Contains(s, "service unavailable"):
		return true
	case strings.Contains(s, "connection refused"):
		return true
	case strings.Contains(s, "connection reset"):
		return true
	case strings.Contains(s, "broken pipe"):
		return true
	case strings.Contains(s, "i/o timeout"):
		return true
	case strings.Contains(s, "eof"):
		return true
	default:
		return false
	}
}

// StartHealthWatcher periodically checks Vault health and auto-unseals when sealed.
// It is safe to call multiple times; if no credentials are configured it no-ops.
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
		interval = VaultUnsealCheckInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("starting vault health watcher with %v interval", interval)

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
