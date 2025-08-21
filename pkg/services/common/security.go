package common

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"
)

// TLSConfig represents enhanced TLS configuration
type TLSConfig struct {
	InsecureSkipVerify bool
	RootCAs            *x509.CertPool
	ServerName         string
	MinVersion         uint16
	Certificates       []tls.Certificate
}

// BuildTLSConfig creates a TLS configuration from credentials
func BuildTLSConfig(creds Credentials, serverName string) *tls.Config {
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}

	// Check for custom CA certificate
	if caCert := creds.GetString("ca_certificate"); caCert != "" {
		caCertPool := x509.NewCertPool()
		if caCertPool.AppendCertsFromPEM([]byte(caCert)) {
			config.RootCAs = caCertPool
		}
	} else if caCert := creds.GetString("tls_ca"); caCert != "" {
		caCertPool := x509.NewCertPool()
		if caCertPool.AppendCertsFromPEM([]byte(caCert)) {
			config.RootCAs = caCertPool
		}
	} else {
		// For self-signed certificates in development environments
		config.InsecureSkipVerify = true
	}

	// Check for client certificates
	if clientCert := creds.GetString("client_certificate"); clientCert != "" {
		if clientKey := creds.GetString("client_key"); clientKey != "" {
			cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
			if err == nil {
				config.Certificates = []tls.Certificate{cert}
			}
		}
	}

	return config
}

// RateLimiter provides basic rate limiting functionality
type RateLimiter struct {
	requests map[string][]time.Time
	Limit    int
	Window   time.Duration
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		Limit:    limit,
		Window:   window,
	}
}

// AllowRequest checks if a request is allowed for the given key
func (rl *RateLimiter) AllowRequest(key string) bool {
	now := time.Now()

	// Get existing requests for this key
	requests := rl.requests[key]

	// Remove requests outside the time window
	validRequests := make([]time.Time, 0)
	for _, reqTime := range requests {
		if now.Sub(reqTime) < rl.Window {
			validRequests = append(validRequests, reqTime)
		}
	}

	// Check if we're under the limit
	if len(validRequests) >= rl.Limit {
		return false
	}

	// Add current request
	validRequests = append(validRequests, now)
	rl.requests[key] = validRequests

	return true
}

// GetRemainingRequests returns the number of remaining requests in the current window
func (rl *RateLimiter) GetRemainingRequests(key string) int {
	now := time.Now()

	// Count valid requests
	validCount := 0
	for _, reqTime := range rl.requests[key] {
		if now.Sub(reqTime) < rl.Window {
			validCount++
		}
	}

	remaining := rl.Limit - validCount
	if remaining < 0 {
		remaining = 0
	}

	return remaining
}

// AuditLogger provides audit logging functionality
type AuditLogger struct {
	logger func(string, ...interface{})
}

// NewAuditLogger creates a new audit logger
func NewAuditLogger(logger func(string, ...interface{})) *AuditLogger {
	if logger == nil {
		logger = func(string, ...interface{}) {} // No-op logger
	}

	return &AuditLogger{
		logger: logger,
	}
}

// LogOperation logs a service operation for auditing
func (al *AuditLogger) LogOperation(user, instanceID, serviceType, operation string, params interface{}, result interface{}, err error) {
	entry := map[string]interface{}{
		"timestamp":    time.Now().Unix(),
		"user":         user,
		"instance_id":  instanceID,
		"service_type": serviceType,
		"operation":    operation,
		"params":       MaskCredentials(params.(Credentials)),
		"success":      err == nil,
	}

	if err != nil {
		entry["error"] = err.Error()
	} else {
		entry["result"] = result
	}

	al.logger("service_testing_audit: %+v", entry)
}

// ValidateInputs performs basic input validation
func ValidateInputs(inputs map[string]interface{}) error {
	for key, value := range inputs {
		if str, ok := value.(string); ok {
			// Check for potential security issues
			if containsSQLInjection(str) {
				return fmt.Errorf("potentially unsafe input detected in %s", key)
			}
			if containsScriptInjection(str) {
				return fmt.Errorf("potentially unsafe script content in %s", key)
			}
		}
	}
	return nil
}

// containsSQLInjection checks for basic SQL injection patterns
func containsSQLInjection(input string) bool {
	dangerousPatterns := []string{
		"'; DROP",
		"\"; DROP",
		"' OR '1'='1",
		"\" OR \"1\"=\"1",
		"' UNION SELECT",
		"\" UNION SELECT",
	}

	for _, pattern := range dangerousPatterns {
		if len(input) >= len(pattern) {
			for i := 0; i <= len(input)-len(pattern); i++ {
				match := true
				for j := 0; j < len(pattern); j++ {
					if input[i+j] != pattern[j] && input[i+j] != pattern[j]+32 && input[i+j] != pattern[j]-32 {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
		}
	}

	return false
}

// containsScriptInjection checks for basic script injection patterns
func containsScriptInjection(input string) bool {
	dangerousPatterns := []string{
		"<script",
		"javascript:",
		"vbscript:",
		"onload=",
		"onerror=",
		"onclick=",
	}

	for _, pattern := range dangerousPatterns {
		if len(input) >= len(pattern) {
			for i := 0; i <= len(input)-len(pattern); i++ {
				match := true
				for j := 0; j < len(pattern); j++ {
					if input[i+j] != pattern[j] && input[i+j] != pattern[j]+32 && input[i+j] != pattern[j]-32 {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
		}
	}

	return false
}
