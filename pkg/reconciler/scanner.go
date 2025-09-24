package reconciler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"gopkg.in/yaml.v2"
)

type BoshScanner struct {
	director bosh.Director
	logger   Logger
	cache    *DeploymentCache
}

type DeploymentCache struct {
	mu     sync.RWMutex
	Data   map[string]*DeploymentDetail
	Expiry map[string]time.Time
	TTL    time.Duration
}

// NewBOSHScanner creates a new BOSH scanner.
func NewBOSHScanner(director bosh.Director, logger Logger) *BoshScanner {
	return &BoshScanner{
		director: director,
		logger:   logger,
		cache: &DeploymentCache{
			Data:   make(map[string]*DeploymentDetail),
			Expiry: make(map[string]time.Time),
			TTL:    defaultDeploymentScanTimeout,
		},
	}
}

// ScanDeployments scans BOSH for all deployments.
func (s *BoshScanner) ScanDeployments(ctx context.Context) ([]DeploymentInfo, error) {
	s.logger.Debugf("Scanning BOSH deployments")

	deployments, err := s.director.GetDeployments()
	if err != nil {
		return nil, fmt.Errorf("failed to get deployments: %w", err)
	}

	result := make([]DeploymentInfo, 0, len(deployments))
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

	s.logger.Debugf("Found %d deployments", len(result))

	return result, nil
}

