package cf

import (
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
func (h *Handler) ListRegistrations(w http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-list-registrations")
	logger.Debug("listing CF registrations")

	registrations, err := h.vault.ListCFRegistrations(req.Context())
	if err != nil {
		logger.Error("failed to list CF registrations: %s", err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to list registrations: %w", err))

		return
	}

	logger.Debug("found %d CF registrations", len(registrations))
	response.HandleJSON(w, map[string]interface{}{
		"registrations": registrations,
		"count":         len(registrations),
	}, nil)
}

// CreateRegistration handles POST /b/cf/registrations.
func (h *Handler) CreateRegistration(w http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-create-registration")
	logger.Debug("creating CF registration")

	// Read request body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("failed to read request body: %v", err)
		response.HandleJSON(w, nil, errFailedToReadRequestBody)

		return
	}

	// Parse registration request
	var regReq map[string]interface{}
	if err := json.Unmarshal(body, &regReq); err != nil {
		logger.Error("failed to parse registration request: %v", err)
		response.HandleJSON(w, nil, fmt.Errorf("invalid request format: %w", err))

		return
	}

	// Store registration in Vault
	if err := h.vault.SaveCFRegistration(req.Context(), regReq); err != nil {
		logger.Error("failed to store CF registration: %v", err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to create registration: %w", err))

		return
	}

	logger.Info("created CF registration")

	response.HandleJSON(w, map[string]interface{}{
		"message":      "Registration created successfully",
		"registration": regReq,
	}, nil)
}

// GetRegistration handles GET /b/cf/registrations/{id}.
func (h *Handler) GetRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-get-registration")
	logger.Debug("getting CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(w, nil, errRegistrationIDRequired)

		return
	}

	var registration map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &registration)
	if err != nil {
		logger.Error("failed to get CF registration %s: %v", registrationID, err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to retrieve registration: %w", err))

		return
	}

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		response.HandleJSON(w, nil, errRegistrationNotFound)

		return
	}

	logger.Debug("retrieved CF registration: %s", registrationID)
	response.HandleJSON(w, map[string]interface{}{
		"registration": registration,
	}, nil)
}

// UpdateRegistration handles PUT /b/cf/registrations/{id}.
func (h *Handler) UpdateRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-update-registration")
	logger.Debug("updating CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(w, nil, errRegistrationIDRequired)

		return
	}

	// Check if registration exists first
	var existingReg map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &existingReg)
	if err != nil {
		logger.Error("failed to get CF registration %s: %v", registrationID, err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to verify registration: %w", err))

		return
	}

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		response.HandleJSON(w, nil, errRegistrationNotFound)

		return
	}

	// Read request body
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("failed to read request body: %v", err)
		response.HandleJSON(w, nil, errFailedToReadRequestBody)

		return
	}

	// Parse registration request
	var regReq map[string]interface{}
	if err := json.Unmarshal(body, &regReq); err != nil {
		logger.Error("failed to parse registration request: %v", err)
		response.HandleJSON(w, nil, fmt.Errorf("invalid request format: %w", err))

		return
	}

	// Update registration in Vault
	if err := h.vault.SaveCFRegistration(req.Context(), regReq); err != nil {
		logger.Error("failed to update CF registration %s: %v", registrationID, err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to update registration: %w", err))

		return
	}

	logger.Info("updated CF registration: %s", registrationID)
	response.HandleJSON(w, map[string]interface{}{
		"message":      "Registration updated successfully",
		"registration": regReq,
	}, nil)
}

// DeleteRegistration handles DELETE /b/cf/registrations/{id}.
func (h *Handler) DeleteRegistration(w http.ResponseWriter, req *http.Request, registrationID string) {
	logger := h.logger.Named("cf-delete-registration")
	logger.Debug("deleting CF registration: %s", registrationID)

	if registrationID == "" {
		response.HandleJSON(w, nil, errRegistrationIDRequired)

		return
	}

	// Check if registration exists first
	var registration map[string]interface{}

	exists, err := h.vault.GetCFRegistration(req.Context(), registrationID, &registration)
	if err != nil {
		logger.Error("failed to get CF registration %s for deletion: %v", registrationID, err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to verify registration: %w", err))

		return
	}

	if !exists {
		w.WriteHeader(http.StatusNotFound)
		response.HandleJSON(w, nil, errRegistrationNotFound)

		return
	}

	// Delete registration from Vault
	if err := h.vault.DeleteCFRegistration(req.Context(), registrationID); err != nil {
		logger.Error("failed to delete CF registration %s: %v", registrationID, err)
		response.HandleJSON(w, nil, fmt.Errorf("failed to delete registration: %w", err))

		return
	}

	logger.Info("deleted CF registration: %s", registrationID)
	response.HandleJSON(w, map[string]interface{}{
		"message": "Registration deleted successfully",
		"id":      registrationID,
	}, nil)
}
