package services_test

import (
	"testing"

	. "blacksmith/pkg/services"
	"blacksmith/pkg/services/common"
)

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestIsRedisInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		creds    common.Credentials
		expected bool
	}{
		{
			name: "Redis URI",
			creds: common.Credentials{
				"uri": "redis://:password@host:6379",
			},
			expected: true,
		},
		{
			name: "Redis TLS URI",
			creds: common.Credentials{
				"uri": "rediss://:password@host:6380",
			},
			expected: true,
		},
		{
			name: "Redis standard port",
			creds: common.Credentials{
				"host":     "redis.example.com",
				"port":     6379,
				"password": "secret",
			},
			expected: true,
		},
		{
			name: "Redis TLS port",
			creds: common.Credentials{
				"host":     "redis.example.com",
				"port":     6380,
				"password": "secret",
			},
			expected: true,
		},
		{
			name: "Redis without username",
			creds: common.Credentials{
				"host":     "redis.example.com",
				"password": "secret",
			},
			expected: true,
		},
		{
			name: "Not Redis - has username",
			creds: common.Credentials{
				"host":     "redis.example.com",
				"username": "admin",
				"password": "secret",
			},
			expected: false,
		},
		{
			name: "Not Redis - AMQP URI",
			creds: common.Credentials{
				"uri": "amqp://user:pass@host:5672",
			},
			expected: false,
		},
		{
			// The shape the valkey forge writes for a TLS-only instance.
			name: "Valkey named only in service_type, with username and no URI",
			creds: common.Credentials{
				"host":           "q-s0.standalone.valkey-service.example.bosh",
				"port":           0,
				"tls_port":       6379,
				"username":       "default",
				"password":       "secret",
				"admin_password": "secret",
				"ca_certificate": "-----BEGIN CERTIFICATE-----",
				"service_type":   "valkey",
			},
			expected: true,
		},
		{
			name: "Redis named in service_name, with username",
			creds: common.Credentials{
				"host":         "redis.example.com",
				"username":     "admin",
				"password":     "secret",
				"service_name": "Redis",
			},
			expected: true,
		},
		{
			name: "Named non-Redis service wins over the port guess",
			creds: common.Credentials{
				"host":         "generic.example.com",
				"port":         6379,
				"password":     "secret",
				"service_type": "postgresql",
			},
			expected: false,
		},
		{
			name: "Instance name that matches no service falls through to the guesses",
			creds: common.Credentials{
				"host":         "cache.example.com",
				"port":         6379,
				"password":     "secret",
				"service_name": "my-cache",
			},
			expected: true,
		},
		{
			name: "Postgres-named service is not Redis",
			creds: common.Credentials{
				"host":         "db.example.com",
				"port":         6379,
				"password":     "secret",
				"service_name": "postgres-orders",
			},
			expected: false,
		},
		{
			name: "Valkey service type is Redis family",
			creds: common.Credentials{
				"host":         "h",
				"tls_port":     6379,
				"service_type": "valkey",
			},
			expected: true,
		},
		{
			name: "Valkey URI",
			creds: common.Credentials{
				"uri": "valkey://default:password@host:6379",
			},
			expected: true,
		},
		{
			name: "Valkey TLS URI",
			creds: common.Credentials{
				"uri": "valkeys://default:password@host:6379",
			},
			expected: true,
		},
		{
			name: "Short Redis-like URI does not panic",
			creds: common.Credentials{
				"uri": "rediss:/",
			},
			expected: false,
		},
		{
			name: "Not Redis - RabbitMQ instance",
			creds: common.Credentials{
				"host":     "rabbitmq.example.com",
				"port":     5672,
				"username": "admin",
				"password": "secret",
				"vhost":    "/service-instance-123",
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := IsRedisInstance(test.creds)
			if result != test.expected {
				t.Errorf("IsRedisInstance() = %v, expected %v", result, test.expected)
			}
		})
	}
}

//nolint:funlen // This test function is intentionally long for comprehensive testing

//nolint:funlen
func TestIsRabbitMQInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		creds    common.Credentials
		expected bool
	}{
		{
			name: "RabbitMQ AMQP URI",
			creds: common.Credentials{
				"uri": "amqp://user:pass@host:5672/vhost",
			},
			expected: true,
		},
		{
			name: "RabbitMQ AMQPS URI",
			creds: common.Credentials{
				"uri": "amqps://user:pass@host:5671/vhost",
			},
			expected: true,
		},
		{
			name: "RabbitMQ with protocols",
			creds: common.Credentials{
				"protocols": map[string]interface{}{
					"amqp": map[string]interface{}{
						"uri": "amqp://user:pass@host:5672",
					},
				},
			},
			expected: true,
		},
		{
			name: "RabbitMQ standard port",
			creds: common.Credentials{
				"host":     "rabbitmq.example.com",
				"port":     5672,
				"username": "admin",
				"password": "secret",
			},
			expected: true,
		},
		{
			name: "RabbitMQ TLS port",
			creds: common.Credentials{
				"host":     "rabbitmq.example.com",
				"port":     5671,
				"username": "admin",
				"password": "secret",
			},
			expected: true,
		},
		{
			name: "RabbitMQ with vhost",
			creds: common.Credentials{
				"host":     "rabbitmq.example.com",
				"username": "admin",
				"password": "secret",
				"vhost":    "/service-instance-123",
			},
			expected: true,
		},
		{
			name: "Not RabbitMQ - Redis URI",
			creds: common.Credentials{
				"uri": "redis://:pass@host:6379",
			},
			expected: false,
		},
		{
			name: "Not RabbitMQ - Valkey instance with username",
			creds: common.Credentials{
				"host":         "q-s0.standalone.valkey-service.example.bosh",
				"port":         0,
				"tls_port":     6379,
				"username":     "default",
				"password":     "secret",
				"service_type": "valkey",
			},
			expected: false,
		},
		{
			name: "Not RabbitMQ - Valkey URI",
			creds: common.Credentials{
				"uri": "valkeys://default:pass@host:6379",
			},
			expected: false,
		},
		{
			name: "Not RabbitMQ - no identifying fields",
			creds: common.Credentials{
				"host":     "generic.example.com",
				"password": "secret",
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := IsRabbitMQInstance(test.creds)
			if result != test.expected {
				t.Errorf("IsRabbitMQInstance() = %v, expected %v", result, test.expected)
			}
		})
	}
}

func TestNewManager(t *testing.T) {
	t.Parallel()
	// Test with logger
	logger := func(format string, args ...interface{}) {
		// Test logger
	}

	manager := NewManager(logger)

	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}

	if manager.Redis == nil {
		t.Error("Redis handler is nil")
	}

	if manager.RabbitMQ == nil {
		t.Error("RabbitMQ handler is nil")
	}

	if manager.Security == nil {
		t.Error("Security middleware is nil")
	}

	// Test without logger
	manager2 := NewManager(nil)
	if manager2 == nil {
		t.Fatal("NewManager(nil) returned nil")
	}

	// Test Close method
	err := manager.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}
