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
	cfManager interfaces.CFManager
	vault     interfaces.Vault
}

// NewHandler creates a new CF registration handler.
func NewHandler(logger interfaces.Logger, cfManager interfaces.CFManager, vault interfaces.Vault) *Handler {
	return &Handler{
		logger:    logger,
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
	// TODO: Extract and implement CF endpoint routes
	// This should include:
	// - List endpoints
	// - Get marketplace
	// - Get organizations
	// - Get spaces
	// - Get services
	// - Get service bindings
	// - Connect endpoint
	return false
}
