package common

import (
	"testing"
)

func TestCredentials_GetString(t *testing.T) {
	creds := Credentials{
		"host":     "example.com",
		"port":     6379,
		"password": "secret",
	}

	tests := []struct {
		key      string
		expected string
	}{
		{"host", "example.com"},
		{"password", "secret"},
		{"nonexistent", ""},
	}

	for _, test := range tests {
		result := creds.GetString(test.key)
		if result != test.expected {
			t.Errorf("GetString(%s) = %s, expected %s", test.key, result, test.expected)
		}
	}
}

func TestCredentials_GetInt(t *testing.T) {
	creds := Credentials{
		"port":       6379,
		"float_port": 6380.0,
		"string_val": "not_a_number",
	}

	tests := []struct {
		key      string
		expected int
	}{
		{"port", 6379},
		{"float_port", 6380},
		{"string_val", 0},
		{"nonexistent", 0},
	}

	for _, test := range tests {
		result := creds.GetInt(test.key)
		if result != test.expected {
			t.Errorf("GetInt(%s) = %d, expected %d", test.key, result, test.expected)
		}
	}
}

func TestCredentials_GetBool(t *testing.T) {
	creds := Credentials{
		"enabled":  true,
		"disabled": false,
		"string":   "not_bool",
	}

	tests := []struct {
		key      string
		expected bool
	}{
		{"enabled", true},
		{"disabled", false},
		{"string", false},
		{"nonexistent", false},
	}

	for _, test := range tests {
		result := creds.GetBool(test.key)
		if result != test.expected {
			t.Errorf("GetBool(%s) = %v, expected %v", test.key, result, test.expected)
		}
	}
}

func TestCredentialsMasking(t *testing.T) {
	creds := Credentials{
		"host":        "example.com",
		"port":        6379,
		"password":    "secret123",
		"secret":      "topsecret",
		"certificate": "-----BEGIN CERTIFICATE-----",
		"public_key":  "public_data",
	}

	masked := MaskCredentials(creds)

	// Check that sensitive fields are masked
	if masked["password"] != "***MASKED***" {
		t.Errorf("Password should be masked, got: %v", masked["password"])
	}
	if masked["secret"] != "***MASKED***" {
		t.Errorf("Secret should be masked, got: %v", masked["secret"])
	}

	// Check that non-sensitive fields are preserved
	if masked["host"] != "example.com" {
		t.Errorf("Host should be preserved, got: %v", masked["host"])
	}
	if masked["port"] != 6379 {
		t.Errorf("Port should be preserved, got: %v", masked["port"])
	}

	// Check that certificates are preserved (for debugging)
	if masked["certificate"] != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("Certificate should be preserved, got: %v", masked["certificate"])
	}
}
