package vault

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// Vault represents the main Vault client with Blacksmith-specific functionality.
type Vault struct {
	URL      string
	Token    string
	Insecure bool
	client   *vaultPkg.Client // HashiCorp API client wrapper
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

// New creates a new Vault instance.
func New(url, token string, insecure bool) *Vault {
	return &Vault{
		URL:      url,
		Token:    token,
		Insecure: insecure,
	}
}

// SetToken updates the in-memory token and refreshes it on the underlying client if present.
func (vault *Vault) SetToken(token string) {
	vault.Token = token
	if vault.client != nil && token != "" {
		vault.client.SetToken(token)
	}
}

// SetCredentialsPath records the file path used to persist Vault credentials.
// If an empty value is supplied the default BLACKSMITH_OPER_HOME derived path is used.
func (vault *Vault) SetCredentialsPath(path string) {
	if path != "" {
		vault.credentialsPath = path
	} else if vault.credentialsPath == "" {
		vault.updateHomeDirs()
	}
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

// NewRequest creates a new HTTP request for vault operations.
func (vault *Vault) NewRequest(method, url string, data interface{}) (*http.Request, error) {
	ctx, cancel := context.WithTimeout(context.Background(), vaultPkg.DefaultTimeout)
	defer cancel()

	var req *http.Request

	if data == nil {
		var err error

		req, err = http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request: %w", err)
		}
	} else {
		dataBytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal data: %w", err)
		}

		req, err = http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(dataBytes)))
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP request with data: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
	}

	req.Header.Set("X-Vault-Token", vault.Token)

	return req, nil
}

// Do executes an HTTP request against vault.
func (vault *Vault) Do(method, url string, data interface{}) (*http.Response, error) {
	req, err := vault.NewRequest(method, fmt.Sprintf("%s%s", vault.URL, url), data)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{}
	if vault.Insecure {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // #nosec G402 - This is configurable by user
			},
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	return resp, nil
}

// Get retrieves data from vault at the specified path.
func (vault *Vault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	logger := logger.Get().Named("vault get")

	err := vault.ensureClient()
	if err != nil {
		return false, err
	}

	data, exists, err := vault.client.GetSecret(path)
	if err != nil {
		return false, vault.withAutoUnseal(ctx, func() error {
			data, exists, err = vault.client.GetSecret(path)
			if err != nil {
				return fmt.Errorf("failed to get secret: %w", err)
			}

			return nil
		})
	}

	if !exists {
		logger.Debug("secret not found at %s", path)

		return false, nil
	}

	// Convert the data to the expected output format
	err = convertToStruct(data, out)
	if err != nil {
		logger.Error("failed to convert data at %s: %s", path, err)

		return false, fmt.Errorf("%w: failed to convert data", vaultPkg.ErrCouldNotRemarshalData)
	}

	logger.Debug("secret retrieved from %s", path)

	return true, nil
}

// Put stores data in vault at the specified path.
func (vault *Vault) Put(ctx context.Context, path string, data interface{}) error {
	logger := logger.Get().Named("vault put")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	// Convert data to map[string]interface{}
	dataMap, err := convertToMap(data)
	if err != nil {
		return fmt.Errorf("failed to convert data: %w", err)
	}

	err = vault.client.PutSecret(path, dataMap)
	if err != nil {
		return vault.withAutoUnseal(ctx, func() error {
			return vault.client.PutSecret(path, dataMap)
		})
	}

	logger.Debug("secret stored at %s", path)

	return nil
}

// Delete removes data from vault at the specified path.
func (vault *Vault) Delete(ctx context.Context, path string) error {
	logger := logger.Get().Named("vault delete")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	err = vault.client.DeleteSecret(path)
	if err != nil {
		return vault.withAutoUnseal(ctx, func() error {
			return vault.client.DeleteSecret(path)
		})
	}

	logger.Debug("secret deleted at %s", path)

	return nil
}

// Init initializes the vault with the specified store.
func (vault *Vault) Init(store string) error {
	logger := logger.Get().Named("vault init")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	// Check if already initialized
	initStatus, err := vault.client.Sys().InitStatus()
	if err != nil {
		logger.Error("failed to check initialization status: %s", err)

		return fmt.Errorf("failed to check vault initialization status: %w", err)
	}

	if initStatus {
		logger.Info("vault is already initialized")

		err := vault.ensureToken(logger)
		if err != nil {
			logger.Warn("vault token not available: %s", err)
		}

		return nil
	}

	// Initialize vault with 1 share (single key setup)
	initResp, err := vault.client.InitVault(1, 1)
	if err != nil {
		logger.Error("failed to initialize vault: %s", err)

		return fmt.Errorf("failed to initialize vault: %w", err)
	}

	if initResp == nil {
		logger.Info("vault was already initialized")

		return nil
	}

	// Store credentials
	creds := vaultPkg.VaultCreds{
		SealKey:   initResp.Keys[0],
		RootToken: initResp.RootToken,
	}

	err = vault.storeCredentials(creds)
	if err != nil {
		logger.Error("failed to store vault credentials: %s", err)

		return fmt.Errorf("failed to store credentials: %w", err)
	}

	// Update token for future operations
	vault.SetToken(initResp.RootToken)

	logger.Info("vault initialized successfully")

	return nil
}

