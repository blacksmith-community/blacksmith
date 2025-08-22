package reconciler

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

type serviceMatcher struct {
	broker interface{} // Will be replaced with actual Broker type
	logger Logger
}

// NewServiceMatcher creates a new service matcher
func NewServiceMatcher(broker interface{}, logger Logger) Matcher {
	return &serviceMatcher{
		broker: broker,
		logger: logger,
	}
}

// MatchDeployment matches a deployment to a service and plan
func (m *serviceMatcher) MatchDeployment(deployment DeploymentInfo, services []Service) (*MatchResult, error) {
	m.logDebug("Matching deployment %s", deployment.Name)

	// Try to parse deployment name (format: service-plan-instanceID)
	// First, extract the UUID at the end
	uuidPattern := regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	matches := uuidPattern.FindStringSubmatch(deployment.Name)

	if len(matches) < 2 {
		m.logDebug("No UUID found in deployment name %s", deployment.Name)
		// Try alternative matching strategies
		result := m.tryAlternativeMatching(deployment, services)
		if result != nil {
			m.logInfo("Found alternative match for deployment %s", deployment.Name)
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
				m.logDebug("Found exact match: service=%s, plan=%s", service.ID, plan.ID)

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
					if confidence < 0.5 {
						m.logWarning("Low confidence match for %s: %f", deployment.Name, confidence)
						result.MatchReason = "low_confidence_manifest"
					} else if confidence == 1.0 {
						result.MatchReason = "perfect_manifest_match"
					} else {
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
		m.logInfo("Found alternative match for deployment %s", deployment.Name)
		return result, nil
	}

	m.logDebug("No match found for deployment %s", deployment.Name)
	return nil, nil
}

// ValidateMatch validates a match result
func (m *serviceMatcher) ValidateMatch(match *MatchResult) error {
	if match == nil {
		return fmt.Errorf("match is nil")
	}

	if match.Confidence < 0.3 {
		return fmt.Errorf("confidence too low: %f", match.Confidence)
	}

	// Validate service ID
	if match.ServiceID == "" {
		return fmt.Errorf("service ID is empty")
	}

	// Validate plan ID
	if match.PlanID == "" {
		return fmt.Errorf("plan ID is empty")
	}

	// Validate instance ID format
	if !isValidUUID(match.InstanceID) {
		return fmt.Errorf("invalid instance ID format: %s", match.InstanceID)
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
		return fmt.Errorf("service not found: %s", match.ServiceID)
	}

	if !planFound {
		return fmt.Errorf("plan not found: %s in service %s", match.PlanID, match.ServiceID)
	}

	return nil
}

// tryAlternativeMatching tries alternative matching strategies
func (m *serviceMatcher) tryAlternativeMatching(deployment DeploymentInfo, services []Service) *MatchResult {
	// Strategy 1: Check manifest for blacksmith metadata
	if deployment.Manifest != "" {
		var manifest map[string]interface{}
		if err := yaml.Unmarshal([]byte(deployment.Manifest), &manifest); err == nil {
			m.logDebug("Parsed manifest for %s", deployment.Name)
			if props, ok := manifest["properties"].(map[interface{}]interface{}); ok {
				m.logDebug("Found properties section (map[interface{}]interface{})")
				if blacksmith, ok := props["blacksmith"].(map[interface{}]interface{}); ok {
					m.logDebug("Found blacksmith section (map[interface{}]interface{})")
					serviceID, _ := blacksmith["service_id"].(string)
					planID, _ := blacksmith["plan_id"].(string)
					instanceID, _ := blacksmith["instance_id"].(string)

					if serviceID != "" && planID != "" && instanceID != "" {
						m.logDebug("Found blacksmith metadata in manifest: service=%s, plan=%s, instance=%s",
							serviceID, planID, instanceID)
						return &MatchResult{
							ServiceID:   serviceID,
							PlanID:      planID,
							InstanceID:  instanceID,
							Confidence:  0.9,
							MatchReason: "manifest_metadata",
						}
					}
				}
			} else if props, ok := manifest["properties"].(map[string]interface{}); ok {
				m.logDebug("Found properties section (map[string]interface{})")
				if blacksmith, ok := props["blacksmith"].(map[string]interface{}); ok {
					m.logDebug("Found blacksmith section (map[string]interface{})")
					serviceID, _ := blacksmith["service_id"].(string)
					planID, _ := blacksmith["plan_id"].(string)
					instanceID, _ := blacksmith["instance_id"].(string)

					if serviceID != "" && planID != "" && instanceID != "" {
						m.logDebug("Found blacksmith metadata in manifest: service=%s, plan=%s, instance=%s",
							serviceID, planID, instanceID)
						return &MatchResult{
							ServiceID:   serviceID,
							PlanID:      planID,
							InstanceID:  instanceID,
							Confidence:  0.9,
							MatchReason: "manifest_metadata",
						}
					}
				} else if blacksmith, ok := props["blacksmith"].(map[interface{}]interface{}); ok {
					m.logDebug("Found blacksmith section (map[interface{}]interface{})")
					serviceID, _ := blacksmith["service_id"].(string)
					planID, _ := blacksmith["plan_id"].(string)
					instanceID, _ := blacksmith["instance_id"].(string)

					if serviceID != "" && planID != "" && instanceID != "" {
						m.logDebug("Found blacksmith metadata in manifest: service=%s, plan=%s, instance=%s",
							serviceID, planID, instanceID)
						return &MatchResult{
							ServiceID:   serviceID,
							PlanID:      planID,
							InstanceID:  instanceID,
							Confidence:  0.9,
							MatchReason: "manifest_metadata",
						}
					}
				}
			}

			// Check meta section
			if meta, ok := manifest["meta"].(map[string]interface{}); ok {
				if params, ok := meta["params"].(map[string]interface{}); ok {
					instanceID, _ := params["instance_id"].(string)
					if instanceID != "" && isValidUUID(instanceID) {
						// Try to infer service and plan from deployment name
						for _, service := range services {
							for _, plan := range service.Plans {
								if strings.HasPrefix(deployment.Name, plan.ID+"-") {
									return &MatchResult{
										ServiceID:   service.ID,
										PlanID:      plan.ID,
										InstanceID:  instanceID,
										Confidence:  0.7,
										MatchReason: "meta_params_inference",
									}
								}
							}
						}
					}
				}
			} else if meta, ok := manifest["meta"].(map[interface{}]interface{}); ok {
				if params, ok := meta["params"].(map[interface{}]interface{}); ok {
					instanceID, _ := params["instance_id"].(string)
					if instanceID != "" && isValidUUID(instanceID) {
						// Try to infer service and plan from deployment name
						for _, service := range services {
							for _, plan := range service.Plans {
								if strings.HasPrefix(deployment.Name, plan.ID+"-") {
									return &MatchResult{
										ServiceID:   service.ID,
										PlanID:      plan.ID,
										InstanceID:  instanceID,
										Confidence:  0.7,
										MatchReason: "meta_params_inference",
									}
								}
							}
						}
					}
				}
			}
		} else {
			m.logDebug("Failed to parse manifest: %v", err)
		}
	}

	// Strategy 2: Pattern matching with service/plan names
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
						Confidence:  0.5,
						MatchReason: "pattern_matching",
					}
				}
			}
		}
	}

	return nil
}

