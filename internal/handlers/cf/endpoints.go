package cf

import (
	"context"
	"net/http"
	"time"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"

	"blacksmith/pkg/http/response"
)

// GetMarketplace handles GET /b/cf/endpoints/{name}/marketplace
func (h *Handler) GetMarketplace(w http.ResponseWriter, r *http.Request, endpointName string) {
	logger := h.logger.Named("cf-get-marketplace")
	logger.Debug("getting marketplace services for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(w, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusServiceUnavailable, "failed to connect to CF endpoint")
		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(w, http.StatusInternalServerError, "invalid CF client type")
		return
	}

	// Get service offerings (marketplace services)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	params := capi.NewQueryParams().WithPerPage(100)
	resp, err := client.ServiceOfferings().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service offerings from CF endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusBadGateway, "failed to get marketplace services from CF")
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

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"endpoint": endpointName,
		"services": services,
	})
}

// GetOrganizations handles GET /b/cf/endpoints/{name}/orgs
func (h *Handler) GetOrganizations(w http.ResponseWriter, r *http.Request, endpointName string) {
	logger := h.logger.Named("cf-get-organizations")
	logger.Debug("getting organizations for CF endpoint %s", endpointName)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(w, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusServiceUnavailable, "failed to connect to CF endpoint")
		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(w, http.StatusInternalServerError, "invalid CF client type")
		return
	}

	// Get organizations
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	params := capi.NewQueryParams().WithPerPage(100)
	resp, err := client.Organizations().List(ctx, params)
	if err != nil {
		logger.Error("failed to get organizations from CF endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusBadGateway, "failed to get organizations from CF")
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

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"endpoint":      endpointName,
		"organizations": orgs,
	})
}

// GetSpaces handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces
func (h *Handler) GetSpaces(w http.ResponseWriter, r *http.Request, endpointName string, orgGUID string) {
	logger := h.logger.Named("cf-get-spaces")
	logger.Debug("getting spaces for CF endpoint %s, org %s", endpointName, orgGUID)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(w, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusServiceUnavailable, "failed to connect to CF endpoint")
		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(w, http.StatusInternalServerError, "invalid CF client type")
		return
	}

	// Get spaces for the organization
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("organization_guids", orgGUID).WithPerPage(100)
	resp, err := client.Spaces().List(ctx, params)
	if err != nil {
		logger.Error("failed to get spaces from CF endpoint %s for org %s: %s", endpointName, orgGUID, err)
		response.WriteError(w, http.StatusBadGateway, "failed to get spaces from CF")
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

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"endpoint": endpointName,
		"org_guid": orgGUID,
		"spaces":   spaces,
	})
}

// GetServices handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/services
func (h *Handler) GetServices(w http.ResponseWriter, r *http.Request, endpointName string, orgGUID string, spaceGUID string) {
	logger := h.logger.Named("cf-get-services")
	logger.Debug("getting services for CF endpoint %s, org %s, space %s", endpointName, orgGUID, spaceGUID)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(w, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusServiceUnavailable, "failed to connect to CF endpoint")
		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(w, http.StatusInternalServerError, "invalid CF client type")
		return
	}

	// Get service instances for the space
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("space_guids", spaceGUID).WithPerPage(100)
	resp, err := client.ServiceInstances().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service instances from CF endpoint %s for space %s: %s", endpointName, spaceGUID, err)
		response.WriteError(w, http.StatusBadGateway, "failed to get service instances from CF")
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

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"endpoint":   endpointName,
		"org_guid":   orgGUID,
		"space_guid": spaceGUID,
		"services":   services,
	})
}

// GetServiceBindings handles GET /b/cf/endpoints/{name}/orgs/{org_guid}/spaces/{space_guid}/service_instances/{service_guid}/bindings
func (h *Handler) GetServiceBindings(w http.ResponseWriter, r *http.Request, endpointName string, orgGUID string, spaceGUID string, serviceGUID string) {
	logger := h.logger.Named("cf-get-service-bindings")
	logger.Debug("getting service bindings for CF endpoint %s, org %s, space %s, service %s", endpointName, orgGUID, spaceGUID, serviceGUID)

	// Check if CF manager is initialized
	if h.cfManager == nil {
		response.WriteError(w, http.StatusServiceUnavailable, "CF functionality disabled - no CF endpoints configured")
		return
	}

	// Get CF client through CF manager (handles health checks and retries)
	clientInterface, err := h.cfManager.GetClient(endpointName)
	if err != nil {
		logger.Error("failed to get CF client for endpoint %s: %s", endpointName, err)
		response.WriteError(w, http.StatusServiceUnavailable, "failed to connect to CF endpoint")
		return
	}

	// Type assert to capi.Client
	client, ok := clientInterface.(capi.Client)
	if !ok {
		logger.Error("CF client is not of expected type for endpoint %s", endpointName)
		response.WriteError(w, http.StatusInternalServerError, "invalid CF client type")
		return
	}

	// Get service credential bindings for the service instance
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	params := capi.NewQueryParams().WithFilter("service_instance_guids", serviceGUID).WithPerPage(100)
	resp, err := client.ServiceCredentialBindings().List(ctx, params)
	if err != nil {
		logger.Error("failed to get service bindings from CF endpoint %s for service %s: %s", endpointName, serviceGUID, err)
		response.WriteError(w, http.StatusBadGateway, "failed to get service bindings from CF")
		return
	}

	// Format response
	bindings := make([]map[string]interface{}, 0, len(resp.Resources))
	for _, binding := range resp.Resources {
		bindingData := map[string]interface{}{
			"guid":       binding.GUID,
			"name":       binding.Name,
			"type":       binding.Type,
			"created_at": binding.CreatedAt,
			"updated_at": binding.UpdatedAt,
		}

		// Add app information if available
		if binding.Relationships.App != nil && binding.Relationships.App.Data != nil {
			bindingData["app_guid"] = binding.Relationships.App.Data.GUID
		}

		bindings = append(bindings, bindingData)
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"endpoint":     endpointName,
		"org_guid":     orgGUID,
		"space_guid":   spaceGUID,
		"service_guid": serviceGUID,
		"bindings":     bindings,
	})
}
