package cf

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"blacksmith/pkg/reconciler"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Static errors for err113 compliance.
var (
	ErrServiceInstanceNotFoundInCF = errors.New("service instance not found in CF")
)

// DiscoverAllServiceInstances discovers all service instances from all CF endpoints
// that belong to services offered by this broker.
func (m *Manager) DiscoverAllServiceInstances(brokerServices []string) []reconciler.CFServiceInstanceDetails {
	if len(m.clients) == 0 {
		m.logger.Debug("skipping CF service instance discovery: no CF endpoints configured")

		return nil
	}

	var (
		allInstances []reconciler.CFServiceInstanceDetails
		mutex        sync.Mutex
	)

	// Process each healthy CF endpoint

	for endpointName, client := range m.GetHealthyClients() {
		m.logger.Debug("discovering service instances from CF endpoint: %s", endpointName)

		instances, err := m.discoverServiceInstancesFromEndpoint(client, brokerServices, endpointName)
		if err != nil {
			m.logger.Error("failed to discover service instances from CF endpoint %s: %v", endpointName, err)

			continue
		}

		mutex.Lock()

		allInstances = append(allInstances, instances...)

		mutex.Unlock()

		m.logger.Info("discovered %d service instances from CF endpoint %s", len(instances), endpointName)
	}

	m.logger.Info("total service instances discovered from CF: %d", len(allInstances))

	return allInstances
}

// processOrganizations processes all organizations for service instance discovery.
func (m *Manager) processOrganizations(ctx context.Context, client capi.Client, brokerServices []string) ([]reconciler.CFServiceInstanceDetails, error) {
	orgParams := capi.NewQueryParams().WithPerPage(DefaultCFPerPage)

	orgResponse, err := client.Organizations().List(ctx, orgParams)
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	var allInstances []reconciler.CFServiceInstanceDetails

	for _, org := range orgResponse.Resources {
		orgInstances := m.processOrganization(ctx, client, org, brokerServices)
		allInstances = append(allInstances, orgInstances...)
	}

	return allInstances, nil
}

// processOrganization processes a single organization for service instance discovery.
func (m *Manager) processOrganization(ctx context.Context, client capi.Client, org capi.Organization, brokerServices []string) []reconciler.CFServiceInstanceDetails {
	spaceParams := capi.NewQueryParams().WithFilter("organization_guids", org.GUID).WithPerPage(DefaultCFPerPage)

	spaceResponse, err := client.Spaces().List(ctx, spaceParams)
	if err != nil {
		m.logger.Debug("failed to list spaces for org %s: %v", org.Name, err)

		return nil
	}

	m.logger.Debug("found %d spaces in org %s", len(spaceResponse.Resources), org.Name)

	var orgInstances []reconciler.CFServiceInstanceDetails

	for _, space := range spaceResponse.Resources {
		spaceInstances := m.processSpace(ctx, client, org, space, brokerServices)
		orgInstances = append(orgInstances, spaceInstances...)
	}

	return orgInstances
}

// processSpace processes a single space for service instance discovery.
func (m *Manager) processSpace(ctx context.Context, client capi.Client, org capi.Organization, space capi.Space, brokerServices []string) []reconciler.CFServiceInstanceDetails {
	siParams := capi.NewQueryParams().WithFilter("space_guids", space.GUID).WithPerPage(DefaultCFPerPage)

	siResponse, err := client.ServiceInstances().List(ctx, siParams)
	if err != nil {
		m.logger.Debug("failed to list service instances for space %s: %v", space.Name, err)

		return nil
	}

	m.logger.Debug("found %d service instances in space %s/%s", len(siResponse.Resources), org.Name, space.Name)

	var spaceInstances []reconciler.CFServiceInstanceDetails

	for _, serviceInstance := range siResponse.Resources {
		if m.isServiceInstanceManagedByBroker(serviceInstance, brokerServices) {
			instanceDetails := m.createServiceInstanceDetails(ctx, client, org, space, serviceInstance)
			spaceInstances = append(spaceInstances, instanceDetails)

			m.logger.Debug("added service instance %s (%s) from %s/%s", serviceInstance.Name, serviceInstance.GUID, org.Name, space.Name)
		}
	}

	return spaceInstances
}

