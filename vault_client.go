package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
)

// VaultClient wraps the HashiCorp Vault API client
type VaultClient struct {
	*api.Client
	URL       string
	Token     string
	Insecure  bool
	KVVersion string // "1" or "2" - defaults to "2"
}

// NewVaultClient creates a new vault client using the HashiCorp API
func NewVaultClient(url, token string, insecure bool) (*VaultClient, error) {
	l := Logger.Wrap("vault client init")
	l.Debug("creating new vault client for %s", url)

	if insecure {
		Info("vault client configured with InsecureSkipVerify=true - TLS certificate verification will be bypassed")
	}

	config := api.DefaultConfig()
	config.Address = url

	// Configure HTTP client with secure TLS and redirect handling
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecure,         // #nosec G402 - This is configurable by user for development/testing
		MinVersion:         tls.VersionTLS12, // Enforce TLS 1.2 minimum
	}

	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			// Preserve token on redirects
			req.Header.Set("X-Vault-Token", token)
			return nil
		},
	}
	config.HttpClient = httpClient

	client, err := api.NewClient(config)
	if err != nil {
		l.Error("failed to create vault client: %s", err)
		return nil, err
	}

	// Set the token
	client.SetToken(token)

	l.Debug("vault client created successfully")
	return &VaultClient{
		Client:    client,
		URL:       url,
		Token:     token,
		Insecure:  insecure,
		KVVersion: "2", // Default to KV v2, will be updated by VerifyMount
	}, nil
}

// InitVault initializes the vault with the specified key shares
func (vc *VaultClient) InitVault(shares, threshold int) (*api.InitResponse, error) {
	l := Logger.Wrap("vault init")

	// Check if already initialized
	l.Debug("checking initialization state of the vault")
	initStatus, err := vc.Sys().InitStatus()
	if err != nil {
		l.Error("failed to check initialization state of the vault: %s", err)
		return nil, err
	}

	if initStatus {
		l.Info("vault is already initialized")
		return nil, nil
	}

	// Initialize the vault
	l.Info("initializing the vault with %d/%d keys", threshold, shares)
	initReq := &api.InitRequest{
		SecretShares:    shares,
		SecretThreshold: threshold,
	}

	initResp, err := vc.Sys().Init(initReq)
	if err != nil {
		l.Error("failed to initialize the vault: %s", err)
		return nil, err
	}

	l.Debug("vault initialized successfully")
	return initResp, nil
}

// UnsealVault unseals the vault with the provided key
func (vc *VaultClient) UnsealVault(key string) error {
	l := Logger.Wrap("vault unseal")

	// Check current seal status
	l.Debug("checking current seal status of the vault")
	sealStatus, err := vc.Sys().SealStatus()
	if err != nil {
		l.Error("failed to check current seal status of the vault: %s", err)
		return err
	}

	if !sealStatus.Sealed {
		l.Info("vault is already unsealed")
		return nil
	}

	// Unseal the vault
	l.Info("vault is sealed; unsealing it")
	unsealResp, err := vc.Sys().Unseal(key)
	if err != nil {
		l.Error("failed to unseal vault: %s", err)
		return err
	}

	if unsealResp.Sealed {
		err = fmt.Errorf("vault is still sealed after unseal attempt")
		l.Error("%s", err)
		return err
	}

	l.Info("unsealed the vault")
	return nil
}

