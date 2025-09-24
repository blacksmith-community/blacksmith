package cf

import (
	"context"
	"net/http"
	"time"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Constants for CF API operations.
const (
	// Default timeout for CF API operations.
	cfAPITimeout = 30 * time.Second

	// Default items per page for CF API requests.
	cfAPIPerPage = 100
)

// GetMarketplace handles GET /b/cf/endpoints/{name}/marketplace.
func (h *Handler) GetMarketplace(writer http.ResponseWriter, request *http.Request, endpointName string) {
	logger := h.logger.Named("cf-get-marketplace")
	logger.Debug("getting marketplace services for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")

		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusServiceUnavailable, "failed to connect to CF endpoint")

		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(writer, http.StatusInternalServerError, "invalid CF client type")

		return
	}

	// Get service offerings (marketplace services)
	ctx, cancel := context.WithTimeout(request.Context(), cfAPITimeout)
	defer cancel()

	params := capi.NewQueryParams().WithPerPage(cfAPIPerPage)

	resp, err := client.ServiceOfferings().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service offerings from CF endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusBadGateway, "failed to get marketplace services from CF")

		return
	}

	// Format response
	services := make([]map[string]interface{}, 0, len(resp.Resources))
	for _, offering := range resp.Resources {
		service := map[string]interface{}{
			"guid":        offering.GUID,
			"name":        offering.Name,
			"description": offering.Description,
			"broker_catalog": map[string]interface{}{
				"id": offering.BrokerCatalog.ID,
			},
		}
		services = append(services, service)
	}

	response.JSON(writer, http.StatusOK, map[string]interface{}{
		"endpoint": endpointName,
		"services": services,
	})
}

// GetOrganizations handles GET /b/cf/endpoints/{name}/orgs.
func (h *Handler) GetOrganizations(writer http.ResponseWriter, request *http.Request, endpointName string) {
	logger := h.logger.Named("cf-get-organizations")
	logger.Debug("getting organizations for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")

		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusServiceUnavailable, "failed to connect to CF endpoint")

		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(writer, http.StatusInternalServerError, "invalid CF client type")

		return
	}

	// Get organizations
	ctx, cancel := context.WithTimeout(request.Context(), cfAPITimeout)
	defer cancel()

	params := capi.NewQueryParams().WithPerPage(cfAPIPerPage)

	resp, err := client.Organizations().List(ctx, params)
	if err != nil {
		logger.Error("failed to get organizations from CF endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusBadGateway, "failed to get organizations from CF")

		return
	}

	// Format response
	orgs := make([]map[string]interface{}, 0, len(resp.Resources))
	for _, org := range resp.Resources {
		orgData := map[string]interface{}{
			"guid":       org.GUID,
			"name":       org.Name,
			"created_at": org.CreatedAt,
			"updated_at": org.UpdatedAt,
		}
		orgs = append(orgs, orgData)
	}

	response.JSON(writer, http.StatusOK, map[string]interface{}{
		"endpoint":      endpointName,
		"organizations": orgs,
	})
}

// GetSpaces handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces.
func (h *Handler) GetSpaces(writer http.ResponseWriter, request *http.Request, endpointName string, orgGUID string) {
	logger := h.logger.Named("cf-get-spaces")
	logger.Debug("getting spaces for CF endpoint %s, org %s", endpointName, orgGUID)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")

		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusServiceUnavailable, "failed to connect to CF endpoint")

		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(writer, http.StatusInternalServerError, "invalid CF client type")

		return
	}

	// Get spaces for the organization
	ctx, cancel := context.WithTimeout(request.Context(), cfAPITimeout)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("organization_guids", orgGUID).WithPerPage(cfAPIPerPage)

	resp, err := client.Spaces().List(ctx, params)
	if err != nil {
		logger.Error("failed to get spaces from CF endpoint %s for org %s: %s", endpointName, orgGUID, err)
		response.WriteError(writer, http.StatusBadGateway, "failed to get spaces from CF")

		return
	}

	// Format response
	spaces := make([]map[string]interface{}, 0, len(resp.Resources))
	for _, space := range resp.Resources {
		spaceData := map[string]interface{}{
			"guid":       space.GUID,
			"name":       space.Name,
			"created_at": space.CreatedAt,
			"updated_at": space.UpdatedAt,
		}
		spaces = append(spaces, spaceData)
	}

	response.JSON(writer, http.StatusOK, map[string]interface{}{
		"endpoint": endpointName,
		"org_guid": orgGUID,
		"spaces":   spaces,
	})
}

