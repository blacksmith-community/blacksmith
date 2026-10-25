package redis_test

import (
	"testing"

	"blacksmith/pkg/services/common"
	. "blacksmith/pkg/services/redis"
)

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestNewCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		vaultData   common.Credentials
		expectError bool
		expected    *Credentials
	}{
		{
			name: "valid Redis credentials",
			vaultData: common.Credentials{
				"host":     "redis.example.com",
				"port":     6379,
				"password": "secret123",
			},
			expectError: false,
			expected: &Credentials{
				Host:     "redis.example.com",
				Port:     6379,
				Password: "secret123",
			},
		},
		{
			name: "Redis with TLS",
			vaultData: common.Credentials{
				"hostname": "redis.example.com",
				"port":     6379,
				"tls_port": 6380,
				"password": "secret123",
				"uri":      "redis://:secret123@redis.example.com:6379",
				"tls_uri":  "rediss://:secret123@redis.example.com:6380",
			},
			expectError: false,
			expected: &Credentials{
				Host:     "redis.example.com",
				Hostname: "redis.example.com",
				Port:     6379,
				TLSPort:  6380,
				Password: "secret123",
				URI:      "redis://:secret123@redis.example.com:6379",
				TLSURI:   "rediss://:secret123@redis.example.com:6380",
			},
		},
		{
			name: "missing host",
			vaultData: common.Credentials{
				"port":     6379,
				"password": "secret123",
			},
			expectError: true,
		},
		{
			name: "hostname fallback",
			vaultData: common.Credentials{
				"hostname": "redis.fallback.com",
				"port":     6379,
				"password": "secret123",
			},
			expectError: false,
			expected: &Credentials{
				Host:     "redis.fallback.com",
				Hostname: "redis.fallback.com",
				Port:     6379,
				Password: "secret123",
			},
		},
		{
			name: "default port",
			vaultData: common.Credentials{
				"host":     "redis.example.com",
				"password": "secret123",
			},
			expectError: false,
			expected: &Credentials{
				Host:     "redis.example.com",
				Port:     6379, // Default port
				Password: "secret123",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := NewCredentials(test.vaultData)

			if test.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}

				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)

				return
			}

			if result.Host != test.expected.Host {
				t.Errorf("Host = %s, expected %s", result.Host, test.expected.Host)
			}

			if result.Port != test.expected.Port {
				t.Errorf("Port = %d, expected %d", result.Port, test.expected.Port)
			}

			if result.Password != test.expected.Password {
				t.Errorf("Password = %s, expected %s", result.Password, test.expected.Password)
			}
		})
	}
}

func TestNewCredentialsReadsValkeyFields(t *testing.T) {
	t.Parallel()

	result, err := NewCredentials(common.Credentials{
		"host":           "q-s0.standalone.valkey-service.example.bosh",
		"port":           0,
		"tls_port":       6379,
		"username":       "default",
		"password":       "secret123",
		"ca_certificate": "-----BEGIN CERTIFICATE-----\n",
		"service_type":   "valkey",
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Username != "default" {
		t.Errorf("Username = %q, expected %q", result.Username, "default")
	}

	if result.CACertificate != "-----BEGIN CERTIFICATE-----\n" {
		t.Errorf("CACertificate was not carried over")
	}

	// A TLS-only instance has no plain-text port, so the TLS port must not
	// be mistaken for one.
	if result.Port != 0 {
		t.Errorf("Port = %d, expected 0 for a TLS-only instance", result.Port)
	}

	if result.TLSPort != 6379 {
		t.Errorf("TLSPort = %d, expected 6379", result.TLSPort)
	}
}
