package reconciler

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

// Static errors for err113 compliance.
var (
	ErrMatchIsNil              = errors.New("match is nil")
	ErrConfidenceTooLow        = errors.New("confidence too low")
	ErrServiceIDIsEmpty        = errors.New("service ID is empty")
	ErrPlanIDIsEmpty           = errors.New("plan ID is empty")
	ErrInvalidInstanceIDFormat = errors.New("invalid instance ID format")
	ErrServiceNotFound         = errors.New("service not found")
	ErrPlanNotFound            = errors.New("plan not found")
)

type ServiceMatcher struct {
	broker interface{} // Will be replaced with actual Broker type
	logger Logger
}

// NewServiceMatcher creates a new service matcher.
func NewServiceMatcher(broker interface{}, logger Logger) *ServiceMatcher {
	return &ServiceMatcher{
		broker: broker,
		logger: logger,
	}
}

// MatchDeployment matches a deployment to a service and plan.
func (m *ServiceMatcher) MatchDeployment(deployment DeploymentDetail, services []Service) (*MatchResult, error) {
	m.logger.Debugf("Matching deployment %s", deployment.Name)

	// Try to parse deployment name (format: service-plan-instanceID)
	// First, extract the UUID at the end
	uuidPattern := regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	matches := uuidPattern.FindStringSubmatch(deployment.Name)

	if len(matches) < confidenceThresholdMin {
		m.logger.Debugf("No UUID found in deployment name %s", deployment.Name)
		// Try alternative matching strategies
		result := m.tryAlternativeMatching(deployment, services)
		if result != nil {
			m.logger.Infof("Found alternative match for deployment %s", deployment.Name)

			return result, nil
		}

		return nil, nil
	}

	instanceID := matches[1]
	// Remove the UUID and trailing dash to get service-plan
	servicePlanPrefix := strings.TrimSuffix(deployment.Name[:len(deployment.Name)-len(instanceID)], "-")

	// If services are nil, try to get them from broker
	if services == nil {
		services = m.getServicesFromBroker()
	}

	// Find matching service and plan
	// Try to match the full service-plan pattern
	for _, service := range services {
		for _, plan := range service.Plans {
			// Build expected prefix: service-plan
			expectedPrefix := fmt.Sprintf("%s-%s", service.ID, plan.ID)
			if servicePlanPrefix == expectedPrefix {
				m.logger.Debugf("Found exact match: service=%s, plan=%s", service.ID, plan.ID)

				result := &MatchResult{
					ServiceID:   service.ID,
					PlanID:      plan.ID,
					InstanceID:  instanceID,
					Confidence:  1.0,
					MatchReason: "exact_name_match",
				}

				// Validate against manifest if available
				if deployment.Manifest != "" {
					confidence := m.validateManifest(deployment.Manifest, service, plan)

					result.Confidence = confidence
					switch {
					case confidence < LowConfidenceThreshold:
						m.logger.Warningf("Low confidence match for %s: %f", deployment.Name, confidence)

						result.MatchReason = "low_confidence_manifest"
					case confidence == 1.0:
						result.MatchReason = "perfect_manifest_match"
					default:
						result.MatchReason = "partial_manifest_match"
					}
				}

				return result, nil
			}
		}
	}

	// Try alternative matching strategies
	result := m.tryAlternativeMatching(deployment, services)
	if result != nil {
		m.logger.Infof("Found alternative match for deployment %s", deployment.Name)

		return result, nil
	}

	m.logger.Debugf("No match found for deployment %s", deployment.Name)

	return nil, nil
}