// GetDeploymentDetails gets detailed information about a deployment.
func (s *BoshScanner) GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error) {
	// Check cache first
	if cached := s.cache.Get(name); cached != nil {
		s.logger.Debugf("Using cached details for deployment %s", name)

		return cached, nil
	}

	s.logger.Debugf("Fetching details for deployment %s", name)

	// Get basic deployment info
	dep, err := s.director.GetDeployment(name)
	if err != nil {
		// Check if it's a 404 error (deployment doesn't exist)
		errStr := err.Error()
		if strings.Contains(errStr, "doesn't exist") || strings.Contains(errStr, "status code '404'") {
			s.logger.Warningf("Deployment %s not found in BOSH director (404)", name)
			// Return a detail with NotFound flag set
			return &DeploymentDetail{
				DeploymentInfo: DeploymentInfo{
					Name: name,
				},
				NotFound:       true,
				NotFoundReason: fmt.Sprintf("Deployment not found in BOSH director: %v", err),
			}, nil
		}
		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	// Get deployment timestamps
	createdAt, updatedAt, latestTaskID := s.getDeploymentTimestampsWithFallback(name)

	// Create deployment detail
	detail := s.createDeploymentDetail(dep, createdAt, updatedAt)

	// Parse manifest data
	s.parseManifestData(detail, dep.Manifest)

	// Get VM information
	s.addVMInformation(detail, name)

	// Parse manifest properties
	s.parseManifestProperties(detail)

	// Store latest task ID
	s.storeLatestTaskID(detail, latestTaskID)

	// Cache the result
	s.cache.Set(name, detail)

	return detail, nil
}

// parseManifestProperties parses manifest properties
// convertInterfaceMapToStringMap converts map[interface{}]interface{} to map[string]interface{}.
func convertInterfaceMapToStringMap(input map[interface{}]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range input {
		if key, ok := k.(string); ok {
			result[key] = v
		}
	}

	return result
}

// extractBlacksmithMetadata extracts blacksmith metadata from properties.
func extractBlacksmithMetadata(props interface{}) map[string]interface{} {
	switch properties := props.(type) {
	case map[string]interface{}:
		if blacksmith, ok := properties["blacksmith"].(map[string]interface{}); ok {
			return blacksmith
		}

		if blacksmith, ok := properties["blacksmith"].(map[interface{}]interface{}); ok {
			return convertInterfaceMapToStringMap(blacksmith)
		}
	case map[interface{}]interface{}:
		if blacksmith, ok := properties["blacksmith"].(map[interface{}]interface{}); ok {
			return convertInterfaceMapToStringMap(blacksmith)
		}
	}

	return nil
}

// parseManifestProperties extracts properties from a manifest.
func (s *BoshScanner) ParseManifestProperties(manifest string) (map[string]interface{}, error) {
	var manifestData map[string]interface{}

	err := yaml.Unmarshal([]byte(manifest), &manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	result := make(map[string]interface{})

	// Extract properties (handles both map[string]interface{} and map[interface{}]interface{})
	if props, ok := manifestData["properties"].(map[string]interface{}); ok {
		result["properties"] = props
		if blacksmith := extractBlacksmithMetadata(props); blacksmith != nil {
			result["blacksmith"] = blacksmith
		}
	} else if props, ok := manifestData["properties"].(map[interface{}]interface{}); ok {
		convertedProps := convertInterfaceMapToStringMap(props)

		result["properties"] = convertedProps
		if blacksmith := extractBlacksmithMetadata(convertedProps); blacksmith != nil {
			result["blacksmith"] = blacksmith
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

// getDeploymentTimestampsWithFallback gets deployment timestamps with fallback to current time.
func (s *BoshScanner) getDeploymentTimestampsWithFallback(name string) (time.Time, time.Time, string) {
	createdAt, updatedAt, latestTaskID, err := s.getDeploymentTimestamps(name)
	if err != nil {
		s.logger.Errorf("Failed to get deployment timestamps for %s: %s", name, err)
		// Fall back to current time if events are unavailable
		now := time.Now()

		return now, now, ""
	}

	// Ensure we never have zero times
	if createdAt.IsZero() {
		s.logger.Debugf("Got zero created time for %s, using current time", name)

		createdAt = time.Now()
	}

	if updatedAt.IsZero() {
		s.logger.Debugf("Got zero updated time for %s, using created time", name)

		updatedAt = createdAt
	}

	return createdAt, updatedAt, latestTaskID
}

// createDeploymentDetail creates the basic deployment detail structure.
func (s *BoshScanner) createDeploymentDetail(dep *bosh.DeploymentDetail, createdAt, updatedAt time.Time) *DeploymentDetail {
	return &DeploymentDetail{
		DeploymentInfo: DeploymentInfo{
			Name:      dep.Name,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		},
		Manifest:   dep.Manifest,
		Properties: make(map[string]interface{}),
	}
}

// parseManifestData parses the manifest to extract releases and stemcells.
func (s *BoshScanner) parseManifestData(detail *DeploymentDetail, manifest string) {
	if manifest == "" {
		return
	}

	var manifestData map[string]interface{}

	err := yaml.Unmarshal([]byte(manifest), &manifestData)
	if err != nil {
		return
	}

	s.extractReleases(detail, manifestData)
	s.extractStemcells(detail, manifestData)
}

// extractReleases extracts release information from manifest data.
func (s *BoshScanner) extractReleases(detail *DeploymentDetail, manifestData map[string]interface{}) {
	releases, ok := manifestData["releases"].([]interface{})
	if !ok {
		return
	}

	for _, rel := range releases {
		releaseStr := s.parseReleaseInfo(rel)
		if releaseStr != "" {
			detail.Releases = append(detail.Releases, releaseStr)
		}
	}
}

// parseReleaseInfo parses individual release information.
func (s *BoshScanner) parseReleaseInfo(rel interface{}) string {
	var name, version string

	switch relMap := rel.(type) {
	case map[string]interface{}:
		name = s.getStringValue(relMap, "name")
		version = s.getVersionValue(relMap, "version")
	case map[interface{}]interface{}:
		name = s.getInterfaceStringValue(relMap, "name")
		version = s.getInterfaceVersionValue(relMap, "version")
	}

	if name != "" {
		return fmt.Sprintf("%s/%s", name, version)
	}

	return ""
}

// extractStemcells extracts stemcell information from manifest data.
func (s *BoshScanner) extractStemcells(detail *DeploymentDetail, manifestData map[string]interface{}) {
	stemcells, ok := manifestData["stemcells"].([]interface{})
	if !ok {
		return
	}

	for _, stem := range stemcells {
		stemcellStr := s.parseStemcellInfo(stem)
		if stemcellStr != "" {
			detail.Stemcells = append(detail.Stemcells, stemcellStr)
		}
	}
}

// parseStemcellInfo parses individual stemcell information.
func (s *BoshScanner) parseStemcellInfo(stem interface{}) string {
	var alias, operatingSystem, version string

	switch stemMap := stem.(type) {
	case map[string]interface{}:
		alias = s.getStringValue(stemMap, "alias")
		operatingSystem = s.getStringValue(stemMap, "os")
		version = s.getVersionValue(stemMap, "version")
	case map[interface{}]interface{}:
		alias = s.getInterfaceStringValue(stemMap, "alias")
		operatingSystem = s.getInterfaceStringValue(stemMap, "os")
		version = s.getInterfaceVersionValue(stemMap, "version")
	}

	return s.formatStemcellString(alias, operatingSystem, version)
}

// getStringValue safely gets a string value from a map.
func (s *BoshScanner) getStringValue(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}

	return ""
}

// getVersionValue safely gets a version value from a map (string or int).
func (s *BoshScanner) getVersionValue(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}

	if v, ok := m[key].(int); ok {
		return strconv.Itoa(v)
	}

	return ""
}

// getInterfaceStringValue safely gets a string value from an interface{} map.
func (s *BoshScanner) getInterfaceStringValue(m map[interface{}]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}

	return ""
}

// getInterfaceVersionValue safely gets a version value from an interface{} map.
func (s *BoshScanner) getInterfaceVersionValue(dataMap map[interface{}]interface{}, key string) string {
	if value, ok := dataMap[key].(string); ok {
		return value
	}

	if value, ok := dataMap[key].(int); ok {
		return strconv.Itoa(value)
	}

	if value, ok := dataMap[key].(float64); ok {
		return fmt.Sprintf("%.1f", value)
	}

	return ""
}

// formatStemcellString formats stemcell information into a string.
func (s *BoshScanner) formatStemcellString(alias, operatingSystem, version string) string {
	if alias != "" {
		return fmt.Sprintf("%s/%s/%s", alias, operatingSystem, version)
	}

	if operatingSystem != "" {
		return fmt.Sprintf("%s/%s", operatingSystem, version)
	}

	return ""
}

// addVMInformation adds VM information to the deployment detail.
func (s *BoshScanner) addVMInformation(detail *DeploymentDetail, name string) {
	vms, err := s.director.GetDeploymentVMs(name)
	if err != nil {
		s.logger.Errorf("Failed to get VMs for %s: %s", name, err)

		return
	}

	for _, vm := range vms {
		detail.VMs = append(detail.VMs, VMInfo{
			Name:  vm.ID, // Use ID as Name
			State: vm.State,
			IPs:   vm.IPs,
			AZ:    vm.AZ,
		})
	}
}

func (s *BoshScanner) parseManifestProperties(detail *DeploymentDetail) {
	if detail.Manifest == "" {
		return
	}

	props, err := s.ParseManifestProperties(detail.Manifest)
	if err != nil {
		s.logger.Errorf("Failed to parse manifest properties: %s", err)
	} else {
		detail.Properties = props
	}
}

// storeLatestTaskID stores the latest task ID in properties.
func (s *BoshScanner) storeLatestTaskID(detail *DeploymentDetail, latestTaskID string) {
	if latestTaskID == "" {
		return
	}

	if detail.Properties == nil {
		detail.Properties = make(map[string]interface{})
	}

	detail.Properties["latest_task_id"] = latestTaskID
}

// getDeploymentTimestamps extracts creation/update timestamps and latest task ID from deployment events.
func (s *BoshScanner) getDeploymentTimestamps(deploymentName string) (time.Time, time.Time, string, error) {
	events, err := s.director.GetEvents(deploymentName)
	if err != nil {
		return time.Time{}, time.Time{}, "", fmt.Errorf("failed to get events for deployment %s: %w", deploymentName, err)
	}

	var (
		earliestCreate time.Time
		latestUpdate   time.Time
		taskID         string
	)

	s.processEventsForTimestamps(events, deploymentName, &earliestCreate, &latestUpdate, &taskID)
	s.setDefaultTimestamps(&earliestCreate, &latestUpdate)

	return earliestCreate, latestUpdate, taskID, nil
}

func (s *BoshScanner) processEventsForTimestamps(events []bosh.Event, deploymentName string, earliestCreate, latestUpdate *time.Time, taskID *string) {
	for _, event := range events {
		if event.Deployment != deploymentName {
			continue
		}

		isCreateAction := s.isCreateAction(event.Action)
		isUpdateAction := s.isUpdateAction(event.Action)

		s.updateEarliestCreate(event, isCreateAction, earliestCreate)
		s.updateLatestUpdate(event, isCreateAction, isUpdateAction, latestUpdate, taskID)
	}
}

func (s *BoshScanner) isCreateAction(action string) bool {
	return action == "create" || action == "deploy" || action == "create_deployment"
}

func (s *BoshScanner) isUpdateAction(action string) bool {
	return action == "update" || action == "deploy" || action == "update_deployment"
}

func (s *BoshScanner) updateEarliestCreate(event bosh.Event, isCreateAction bool, earliestCreate *time.Time) {
	if isCreateAction && (earliestCreate.IsZero() || event.Time.Before(*earliestCreate)) {
		*earliestCreate = event.Time
	}
}

func (s *BoshScanner) updateLatestUpdate(event bosh.Event, isCreateAction, isUpdateAction bool, latestUpdate *time.Time, taskID *string) {
	if (isCreateAction || isUpdateAction) && (latestUpdate.IsZero() || event.Time.After(*latestUpdate)) {
		*latestUpdate = event.Time
		if event.TaskID != "" {
			*taskID = event.TaskID
		}
	}
}

func (s *BoshScanner) setDefaultTimestamps(earliestCreate, latestUpdate *time.Time) {
	if earliestCreate.IsZero() {
		*earliestCreate = time.Now()
	}

	if latestUpdate.IsZero() {
		*latestUpdate = *earliestCreate
	}
}

// Cache methods

func (c *DeploymentCache) Get(name string) *DeploymentDetail {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if expiry, ok := c.Expiry[name]; ok {
		if time.Now().After(expiry) {
			// Cache expired
			return nil
		}

		return c.Data[name]
	}

	return nil
}

func (c *DeploymentCache) Set(name string, detail *DeploymentDetail) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Data[name] = detail
	c.Expiry[name] = time.Now().Add(c.TTL)

	// Clean up expired entries
	c.Cleanup()
}

func (c *DeploymentCache) Cleanup() {
	now := time.Now()
	for name, expiry := range c.Expiry {
		if now.After(expiry) {
			delete(c.Data, name)
			delete(c.Expiry, name)
		}
	}
}
