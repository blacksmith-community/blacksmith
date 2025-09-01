package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"blacksmith/bosh"
	"gopkg.in/yaml.v2"
)

type boshScanner struct {
	director bosh.Director
	logger   Logger
	cache    *deploymentCache
}

type deploymentCache struct {
	mu     sync.RWMutex
	data   map[string]*DeploymentDetail
	expiry map[string]time.Time
	ttl    time.Duration
}

// NewBOSHScanner creates a new BOSH scanner
func NewBOSHScanner(director bosh.Director, logger Logger) Scanner {
	return &boshScanner{
		director: director,
		logger:   logger,
		cache: &deploymentCache{
			data:   make(map[string]*DeploymentDetail),
			expiry: make(map[string]time.Time),
			ttl:    5 * time.Minute,
		},
	}
}

// ScanDeployments scans BOSH for all deployments
func (s *boshScanner) ScanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	s.logDebug("Scanning BOSH deployments")

	deployments, err := s.director.GetDeployments()
	if err != nil {
		return nil, fmt.Errorf("failed to get deployments: %w", err)
	}

	var result []DeploymentInfo
	for _, dep := range deployments {
		info := DeploymentInfo{
			Name:        dep.Name,
			CloudConfig: dep.CloudConfig,
			CreatedAt:   time.Now(), // BOSH doesn't provide this, approximate
			UpdatedAt:   time.Now(),
		}

		// Copy releases directly as strings
		info.Releases = dep.Releases

		// Copy stemcells directly as strings
		info.Stemcells = dep.Stemcells

		result = append(result, info)
	}

	s.logDebug("Found %d deployments", len(result))
	return result, nil
}