// ValidateMatch validates a match result.
func (m *ServiceMatcher) ValidateMatch(match *MatchResult) error {
	if match == nil {
		return ErrMatchIsNil
	}

	if match.Confidence < VeryLowConfidenceThreshold {
		return fmt.Errorf("%w: %f", ErrConfidenceTooLow, match.Confidence)
	}

	// Validate service ID
	if match.ServiceID == "" {
		return ErrServiceIDIsEmpty
	}

	// Validate plan ID
	if match.PlanID == "" {
		return ErrPlanIDIsEmpty
	}

	// Validate instance ID format
	if !IsValidUUID(match.InstanceID) {
		return fmt.Errorf("%w: %s", ErrInvalidInstanceIDFormat, match.InstanceID)
	}

	// Additional validation could check if service and plan exist
	services := m.getServicesFromBroker()
	serviceFound := false
	planFound := false

	for _, service := range services {
		if service.ID == match.ServiceID {
			serviceFound = true

			for _, plan := range service.Plans {
				if plan.ID == match.PlanID {
					planFound = true

					break
				}
			}

			break
		}
	}

	if !serviceFound {
		return fmt.Errorf("%w: %s", ErrServiceNotFound, match.ServiceID)
	}

	if !planFound {
		return fmt.Errorf("%w: %s in service %s", ErrPlanNotFound, match.PlanID, match.ServiceID)
	}

	return nil
}

// tryAlternativeMatching tries alternative matching strategies.
func (m *ServiceMatcher) tryAlternativeMatching(deployment DeploymentDetail, services []Service) *MatchResult {
	// Strategy 1: Check manifest for blacksmith metadata
	if result := m.checkManifestMetadata(deployment); result != nil {
		return result
	}

	// Strategy 2: Pattern matching with service/plan names
	return m.tryPatternMatching(deployment, services)
}

// checkManifestMetadata checks the manifest for blacksmith metadata.
func (m *ServiceMatcher) checkManifestMetadata(deployment DeploymentDetail) *MatchResult {
	if deployment.Manifest == "" {
		return nil
	}

	var manifest map[string]interface{}
	if err := yaml.Unmarshal([]byte(deployment.Manifest), &manifest); err != nil {
		m.logger.Debugf("Failed to parse manifest: %v", err)

		return nil
	}

	m.logger.Debugf("Parsed manifest for %s", deployment.Name)

	// Check for blacksmith metadata in properties section
	if result := m.checkPropertiesSection(manifest); result != nil {
		return result
	}

	// Check meta section for instance ID
	return m.checkMetaSection(manifest, deployment)
}

// checkPropertiesSection checks the properties section for blacksmith metadata.
func (m *ServiceMatcher) checkPropertiesSection(manifest map[string]interface{}) *MatchResult {
	// Try map[interface{}]interface{} format
	if result := m.checkInterfaceProperties(manifest); result != nil {
		return result
	}

	// Try map[string]interface{} format
	return m.checkStringProperties(manifest)
}

// checkInterfaceProperties checks properties in map[interface{}]interface{} format.
func (m *ServiceMatcher) checkInterfaceProperties(manifest map[string]interface{}) *MatchResult {
	props, ok := manifest["properties"].(map[interface{}]interface{})
	if !ok {
		return nil
	}

	m.logger.Debugf("Found properties section (map[interface{}]interface{})")

	blacksmith, ok := props["blacksmith"].(map[interface{}]interface{})
	if !ok {
		return nil
	}

	return m.extractBlacksmithMetadataFromInterface(blacksmith)
}

// checkStringProperties checks properties in map[string]interface{} format.
func (m *ServiceMatcher) checkStringProperties(manifest map[string]interface{}) *MatchResult {
	props, ok := manifest["properties"].(map[string]interface{})
	if !ok {
		return nil
	}

	m.logger.Debugf("Found properties section (map[string]interface{})")

	// Try both map formats for blacksmith section
	if blacksmith, ok := props["blacksmith"].(map[string]interface{}); ok {
		return m.extractBlacksmithMetadataFromString(blacksmith)
	}

	if blacksmith, ok := props["blacksmith"].(map[interface{}]interface{}); ok {
		return m.extractBlacksmithMetadataFromInterface(blacksmith)
	}

	return nil
}

