package common

import (
	"context"
	"time"
)

// ServiceTester defines the interface for testing service instances.
type ServiceTester interface {
	TestConnection(ctx context.Context, creds Credentials, opts ConnectionOptions) (*TestResult, error)
	GetCapabilities() []Capability
	Close() error
}

// Credentials represents service credentials from Vault.
type Credentials map[string]interface{}

// GetString retrieves a string value from credentials.
func (c Credentials) GetString(key string) string {
	if val, ok := c[key].(string); ok {
		return val
	}

	return ""
}

// GetInt retrieves an int value from credentials.
func (c Credentials) GetInt(key string) int {
	if val, ok := c[key].(int); ok {
		return val
	}

	if val, ok := c[key].(float64); ok {
		return int(val)
	}

	return 0
}

// GetBool retrieves a bool value from credentials.
func (c Credentials) GetBool(key string) bool {
	if val, ok := c[key].(bool); ok {
		return val
	}

	return false
}

// GetMap retrieves a map value from credentials.
func (c Credentials) GetMap(key string) map[string]interface{} {
	if val, ok := c[key].(map[string]interface{}); ok {
		return val
	}

	return nil
}

// ConnectionOptions contains options for service connections.
type ConnectionOptions struct {
	UseTLS           bool          `json:"use_tls"`
	UseAMQPS         bool          `json:"use_amqps"`
	Timeout          time.Duration `json:"timeout"`
	MaxRetries       int           `json:"max_retries"`
	OverrideUser     string        `json:"override_user,omitempty"`     // For RabbitMQ user selection
	OverridePassword string        `json:"override_password,omitempty"` // For RabbitMQ user password
	OverrideVHost    string        `json:"override_vhost,omitempty"`    // For RabbitMQ vhost selection
}

// TestResult represents the result of a service test operation.
type TestResult struct {
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp int64                  `json:"timestamp"`
	Duration  time.Duration          `json:"duration"`
}

// Capability represents a testing capability of a service.
type Capability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// OperationResult represents the result of a service operation.
type OperationResult struct {
	Success   bool                   `json:"success"`
	Error     string                 `json:"error,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp int64                  `json:"timestamp"`
	Duration  time.Duration          `json:"duration"`
}