// createServiceInstanceDetails creates service instance details with maintenance info.
func (m *Manager) createServiceInstanceDetails(ctx context.Context, client capi.Client, org capi.Organization, space capi.Space, serviceInstance capi.ServiceInstance) reconciler.CFServiceInstanceDetails {
	instanceDetails := reconciler.CFServiceInstanceDetails{
		GUID:           serviceInstance.GUID,
		Name:           serviceInstance.Name,
		ServiceID:      serviceInstance.GUID,
		PlanID:         serviceInstance.GUID,
		OrganizationID: org.GUID,
		SpaceID:        space.GUID,
		CreatedAt:      serviceInstance.CreatedAt,
		UpdatedAt:      serviceInstance.UpdatedAt,
	}

	instanceDetails.MaintenanceInfo = map[string]interface{}{
		"service_name":    serviceInstance.Name,
		"org_name":        org.Name,
		"org_guid":        org.GUID,
		"space_name":      space.Name,
		"space_guid":      space.GUID,
		"bindings":        m.getServiceBindings(ctx, client, serviceInstance.GUID),
		"last_checked_at": time.Now(),
	}

	return instanceDetails
}

// discoverServiceInstancesFromEndpoint discovers service instances from a single CF endpoint.
func (m *Manager) discoverServiceInstancesFromEndpoint(client capi.Client, brokerServices []string, endpointName string) ([]reconciler.CFServiceInstanceDetails, error) {
	const discoveryTimeoutMinutes = 2

	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeoutMinutes*time.Minute)
	defer cancel()

	instances, err := m.processOrganizations(ctx, client, brokerServices)
	if err != nil {
		return nil, err
	}

	m.logger.Debug("found %d organizations in CF endpoint %s", len(instances), endpointName)

	return instances, nil
}

// isServiceInstanceManagedByBroker checks if a service instance belongs to our broker.
func (m *Manager) isServiceInstanceManagedByBroker(serviceInstance capi.ServiceInstance, brokerServices []string) bool {
	// If no broker services specified, include all (for discovery purposes)
	if len(brokerServices) == 0 {
		return true
	}

	// Check service name against broker services - simplified for now
	// TODO: Implement proper relationship parsing when CAPI structure is clarified
	if m.matchesDirectServiceName(serviceInstance.Name, brokerServices) {
		return true
	}

	// Additional check: look for blacksmith-specific patterns in service instance name
	if m.matchesServiceParts(serviceInstance.Name, brokerServices) {
		return true
	}

	// Enhanced matching for common service abbreviations
	return m.matchesServiceAbbreviations(serviceInstance.Name, brokerServices)
}

// matchesDirectServiceName checks if the service instance name directly matches any broker service.
func (m *Manager) matchesDirectServiceName(instanceName string, brokerServices []string) bool {
	lowerInstanceName := strings.ToLower(instanceName)

	for _, serviceName := range brokerServices {
		if strings.Contains(lowerInstanceName, strings.ToLower(serviceName)) {
			return true
		}
	}

	return false
}

// matchesServiceParts checks if the service instance name contains parts of broker service names.
func (m *Manager) matchesServiceParts(instanceName string, brokerServices []string) bool {
	lowerInstanceName := strings.ToLower(instanceName)

	// Common patterns in service instance names
	for _, serviceName := range brokerServices {
		lowerServiceName := strings.ToLower(serviceName)

		// Check for hyphenated patterns (e.g., "redis-service" from "redis")
		if strings.Contains(lowerInstanceName, lowerServiceName+"-") ||
			strings.Contains(lowerInstanceName, "-"+lowerServiceName) {
			return true
		}

		// Check for prefix/suffix patterns
		if strings.HasPrefix(lowerInstanceName, lowerServiceName+"-") ||
			strings.HasSuffix(lowerInstanceName, "-"+lowerServiceName) {
			return true
		}

		// Check for common service patterns with numbers or IDs
		serviceParts := strings.Split(lowerServiceName, "-")
		for _, part := range serviceParts {
			if len(part) > 2 && strings.Contains(lowerInstanceName, part) {
				// Found a substantial part of the service name
				return true
			}
		}
	}

	return false
}

