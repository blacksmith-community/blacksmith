package vault

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"
	api "github.com/hashicorp/vault/api"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft"
)

// GlobalReader is the module-level vault reader, initialized lazily from the
// environment. Evaluation code reaches it through DefaultReader, which
// serializes the first initialization; tests may swap it directly while no
// evaluation is running.
var GlobalReader Reader

// globalReaderMu guards the lazy initialization of GlobalReader.
var globalReaderMu sync.Mutex

// DefaultReader returns GlobalReader, initializing it from the environment
// on first use. Concurrent callers share one initialization, and a failed
// initialization is retried by the next call.
func DefaultReader() (Reader, error) {
	globalReaderMu.Lock()
	defer globalReaderMu.Unlock()

	if GlobalReader == nil {
		if err := InitializeClient(); err != nil {
			return nil, err
		}
	}

	return GlobalReader, nil
}

// ClientPool manages vault readers for different targets. clients/configs
// are read and lazily populated from concurrent operator evaluation (see
// pkg/graft/evaluator_parallel.go), so all access goes through mu.
type ClientPool struct {
	mu      sync.RWMutex
	clients map[string]Reader
	configs map[string]*Target
}

// DefaultPool is the global client pool for target-aware vault readers.
var DefaultPool = &ClientPool{
	clients: make(map[string]Reader),
	configs: make(map[string]*Target),
}

// GetClient returns a vault reader for the specified target.
func (vcp *ClientPool) GetClient(targetName string, engine graft.Engine) (Reader, error) {
	// Return existing client if available
	vcp.mu.RLock()
	if client, exists := vcp.clients[targetName]; exists {
		vcp.mu.RUnlock()
		return client, nil
	}
	vcp.mu.RUnlock()

	// Get target configuration
	config, err := vcp.getTargetConfig(targetName, engine)
	if err != nil {
		return nil, fmt.Errorf("vault target '%s' not found: %w", targetName, err)
	}

	// Create new client
	client, err := CreateClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client for target '%s': %w", targetName, err)
	}

	// Store client for reuse. Re-check under the write lock in case another
	// concurrent evaluation goroutine created and stored a client for the
	// same target while this one was building its own (harmless duplicate
	// work); keep whichever was stored first so all callers converge on one
	// client per target.
	vcp.mu.Lock()
	if existing, exists := vcp.clients[targetName]; exists {
		vcp.mu.Unlock()
		return existing, nil
	}
	vcp.clients[targetName] = client
	vcp.configs[targetName] = config
	vcp.mu.Unlock()

	return client, nil
}

// getTargetConfig retrieves target configuration from the engine or environment.
//
//nolint:unparam // engine reserved for future configuration retrieval from engine
func (vcp *ClientPool) getTargetConfig(targetName string, engine graft.Engine) (*Target, error) {
	// Check if we have cached config
	vcp.mu.RLock()
	if config, exists := vcp.configs[targetName]; exists {
		vcp.mu.RUnlock()
		return config, nil
	}
	vcp.mu.RUnlock()

	// For now, try environment variables with target suffix
	// In a full implementation, this would query the engine's configuration
	envPrefix := fmt.Sprintf("VAULT_%s_", strings.ToUpper(targetName))

	config := &Target{
		URL:       os.Getenv(envPrefix + "ADDR"),
		Token:     os.Getenv(envPrefix + "TOKEN"),
		Namespace: os.Getenv(envPrefix + "NAMESPACE"),
	}

	// Check for skip verify
	if skipStr := os.Getenv(envPrefix + "SKIP_VERIFY"); skipStr == "true" || skipStr == "1" {
		config.SkipVerify = true
	}

	// If no environment variables found, return error
	if config.URL == "" || config.Token == "" {
		return nil, fmt.Errorf("vault target '%s' configuration not found (expected %sADDR and %sTOKEN environment variables)",
			targetName, envPrefix, envPrefix)
	}

	return config, nil
}