// extractBlacksmithMetadataFromInterface extracts metadata from interface{} map.
func (m *ServiceMatcher) extractBlacksmithMetadataFromInterface(blacksmith map[interface{}]interface{}) *MatchResult {
	m.logger.Debugf("Found blacksmith section (map[interface{}]interface{})")

	serviceID, _ := blacksmith["service_id"].(string)
	planID, _ := blacksmith["plan_id"].(string)
	instanceID, _ := blacksmith["instance_id"].(string)

	if serviceID != "" && planID != "" && instanceID != "" {
		m.logger.Debugf("Found blacksmith metadata in manifest: service=%s, plan=%s, instance=%s",
			serviceID, planID, instanceID)

		return &MatchResult{
			ServiceID:   serviceID,
			PlanID:      planID,
			InstanceID:  instanceID,
			Confidence:  deploymentConfidenceHigh,
			MatchReason: "manifest_metadata",
		}
	}

	return nil
}

// extractBlacksmithMetadataFromString extracts metadata from string map.
func (m *ServiceMatcher) extractBlacksmithMetadataFromString(blacksmith map[string]interface{}) *MatchResult {
	m.logger.Debugf("Found blacksmith section (map[string]interface{})")

	serviceID, _ := blacksmith["service_id"].(string)
	planID, _ := blacksmith["plan_id"].(string)
	instanceID, _ := blacksmith["instance_id"].(string)

	if serviceID != "" && planID != "" && instanceID != "" {
		m.logger.Debugf("Found blacksmith metadata in manifest: service=%s, plan=%s, instance=%s",
			serviceID, planID, instanceID)

		return &MatchResult{
			ServiceID:   serviceID,
			PlanID:      planID,
			InstanceID:  instanceID,
			Confidence:  deploymentConfidenceHigh,
			MatchReason: "manifest_metadata",
		}
	}

	return nil
}

// checkMetaSection checks the meta section for instance ID.
func (m *ServiceMatcher) checkMetaSection(manifest map[string]interface{}, deployment DeploymentDetail) *MatchResult {
	// Try map[string]interface{} format
	if result := m.checkStringMeta(manifest, deployment); result != nil {
		return result
	}

	// Try map[interface{}]interface{} format
	return m.checkInterfaceMeta(manifest, deployment)
}

// checkStringMeta checks meta section in map[string]interface{} format.
func (m *ServiceMatcher) checkStringMeta(manifest map[string]interface{}, deployment DeploymentDetail) *MatchResult {
	meta, ok := manifest["meta"].(map[string]interface{})
	if !ok {
		return nil
	}

	params, ok := meta["params"].(map[string]interface{})
	if !ok {
		return nil
	}

	instanceID, _ := params["instance_id"].(string)
	if instanceID != "" && IsValidUUID(instanceID) {
		return m.inferServiceFromDeploymentName(deployment.Name, instanceID)
	}

	return nil
}

// checkInterfaceMeta checks meta section in map[interface{}]interface{} format.
func (m *ServiceMatcher) checkInterfaceMeta(manifest map[string]interface{}, deployment DeploymentDetail) *MatchResult {
	meta, exists := manifest["meta"].(map[interface{}]interface{})
	if !exists {
		return nil
	}

	params, exists := meta["params"].(map[interface{}]interface{})
	if !exists {
		return nil
	}

	instanceID, _ := params["instance_id"].(string)
	if instanceID != "" && IsValidUUID(instanceID) {
		return m.inferServiceFromDeploymentName(deployment.Name, instanceID)
	}

	return nil
}

// inferServiceFromDeploymentName tries to infer service and plan from deployment name.
func (m *ServiceMatcher) inferServiceFromDeploymentName(deploymentName, instanceID string) *MatchResult {
	broker, ok := m.broker.(interface{ GetServices() []Service })
	if !ok {
		return nil
	}

	for _, service := range broker.GetServices() {
		for _, plan := range service.Plans {
			if strings.HasPrefix(deploymentName, plan.ID+"-") {
				return &MatchResult{
					ServiceID:   service.ID,
					PlanID:      plan.ID,
					InstanceID:  instanceID,
					Confidence:  deploymentConfidenceMid,
					MatchReason: "meta_params_inference",
				}
			}
		}
	}

	return nil
}

