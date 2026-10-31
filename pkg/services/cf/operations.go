package cf

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"blacksmith/pkg/services/common"
)

// Static errors for err113 compliance.
var (
	ErrRegistrationNameRequired = errors.New("registration name is required")
	ErrCFAPIURLRequired         = errors.New("CF API URL is required")
	ErrUsernameRequired         = errors.New("username is required")
	ErrPasswordRequired         = errors.New("password is required")
	ErrNoServicesEnabled        = errors.New("no services were enabled")
)

// Handler provides CF registration operations.
type Handler struct {
	brokerURL  string
	brokerUser string
	brokerPass string
	logger     func(string, ...interface{})
	// httpClient, when set, is the client every CF client the handler builds
	// uses. Nil keeps NewCFAuthClient's.
	httpClient *http.Client
}

// NewHandler creates a new CF operations handler.
func NewHandler(brokerURL, brokerUser, brokerPass string, logger func(string, ...interface{})) *Handler {
	return &Handler{
		brokerURL:  brokerURL,
		brokerUser: brokerUser,
		brokerPass: brokerPass,
		logger:     logger,
	}
}

// client builds a CF client for one API URL and set of credentials.
func (h *Handler) client(apiURL, username, password string) *Client {
	client := NewClient(apiURL, username, password, h.brokerURL, h.brokerUser, h.brokerPass)
	if h.httpClient != nil {
		client.authClient.httpClient = h.httpClient
	}

	return client
}

// TestConnection tests connection to a CF environment.
func (h *Handler) TestConnection(req *RegistrationTest) (*RegistrationTestResult, error) {
	h.logger("Testing CF connection to %s", req.APIURL)

	client := h.client(req.APIURL, req.Username, req.Password)

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

// PerformRegistration performs the full registration process.
//
//nolint:funlen
func (h *Handler) PerformRegistration(req *RegistrationRequest, progressChan chan<- RegistrationProgress) error {
	defer close(progressChan)

	h.sendProgress(progressChan, StepValidating, ProgressStatusRunning, "Validating registration request")

	// Validate request
	err := h.validateRegistrationRequest(req)
	if err != nil {
		h.sendProgress(progressChan, StepValidating, ProgressStatusError, fmt.Sprintf("Validation failed: %s", err))

		return err
	}

	h.sendProgress(progressChan, StepValidating, ProgressStatusSuccess, "Request validation complete")

	// Set default broker name if not provided
	brokerName := req.BrokerName
	if brokerName == "" {
		brokerName = "blacksmith"
	}

	client := h.client(req.APIURL, req.Username, req.Password)

	// Connect: the API root must answer and name its authentication
	// endpoint. No credentials are sent yet.
	h.sendProgress(progressChan, StepConnecting, ProgressStatusRunning, "Connecting to Cloud Foundry")

	authURL, err := client.authClient.connect()
	if err != nil {
		h.sendProgress(progressChan, StepConnecting, ProgressStatusError, fmt.Sprintf("Connection failed: %s", err))

		return err
	}

	h.sendProgress(progressChan, StepConnecting, ProgressStatusSuccess, "Connected to Cloud Foundry")

	// Authenticate with one password grant to the endpoint the root named.
	h.sendProgress(progressChan, StepAuthenticating, ProgressStatusRunning, "Authenticating with Cloud Foundry")

	err = client.authClient.requestToken(authURL)
	if err != nil {
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
		h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusRunning, "Updating existing service broker: "+brokerName)

		brokerInfo, err = client.UpdateServiceBroker(existingBroker.ID, brokerName)
		if err != nil {
			h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusError, fmt.Sprintf("Failed to update broker: %s", err))

			return err
		}

		h.sendProgress(progressChan, StepUpdatingBroker, ProgressStatusSuccess, "Service broker updated: "+brokerName)
	} else {
		// Create new broker
		h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusRunning, "Creating service broker: "+brokerName)

		brokerInfo, err = client.CreateServiceBroker(brokerName)
		if err != nil {
			h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusError, fmt.Sprintf("Failed to create broker: %s", err))

			return err
		}

		h.sendProgress(progressChan, StepCreatingBroker, ProgressStatusSuccess, "Service broker created: "+brokerName)
	}

	// Enable service access
	h.sendProgress(progressChan, StepEnablingServices, ProgressStatusRunning, "Enabling service access")

	err = h.enableServiceAccess(client, progressChan)
	if err != nil {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusWarning, fmt.Sprintf("Some services may not be enabled: %s", err))
		// Don't return error here as the broker is registered, just service access might be partial
	} else {
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusSuccess, "Service access enabled")
	}

	h.sendProgress(progressChan, StepCompleted, ProgressStatusSuccess, "Registration completed successfully. Broker: "+brokerInfo.Name)

	return nil
}

// SyncRegistration syncs the registration status with CF.
func (h *Handler) SyncRegistration(req *SyncRequest, registration *CFRegistration) (*SyncResult, error) {
	h.logger("Syncing registration %s with CF", req.RegistrationID)

	// The broker keeps no CF password for a registration, so sync checks
	// that the API answers and is Cloud Foundry without a password grant.
	client := h.client(registration.APIURL, registration.Username, "")

	_, connectErr := client.authClient.connect()
	if connectErr != nil {
		return &SyncResult{
			Success: false,
			Message: "Failed to connect to CF during sync",
			Error:   connectErr.Error(),
		}, nil
	}

	// Reading the broker needs a token, and requestToken refuses the empty
	// password without sending it, so this fails with ErrPasswordRequired.
	brokerInfo, brokerErr := client.FindServiceBroker(registration.BrokerName)
	if brokerErr != nil {
		return nil, fmt.Errorf("failed to find service broker: %w", brokerErr)
	}

	if brokerInfo == nil {
		return &SyncResult{
			Success: false,
			Message: "Service broker not found in CF",
			Error:   fmt.Sprintf("Broker '%s' not found", registration.BrokerName),
		}, nil
	}

	// Get service offerings
	services, servicesErr := client.GetServiceOfferings()
	if servicesErr != nil {
		return nil, fmt.Errorf("failed to get service offerings: %w", servicesErr)
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

// Capabilities returns the capabilities of this service handler.
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

// validateRegistrationRequest validates a registration request.
func (h *Handler) validateRegistrationRequest(req *RegistrationRequest) error {
	if req.Name == "" {
		return ErrRegistrationNameRequired
	}

	if req.APIURL == "" {
		return ErrCFAPIURLRequired
	}

	if req.Username == "" {
		return ErrUsernameRequired
	}

	if req.Password == "" {
		return ErrPasswordRequired
	}

	_, err := registrationAPIURL(req.APIURL)

	return err
}

// enableServiceAccess enables access to all services from the broker.
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
		h.sendProgress(progressChan, StepEnablingServices, ProgressStatusRunning, "Enabling access for service: "+service.Name)

		err := client.EnableServiceAccess(service.ID)
		if err != nil {
			h.logger("Failed to enable access for service %s: %s", service.Name, err)
			h.sendProgress(progressChan, StepEnablingServices, ProgressStatusWarning, fmt.Sprintf("Failed to enable access for service %s: %s", service.Name, err))

			continue
		}

		enabledCount++
	}

	if enabledCount == 0 {
		return ErrNoServicesEnabled
	}

	h.sendProgress(progressChan, StepEnablingServices, ProgressStatusSuccess, fmt.Sprintf("Enabled access for %d of %d services", enabledCount, len(services)))

	return nil
}

// sendProgress sends a progress update through the channel.
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