// newAPIClient creates a hashicorp/vault/api Client with the given parameters.
func newAPIClient(addr string, token string, namespace string, skip bool) (*api.Client, error) {
	parsedURL, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("could not parse Vault URL `%s': %w", addr, err)
	}

	// Port handling
	if parsedURL.Port() == "" {
		if parsedURL.Scheme == "http" {
			parsedURL.Host += ":80"
		} else {
			parsedURL.Host += ":443"
		}
	}

	// TLS configuration
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve system root certificate authorities: %w", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{
				RootCAs:            roots,
				InsecureSkipVerify: skip, // #nosec G402 - controlled by user configuration
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			req.Header.Add("X-Vault-Token", token)
			req.Header.Add("X-Vault-Namespace", namespace)
			return nil
		},
	}

	config := &api.Config{
		Address:    parsedURL.String(),
		HttpClient: httpClient,
	}

	client, err := api.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Vault API client: %w", err)
	}

	client.SetToken(token)
	if namespace != "" {
		client.SetNamespace(namespace)
	}

	return client, nil
}

// CreateClientFromConfig creates a vault reader from target configuration.
func CreateClientFromConfig(config *Target) (Reader, error) {
	addr := os.ExpandEnv(config.URL)
	token := os.ExpandEnv(config.Token)
	namespace := os.ExpandEnv(config.Namespace)

	client, err := newAPIClient(addr, token, namespace, config.SkipVerify)
	if err != nil {
		return nil, err
	}

	return NewReader(client), nil
}

// InitializeClient initializes the global vault reader from environment variables.
func InitializeClient() error {
	addr := os.Getenv("VAULT_ADDR")
	token := os.Getenv("VAULT_TOKEN")
	namespace := os.Getenv("VAULT_NAMESPACE")
	skip := false

	if addr == "" || token == "" {
		svtoken := struct {
			Vault      string `yaml:"vault"`
			Token      string `yaml:"token"`
			Namespace  string `yaml:"namespace"`
			SkipVerify bool   `yaml:"skip_verify"`
		}{}
		b, err := os.ReadFile(os.ExpandEnv("${HOME}/.svtoken"))
		if err == nil {
			err = yaml.Unmarshal(b, &svtoken)
			if err == nil {
				addr = svtoken.Vault
				token = svtoken.Token
				namespace = svtoken.Namespace
				skip = svtoken.SkipVerify
			}
		}
	}

	if SkipVerify(os.Getenv("VAULT_SKIP_VERIFY")) {
		skip = true
	}

	if token == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			// The Vault CLI's own token sink; reading the invoking user's
			// home directory is the documented lookup, not tainted input.
			b, err := os.ReadFile(filepath.Join(home, ".vault-token")) // #nosec G304 -- fixed path under the user's home directory
			if err == nil {
				token = strings.TrimSuffix(string(b), "\n")
			}
		}
	}

	if addr == "" || token == "" {
		return fmt.Errorf("failed to determine Vault URL / token, and the $REDACT environment variable is not set")
	}

	client, err := newAPIClient(addr, token, namespace, skip)
	if err != nil {
		return err
	}

	GlobalReader = NewReader(client)

	return nil
}

// GetSecretWithReader retrieves a secret using the provided Reader.
func GetSecretWithReader(reader Reader, secret string) (map[string]interface{}, error) {
	graft.DEBUG("Fetching Vault secret at `%s'", secret)

	ret, err := reader.ReadSecret(context.Background(), secret)
	if err != nil {
		graft.DEBUG(" failure.")
		return nil, err
	}

	graft.DEBUG("  success.")
	return ret, nil
}

// ExtractSubkey extracts a named subkey from a vault secret map and returns it as a string.
func ExtractSubkey(secretMap map[string]interface{}, secret, subkey string) (string, error) {
	graft.DEBUG("  extracting the [%s] subkey from the secret", subkey)

	secretSubkeyPath := fmt.Sprintf("%s:%s", secret, subkey)
	v, ok := secretMap[subkey]
	if !ok {
		graft.DEBUG("    !! %s not found!\n", secretSubkeyPath)
		return "", ansi.Errorf("@R{secret} @c{%s} @R{not found}", secretSubkeyPath)
	}
	vStr, ok := v.(string)
	if !ok {
		graft.DEBUG("    !! %s is not a string!\n", secretSubkeyPath)
		return "", ansi.Errorf("@R{secret} @c{%s} @R{is not a string}", secretSubkeyPath)
	}
	graft.DEBUG(" success.")
	return vStr, nil
}

// ParsePath splits a vault path into the secret path and the key name.
// The key is the portion after the last colon.
func ParsePath(path string) (secret, key string) {
	secret = path
	if idx := strings.LastIndex(path, ":"); idx >= 0 {
		secret = path[:idx]
		key = path[idx+1:]
	}
	return
}