// tryPatternMatching attempts pattern matching with service/plan names.
func (m *ServiceMatcher) tryPatternMatching(deployment DeploymentDetail, services []Service) *MatchResult {
	for _, service := range services {
		for _, plan := range service.Plans {
			// Check if deployment name contains plan name
			if strings.Contains(strings.ToLower(deployment.Name), strings.ToLower(plan.Name)) {
				// Extract potential instance ID
				pattern := regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
				matches := pattern.FindStringSubmatch(deployment.Name)

				if len(matches) > 1 {
					return &MatchResult{
						ServiceID:   service.ID,
						PlanID:      plan.ID,
						InstanceID:  matches[1],
						Confidence:  deploymentConfidenceLow,
						MatchReason: "pattern_matching",
					}
				}
			}
		}
	}

	return nil
}

// validateManifest validates a deployment manifest against a service/plan.
func (m *ServiceMatcher) validateManifest(manifest string, service Service, plan Plan) float64 {
	parsed, err := m.parseManifest(manifest)
	if err != nil {
		return deploymentConfidenceLow
	}

	// Check for perfect match via blacksmith metadata
	if confidence := m.checkBlacksmithMetadata(parsed, service, plan); confidence > 0 {
		return confidence
	}

	// Calculate confidence based on releases and instance groups
	confidence := 1.0
	confidence = m.adjustConfidenceByReleases(parsed, service, plan, confidence)
	confidence = m.adjustConfidenceByInstanceGroups(parsed, service, plan, confidence)

	return confidence
}

func (m *ServiceMatcher) parseManifest(manifest string) (map[string]interface{}, error) {
	var parsed map[string]interface{}

	err := yaml.Unmarshal([]byte(manifest), &parsed)
	if err != nil {
		m.logger.Errorf("Failed to parse manifest: %s", err)

		return nil, err
	}

	return parsed, nil
}

func (m *ServiceMatcher) checkBlacksmithMetadata(parsed map[string]interface{}, service Service, plan Plan) float64 {
	props, ok := parsed["properties"].(map[string]interface{})
	if !ok {
		return 0
	}

	blacksmith, ok := props["blacksmith"].(map[string]interface{})
	if !ok {
		return 0
	}

	svcID, hasServiceID := blacksmith["service_id"].(string)
	planID, hasPlanID := blacksmith["plan_id"].(string)

	if hasServiceID && svcID == service.ID && hasPlanID && planID == plan.ID {
		return 1.0 // Perfect match
	}

	// Partial match if blacksmith metadata exists but doesn't match
	return deploymentConfidenceMin
}

func (m *ServiceMatcher) adjustConfidenceByReleases(parsed map[string]interface{}, service Service, plan Plan, confidence float64) float64 {
	releases, ok := parsed["releases"].([]interface{})
	if !ok {
		return confidence
	}

	expectedReleases := m.getExpectedReleases(service, plan)
	if len(expectedReleases) == 0 {
		return confidence
	}

	matchCount := m.countMatchingReleases(releases, expectedReleases)
	releaseConfidence := float64(matchCount) / float64(len(expectedReleases))

	return confidence * (releaseConfidenceFactor + releaseConfidenceFactor*releaseConfidence)
}

func (m *ServiceMatcher) countMatchingReleases(releases []interface{}, expectedReleases []string) int {
	matchCount := 0

	for _, rel := range releases {
		relMap, ok := rel.(map[string]interface{})
		if !ok {
			continue
		}

		relName, _ := relMap["name"].(string)
		for _, expected := range expectedReleases {
			if relName == expected {
				matchCount++

				break
			}
		}
	}

	return matchCount
}

