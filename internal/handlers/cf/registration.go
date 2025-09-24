package cf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	cfservices "blacksmith/pkg/services/cf"
)

// Constants for CF registration operations.
const (
	// Buffer size for progress tracking channel.
	progressChannelBuffer = 100
)

// Static errors for err113 linter compliance.
var (
	errFailedToReadRequestBody      = errors.New("failed to read request body")
	errRegistrationIDRequired       = errors.New("registration ID is required")
	errRegistrationNotFound         = errors.New("registration not found")
	errCFOperationsNotConfigured    = errors.New("cf operations not configured")
	errCFRegistrationDisabled       = errors.New("cf registration is disabled")
	errPasswordRequiredRegistration = errors.New("password is required to start registration")
)

type cfOperations interface {
	PerformRegistration(req *cfservices.RegistrationRequest, progressChan chan<- cfservices.RegistrationProgress) error
}

// ListRegistrations handles GET /b/cf/registrations.
func (h *Handler) ListRegistrations(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-list-registrations")
	logger.Debug("listing CF registrations")

	registrations, err := h.vault.ListCFRegistrations(req.Context())
	if err != nil {
		logger.Error("failed to list CF registrations: %s", err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to list registrations: %w", err))

		return
	}

	logger.Debug("found %d CF registrations", len(registrations))
	response.HandleJSON(writer, map[string]interface{}{
		"registrations": registrations,
		"count":         len(registrations),
	}, nil)
}

// ListEndpoints handles GET /b/cf/endpoints.
// Returns the list of configured CF API endpoints.
// ListEndpoints handles GET /b/cf/endpoints.
// Returns the list of configured CF API endpoints.
func (h *Handler) ListEndpoints(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-list-endpoints")
	logger.Debug("listing CF endpoints")

	// Get the CF configuration
	cfConfig := h.config.GetCFConfig()

	// Build endpoints object from the configured APIs
	// JavaScript expects an object with keys as endpoint IDs
	endpoints := make(map[string]map[string]string)
	for key, api := range cfConfig.APIs {
		endpoints[key] = map[string]string{
			"key":      key,
			"name":     api.Name,
			"endpoint": api.Endpoint,
		}
	}

	logger.Debug("returning %d CF endpoints", len(endpoints))
	response.HandleJSON(writer, map[string]interface{}{
		"endpoints": endpoints,
		"count":     len(endpoints),
	}, nil)
}

// ConnectEndpoint handles POST /b/cf/endpoints/{id}/connect.
// ConnectEndpoint handles POST /b/cf/endpoints/{id}/connect.
func (h *Handler) ConnectEndpoint(ctx context.Context, writer http.ResponseWriter, req *http.Request, endpointID string) {
	logger := h.logger.Named("cf-connect-endpoint")
	logger.Debug("connecting to CF endpoint", "endpoint", endpointID)

	// Get the CF configuration
	cfConfig := h.config.GetCFConfig()

	// Check if the endpoint exists
	api, exists := cfConfig.APIs[endpointID]
	if !exists {
		logger.Debug("endpoint not found", "endpoint", endpointID)
		writer.WriteHeader(http.StatusNotFound)
		response.HandleJSON(writer, map[string]interface{}{
			"error":   "Endpoint not found",
			"success": false,
		}, nil)

		return
	}

	// For now, we'll just mark the connection as successful
	// In the future, this could test the actual CF API connection
	logger.Debug("endpoint connected successfully", "endpoint", endpointID, "name", api.Name)
	response.HandleJSON(writer, map[string]interface{}{
		"success":  true,
		"endpoint": endpointID,
		"name":     api.Name,
		"message":  "Connected to " + api.Name,
	}, nil)
}

// CreateRegistration handles POST /b/cf/registrations.
func (h *Handler) CreateRegistration(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-create-registration")
	logger.Debug("creating CF registration")

	// Read request body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("failed to read request body: %v", err)
		response.HandleJSON(writer, nil, errFailedToReadRequestBody)

		return
	}

	// Parse registration request
	var regReq map[string]interface{}

	err = json.Unmarshal(body, &regReq)
	if err != nil {
		logger.Error("failed to parse registration request: %v", err)
		response.HandleJSON(writer, nil, fmt.Errorf("invalid request format: %w", err))

		return
	}

	// Store registration in Vault
	err = h.vault.SaveCFRegistration(req.Context(), regReq)
	if err != nil {
		logger.Error("failed to store CF registration: %v", err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to create registration: %w", err))

		return
	}

	logger.Info("created CF registration")

	response.HandleJSON(writer, map[string]interface{}{
		"message":      "Registration created successfully",
		"registration": regReq,
	}, nil)
}

// GetRegistration handles GET /b/cf/registrations/{id}.
func (h *Handler) GetRegistration(writer http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-get-registration")
	logger.Debug("getting CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(writer, nil, errRegistrationIDRequired)

		return
	}

	var registration map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &registration)
	if err != nil {
		logger.Error("failed to get CF registration %s: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to retrieve registration: %w", err))

		return
	}

	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		response.HandleJSON(writer, nil, errRegistrationNotFound)

		return
	}

	logger.Debug("retrieved CF registration: %s", registrationID)
	response.HandleJSON(writer, map[string]interface{}{
		"registration": registration,
	}, nil)
}