// matchesServiceAbbreviations checks for common service name abbreviations.
func (m *Manager) matchesServiceAbbreviations(instanceName string, brokerServices []string) bool {
	lowerInstanceName := strings.ToLower(instanceName)

	// Define common abbreviations and their full forms
	abbreviations := map[string][]string{
		"redis":      {"rd", "rds"},
		"postgresql": {"pg", "postgres", "psql"},
		"mysql":      {"my", "msql"},
		"mongodb":    {"mongo", "mdb"},
		"rabbitmq":   {"rmq", "rabbit"},
		"kafka":      {"kf"},
	}

	for _, serviceName := range brokerServices {
		lowerServiceName := strings.ToLower(serviceName)

		// Check if the service name has known abbreviations
		if abbrevs, exists := abbreviations[lowerServiceName]; exists {
			for _, abbrev := range abbrevs {
				if strings.Contains(lowerInstanceName, abbrev) {
					return true
				}
			}
		}

		// Check reverse - if the instance name is an abbreviation of the service
		for fullName, abbrevs := range abbreviations {
			if strings.Contains(lowerServiceName, fullName) {
				for _, abbrev := range abbrevs {
					if strings.Contains(lowerInstanceName, abbrev) {
						return true
					}
				}
			}
		}
	}

	return false
}

// getServiceBindings retrieves service bindings for a service instance.
func (m *Manager) getServiceBindings(ctx context.Context, client capi.Client, serviceInstanceGUID string) []map[string]interface{} {
	const bindingTimeoutSeconds = 30

	timeoutCtx, cancel := context.WithTimeout(ctx, bindingTimeoutSeconds*time.Second)
	defer cancel()

	// Get service bindings for this service instance
	bindingParams := capi.NewQueryParams().
		WithFilter("service_instance_guids", serviceInstanceGUID).
		WithPerPage(DefaultCFPerPage)

	bindingResponse, err := client.ServiceCredentialBindings().List(timeoutCtx, bindingParams)
	if err != nil {
		m.logger.Debug("failed to get service bindings for instance %s: %v", serviceInstanceGUID, err)

		return nil
	}

	bindings := make([]map[string]interface{}, 0, len(bindingResponse.Resources))

	for _, binding := range bindingResponse.Resources {
		bindingInfo := map[string]interface{}{
			"guid": binding.GUID,
			"name": binding.Name,
			"type": binding.Type,
		}

		// Add app information if available
		if binding.Relationships.App != nil && binding.Relationships.App.Data != nil {
			bindingInfo["app_guid"] = binding.Relationships.App.Data.GUID
		}

		bindings = append(bindings, bindingInfo)
	}

	m.logger.Debug("found %d service bindings for instance %s", len(bindings), serviceInstanceGUID)

	return bindings
}

// EnrichServiceInstanceWithCF enriches a service instance with CF metadata.
func (m *Manager) EnrichServiceInstanceWithCF(instanceID, serviceName string) map[string]interface{} {
	if len(m.clients) == 0 {
		m.logger.Debug("no CF endpoints available for enrichment")

		return map[string]interface{}{}
	}

	for endpointName, client := range m.GetHealthyClients() {
		cfData, err := m.findServiceInstanceInCF(client, instanceID, serviceName, endpointName)
		if err == nil {
			m.logger.Debug("enriched service instance %s with CF data from %s", instanceID, endpointName)

			return cfData
		}
	}

	m.logger.Debug("no CF data found for service instance %s", instanceID)

	return map[string]interface{}{}
}

// findServiceInstanceInCF finds a service instance in a specific CF endpoint.
func (m *Manager) findServiceInstanceInCF(client capi.Client, instanceID, serviceName, _ string) (map[string]interface{}, error) {
	const searchTimeoutSeconds = 30

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeoutSeconds*time.Second)
	defer cancel()

	// Search by service instance name first (more likely to match)
	if serviceName != "" {
		params := capi.NewQueryParams().
			WithFilter("names", serviceName).
			WithPerPage(DefaultCFPerPage)

		response, err := client.ServiceInstances().List(ctx, params)
		if err == nil && len(response.Resources) > 0 {
			// Found by name, return the first match
			serviceInstance := response.Resources[0]

			return map[string]interface{}{
				"guid":       serviceInstance.GUID,
				"name":       serviceInstance.Name,
				"created_at": serviceInstance.CreatedAt,
				"updated_at": serviceInstance.UpdatedAt,
			}, nil
		}
	}

	// Try searching by GUID if instanceID looks like a GUID
	if len(instanceID) > 10 && strings.Contains(instanceID, "-") {
		serviceInstance, err := client.ServiceInstances().Get(ctx, instanceID)
		if err == nil {
			return map[string]interface{}{
				"guid":       serviceInstance.GUID,
				"name":       serviceInstance.Name,
				"created_at": serviceInstance.CreatedAt,
				"updated_at": serviceInstance.UpdatedAt,
			}, nil
		}
	}

	return nil, ErrServiceInstanceNotFoundInCF
}
