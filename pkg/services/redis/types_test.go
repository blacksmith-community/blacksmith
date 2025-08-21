package redis

import (
	"testing"

	"blacksmith/pkg/services/common"
)

func TestNewCredentials(t *testing.T) {
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