// UpdateRegistration handles PUT /b/cf/registrations/{id}.
func (h *Handler) UpdateRegistration(writer http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-update-registration")
	logger.Debug("updating CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(writer, nil, errRegistrationIDRequired)

		return
	}

	// Check if registration exists first
	var existingReg map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &existingReg)
	if err != nil {
		logger.Error("failed to get CF registration %s: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to verify registration: %w", err))

		return
	}

	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		response.HandleJSON(writer, nil, errRegistrationNotFound)

		return
	}

	// Read request body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("failed to read request body: %v", err)
		response.HandleJSON(writer, nil, errFailedToReadRequestBody)

		return
	}

	// Parse registration request
	var regReq map[string]interface{}

	err = json.Unmarshal(body, &regReq)
	if err != nil {
		logger.Error("failed to parse registration request: %v", err)
		response.HandleJSON(writer, nil, fmt.Errorf("invalid request format: %w", err))

		return
	}

	// Update registration in Vault
	err = h.vault.SaveCFRegistration(req.Context(), regReq)
	if err != nil {
		logger.Error("failed to update CF registration %s: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to update registration: %w", err))

		return
	}

	logger.Info("updated CF registration: %s", registrationID)
	response.HandleJSON(writer, map[string]interface{}{
		"message":      "Registration updated successfully",
		"registration": regReq,
	}, nil)
}

// DeleteRegistration handles DELETE /b/cf/registrations/{id}.
func (h *Handler) DeleteRegistration(writer http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-delete-registration")
	logger.Debug("deleting CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(writer, nil, errRegistrationIDRequired)

		return
	}

	// Check if registration exists first
	var registration map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &registration)
	if err != nil {
		logger.Error("failed to get CF registration %s for deletion: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to verify registration: %w", err))

		return
	}

	if !exists {
		writer.WriteHeader(http.StatusNotFound)
		response.HandleJSON(writer, nil, errRegistrationNotFound)

		return
	}

	// Delete registration from Vault
	err = h.vault.DeleteCFRegistration(req.Context(), registrationID)
	if err != nil {
		logger.Error("failed to delete CF registration %s: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to delete registration: %w", err))

		return
	}

	logger.Info("deleted CF registration: %s", registrationID)
	response.HandleJSON(writer, map[string]interface{}{
		"message": "Registration deleted successfully",
		"id":      registrationID,
	}, nil)
}

// StartRegistration handles POST /b/cf/registrations/{id}/register.
func (h *Handler) StartRegistration(writer http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-start-registration")
	logger.Debug("starting CF registration process for %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(writer, nil, errRegistrationIDRequired)

		return
	}

	ctx := req.Context()

	registration, success := h.validateAndGetRegistration(writer, ctx, logger, registrationID)
	if !success {
		return
	}

	if h.checkAlreadyRegistering(writer, logger, registration, registrationID) {
		return
	}

	operations, operationsFound := h.getCFOperations(writer, logger)
	if !operationsFound {
		return
	}

	regReq, ok := h.buildRegistrationRequest(writer, req, logger, registrationID, registration)
	if !ok {
		return
	}

	h.startAsyncRegistration(writer, ctx, logger, registrationID, regReq, operations)
}

