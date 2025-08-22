package cf

import (
	"fmt"
	"time"

	"blacksmith/pkg/services/common"
)

// Handler provides CF registration operations
type Handler struct {
	brokerURL  string
	brokerUser string
	brokerPass string
	logger     func(string, ...interface{})
}

// NewHandler creates a new CF operations handler
func NewHandler(brokerURL, brokerUser, brokerPass string, logger func(string, ...interface{})) *Handler {
	return &Handler{
		brokerURL:  brokerURL,
		brokerUser: brokerUser,
		brokerPass: brokerPass,
		logger:     logger,
	}
}

// TestConnection tests connection to a CF environment
func (h *Handler) TestConnection(req *RegistrationTest) (*RegistrationTestResult, error) {
	h.logger("Testing CF connection to %s", req.APIURL)

	client := NewClient(req.APIURL, req.Username, req.Password, h.brokerURL, h.brokerUser, h.brokerPass)
	result, err := client.TestConnection()
	if err != nil {
		h.logger("CF connection test failed: %s", err)
		return &RegistrationTestResult{
			Success: false,
			Message: "Connection test failed",
			Error:   err.Error(),
		}, nil
	}

	h.logger("CF connection test successful")
	return result, nil
}

// PerformRegistration performs the full registration process
func (h *Handler) PerformRegistration(req *RegistrationRequest, progressChan chan<- RegistrationProgress) error {
	defer close(progressChan)

	h.sendProgress(progressChan, StepValidating, ProgressStatusRunning, "Validating registration request")

	// Validate request
	if err := h.validateRegistrationRequest(req); err != nil {
		h.sendProgress(progressChan, StepValidating, ProgressStatusError, fmt.Sprintf("Validation failed: %s", err))
		return err
	}

	h.sendProgress(progressChan, StepValidating, ProgressStatusSuccess, "Request validation complete")

	// Set default broker name if not provided
	brokerName := req.BrokerName
	if brokerName == "" {
		brokerName = "blacksmith"
	}

	client := NewClient(req.APIURL, req.Username, req.Password, h.brokerURL, h.brokerUser, h.brokerPass)

	// Test connection
	h.sendProgress(progressChan, StepConnecting, ProgressStatusRunning, "Connecting to Cloud Foundry")
	_, err := client.TestConnection()
	if err != nil {
		h.sendProgress(progressChan, StepConnecting, ProgressStatusError, fmt.Sprintf("Connection failed: %s", err))
		return err
	}
	h.sendProgress(progressChan, StepConnecting, ProgressStatusSuccess, "Connected to Cloud Foundry")

	// Authenticate
	h.sendProgress(progressChan, StepAuthenticating, ProgressStatusRunning, "Authenticating with Cloud Foundry")
	if err := client.authClient.Authenticate(); err != nil {
		h.sendProgress(progressChan, StepAuthenticating, ProgressStatusError, fmt.Sprintf("Authentication failed: %s", err))
		return err
	}
	h.sendProgress(progressChan, StepAuthenticating, ProgressStatusSuccess, "Authentication successful")

	// Check for existing broker
	h.sendProgress(progressChan, StepCheckingBroker, ProgressStatusRunning, "Checking for existing service broker")
	existingBroker, err := client.FindServiceBroker(brokerName)
	if err != nil {
		h.sendProgress(progressChan, StepCheckingBroker, ProgressStatusError, fmt.Sprintf("Failed to check for existing broker: %s", err))
		return err
	}

	var brokerInfo *BrokerInfo
	if existingBroker != nil {
		// Update existing broker
		h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusRunning, fmt.Sprintf("Updating existing service broker: %s", brokerName))
		brokerInfo, err = client.UpdateServiceBroker(existingBroker.ID, brokerName)
		if err != nil {
			h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusError, fmt.Sprintf("Failed to update broker: %s", err))
			return err
		}
		h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusSuccess, fmt.Sprintf("Service broker updated: %s", brokerName))
	} else {
		// Create new broker
		h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusRunning, fmt.Sprintf("Creating service broker: %s", brokerName))
		brokerInfo, err = client.CreateServiceBroker(brokerName)
		if err != nil {
			h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusError, fmt.Sprintf("Failed to create broker: %s", err))
			return err
		}
		h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusSuccess, fmt.Sprintf("Service broker created: %s", brokerName))
	}

	// Enable service access
	h.sendProgress(progressChan, StepEnablingServices, ProgressStatusRunning, "Enabling service access")
	if err := h.enableServiceAccess(client, progressChan); err != nil {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusWarning, fmt.Sprintf("Some services may not be enabled: %s", err))
		// Don't return error here as the broker is registered, just service access might be partial
	} else {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusSuccess, "Service access enabled")
	}

	h.sendProgress(progressChan, StepCompleted, ProgressStatusSuccess, fmt.Sprintf("Registration completed successfully. Broker: %s", brokerInfo.Name))
	return nil
}

