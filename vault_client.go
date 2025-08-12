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
	URL      string
	Token    string
	Insecure bool
}

// NewVaultClient creates a new vault client using the HashiCorp API
func NewVaultClient(url, token string, insecure bool) (*VaultClient, error) {
	l := Logger.Wrap("vault client init")
	l.Debug("creating new vault client for %s", url)

	config := api.DefaultConfig()
	config.Address = url

	// Configure HTTP client with custom TLS and redirect handling
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecure,
			},
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
		Client:   client,
		URL:      url,
		Token:    token,
		Insecure: insecure,
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

	// Normalize mount path
	mountPath := strings.Trim(mount, "/") + "/"

	// Check if mount exists
	if _, exists := mounts[mountPath]; exists {
		l.Debug("Found secret mount %s", mount)
		return nil
	}

	if !createIfMissing {
		err = fmt.Errorf("Secret mount %s is missing", mount)
		l.Error("%s", err)
		return err
	}

	// Create the mount
	l.Info("creating mount %s", mount)
	mountInput := &api.MountInput{
		Type:        "kv",
		Description: fmt.Sprintf("A KV v1 Mount created by safe"),
		Options: map[string]string{
			"version": "1",
		},
	}

	err = vc.Sys().Mount(mount, mountInput)
	if err != nil {
		l.Error("failed to create mount %s: %s", mount, err)
		return err
	}

	l.Info("mount %s created successfully", mount)
	return nil
}

// GetSecret reads a secret from vault
func (vc *VaultClient) GetSecret(path string) (map[string]interface{}, bool, error) {
	l := Logger.Wrap("vault get")
	l.Debug("reading secret at secret/%s", path)

	secret, err := vc.Logical().Read("secret/" + path)
	if err != nil {
		l.Error("failed to read secret at %s: %s", path, err)
		return nil, false, err
	}

	if secret == nil || secret.Data == nil {
		l.Debug("secret not found at %s", path)
		return nil, false, nil
	}

	l.Debug("secret found at %s", path)
	return secret.Data, true, nil
}

// PutSecret writes a secret to vault
func (vc *VaultClient) PutSecret(path string, data map[string]interface{}) error {
	l := Logger.Wrap("vault put")
	l.Debug("writing secret to secret/%s", path)

	_, err := vc.Logical().Write("secret/"+path, data)
	if err != nil {
		l.Error("failed to write secret to %s: %s", path, err)
		return err
	}

	l.Debug("secret written successfully to %s", path)
	return nil
}

// DeleteSecret deletes a secret from vault
func (vc *VaultClient) DeleteSecret(path string) error {
	l := Logger.Wrap("vault delete")
	l.Debug("deleting secret at secret/%s", path)

	_, err := vc.Logical().Delete("secret/" + path)
	if err != nil {
		// Don't error on 404s
		if !strings.Contains(err.Error(), "404") {
			l.Error("failed to delete secret at %s: %s", path, err)
			return err
		}
		l.Debug("secret not found at %s (already deleted)", path)
	} else {
		l.Debug("secret deleted at %s", path)
	}

	return nil
}

// ListSecrets lists secrets at a given path
func (vc *VaultClient) ListSecrets(path string) ([]string, error) {
	l := Logger.Wrap("vault list")
	l.Debug("listing secrets at secret/%s", path)

	secret, err := vc.Logical().List("secret/" + path)
	if err != nil {
		// Don't error on 404s - just return empty list
		if strings.Contains(err.Error(), "404") {
			l.Debug("no secrets found at %s", path)
			return []string{}, nil
		}
		l.Error("failed to list secrets at %s: %s", path, err)
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
	// If it's already a map, return it
	if m, ok := data.(map[string]interface{}); ok {
		return m, nil
	}

	// Otherwise, marshal and unmarshal through JSON
	jsonData, err := json.Marshal(data)
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
