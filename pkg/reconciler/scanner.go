package reconciler

import (
	"context"
	"fmt"
	"strings"
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
			Teams:       dep.Teams,
			CreatedAt:   time.Now(), // BOSH doesn't provide this, approximate
			UpdatedAt:   time.Now(),
		}

		// Parse releases from string format (e.g., "redis/1.0.0")
		if dep.Releases != nil {
			for _, relStr := range dep.Releases {
				parts := strings.Split(relStr, "/")
				releaseInfo := ReleaseInfo{
					Name:    relStr, // Default to full string if no slash
					Version: "unknown",
				}
				if len(parts) >= 2 {
					releaseInfo.Name = parts[0]
					releaseInfo.Version = parts[1]
				}
				info.Releases = append(info.Releases, releaseInfo)
			}
		}

		// Parse stemcells from string format (e.g., "ubuntu-xenial/456.789")
		if dep.Stemcells != nil {
			for _, stemStr := range dep.Stemcells {
				parts := strings.Split(stemStr, "/")
				stemcellInfo := StemcellInfo{
					Name:    stemStr, // Default to full string if no slash
					Version: "unknown",
				}
				if len(parts) >= 2 {
					stemcellInfo.Name = parts[0]
					stemcellInfo.Version = parts[1]
				}
				info.Stemcells = append(info.Stemcells, stemcellInfo)
			}
		}

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

	// Create detail with basic info from deployment
	detail := &DeploymentDetail{
		DeploymentInfo: DeploymentInfo{
			Name:      dep.Name,
			Manifest:  dep.Manifest,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		Variables:  make(map[string]interface{}),
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
						releaseInfo := ReleaseInfo{}
						if name, ok := relMap["name"].(string); ok {
							releaseInfo.Name = name
						}
						if version, ok := relMap["version"].(string); ok {
							releaseInfo.Version = version
						} else if version, ok := relMap["version"].(int); ok {
							releaseInfo.Version = fmt.Sprintf("%d", version)
						}
						if url, ok := relMap["url"].(string); ok {
							releaseInfo.URL = url
						}
						if sha1, ok := relMap["sha1"].(string); ok {
							releaseInfo.SHA1 = sha1
						}
						detail.Releases = append(detail.Releases, releaseInfo)
					} else if relMap, ok := rel.(map[interface{}]interface{}); ok {
						releaseInfo := ReleaseInfo{}
						if name, ok := relMap["name"].(string); ok {
							releaseInfo.Name = name
						}
						if version, ok := relMap["version"].(string); ok {
							releaseInfo.Version = version
						} else if version, ok := relMap["version"].(int); ok {
							releaseInfo.Version = fmt.Sprintf("%d", version)
						} else if version, ok := relMap["version"].(float64); ok {
							releaseInfo.Version = fmt.Sprintf("%.1f", version)
						}
						if url, ok := relMap["url"].(string); ok {
							releaseInfo.URL = url
						}
						if sha1, ok := relMap["sha1"].(string); ok {
							releaseInfo.SHA1 = sha1
						}
						detail.Releases = append(detail.Releases, releaseInfo)
					}
				}
			}

			// Extract stemcells from manifest
			if stemcells, ok := manifestData["stemcells"].([]interface{}); ok {
				for _, stem := range stemcells {
					// Handle both map[string]interface{} and map[interface{}]interface{}
					if stemMap, ok := stem.(map[string]interface{}); ok {
						stemcellInfo := StemcellInfo{}
						if alias, ok := stemMap["alias"].(string); ok {
							stemcellInfo.Name = alias
						}
						if os, ok := stemMap["os"].(string); ok {
							stemcellInfo.OS = os
						}
						if version, ok := stemMap["version"].(string); ok {
							stemcellInfo.Version = version
						} else if version, ok := stemMap["version"].(int); ok {
							stemcellInfo.Version = fmt.Sprintf("%d", version)
						}
						detail.Stemcells = append(detail.Stemcells, stemcellInfo)
					} else if stemMap, ok := stem.(map[interface{}]interface{}); ok {
						stemcellInfo := StemcellInfo{}
						if alias, ok := stemMap["alias"].(string); ok {
							stemcellInfo.Name = alias
						}
						if os, ok := stemMap["os"].(string); ok {
							stemcellInfo.OS = os
						}
						if version, ok := stemMap["version"].(string); ok {
							stemcellInfo.Version = version
						} else if version, ok := stemMap["version"].(int); ok {
							stemcellInfo.Version = fmt.Sprintf("%d", version)
						}
						detail.Stemcells = append(detail.Stemcells, stemcellInfo)
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
				CID:          vm.CID,
				Name:         vm.ID,  // Use ID as Name
				JobName:      vm.Job, // Job is the job name
				Index:        vm.Index,
				State:        vm.State,
				AZ:           vm.AZ,
				IPs:          vm.IPs,
				ResourcePool: vm.ResourcePool,
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

	// Cache the result
	s.cache.set(name, detail)

	return detail, nil
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
