package services

import (
	"blacksmith/pkg/services/cf"
	"blacksmith/pkg/services/common"
	"blacksmith/pkg/services/rabbitmq"
	"blacksmith/pkg/services/redis"
	"fmt"
	"strings"
)

// Manager manages all service testing handlers.
type Manager struct {
	Redis    *redis.Handler
	RabbitMQ *rabbitmq.Handler
	CF       *cf.Handler
	Security *SecurityMiddleware
	logger   func(string, ...interface{})
}

// NewManager creates a new service testing manager.
func NewManager(logger func(string, ...interface{})) *Manager {
	if logger == nil {
		logger = func(string, ...interface{}) {} // No-op logger
	}

	return &Manager{
		Redis:    redis.NewHandler(logger),
		RabbitMQ: rabbitmq.NewHandler(logger),
		CF:       cf.NewHandler("", "", "", logger), // Will be configured later
		Security: NewSecurityMiddleware(logger),
		logger:   logger,
	}
}

// NewManagerWithCFConfig creates a new service testing manager with CF broker configuration.
func NewManagerWithCFConfig(logger func(string, ...interface{}), brokerURL, brokerUser, brokerPass string) *Manager {
	if logger == nil {
		logger = func(string, ...interface{}) {} // No-op logger
	}

	return &Manager{
		Redis:    redis.NewHandler(logger),
		RabbitMQ: rabbitmq.NewHandler(logger),
		CF:       cf.NewHandler(brokerURL, brokerUser, brokerPass, logger),
		Security: NewSecurityMiddleware(logger),
		logger:   logger,
	}
}

// Close closes all service handlers.
func (m *Manager) Close() error {
	if m.Redis != nil {
		err := m.Redis.Close()
		if err != nil {
			return fmt.Errorf("failed to close Redis handler: %w", err)
		}
	}

	if m.RabbitMQ != nil {
		err := m.RabbitMQ.Close()
		if err != nil {
			return fmt.Errorf("failed to close RabbitMQ handler: %w", err)
		}
	}
	// CF handler doesn't need explicit closing
	return nil
}

// otherServiceNames are the names of services that are known not to be
// Redis, so a credentials name that holds one rules the instance out.
var otherServiceNames = []string{
	"rabbit", "amqp", "postgres", "mysql", "mariadb", "mongo", "kafka", "nats", "elasticsearch",
}

// IsRedisInstance checks if the credentials indicate a Redis service.
// Valkey belongs to the Redis family. A service the credentials name in
// service_name or service_type decides on its own when it names Redis,
// Valkey, or a known other service. A name that matches none of these may
// only be the instance name the operator chose, so the URI scheme, the
// port, and the password shape guess as they do for unnamed instances.
func IsRedisInstance(creds common.Credentials) bool {
	named := false

	for _, key := range []string{"service_name", "service_type"} {
		name := strings.ToLower(strings.TrimSpace(creds.GetString(key)))
		if name == "" {
			continue
		}

		if strings.Contains(name, "valkey") || strings.Contains(name, "redis") {
			return true
		}

		for _, other := range otherServiceNames {
			if strings.Contains(name, other) {
				named = true

				break
			}
		}
	}

	if named {
		return false
	}

	// Check for Redis-specific fields
	if uri := creds.GetString("uri"); uri != "" {
		scheme, _, _ := strings.Cut(uri, "://")

		switch strings.ToLower(scheme) {
		case "redis", "rediss", "valkey", "valkeys":
			return true
		default:
			return false
		}
	}

	// Check for typical Redis port
	port := creds.GetInt("port")
	if port == 6379 || port == 6380 {
		return true
	}

	// Check for Redis-specific fields
	if creds.GetString("password") != "" && (creds.GetString("host") != "" || creds.GetString("hostname") != "") {
		// Could be Redis if it has host and password but no username (typical for Redis)
		return creds.GetString("username") == ""
	}

	return false
}

// IsRabbitMQInstance checks if the credentials indicate a RabbitMQ service.
func IsRabbitMQInstance(creds common.Credentials) bool {
	// Check for AMQP URI
	if creds.GetString("uri") != "" {
		uri := creds.GetString("uri")

		return len(uri) > 7 && (uri[:7] == "amqp://" || uri[:8] == "amqps://")
	}

	// Check for RabbitMQ management API or AMQP protocols
	if protocols := creds.GetMap("protocols"); protocols != nil {
		if _, ok := protocols["management"]; ok {
			return true
		}

		if _, ok := protocols["amqp"]; ok {
			return true
		}

		if _, ok := protocols["amqps"]; ok {
			return true
		}
	}

	// Check for typical RabbitMQ ports
	port := creds.GetInt("port")
	if port == 5672 || port == 5671 {
		return true
	}

	// Check for RabbitMQ-specific fields
	if creds.GetString("vhost") != "" && creds.GetString("username") != "" {
		return true
	}

	return false
}
