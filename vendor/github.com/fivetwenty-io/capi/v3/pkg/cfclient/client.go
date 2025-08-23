// Package cfclient provides the main entry point for creating Cloud Foundry API clients
package cfclient

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/client"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// New creates a new Cloud Foundry API client with automatic UAA discovery
func New(config *capi.Config) (capi.Client, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if config.APIEndpoint == "" {
		return nil, fmt.Errorf("API endpoint is required")
	}

	// Normalize API endpoint
	apiEndpoint := strings.TrimSuffix(config.APIEndpoint, "/")
	if !strings.HasPrefix(apiEndpoint, "http://") && !strings.HasPrefix(apiEndpoint, "https://") {
		apiEndpoint = "https://" + apiEndpoint
	}
	config.APIEndpoint = apiEndpoint

	// If we need authentication and don't have a token URL, discover the UAA endpoint
	if needsAuth(config) && config.TokenURL == "" {
		uaaURL, err := discoverUAAEndpoint(apiEndpoint, config.SkipTLSVerify)
		if err != nil {
			return nil, fmt.Errorf("discovering UAA endpoint: %w", err)
		}

		// Set the token URL for OAuth2
		config.TokenURL = strings.TrimSuffix(uaaURL, "/") + "/oauth/token"
	}

	// Enable fetching API links on init for better log support
	config.FetchAPILinksOnInit = true

	// Use the internal client implementation
	return client.New(config)
}

// needsAuth checks if the config requires authentication
func needsAuth(config *capi.Config) bool {
	return config.AccessToken == "" &&
		(config.Username != "" || config.ClientID != "" || config.RefreshToken != "")
}

// isDevelopmentEnvironment checks if we're in a development environment
func isDevelopmentEnvironment() bool {
	devMode := os.Getenv("CAPI_DEV_MODE")
	return devMode == "true" || devMode == "1"
}

// discoverUAAEndpoint discovers the UAA endpoint from the CF API root
func discoverUAAEndpoint(apiEndpoint string, skipTLS bool) (string, error) {
	// Create a simple HTTP client for discovery
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}
	if skipTLS {
		// Only allow insecure TLS in explicit development environments
		if isDevelopmentEnvironment() {
			httpClient.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- Protected by development environment check above
			}
		} else {
			return "", fmt.Errorf("skipTLS is only allowed in development environments (set CAPI_DEV_MODE=true)")
		}
	}

	// Get the root info
	resp, err := httpClient.Get(apiEndpoint + "/")
	if err != nil {
		return "", fmt.Errorf("getting root info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("root info request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var rootInfo struct {
		Links struct {
			UAA struct {
				Href string `json:"href"`
			} `json:"uaa"`
			Login struct {
				Href string `json:"href"`
			} `json:"login"`
		} `json:"links"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rootInfo); err != nil {
		return "", fmt.Errorf("parsing root info: %w", err)
	}

	// Prefer UAA URL, fall back to login URL
	uaaURL := rootInfo.Links.UAA.Href
	if uaaURL == "" {
		uaaURL = rootInfo.Links.Login.Href
	}

	if uaaURL == "" {
		return "", fmt.Errorf("no UAA or login URL found in API root response")
	}

	return uaaURL, nil
}

// NewWithEndpoint creates a new client with just an API endpoint (no auth)
func NewWithEndpoint(endpoint string) (capi.Client, error) {
	return New(&capi.Config{
		APIEndpoint: endpoint,
	})
}

// NewWithToken creates a new client with an API endpoint and access token
func NewWithToken(endpoint, token string) (capi.Client, error) {
	return New(&capi.Config{
		APIEndpoint: endpoint,
		AccessToken: token,
	})
}

// NewWithClientCredentials creates a new client using OAuth2 client credentials
func NewWithClientCredentials(endpoint, clientID, clientSecret string) (capi.Client, error) {
	return New(&capi.Config{
		APIEndpoint:  endpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
}

// NewWithPassword creates a new client using username/password authentication
func NewWithPassword(endpoint, username, password string) (capi.Client, error) {
	return New(&capi.Config{
		APIEndpoint: endpoint,
		Username:    username,
		Password:    password,
	})
}
