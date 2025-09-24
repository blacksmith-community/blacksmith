package cf

import (
	"context"
	"net/http"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Handler handles Cloud Foundry registration HTTP requests.
type Handler struct {
	logger    interfaces.Logger
	config    interfaces.Config
	cfManager interfaces.CFManager
	vault     interfaces.Vault
}

// NewHandler creates a new CF registration handler.
func NewHandler(logger interfaces.Logger, config interfaces.Config, cfManager interfaces.CFManager, vault interfaces.Vault) *Handler {
	return &Handler{
		logger:    logger,
		config:    config,
		cfManager: cfManager,
		vault:     vault,
	}
}

// ServeHTTP handles HTTP requests for CF registration endpoints.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-registration-api")

	// Check if this is a CF registration endpoint
	if !strings.HasPrefix(req.URL.Path, "/b/cf/") {
		response.WriteError(writer, http.StatusNotFound, "endpoint not found")

		return
	}

	// Remove the /b/cf prefix to get the actual path
	path := strings.TrimPrefix(req.URL.Path, "/b/cf")
	logger.Debug("handling CF registration endpoint: %s %s", req.Method, path)

	// Handle different endpoint categories
	if h.handleCFRegistrationRoutes(writer, req, path) {
		return
	}

	if h.handleCFEndpointRoutes(req.Context(), writer, req, path) {
		return
	}

	// No route matched
	logger.Debug("unknown CF registration endpoint: %s %s", req.Method, path)
	response.WriteError(writer, http.StatusNotFound, "endpoint not found")
}

// handleCFRegistrationRoutes handles CF registration management routes.
// Returns true if the route was handled, false otherwise.
func (h *Handler) handleCFRegistrationRoutes(writer http.ResponseWriter, req *http.Request, path string) bool {
	// Handle /registrations routes
	if path == "/registrations" && req.Method == http.MethodGet {
		h.ListRegistrations(writer, req)

		return true
	}

	if path == "/registrations" && req.Method == http.MethodPost {
		h.CreateRegistration(writer, req)

		return true
	}

	// Handle /registrations/{id} routes
	if strings.HasPrefix(path, "/registrations/") {
		registrationID := strings.TrimPrefix(path, "/registrations/")

		switch req.Method {
		case http.MethodGet:
			h.GetRegistration(writer, req, registrationID)

			return true
		case http.MethodPut:
			h.UpdateRegistration(writer, req, registrationID)

			return true
		case http.MethodDelete:
			h.DeleteRegistration(writer, req, registrationID)

			return true
		}
	}

	// TODO: Add other registration routes like test, sync, stream progress
	return false
}

// handleCFEndpointRoutes handles CF endpoint routes.
// Returns true if the route was handled, false otherwise.
func (h *Handler) handleCFEndpointRoutes(ctx context.Context, writer http.ResponseWriter, req *http.Request, path string) bool {
	// Handle /endpoints route - list available CF API endpoints
	if path == "/endpoints" && req.Method == http.MethodGet {
		h.ListEndpoints(writer, req)
		return true
	}

	// Handle /endpoints/{id}/connect route
	if strings.Contains(path, "/connect") && req.Method == http.MethodPost {
		parts := strings.Split(strings.TrimPrefix(path, "/endpoints/"), "/")
		if len(parts) == 2 && parts[1] == "connect" {
			endpointID := parts[0]
			h.ConnectEndpoint(ctx, writer, req, endpointID)
			return true
		}
	}

	// TODO: Implement other CF endpoint routes
	// - Get marketplace
	// - Get organizations
	// - Get spaces
	// - Get services
	// - Get service bindings
	return false
}
