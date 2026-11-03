// Package aws provides AWS client management and configuration
// for the AWS operator backend.
package aws

import (
	"os"
	"strconv"
	"time"
)

// Target represents an AWS target configuration.
type Target struct {
	Region             string        `yaml:"region"`
	Profile            string        `yaml:"profile"`
	Role               string        `yaml:"role"`
	AccessKeyID        string        `yaml:"access_key_id"`
	SecretAccessKey    string        `yaml:"secret_access_key"`
	SessionToken       string        `yaml:"session_token"`
	Endpoint           string        `yaml:"endpoint"`
	DisableSSL         bool          `yaml:"disable_ssl"`
	MaxRetries         int           `yaml:"max_retries"`
	HTTPTimeout        time.Duration `yaml:"http_timeout"`
	AssumeRoleDuration time.Duration `yaml:"assume_role_duration"`
	ExternalID         string        `yaml:"external_id"`
	SessionName        string        `yaml:"session_name"`
	MfaSerial          string        `yaml:"mfa_serial"`

	// MfaToken is a one-shot MFA code for MfaSerial, read once from
	// AWS_{TARGET}_MFA_TOKEN (or, for the "default" target only, also
	// AWS_MFA_TOKEN - see GetTargetConfig). It is not a persisted
	// configuration value (a TOTP code baked into a config file would be
	// useless within seconds), so it carries no yaml tag.
	MfaToken string `yaml:"-"`

	// EnvPrefix is the "AWS_<TARGET>_" prefix GetTargetConfig read this
	// Target's environment variables from (e.g. "AWS_PROD_"). It exists
	// only so BuildConfig's MFA error messages can name the exact
	// environment variable a caller needs to set; it carries no yaml tag
	// and is left "" by a Target built directly (as most tests do), in
	// which case BuildConfig falls back to a generic "AWS_MFA_TOKEN"
	// name.
	EnvPrefix string `yaml:"-"`
}

// GetEnvOrDefault returns environment variable value or default.
func GetEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// ParseDurationOrDefault parses duration string or returns default.
func ParseDurationOrDefault(value string, defaultValue time.Duration) time.Duration {
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	return defaultValue
}

// ParseIntOrDefault parses integer string or returns default.
func ParseIntOrDefault(value string, defaultValue int) int {
	if i, err := strconv.Atoi(value); err == nil {
		return i
	}
	return defaultValue
}

// ParseBoolOrDefault parses boolean string or returns false as default.
func ParseBoolOrDefault(value string) bool {
	if b, err := strconv.ParseBool(value); err == nil {
		return b
	}
	return false
}