// validateManifest validates a deployment manifest against a service/plan
func (m *serviceMatcher) validateManifest(manifest string, service Service, plan Plan) float64 {
	confidence := 1.0

	// Parse manifest
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		m.logError("Failed to parse manifest: %s", err)
		return 0.5
	}

	// Check for blacksmith-specific properties
	if props, ok := parsed["properties"].(map[string]interface{}); ok {
		if blacksmith, ok := props["blacksmith"].(map[string]interface{}); ok {
			// Perfect match if blacksmith metadata exists and matches
			if svcID, ok := blacksmith["service_id"].(string); ok && svcID == service.ID {
				if pID, ok := blacksmith["plan_id"].(string); ok && pID == plan.ID {
					return 1.0 // Perfect match
				}
			}
			// Partial match if blacksmith metadata exists but doesn't match
			return 0.3
		}
	}

	// Check for expected releases
	if releases, ok := parsed["releases"].([]interface{}); ok {
		expectedReleases := m.getExpectedReleases(service, plan)
		if len(expectedReleases) > 0 {
			matchCount := 0
			for _, rel := range releases {
				if relMap, ok := rel.(map[string]interface{}); ok {
					relName, _ := relMap["name"].(string)
					for _, expected := range expectedReleases {
						if relName == expected {
							matchCount++
							break
						}
					}
				}
			}
			releaseConfidence := float64(matchCount) / float64(len(expectedReleases))
			confidence *= (0.5 + 0.5*releaseConfidence) // Releases contribute 50% to confidence
		}
	}

	// Check for expected instance groups
	if instanceGroups, ok := parsed["instance_groups"].([]interface{}); ok {
		expectedGroups := m.getExpectedInstanceGroups(service, plan)
		if len(expectedGroups) > 0 {
			matchCount := 0
			for _, group := range instanceGroups {
				if groupMap, ok := group.(map[string]interface{}); ok {
					groupName, _ := groupMap["name"].(string)
					for _, expected := range expectedGroups {
						if groupName == expected {
							matchCount++
							break
						}
					}
				}
			}
			groupConfidence := float64(matchCount) / float64(len(expectedGroups))
			confidence *= (0.7 + 0.3*groupConfidence) // Instance groups contribute 30% to confidence
		}
	}

	return confidence
}

