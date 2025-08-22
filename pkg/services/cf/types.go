package cf

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"blacksmith/pkg/services/common"
)

// CFRegistration represents a Cloud Foundry environment registration
type CFRegistration struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	APIURL     string            `json:"api_url"`
	Username   string            `json:"username"`
	BrokerName string            `json:"broker_name"`
	Status     string            `json:"status"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	LastError  string            `json:"last_error,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// RegistrationRequest represents a request to register with CF
type RegistrationRequest struct {
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name" validate:"required"`
	APIURL       string            `json:"api_url" validate:"required,url"`
	Username     string            `json:"username" validate:"required"`
	Password     string            `json:"password" validate:"required"`
	BrokerName   string            `json:"broker_name,omitempty"`
	AutoRegister bool              `json:"auto_register,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// RegistrationProgress represents progress during registration
type RegistrationProgress struct {
	Step      string    `json:"step"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Error     string    `json:"error,omitempty"`
}

// RegistrationTest represents a CF connection test request
type RegistrationTest struct {
	APIURL   string `json:"api_url" validate:"required,url"`
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// RegistrationTestResult represents CF connection test results
type RegistrationTestResult struct {
	Success      bool                `json:"success"`
	Message      string              `json:"message"`
	CFInfo       *CFInfo             `json:"cf_info,omitempty"`
	Error        string              `json:"error,omitempty"`
	Capabilities []common.Capability `json:"capabilities,omitempty"`
}

// CFInfo represents Cloud Foundry environment information
type CFInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	APIURL       string `json:"api_url"`
	Organization string `json:"organization,omitempty"`
	Space        string `json:"space,omitempty"`
}

// BrokerInfo represents service broker information in CF
type BrokerInfo struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Username string    `json:"username"`
	State    string    `json:"state"`
	Created  time.Time `json:"created_at"`
	Updated  time.Time `json:"updated_at"`
}

// ServiceInfo represents service information in CF catalog
type ServiceInfo struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	BrokerID    string     `json:"broker_id"`
	Active      bool       `json:"active"`
	Plans       []PlanInfo `json:"plans,omitempty"`
}

// PlanInfo represents service plan information
type PlanInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Free        bool   `json:"free"`
	Active      bool   `json:"active"`
}

// SyncRequest represents a request to sync registration status
type SyncRequest struct {
	RegistrationID string `json:"registration_id"`
	Force          bool   `json:"force,omitempty"`
}

// SyncResult represents sync operation results
type SyncResult struct {
	Success       bool          `json:"success"`
	Message       string        `json:"message"`
	BrokerInfo    *BrokerInfo   `json:"broker_info,omitempty"`
	Services      []ServiceInfo `json:"services,omitempty"`
	ServicesCount int           `json:"services_count"`
	Error         string        `json:"error,omitempty"`
}

// Registration status constants
const (
	StatusPending    = "pending"
	StatusConnecting = "connecting"
	StatusRegistered = "registered"
	StatusFailed     = "failed"
	StatusSyncing    = "syncing"
	StatusUnknown    = "unknown"
)

// Progress step constants
const (
	StepValidating       = "validating"
	StepConnecting       = "connecting"
	StepAuthenticating   = "authenticating"
	StepCheckingBroker   = "checking_broker"
	StepCreatingBroker   = "creating_broker"
	StepUpdatingBroker   = "updating_broker"
	StepEnablingServices = "enabling_services"
	StepCompleted        = "completed"
	StepFailed           = "failed"
)

// Progress status constants
const (
	ProgressStatusRunning = "running"
	ProgressStatusSuccess = "success"
	ProgressStatusError   = "error"
	ProgressStatusWarning = "warning"
)

// ValidateRegistrationRequest validates and sanitizes registration request data
func ValidateRegistrationRequest(req *RegistrationRequest) error {
	if req == nil {
		return fmt.Errorf("registration request cannot be nil")
	}

	// Validate required fields
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("registration name is required")
	}

	if strings.TrimSpace(req.APIURL) == "" {
		return fmt.Errorf("CF API URL is required")
	}

	if strings.TrimSpace(req.Username) == "" {
		return fmt.Errorf("username is required")
	}

	if strings.TrimSpace(req.Password) == "" {
		return fmt.Errorf("password is required")
	}

	// Validate URL format
	if err := ValidateURL(req.APIURL); err != nil {
		return fmt.Errorf("invalid API URL: %w", err)
	}

	// Sanitize and validate name
	if err := ValidateName(req.Name); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	// Sanitize broker name if provided
	if req.BrokerName != "" {
		if err := ValidateBrokerName(req.BrokerName); err != nil {
			return fmt.Errorf("invalid broker name: %w", err)
		}
	}

	// Validate username format
	if err := ValidateUsername(req.Username); err != nil {
		return fmt.Errorf("invalid username: %w", err)
	}

	// Validate password strength
	if err := ValidatePassword(req.Password); err != nil {
		return fmt.Errorf("invalid password: %w", err)
	}

	return nil
}

