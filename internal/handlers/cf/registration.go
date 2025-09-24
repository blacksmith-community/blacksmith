package cf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"blacksmith/pkg/http/response"
)

// Static errors for err113 linter compliance.
var (
	errFailedToReadRequestBody = errors.New("failed to read request body")
	errRegistrationIDRequired  = errors.New("registration ID is required")
	errRegistrationNotFound    = errors.New("registration not found")
)

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
		"message":  fmt.Sprintf("Connected to %s", api.Name),
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