func (h *Handler) validateAndGetRegistration(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, registrationID string) (map[string]interface{}, bool) {
	var registration map[string]interface{}

	regExists, err := h.vault.GetCFRegistration(ctx, registrationID, &registration)
	if err != nil {
		logger.Error("failed to retrieve registration %s: %v", registrationID, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to get registration: %w", err))

		return nil, false
	}

	if !regExists {
		logger.Debug("registration %s not found", registrationID)
		response.WriteError(writer, http.StatusNotFound, "registration not found")

		return nil, false
	}

	return registration, true
}

func (h *Handler) checkAlreadyRegistering(writer http.ResponseWriter, logger interfaces.Logger, registration map[string]interface{}, registrationID string) bool {
	status := getStringFromMap(registration, "status")
	if status == "registering" || status == "active" {
		logger.Debug("registration %s already in status %s", registrationID, status)
		response.HandleJSON(writer, map[string]interface{}{
			"message": "registration already " + status,
			"status":  status,
			"id":      registrationID,
		}, nil)

		return true
	}

	return false
}

func (h *Handler) getCFOperations(writer http.ResponseWriter, logger interfaces.Logger) (cfOperations, bool) {
	opHandler := h.newCFOperations
	if opHandler == nil {
		response.HandleJSON(writer, nil, errCFOperationsNotConfigured)

		return nil, false
	}

	operations := opHandler()
	if operations == nil {
		logger.Error("CF operations handler unavailable")
		response.HandleJSON(writer, nil, errCFRegistrationDisabled)

		return nil, false
	}

	return operations, true
}

func (h *Handler) buildRegistrationRequest(writer http.ResponseWriter, req *http.Request, logger interfaces.Logger, registrationID string, registration map[string]interface{}) (*cfservices.RegistrationRequest, bool) {
	brokerConfig := h.config.GetBrokerConfig().CF
	regReq := &cfservices.RegistrationRequest{
		ID:         registrationID,
		Name:       getStringFromMap(registration, "name"),
		APIURL:     getStringFromMap(registration, "api_url"),
		Username:   getStringFromMap(registration, "username"),
		BrokerName: getStringFromMap(registration, "broker_name"),
		Metadata:   make(map[string]string),
	}

	if regReq.BrokerName == "" {
		regReq.BrokerName = brokerConfig.DefaultName
	}

	if regReq.BrokerName == "" {
		regReq.BrokerName = "blacksmith"
	}

	regReq.Password = getStringFromMap(registration, "password")
	if regReq.Password == "" && req.Body != nil {
		var requestData map[string]interface{}

		err := json.NewDecoder(req.Body).Decode(&requestData)
		if err == nil {
			if password, ok := requestData["password"].(string); ok {
				regReq.Password = password
			}
		}
	}

	if regReq.Password == "" {
		logger.Error("registration %s missing password for CF authentication", registrationID)
		response.HandleJSON(writer, nil, errPasswordRequiredRegistration)

		return nil, false
	}

	return regReq, true
}

func (h *Handler) startAsyncRegistration(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, registrationID string, regReq *cfservices.RegistrationRequest, operations cfOperations) {
	go h.performAsyncRegistration(ctx, registrationID, regReq, operations)

	err := h.vault.UpdateCFRegistrationStatus(ctx, registrationID, "registering", "")
	if err != nil {
		logger.Error("failed to update registration status for %s: %v", registrationID, err)
	}

	response.HandleJSON(writer, map[string]interface{}{
		"message": "registration process started",
		"status":  "registering",
		"id":      registrationID,
	}, nil)
}

func (h *Handler) performAsyncRegistration(ctx context.Context, registrationID string, regReq *cfservices.RegistrationRequest, operations cfOperations) {
	logger := h.logger.Named("cf-async-registration")
	logger.Info("starting async CF registration", "id", registrationID)

	progressChan := make(chan cfservices.RegistrationProgress, progressChannelBuffer)
	go h.trackRegistrationProgress(ctx, registrationID, progressChan)

	err := operations.PerformRegistration(regReq, progressChan)
	if err != nil {
		logger.Error("CF registration failed", "id", registrationID, "error", err)

		statusErr := h.vault.UpdateCFRegistrationStatus(ctx, registrationID, "failed", err.Error())
		if statusErr != nil {
			logger.Error("failed to update registration status to failed", "id", registrationID, "error", statusErr)
		}

		return
	}

	statusErr := h.vault.UpdateCFRegistrationStatus(ctx, registrationID, "active", "")
	if statusErr != nil {
		logger.Error("failed to update registration status to active", "id", registrationID, "error", statusErr)
	}

	logger.Info("CF registration completed", "id", registrationID)
}

func (h *Handler) trackRegistrationProgress(ctx context.Context, registrationID string, progressChan <-chan cfservices.RegistrationProgress) {
	logger := h.logger.Named("cf-progress-tracker")

	for progress := range progressChan {
		if h.persistProgress != nil {
			err := h.persistProgress(ctx, registrationID, progress)
			if err != nil {
				logger.Error("failed to persist progress", "id", registrationID, "error", err)
			}
		}
	}

	logger.Debug("completed progress tracking", "id", registrationID)
}

func (h *Handler) saveRegistrationProgress(ctx context.Context, registrationID string, progress cfservices.RegistrationProgress) error {
	progressData := map[string]interface{}{
		"step":      progress.Step,
		"status":    progress.Status,
		"message":   progress.Message,
		"timestamp": progress.Timestamp.Format(time.RFC3339),
	}

	if progress.Error != "" {
		progressData["error"] = progress.Error
	}

	err := h.vault.SaveCFRegistrationProgress(ctx, registrationID, progressData)
	if err != nil {
		return fmt.Errorf("failed to save CF registration progress for registration %q: %w", registrationID, err)
	}

	return nil
}

func getStringFromMap(data map[string]interface{}, key string) string {
	if value, ok := data[key]; ok {
		if str, ok := value.(string); ok {
			return str
		}
	}

	return ""
}