// SyncRegistration syncs the registration status with CF
func (h *Handler) SyncRegistration(req *SyncRequest, registration *CFRegistration) (*SyncResult, error) {
	h.logger("Syncing registration %s with CF", req.RegistrationID)

	client := NewClient(registration.APIURL, registration.Username, "", h.brokerURL, h.brokerUser, h.brokerPass)

	// Test connection first
	testResult, err := client.TestConnection()
	if err != nil || !testResult.Success {
		return &SyncResult{
			Success: false,
			Message: "Failed to connect to CF during sync",
			Error:   err.Error(),
		}, nil
	}

	// Get broker info
	brokerInfo, err := client.FindServiceBroker(registration.BrokerName)
	if err != nil {
		return &SyncResult{
			Success: false,
			Message: "Failed to find service broker",
			Error:   err.Error(),
		}, nil
	}

	if brokerInfo == nil {
		return &SyncResult{
			Success: false,
			Message: "Service broker not found in CF",
			Error:   fmt.Sprintf("Broker '%s' not found", registration.BrokerName),
		}, nil
	}

	// Get service offerings
	services, err := client.GetServiceOfferings()
	if err != nil {
		return &SyncResult{
			Success: false,
			Message: "Failed to get service offerings",
			Error:   err.Error(),
		}, nil
	}

	// Filter services from our broker
	var brokerServices []ServiceInfo
	for _, service := range services {
		if service.BrokerID == brokerInfo.ID {
			brokerServices = append(brokerServices, service)
		}
	}

	return &SyncResult{
		Success:       true,
		Message:       "Sync completed successfully",
		BrokerInfo:    brokerInfo,
		Services:      brokerServices,
		ServicesCount: len(brokerServices),
	}, nil
}

// validateRegistrationRequest validates a registration request
func (h *Handler) validateRegistrationRequest(req *RegistrationRequest) error {
	if req.Name == "" {
		return fmt.Errorf("registration name is required")
	}
	if req.APIURL == "" {
		return fmt.Errorf("CF API URL is required")
	}
	if req.Username == "" {
		return fmt.Errorf("username is required")
	}
	if req.Password == "" {
		return fmt.Errorf("password is required")
	}
	return nil
}

// enableServiceAccess enables access to all services from the broker
func (h *Handler) enableServiceAccess(client *Client, progressChan chan<- RegistrationProgress) error {
	services, err := client.GetServiceOfferings()
	if err != nil {
		return fmt.Errorf("failed to get service offerings: %w", err)
	}

	if len(services) == 0 {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusWarning, "No services found in catalog")
		return nil
	}

	enabledCount := 0
	for _, service := range services {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusRunning, fmt.Sprintf("Enabling access for service: %s", service.Name))

		err := client.EnableServiceAccess(service.ID)
		if err != nil {
			h.logger("Failed to enable access for service %s: %s", service.Name, err)
			h.sendProgress(progressChan, StepEnablingServices, ProgressStatusWarning, fmt.Sprintf("Failed to enable access for service %s: %s", service.Name, err))
			continue
		}
		enabledCount++
	}

	if enabledCount == 0 {
		return fmt.Errorf("no services were enabled")
	}

	h.sendProgress(progressChan, StepEnablingServices, ProgressStatusSuccess, fmt.Sprintf("Enabled access for %d of %d services", enabledCount, len(services)))
	return nil
}

// sendProgress sends a progress update through the channel
func (h *Handler) sendProgress(progressChan chan<- RegistrationProgress, step, status, message string) {
	progress := RegistrationProgress{
		Step:      step,
		Status:    status,
		Message:   message,
		Timestamp: time.Now(),
	}

	// Send progress if channel is open
	select {
	case progressChan <- progress:
		h.logger("Registration progress: %s - %s - %s", step, status, message)
	default:
		// Channel might be closed or full, log instead
		h.logger("Registration progress (channel unavailable): %s - %s - %s", step, status, message)
	}
}

// Capabilities returns the capabilities of this service handler
func (h *Handler) Capabilities() []common.Capability {
	return []common.Capability{
		{
			Name:        "cf_registration",
			Description: "Cloud Foundry service broker registration",
			Category:    "integration",
		},
		{
			Name:        "cf_v3_api",
			Description: "Cloud Foundry V3 API support",
			Category:    "api",
		},
	}
}