func (m *ServiceMatcher) adjustConfidenceByInstanceGroups(parsed map[string]interface{}, service Service, plan Plan, confidence float64) float64 {
	instanceGroups, ok := parsed["instance_groups"].([]interface{})
	if !ok {
		return confidence
	}

	expectedGroups := m.getExpectedInstanceGroups(service, plan)
	if len(expectedGroups) == 0 {
		return confidence
	}

	matchCount := m.countMatchingInstanceGroups(instanceGroups, expectedGroups)
	groupConfidence := float64(matchCount) / float64(len(expectedGroups))

	return confidence * (groupConfidenceFactor + groupConfidenceWeight*groupConfidence)
}

func (m *ServiceMatcher) countMatchingInstanceGroups(instanceGroups []interface{}, expectedGroups []string) int {
	matchCount := 0

	for _, group := range instanceGroups {
		groupMap, ok := group.(map[string]interface{})
		if !ok {
			continue
		}

		groupName, _ := groupMap["name"].(string)
		for _, expected := range expectedGroups {
			if groupName == expected {
				matchCount++

				break
			}
		}
	}

	return matchCount
}

// getServicesFromBroker gets services from the broker.
func (m *ServiceMatcher) getServicesFromBroker() []Service {
	// Try to get services from the actual broker if available
	if broker, ok := m.broker.(interface{ GetServices() []Service }); ok {
		return broker.GetServices()
	}

	// This is a placeholder - will be replaced with actual broker integration
	// For now, return some common service patterns
	return []Service{
		{
			ID:          "postgresql",
			Name:        "PostgreSQL",
			Description: "",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone", Description: "", Properties: nil},
				{ID: "cluster", Name: "Cluster", Description: "", Properties: nil},
			},
		},
		{
			ID:          "redis",
			Name:        "Redis",
			Description: "",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone", Description: "", Properties: nil},
				{ID: "cluster", Name: "Cluster", Description: "", Properties: nil},
			},
		},
		{
			ID:          "rabbitmq",
			Name:        "RabbitMQ",
			Description: "",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone", Description: "", Properties: nil},
				{ID: "cluster", Name: "Cluster", Description: "", Properties: nil},
			},
		},
		{
			ID:          "vault",
			Name:        "Vault",
			Description: "",
			Plans: []Plan{
				{ID: "small", Name: "Small", Description: "", Properties: nil},
				{ID: "medium", Name: "Medium", Description: "", Properties: nil},
				{ID: "large", Name: "Large", Description: "", Properties: nil},
			},
		},
	}
}

// getExpectedReleases gets expected releases for a service/plan.
func (m *ServiceMatcher) getExpectedReleases(service Service, _ Plan) []string {
	// This would be populated from service definitions
	releaseMap := map[string][]string{
		"postgresql": {"postgres", "bpm"},
		"redis":      {"redis", "bpm"},
		"rabbitmq":   {"rabbitmq", "bpm", "routing"},
		"vault":      {"vault", "safe"},
	}

	if releases, ok := releaseMap[service.ID]; ok {
		return releases
	}

	return []string{}
}

// getExpectedInstanceGroups gets expected instance groups for a service/plan.
func (m *ServiceMatcher) getExpectedInstanceGroups(service Service, plan Plan) []string {
	// This would be populated from service definitions
	groupMap := map[string]map[string][]string{
		"postgresql": {
			"standalone": {"postgres"},
			"cluster":    {"postgres", "pgpool"},
		},
		"redis": {
			"standalone": {"redis"},
			"cluster":    {"redis-master", "redis-slave"},
		},
		"rabbitmq": {
			"standalone": {"rabbitmq"},
			"cluster":    {"rabbitmq"},
		},
		"vault": {
			"small":  {"vault"},
			"medium": {"vault"},
			"large":  {"vault"},
		},
	}

	if serviceGroups, ok := groupMap[service.ID]; ok {
		if groups, ok := serviceGroups[plan.ID]; ok {
			return groups
		}
	}

	return []string{}
}

// IsValidUUID checks if a string is a valid UUID.
func IsValidUUID(s string) bool {
	uuidRegex := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	return uuidRegex.MatchString(s)
}