// VerifyMount checks if a mount exists and optionally creates it
func (vc *VaultClient) VerifyMount(mount string, createIfMissing bool) error {
	l := Logger.Wrap("Verify Mount")
	l.Debug("checking vault has the secret mount created")

	mounts, err := vc.Sys().ListMounts()
	if err != nil {
		l.Error("failed to list mounts: %s", err)
		return err
	}

	// Normalize mount path - always add trailing slash for comparison
	mountPath := strings.Trim(mount, "/") + "/"

	// Debug: log all mounts
	l.Debug("Current mounts:")
	for path, mountInfo := range mounts {
		l.Debug("  - %s: type=%s, version=%v", path, mountInfo.Type, mountInfo.Options["version"])
	}

	// Check if mount exists
	if mountInfo, exists := mounts[mountPath]; exists {
		// Verify it's a KV mount
		if mountInfo.Type == "kv" || mountInfo.Type == "generic" {
			version := "1"
			if v, ok := mountInfo.Options["version"]; ok {
				version = v
			}
			l.Debug("Found KV mount at %s (version %s)", mount, version)

			// Check if we need to upgrade from KV v1 to KV v2
			if version == "1" {
				l.Info("Detected KV v1 mount at %s, upgrading to KV v2", mount)

				// Create the tune configuration for upgrading to KV v2
				tuneConfig := api.MountConfigInput{
					Options: map[string]string{
						"version": "2",
					},
				}

				// Perform the upgrade
				err = vc.Sys().TuneMount(mount, tuneConfig)
				if err != nil {
					l.Error("Failed to upgrade mount %s from KV v1 to KV v2: %s", mount, err)
					// Don't fail entirely - KV v1 can still work
					l.Info("WARNING: Continuing with KV v1 mount at %s", mount)
					vc.KVVersion = "1"
					return nil
				}

				l.Info("Successfully upgraded mount %s from KV v1 to KV v2", mount)
				vc.KVVersion = "2"

				// Verify the upgrade
				mounts, err = vc.Sys().ListMounts()
				if err != nil {
					l.Info("WARNING: Failed to verify mount upgrade: %s", err)
				} else if mountInfo, exists := mounts[mountPath]; exists {
					if v, ok := mountInfo.Options["version"]; ok && v == "2" {
						l.Debug("Verified mount %s is now KV v2", mount)
					} else {
						l.Info("WARNING: Mount %s upgrade verification failed, version is %v", mount, mountInfo.Options["version"])
					}
				}
			} else if version == "2" {
				l.Debug("Mount %s is already KV v2", mount)
				vc.KVVersion = "2"
			} else {
				l.Info("WARNING: Mount %s has unexpected KV version %s", mount, version)
				vc.KVVersion = version
			}
		} else {
			l.Info("WARNING: Mount %s exists but is type %s, not KV", mount, mountInfo.Type)
		}
		return nil
	}

	if !createIfMissing {
		err = fmt.Errorf("secret mount %s is missing", mount)
		l.Error("%s", err)
		return err
	}

	// Create the mount as KV v2 directly
	l.Info("creating KV v2 mount at %s", mount)
	mountInput := &api.MountInput{
		Type:        "kv",
		Description: "KV v2 secrets engine for Blacksmith",
		Options: map[string]string{
			"version": "2",
		},
	}

	err = vc.Sys().Mount(mount, mountInput)
	if err != nil {
		// Check if the error is because it already exists (race condition)
		if strings.Contains(err.Error(), "path is already in use") {
			l.Info("mount %s already exists (created elsewhere)", mount)
			return nil
		}
		l.Error("failed to create mount %s: %s", mount, err)
		return err
	}

	l.Info("mount %s created successfully as KV v2", mount)
	vc.KVVersion = "2"

	// Verify the mount was created
	mounts, err = vc.Sys().ListMounts()
	if err != nil {
		l.Info("WARNING: failed to verify mount creation: %s", err)
	} else if _, exists := mounts[mountPath]; !exists {
		l.Error("mount %s was created but not found in list", mount)
	}

	return nil
}

// GetSecret reads a secret from vault
func (vc *VaultClient) GetSecret(path string) (map[string]interface{}, bool, error) {
	l := Logger.Wrap("vault get")

	var fullPath string
	if vc.KVVersion == "2" {
		fullPath = "secret/data/" + path
		l.Debug("reading secret at %s (KV v2)", fullPath)
	} else {
		fullPath = "secret/" + path
		l.Debug("reading secret at %s (KV v1)", fullPath)
	}

	secret, err := vc.Logical().Read(fullPath)
	if err != nil {
		l.Error("failed to read secret at %s: %s", fullPath, err)
		return nil, false, err
	}

	if secret == nil || secret.Data == nil {
		l.Debug("secret not found at %s", fullPath)
		return nil, false, nil
	}

	// For KV v2, the actual data is nested under "data" key
	if vc.KVVersion == "2" {
		if data, ok := secret.Data["data"].(map[string]interface{}); ok {
			var keys []string
			for key := range data {
				keys = append(keys, key)
			}
			l.Debug("secret found at %s with keys: %v", fullPath, keys)
			return data, true, nil
		}
		l.Debug("secret found but no data field at %s", fullPath)
		return nil, false, nil
	}

	var keys []string
	for key := range secret.Data {
		keys = append(keys, key)
	}
	l.Debug("secret found at %s with keys: %v", fullPath, keys)
	return secret.Data, true, nil
}