// Unseal unseals the vault using the stored key.
func (vault *Vault) Unseal(key string) error {
	logger := logger.Get().Named("vault unseal")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	err = vault.client.UnsealVault(key)
	if err != nil {
		logger.Error("failed to unseal vault: %s", err)

		return fmt.Errorf("failed to unseal vault: %w", err)
	}

	logger.Info("vault unsealed successfully")

	return nil
}

// VerifyMount ensures the specified mount exists, creating it if necessary.
func (vault *Vault) VerifyMount(store string, createIfMissing bool) error {
	logger := logger.Get().Named("Verify Mount")

	err := vault.ensureClient()
	if err != nil {
		return err
	}

	err = vault.client.VerifyMount(store, createIfMissing)
	if err != nil {
		logger.Error("failed to verify mount %s: %s", store, err)

		return fmt.Errorf("failed to verify vault mount: %w", err)
	}

	logger.Debug("mount %s verified", store)

	return nil
}

// LoadTokenFromCredentials loads the root token from the credentials file and
// applies it to the Vault client. The loaded token is returned for convenience.
func (vault *Vault) LoadTokenFromCredentials() (string, error) {
	creds, err := vault.loadCredentialsFile()
	if err != nil {
		return "", err
	}

	if creds.RootToken == "" {
		return "", vaultPkg.ErrTokenNotFound
	}

	vault.SetToken(creds.RootToken)

	return creds.RootToken, nil
}

// ensureClient ensures the vault client is initialized.
func (vault *Vault) ensureClient() error {
	if vault.client == nil {
		client, err := vaultPkg.NewClient(vault.URL, vault.Token, vault.Insecure)
		if err != nil {
			return fmt.Errorf("failed to create vault client: %w", err)
		}

		vault.client = client
	}

	if vault.Token != "" {
		vault.client.SetToken(vault.Token)
	}

	return nil
}

// ensureToken guarantees that the client token is populated using either the
// configured value or the stored credentials file.
func (vault *Vault) ensureToken(log logger.Logger) error {
	if vault.Token != "" {
		return nil
	}

	_, err := vault.LoadTokenFromCredentials()
	if err != nil {
		return err
	}

	log.Debug("loaded vault token from credentials file")

	return nil
}

func (vault *Vault) loadCredentialsFile() (vaultPkg.VaultCreds, error) {
	if vault.credentialsPath == "" {
		vault.updateHomeDirs()
	}

	bytes, err := safeReadFile(vault.credentialsPath)
	if err != nil {
		return vaultPkg.VaultCreds{}, err
	}

	creds := vaultPkg.VaultCreds{}

	err = json.Unmarshal(bytes, &creds)
	if err != nil {
		return vaultPkg.VaultCreds{}, fmt.Errorf("failed to unmarshal vault credentials: %w", err)
	}

	return creds, nil
}

// storeCredentials stores vault credentials to the file system.
func (vault *Vault) storeCredentials(creds vaultPkg.VaultCreds) error {
	if vault.credentialsPath == "" {
		vault.updateHomeDirs()
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	err = os.WriteFile(vault.credentialsPath, data, vaultPkg.CredentialsFilePermissions)
	if err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}

	return nil
}

// updateHomeDirs sets up credential paths based on environment.
func (vault *Vault) updateHomeDirs() {
	home := os.Getenv("BLACKSMITH_OPER_HOME")
	if home == "" {
		home = "/var/vcap/store/blacksmith"
	}

	vault.credentialsPath = home + "/.vault-creds"
}

// Helper functions

// convertToStruct converts a map to a struct using JSON marshaling/unmarshaling.
func convertToStruct(data map[string]interface{}, out interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	err = json.Unmarshal(jsonData, out)
	if err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return nil
}

// convertToMap converts various data types to map[string]interface{}.
func convertToMap(data interface{}) (map[string]interface{}, error) {
	if m, ok := data.(map[string]interface{}); ok {
		return m, nil
	}

	// Convert via JSON marshaling/unmarshaling
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data: %w", err)
	}

	var result map[string]interface{}

	err = json.Unmarshal(jsonData, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}

	return result, nil
}
