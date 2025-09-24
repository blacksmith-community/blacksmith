package cf

import (
	"context"
	"net/http"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	cfservices "blacksmith/pkg/services/cf"
)

const (
	cfPathOrgs   = "orgs"
	cfPathSpaces = "spaces"
)

// Handler handles Cloud Foundry registration HTTP requests.
type Handler struct {
	logger    interfaces.Logger
	config    interfaces.Config
	cfManager interfaces.CFManager
	vault     interfaces.Vault

	newCFOperations func() cfOperations
	persistProgress func(ctx context.Context, registrationID string, progress cfservices.RegistrationProgress) error
}

// NewHandler creates a new CF registration handler.
func NewHandler(logger interfaces.Logger, config interfaces.Config, cfManager interfaces.CFManager, vault interfaces.Vault) *Handler {
	handler := &Handler{
		logger:    logger,
		config:    config,
		cfManager: cfManager,
		vault:     vault,
	}

	handler.newCFOperations = func() cfOperations {
		brokerCfg := config.GetBrokerConfig().CF
		if !brokerCfg.Enabled {
			return nil
		}

		loggerFn := logger.Named("cf-services").Debug

		return cfservices.NewHandler(brokerCfg.BrokerURL, brokerCfg.BrokerUser, brokerCfg.BrokerPass, loggerFn)
	}

	handler.persistProgress = handler.saveRegistrationProgress

	return handler
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
		remainder := strings.TrimPrefix(path, "/registrations/")
		parts := strings.Split(remainder, "/")
		registrationID := parts[0]

		if len(parts) == 1 {
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

		if len(parts) == 2 && parts[1] == "register" && req.Method == http.MethodPost {
			h.StartRegistration(writer, req, registrationID)

			return true
		}
	}

	// TODO: Add other registration routes like test, sync, stream progress
	return false
}

// handleCFEndpointRoutes handles CF endpoint routes.
// Returns true if the route was handled, false otherwise.
func (h *Handler) handleCFEndpointRoutes(ctx context.Context, writer http.ResponseWriter, req *http.Request, path string) bool {
	if path == "/endpoints" && req.Method == http.MethodGet {
		h.ListEndpoints(writer, req)

		return true
	}

	if req.Method == http.MethodPost && strings.Contains(path, "/connect") {
		return h.handleConnectRoute(ctx, writer, req, path)
	}

	if req.Method == http.MethodGet {
		if strings.Contains(path, "/marketplace") {
			return h.handleMarketplaceRoute(writer, req, path)
		}

		if strings.HasPrefix(path, "/endpoints/") && strings.Contains(path, "/orgs") {
			return h.handleOrgsRoutes(writer, req, path)
		}
	}

	return false
}

func (h *Handler) handleConnectRoute(ctx context.Context, writer http.ResponseWriter, req *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/endpoints/"), "/")
	if len(parts) == 2 && parts[1] == "connect" {
		h.ConnectEndpoint(ctx, writer, req, parts[0])

		return true
	}

	return false
}

func (h *Handler) handleMarketplaceRoute(writer http.ResponseWriter, req *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/endpoints/"), "/")
	if len(parts) == 2 && parts[1] == "marketplace" {
		h.GetMarketplace(writer, req, parts[0])

		return true
	}

	return false
}

func (h *Handler) handleOrgsRoutes(writer http.ResponseWriter, req *http.Request, path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/endpoints/"), "/")

	if len(parts) == 2 && parts[1] == cfPathOrgs {
		h.GetOrganizations(writer, req, parts[0])

		return true
	}

	if len(parts) == 4 && parts[1] == cfPathOrgs && parts[3] == cfPathSpaces {
		h.GetSpaces(writer, req, parts[0], parts[2])

		return true
	}

	if len(parts) == 6 && parts[1] == cfPathOrgs && parts[3] == cfPathSpaces && parts[5] == "services" {
		h.GetServices(writer, req, parts[0], parts[2], parts[4])

		return true
	}

	if len(parts) == 8 && parts[1] == cfPathOrgs && parts[3] == cfPathSpaces && parts[5] == "service_instances" && parts[7] == "bindings" {
		h.GetServiceBindings(writer, req, parts[0], parts[2], parts[4], parts[6])

		return true
	}

	return false
}