// GetDeploymentDetails gets detailed information about a deployment
func (s *boshScanner) GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error) {
	// Check cache first
	if cached := s.cache.get(name); cached != nil {
		s.logDebug("Using cached details for deployment %s", name)
		return cached, nil
	}

	s.logDebug("Fetching details for deployment %s", name)

	dep, err := s.director.GetDeployment(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	// Try to get deployment timestamps and latest task ID from events
	createdAt, updatedAt, latestTaskID, err := s.getDeploymentTimestamps(name)
	if err != nil {
		s.logError("Failed to get deployment timestamps for %s: %s", name, err)
		// Fall back to current time if events are unavailable
		createdAt = time.Now()
		updatedAt = time.Now()
		latestTaskID = ""
	} else {
		// Ensure we never have zero times even if events parsing succeeds
		if createdAt.IsZero() {
			s.logDebug("Got zero created time for %s, using current time", name)
			createdAt = time.Now()
		}
		if updatedAt.IsZero() {
			s.logDebug("Got zero updated time for %s, using created time", name)
			updatedAt = createdAt
		}
	}

	// Create detail with basic info from deployment
	detail := &DeploymentDetail{
		DeploymentInfo: DeploymentInfo{
			Name:      dep.Name,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		Manifest:   dep.Manifest,
		Properties: make(map[string]interface{}),
	}

	// We need to get additional info from the manifest or from a separate GetDeployments call
	// since DeploymentDetail only has Name and Manifest fields

	// Parse releases and stemcells from the manifest if available
	if dep.Manifest != "" {
		var manifestData map[string]interface{}
		if err := yaml.Unmarshal([]byte(dep.Manifest), &manifestData); err == nil {
			// Extract releases from manifest
			if releases, ok := manifestData["releases"].([]interface{}); ok {
				for _, rel := range releases {
					// Handle both map[string]interface{} and map[interface{}]interface{}
					if relMap, ok := rel.(map[string]interface{}); ok {
						var name, version string
						if n, ok := relMap["name"].(string); ok {
							name = n
						}
						if v, ok := relMap["version"].(string); ok {
							version = v
						} else if v, ok := relMap["version"].(int); ok {
							version = fmt.Sprintf("%d", v)
						}
						if name != "" {
							releaseStr := fmt.Sprintf("%s/%s", name, version)
							detail.Releases = append(detail.Releases, releaseStr)
						}
					} else if relMap, ok := rel.(map[interface{}]interface{}); ok {
						var name, version string
						if n, ok := relMap["name"].(string); ok {
							name = n
						}
						if v, ok := relMap["version"].(string); ok {
							version = v
						} else if v, ok := relMap["version"].(int); ok {
							version = fmt.Sprintf("%d", v)
						} else if v, ok := relMap["version"].(float64); ok {
							version = fmt.Sprintf("%.1f", v)
						}
						if name != "" {
							releaseStr := fmt.Sprintf("%s/%s", name, version)
							detail.Releases = append(detail.Releases, releaseStr)
						}
					}
				}
			}

			// Extract stemcells from manifest
			if stemcells, ok := manifestData["stemcells"].([]interface{}); ok {
				for _, stem := range stemcells {
					// Handle both map[string]interface{} and map[interface{}]interface{}
					if stemMap, ok := stem.(map[string]interface{}); ok {
						var alias, os, version string
						if a, ok := stemMap["alias"].(string); ok {
							alias = a
						}
						if o, ok := stemMap["os"].(string); ok {
							os = o
						}
						if v, ok := stemMap["version"].(string); ok {
							version = v
						} else if v, ok := stemMap["version"].(int); ok {
							version = fmt.Sprintf("%d", v)
						}
						// Format: alias/os/version or just os/version if no alias
						var stemcellStr string
						if alias != "" {
							stemcellStr = fmt.Sprintf("%s/%s/%s", alias, os, version)
						} else if os != "" {
							stemcellStr = fmt.Sprintf("%s/%s", os, version)
						}
						if stemcellStr != "" {
							detail.Stemcells = append(detail.Stemcells, stemcellStr)
						}
					} else if stemMap, ok := stem.(map[interface{}]interface{}); ok {
						var alias, os, version string
						if a, ok := stemMap["alias"].(string); ok {
							alias = a
						}
						if o, ok := stemMap["os"].(string); ok {
							os = o
						}
						if v, ok := stemMap["version"].(string); ok {
							version = v
						} else if v, ok := stemMap["version"].(int); ok {
							version = fmt.Sprintf("%d", v)
						}
						// Format: alias/os/version or just os/version if no alias
						var stemcellStr string
						if alias != "" {
							stemcellStr = fmt.Sprintf("%s/%s/%s", alias, os, version)
						} else if os != "" {
							stemcellStr = fmt.Sprintf("%s/%s", os, version)
						}
						if stemcellStr != "" {
							detail.Stemcells = append(detail.Stemcells, stemcellStr)
						}
					}
				}
			}
		}
	}

	// Get VMs
	vms, err := s.director.GetDeploymentVMs(name)
	if err != nil {
		s.logError("Failed to get VMs for %s: %s", name, err)
	} else {
		for _, vm := range vms {
			detail.VMs = append(detail.VMs, VMInfo{
				Name:  vm.ID, // Use ID as Name
				State: vm.State,
				IPs:   vm.IPs,
				AZ:    vm.AZ,
			})
		}
	}

	// Parse manifest for additional properties
	if detail.Manifest != "" {
		props, err := s.parseManifestProperties(detail.Manifest)
		if err != nil {
			s.logError("Failed to parse manifest properties: %s", err)
		} else {
			detail.Properties = props
		}
	}

	// Store the latest task ID in properties for use by the reconciler
	if latestTaskID != "" {
		if detail.Properties == nil {
			detail.Properties = make(map[string]interface{})
		}
		detail.Properties["latest_task_id"] = latestTaskID
	}

	// Cache the result
	s.cache.set(name, detail)

	return detail, nil
}

// getDeploymentTimestamps extracts creation/update timestamps and latest task ID from deployment events
func (s *boshScanner) getDeploymentTimestamps(deploymentName string) (createdAt, updatedAt time.Time, latestTaskID string, err error) {
	events, err := s.director.GetEvents(deploymentName)
	if err != nil {
		return time.Time{}, time.Time{}, "", fmt.Errorf("failed to get events for deployment %s: %w", deploymentName, err)
	}

	// Track the earliest create event and latest update/deploy event
	var earliestCreate time.Time
	var latestUpdate time.Time
	var taskID string

	for _, event := range events {
		// Skip events not related to this deployment
		if event.Deployment != deploymentName {
			continue
		}

		// Look for deployment-related actions
		isCreateAction := event.Action == "create" || event.Action == "deploy" || event.Action == "create_deployment"
		isUpdateAction := event.Action == "update" || event.Action == "deploy" || event.Action == "update_deployment"

		// Track the earliest creation event
		if isCreateAction && (earliestCreate.IsZero() || event.Time.Before(earliestCreate)) {
			earliestCreate = event.Time
		}

		// Track the latest update/deploy event and its task ID
		if (isCreateAction || isUpdateAction) && (latestUpdate.IsZero() || event.Time.After(latestUpdate)) {
			latestUpdate = event.Time
			if event.TaskID != "" {
				taskID = event.TaskID
			}
		}
	}

	// Set defaults if no events found
	if earliestCreate.IsZero() {
		earliestCreate = time.Now()
	}
	if latestUpdate.IsZero() {
		latestUpdate = earliestCreate
	}

	return earliestCreate, latestUpdate, taskID, nil
}

// parseManifestProperties extracts properties from a manifest
func (s *boshScanner) parseManifestProperties(manifest string) (map[string]interface{}, error) {
	var manifestData map[string]interface{}
	if err := yaml.Unmarshal([]byte(manifest), &manifestData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	result := make(map[string]interface{})

	// Extract properties (handles both map[string]interface{} and map[interface{}]interface{})
	if props, ok := manifestData["properties"].(map[string]interface{}); ok {
		result["properties"] = props
		// Extract blacksmith metadata if present
		if blacksmith, ok := props["blacksmith"].(map[string]interface{}); ok {
			result["blacksmith"] = blacksmith
		} else if blacksmith, ok := props["blacksmith"].(map[interface{}]interface{}); ok {
			// Convert to map[string]interface{}
			converted := make(map[string]interface{})
			for k, v := range blacksmith {
				if key, ok := k.(string); ok {
					converted[key] = v
				}
			}
			result["blacksmith"] = converted
		}
	} else if props, ok := manifestData["properties"].(map[interface{}]interface{}); ok {
		// Convert to map[string]interface{}
		convertedProps := make(map[string]interface{})
		for k, v := range props {
			if key, ok := k.(string); ok {
				convertedProps[key] = v
			}
		}
		result["properties"] = convertedProps
		// Extract blacksmith metadata if present
		if blacksmith, ok := convertedProps["blacksmith"].(map[string]interface{}); ok {
			result["blacksmith"] = blacksmith
		} else if blacksmith, ok := convertedProps["blacksmith"].(map[interface{}]interface{}); ok {
			// Convert to map[string]interface{}
			converted := make(map[string]interface{})
			for k, v := range blacksmith {
				if key, ok := k.(string); ok {
					converted[key] = v
				}
			}
			result["blacksmith"] = converted
		}
	}

	// Extract meta
	if meta, ok := manifestData["meta"].(map[string]interface{}); ok {
		result["meta"] = meta
	} else if meta, ok := manifestData["meta"].(map[interface{}]interface{}); ok {
		// Convert to map[string]interface{}
		converted := make(map[string]interface{})
		for k, v := range meta {
			if key, ok := k.(string); ok {
				converted[key] = v
			}
		}
		result["meta"] = converted
	}

	// Extract networks
	if networks := manifestData["networks"]; networks != nil {
		result["networks"] = networks
	}

	// Extract instance_groups
	if instanceGroups := manifestData["instance_groups"]; instanceGroups != nil {
		result["instance_groups"] = instanceGroups
	}

	return result, nil
}

// Cache methods

func (c *deploymentCache) get(name string) *DeploymentDetail {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if expiry, ok := c.expiry[name]; ok {
		if time.Now().After(expiry) {
			// Cache expired
			return nil
		}
		return c.data[name]
	}
	return nil
}

func (c *deploymentCache) set(name string, detail *DeploymentDetail) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[name] = detail
	c.expiry[name] = time.Now().Add(c.ttl)

	// Clean up expired entries
	c.cleanup()
}

func (c *deploymentCache) cleanup() {
	now := time.Now()
	for name, expiry := range c.expiry {
		if now.After(expiry) {
			delete(c.data, name)
			delete(c.expiry, name)
		}
	}
}

// Logging helper methods - these will be replaced with actual logger calls
func (s *boshScanner) logDebug(format string, args ...interface{}) {
	if s.logger != nil {
		s.logger.Debug(format, args...)
	} else {
		fmt.Printf("[DEBUG] scanner: "+format+"\n", args...)
	}
}

func (s *boshScanner) logError(format string, args ...interface{}) {
	if s.logger != nil {
		s.logger.Error(format, args...)
	} else {
		fmt.Printf("[ERROR] scanner: "+format+"\n", args...)
	}
}