// PutSecret writes a secret to vault
func (vc *VaultClient) PutSecret(path string, data map[string]interface{}) error {
	l := Logger.Wrap("vault put")

	var keys []string
	for key := range data {
		keys = append(keys, key)
	}

	var fullPath string
	var writeData map[string]interface{}

	if vc.KVVersion == "2" {
		fullPath = "secret/data/" + path
		// For KV v2, wrap the data in a "data" field
		writeData = map[string]interface{}{
			"data": data,
		}
		l.Debug("writing secret to %s (KV v2) with keys: %v", fullPath, keys)
	} else {
		fullPath = "secret/" + path
		writeData = data
		l.Debug("writing secret to %s (KV v1) with keys: %v", fullPath, keys)
	}

	_, err := vc.Logical().Write(fullPath, writeData)
	if err != nil {
		l.Error("failed to write secret to %s: %s", fullPath, err)
		return err
	}

	l.Debug("secret written successfully to %s with keys: %v", path, keys)
	return nil
}

// DeleteSecret deletes a secret from vault
func (vc *VaultClient) DeleteSecret(path string) error {
	l := Logger.Wrap("vault delete")

	var fullPath string
	if vc.KVVersion == "2" {
		// For KV v2, delete from data path
		fullPath = "secret/data/" + path
		l.Debug("deleting secret at %s (KV v2)", fullPath)
	} else {
		fullPath = "secret/" + path
		l.Debug("deleting secret at %s (KV v1)", fullPath)
	}

	_, err := vc.Logical().Delete(fullPath)
	if err != nil {
		// Don't error on 404s
		if !strings.Contains(err.Error(), "404") {
			l.Error("failed to delete secret at %s: %s", fullPath, err)
			return err
		}
		l.Debug("secret not found at %s (already deleted)", fullPath)
	} else {
		l.Debug("secret deleted at %s", fullPath)
	}

	return nil
}

// ListSecrets lists secrets at a given path
func (vc *VaultClient) ListSecrets(path string) ([]string, error) {
	l := Logger.Wrap("vault list")

	var fullPath string
	if vc.KVVersion == "2" {
		// For KV v2, list from metadata path
		fullPath = "secret/metadata/" + path
		l.Debug("listing secrets at %s (KV v2)", fullPath)
	} else {
		fullPath = "secret/" + path
		l.Debug("listing secrets at %s (KV v1)", fullPath)
	}

	secret, err := vc.Logical().List(fullPath)
	if err != nil {
		// Don't error on 404s - just return empty list
		if strings.Contains(err.Error(), "404") {
			l.Debug("no secrets found at %s", path)
			return []string{}, nil
		}
		l.Error("failed to list secrets at %s: %s", fullPath, err)
		return nil, err
	}

	if secret == nil || secret.Data == nil {
		l.Debug("no secrets found at %s", path)
		return []string{}, nil
	}

	keys, ok := secret.Data["keys"].([]interface{})
	if !ok {
		l.Debug("no keys found in list response at %s", path)
		return []string{}, nil
	}

	result := make([]string, 0, len(keys))
	for _, k := range keys {
		if s, ok := k.(string); ok {
			result = append(result, s)
		}
	}

	l.Debug("found %d secrets at %s", len(result), path)
	return result, nil
}

// convertToMap converts an interface{} to map[string]interface{}
func convertToMap(data interface{}) (map[string]interface{}, error) {
	// If it's already a map[string]interface{}, return it
	if m, ok := data.(map[string]interface{}); ok {
		return m, nil
	}

	// If it's a map[interface{}]interface{} (common with YAML parsing), use deinterface
	if m, ok := data.(map[interface{}]interface{}); ok {
		return deinterfaceMap(m), nil
	}

	// Use deinterface to handle any nested map[interface{}]interface{} structures
	deinterfaced := deinterface(data)

	// Try to convert the result to map[string]interface{}
	if m, ok := deinterfaced.(map[string]interface{}); ok {
		return m, nil
	}

	// Otherwise, marshal and unmarshal through JSON as fallback
	jsonData, err := json.Marshal(deinterfaced)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data: %s", err)
	}

	var result map[string]interface{}
	err = json.Unmarshal(jsonData, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %s", err)
	}

	return result, nil
}
