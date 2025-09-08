package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Static errors for err113 compliance.
var (
	ErrManagementAPIURLNotAvailable = errors.New("management API URL not available")
	ErrManagementAPIError           = errors.New("management API returned error status")
)

// ManagementClient handles RabbitMQ Management API requests.
type ManagementClient struct {
	client *http.Client
}

// NewManagementClient creates a new management API client.
func NewManagementClient() *ManagementClient {
	return &ManagementClient{
		client: &http.Client{
			Timeout: HTTPTimeout,
		},
	}
}

// GetQueues retrieves queue information via Management API.
func (mc *ManagementClient) GetQueues(ctx context.Context, creds *Credentials, useSSL bool) ([]Queue, error) {
	baseURL := mc.getManagementURL(creds, useSSL)
	if baseURL == "" {
		return nil, ErrManagementAPIURLNotAvailable
	}

	vhost := creds.VHost
	if vhost == "" {
		vhost = "/"
	}

	// URL encode the vhost
	encodedVHost := url.QueryEscape(vhost)
	apiURL := fmt.Sprintf("%s/b/queues/%s", baseURL, encodedVHost)

	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use management credentials if available, otherwise fallback to AMQP credentials
	mgmtUser, mgmtPass := mc.getManagementCredentials(creds)
	req.SetBasicAuth(mgmtUser, mgmtPass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := mc.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrManagementAPIError, resp.StatusCode)
	}

	var apiQueues []map[string]interface{}

	err = json.NewDecoder(resp.Body).Decode(&apiQueues)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JSON response: %w", err)
	}

	// Convert API response to our Queue structs
	queues := make([]Queue, 0, len(apiQueues))
	for _, queueData := range apiQueues {
		queue := Queue{
			Name:      getString(queueData, "name"),
			Messages:  getInt(queueData, "messages"),
			Consumers: getInt(queueData, "consumers"),
			VHost:     getString(queueData, "vhost"),
			Durable:   getBool(queueData, "durable"),
			State:     getString(queueData, "state"),
		}
		queues = append(queues, queue)
	}

	return queues, nil
}

// Request makes a generic request to the Management API.
func (mc *ManagementClient) Request(ctx context.Context, creds *Credentials, method, path string, useSSL bool) (int, interface{}, error) {
	baseURL := mc.getManagementURL(creds, useSSL)
	if baseURL == "" {
		return 0, nil, ErrManagementAPIURLNotAvailable
	}

	apiURL := fmt.Sprintf("%s%s", baseURL, path)

	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, apiURL, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Use management credentials if available, otherwise fallback to AMQP credentials
	mgmtUser, mgmtPass := mc.getManagementCredentials(creds)
	req.SetBasicAuth(mgmtUser, mgmtPass)
	req.Header.Set("Content-Type", "application/json")

	resp, err := mc.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	var data interface{}
	if resp.Header.Get("Content-Type") == "application/json" {
		err := json.NewDecoder(resp.Body).Decode(&data)
		if err != nil {
			return resp.StatusCode, nil, fmt.Errorf("failed to decode JSON response: %w", err)
		}
	}

	return resp.StatusCode, data, nil
}

// getManagementURL extracts the management API URL from credentials.
func (mc *ManagementClient) getManagementURL(creds *Credentials, useSSL bool) string {
	// Check protocols map first
	if creds.Protocols != nil {
		if mgmt, ok := creds.Protocols["management"]; ok {
			if useSSL {
				if sslURI, ok := mgmt["ssl_uri"].(string); ok && sslURI != "" {
					return sslURI
				}
			}

			if uri, ok := mgmt["uri"].(string); ok && uri != "" {
				return uri
			}
		}
	}

	// Fallback: construct URL from host and default ports
	protocol := "http"
	port := 15672

	if useSSL {
		protocol = "https"
		port = 15671
	}

	return fmt.Sprintf("%s://%s:%d", protocol, creds.Host, port)
}

// getManagementCredentials returns the appropriate credentials for management API.
func (mc *ManagementClient) getManagementCredentials(creds *Credentials) (string, string) {
	// First, check if management credentials are explicitly provided
	if creds.ManagementUsername != "" && creds.ManagementPassword != "" {
		return creds.ManagementUsername, creds.ManagementPassword
	}

	// Second, check protocols map for management credentials
	if creds.Protocols != nil {
		if mgmt, ok := creds.Protocols["management"]; ok {
			if username, ok := mgmt["username"].(string); ok && username != "" {
				if password, ok := mgmt["password"].(string); ok && password != "" {
					return username, password
				}
			}
		}
	}

	// Fallback to AMQP credentials
	return creds.Username, creds.Password
}

// Helper functions to safely extract values from interface{} maps.
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}

	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if val, ok := m[key].(float64); ok {
		return int(val)
	}

	if val, ok := m[key].(int); ok {
		return val
	}

	return 0
}

func getBool(m map[string]interface{}, key string) bool {
	if val, ok := m[key].(bool); ok {
		return val
	}

	return false
}