// getServicesFromBroker gets services from the broker
func (m *serviceMatcher) getServicesFromBroker() []Service {
	// Try to get services from the actual broker if available
	if broker, ok := m.broker.(interface{ GetServices() []Service }); ok {
		return broker.GetServices()
	}

	// This is a placeholder - will be replaced with actual broker integration
	// For now, return some common service patterns
	return []Service{
		{
			ID:   "postgresql",
			Name: "PostgreSQL",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone"},
				{ID: "cluster", Name: "Cluster"},
			},
		},
		{
			ID:   "redis",
			Name: "Redis",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone"},
				{ID: "cluster", Name: "Cluster"},
			},
		},
		{
			ID:   "rabbitmq",
			Name: "RabbitMQ",
			Plans: []Plan{
				{ID: "standalone", Name: "Standalone"},
				{ID: "cluster", Name: "Cluster"},
			},
		},
		{
			ID:   "vault",
			Name: "Vault",
			Plans: []Plan{
				{ID: "small", Name: "Small"},
				{ID: "medium", Name: "Medium"},
				{ID: "large", Name: "Large"},
			},
		},
	}
}

// getExpectedReleases gets expected releases for a service/plan
func (m *serviceMatcher) getExpectedReleases(service Service, plan Plan) []string {
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

// getExpectedInstanceGroups gets expected instance groups for a service/plan
func (m *serviceMatcher) getExpectedInstanceGroups(service Service, plan Plan) []string {
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

// isValidUUID checks if a string is a valid UUID
func isValidUUID(s string) bool {
	uuidRegex := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	return uuidRegex.MatchString(s)
}

// Logging helper methods - these will be replaced with actual logger calls
func (m *serviceMatcher) logDebug(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Debug(format, args...)
	} else {
		fmt.Printf("[DEBUG] matcher: "+format+"\n", args...)
	}
}

func (m *serviceMatcher) logInfo(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Info(format, args...)
	} else {
		fmt.Printf("[INFO] matcher: "+format+"\n", args...)
	}
}

func (m *serviceMatcher) logWarning(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Warning(format, args...)
	} else {
		fmt.Printf("[WARN] matcher: "+format+"\n", args...)
	}
}

func (m *serviceMatcher) logError(format string, args ...interface{}) {
	if m.logger != nil {
		m.logger.Error(format, args...)
	} else {
		fmt.Printf("[ERROR] matcher: "+format+"\n", args...)
	}
}