// ValidateURL checks if the URL is valid and uses HTTPS
func ValidateURL(urlStr string) error {
	// Trim whitespace
	urlStr = strings.TrimSpace(urlStr)

	// Parse URL
	u, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("malformed URL: %w", err)
	}

	// Require HTTPS
	if u.Scheme != "https" {
		return fmt.Errorf("API URL must use HTTPS protocol")
	}

	// Validate hostname
	if u.Host == "" {
		return fmt.Errorf("API URL must have a valid hostname")
	}

	// Prevent localhost/private IP access (security)
	if strings.Contains(u.Host, "localhost") ||
		strings.Contains(u.Host, "127.0.0.1") ||
		strings.Contains(u.Host, "::1") ||
		strings.HasPrefix(u.Host, "192.168.") ||
		strings.HasPrefix(u.Host, "10.") ||
		strings.Contains(u.Host, "172.16.") {
		return fmt.Errorf("private/local network URLs are not allowed")
	}

	return nil
}

// ValidateName checks if the registration name is valid
func ValidateName(name string) error {
	name = strings.TrimSpace(name)

	if len(name) < 3 {
		return fmt.Errorf("name must be at least 3 characters long")
	}

	if len(name) > 50 {
		return fmt.Errorf("name must be no more than 50 characters long")
	}

	// Allow alphanumeric, hyphens, underscores, and spaces
	validName := regexp.MustCompile(`^[a-zA-Z0-9\s\-_]+$`)
	if !validName.MatchString(name) {
		return fmt.Errorf("name can only contain letters, numbers, spaces, hyphens, and underscores")
	}

	// Check for potential script injection
	dangerousPatterns := []string{"<script", "javascript:", "data:", "vbscript:", "onload=", "onerror="}
	lowerName := strings.ToLower(name)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lowerName, pattern) {
			return fmt.Errorf("name contains potentially unsafe content")
		}
	}

	return nil
}

// ValidateBrokerName checks if the broker name is valid
func ValidateBrokerName(brokerName string) error {
	brokerName = strings.TrimSpace(brokerName)

	if len(brokerName) < 3 {
		return fmt.Errorf("broker name must be at least 3 characters long")
	}

	if len(brokerName) > 30 {
		return fmt.Errorf("broker name must be no more than 30 characters long")
	}

	// Broker names should be more restrictive (alphanumeric and hyphens only)
	validBrokerName := regexp.MustCompile(`^[a-zA-Z0-9\-]+$`)
	if !validBrokerName.MatchString(brokerName) {
		return fmt.Errorf("broker name can only contain letters, numbers, and hyphens")
	}

	// Must start and end with alphanumeric
	if !regexp.MustCompile(`^[a-zA-Z0-9].*[a-zA-Z0-9]$`).MatchString(brokerName) {
		return fmt.Errorf("broker name must start and end with a letter or number")
	}

	return nil
}

// ValidateUsername checks if the username is valid
func ValidateUsername(username string) error {
	username = strings.TrimSpace(username)

	if len(username) < 2 {
		return fmt.Errorf("username must be at least 2 characters long")
	}

	if len(username) > 100 {
		return fmt.Errorf("username must be no more than 100 characters long")
	}

	// Check for dangerous characters (basic security)
	dangerousChars := []string{";", "'", "\"", "\\", "<", ">", "&", "|", "`"}
	for _, char := range dangerousChars {
		if strings.Contains(username, char) {
			return fmt.Errorf("username contains invalid characters")
		}
	}

	return nil
}

// ValidatePassword checks basic password requirements
func ValidatePassword(password string) error {
	if len(password) < 6 {
		return fmt.Errorf("password must be at least 6 characters long")
	}

	if len(password) > 200 {
		return fmt.Errorf("password is too long (max 200 characters)")
	}

	// Check for null bytes and other control characters
	for _, char := range password {
		if char < 32 && char != 9 && char != 10 && char != 13 { // Allow tab, LF, CR
			return fmt.Errorf("password contains invalid control characters")
		}
	}

	return nil
}

// SanitizeRegistrationRequest sanitizes the registration request data
func SanitizeRegistrationRequest(req *RegistrationRequest) {
	if req == nil {
		return
	}

	// Trim whitespace from all string fields
	req.Name = strings.TrimSpace(req.Name)
	req.APIURL = strings.TrimSpace(req.APIURL)
	req.Username = strings.TrimSpace(req.Username)
	req.BrokerName = strings.TrimSpace(req.BrokerName)

	// Normalize URL (remove trailing slash)
	if req.APIURL != "" {
		req.APIURL = strings.TrimSuffix(req.APIURL, "/")
	}

	// Set default broker name if empty
	if req.BrokerName == "" {
		req.BrokerName = "blacksmith"
	}

	// Sanitize metadata
	if req.Metadata != nil {
		for key, value := range req.Metadata {
			// Remove potentially dangerous metadata
			if strings.Contains(strings.ToLower(key), "password") ||
				strings.Contains(strings.ToLower(key), "secret") ||
				strings.Contains(strings.ToLower(key), "token") {
				delete(req.Metadata, key)
				continue
			}

			// Sanitize values
			req.Metadata[key] = strings.TrimSpace(value)
		}
	}
}