// GetServices handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/services.
func (h *Handler) GetServices(writer http.ResponseWriter, request *http.Request, endpointName string, orgGUID string, spaceGUID string) {
	logger := h.logger.Named("cf-get-services")
	logger.Debug("getting services for CF endpoint %s, org %s, space %s", endpointName, orgGUID, spaceGUID)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")

		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusServiceUnavailable, "failed to connect to CF endpoint")

		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(writer, http.StatusInternalServerError, "invalid CF client type")

		return
	}

	// Get service instances for the space
	ctx, cancel := context.WithTimeout(request.Context(), cfAPITimeout)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("space_guids", spaceGUID).WithPerPage(cfAPIPerPage)

	resp, err := client.ServiceInstances().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service instances from CF endpoint %s for space %s: %s", endpointName, spaceGUID, err)
		response.WriteError(writer, http.StatusBadGateway, "failed to get service instances from CF")

		return
	}

	// Format response
	services := make([]map[string]interface{}, 0, len(resp.Resources))
	for _, instance := range resp.Resources {
		serviceData := map[string]interface{}{
			"guid":       instance.GUID,
			"name":       instance.Name,
			"type":       instance.Type,
			"created_at": instance.CreatedAt,
			"updated_at": instance.UpdatedAt,
		}
		services = append(services, serviceData)
	}

	response.JSON(writer, http.StatusOK, map[string]interface{}{
		"endpoint":   endpointName,
		"org_guid":   orgGUID,
		"space_guid": spaceGUID,
		"services":   services,
	})
}

// GetServiceBindings handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/service_instances/{service_guid}/bindings.
func (h *Handler) GetServiceBindings(writer http.ResponseWriter, request *http.Request, endpointName string, orgGUID string, spaceGUID string, serviceGUID string) {
	logger := h.logger.Named("cf-get-service-bindings")
	logger.Debug("getting service bindings for CF endpoint %s, org %s, space %s, service %s", endpointName, orgGUID, spaceGUID, serviceGUID)

	client, success := h.getCFClient(writer, logger, endpointName)
	if !success {
		return
	}

	resp, ok := h.fetchServiceBindings(writer, request, logger, client, endpointName, serviceGUID)
	if !ok {
		return
	}

	bindings := h.formatServiceBindings(resp.Resources)
	h.sendServiceBindingsResponse(writer, endpointName, orgGUID, spaceGUID, serviceGUID, bindings)
}

func (h *Handler) getCFClient(writer http.ResponseWriter, logger interfaces.Logger, endpointName string) (capi.Client, bool) {
	if h.cfManager == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")

		return nil, false
	}

	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(writer, http.StatusServiceUnavailable, "failed to connect to CF endpoint")

		return nil, false
	}

	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(writer, http.StatusInternalServerError, "invalid CF client type")

		return nil, false
	}

	return client, true
}

type serviceBindingsResponse struct {
	Resources []capi.ServiceCredentialBinding
}

func (h *Handler) fetchServiceBindings(writer http.ResponseWriter, request *http.Request, logger interfaces.Logger, client capi.Client, endpointName string, serviceGUID string) (*serviceBindingsResponse, bool) {
	ctx, cancel := context.WithTimeout(request.Context(), cfAPITimeout)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("service_instance_guids", serviceGUID).WithPerPage(cfAPIPerPage)

	resp, err := client.ServiceCredentialBindings().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service bindings from CF endpoint %s for service %s: %s", endpointName, serviceGUID, err)
		response.WriteError(writer, http.StatusBadGateway, "failed to get service bindings from CF")

		return nil, false
	}

	return &serviceBindingsResponse{Resources: resp.Resources}, true
}

func (h *Handler) formatServiceBindings(resources []capi.ServiceCredentialBinding) []map[string]interface{} {
	bindings := make([]map[string]interface{}, 0, len(resources))
	for _, binding := range resources {
		bindingData := map[string]interface{}{
			"guid":       binding.GUID,
			"name":       binding.Name,
			"type":       binding.Type,
			"created_at": binding.CreatedAt,
			"updated_at": binding.UpdatedAt,
		}

		if binding.Relationships.App != nil && binding.Relationships.App.Data != nil {
			bindingData["app_guid"] = binding.Relationships.App.Data.GUID
		}

		bindings = append(bindings, bindingData)
	}

	return bindings
}

func (h *Handler) sendServiceBindingsResponse(writer http.ResponseWriter, endpointName string, orgGUID string, spaceGUID string, serviceGUID string, bindings []map[string]interface{}) {
	response.JSON(writer, http.StatusOK, map[string]interface{}{
		"endpoint":     endpointName,
		"org_guid":     orgGUID,
		"space_guid":   spaceGUID,
		"service_guid": serviceGUID,
		"bindings":     bindings,
	})
}
