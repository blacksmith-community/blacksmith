package reconciler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"github.com/fivetwenty-io/graft/pkg/yaml"
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

	deployments, err := s.director.GetDeployments(ctx)
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
	dep, err := s.director.GetDeployment(ctx, name)
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
	createdAt, updatedAt, latestTaskID := s.getDeploymentTimestampsWithFallback(ctx, name)

	// Create deployment detail
	detail := s.createDeploymentDetail(dep, createdAt, updatedAt)

	// Parse manifest data
	s.parseManifestData(detail, dep.Manifest)

	// Get VM information
	s.addVMInformation(ctx, detail, name)

	// Parse manifest properties
	s.parseManifestProperties(detail)

	// Store latest task ID
	s.storeLatestTaskID(detail, latestTaskID)

	// Cache the result
	s.cache.Set(name, detail)

	return detail, nil
}

// extractBlacksmithMetadata extracts blacksmith metadata from properties.
func extractBlacksmithMetadata(props map[string]interface{}) map[string]interface{} {
	blacksmith, _ := props["blacksmith"].(map[string]interface{})

	return blacksmith
}

// parseManifestProperties extracts properties from a manifest.
func (s *BoshScanner) ParseManifestProperties(manifest string) (map[string]interface{}, error) {
	var manifestData map[string]interface{}

	err := yaml.Unmarshal([]byte(manifest), &manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
	}

	result := make(map[string]interface{})

	// Extract properties
	if props, ok := manifestData["properties"].(map[string]interface{}); ok {
		result["properties"] = props
		if blacksmith := extractBlacksmithMetadata(props); blacksmith != nil {
			result["blacksmith"] = blacksmith
		}
	}

	// Extract meta
	if meta, ok := manifestData["meta"].(map[string]interface{}); ok {
		result["meta"] = meta
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
func (s *BoshScanner) getDeploymentTimestampsWithFallback(ctx context.Context, name string) (time.Time, time.Time, string) {
	createdAt, updatedAt, latestTaskID, err := s.getDeploymentTimestamps(ctx, name)
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

// getVersionValue safely gets a version value from a map (string, int, or
// float). A float keeps every digit, and a whole one keeps a single decimal,
// so 1.0 stays "1.0" and 621.125 stays "621.125".
func (s *BoshScanner) getVersionValue(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}

	if v, ok := m[key].(int); ok {
		return strconv.Itoa(v)
	}

	if v, ok := m[key].(float64); ok {
		text := strconv.FormatFloat(v, 'f', -1, 64)
		if !strings.Contains(text, ".") {
			text += ".0"
		}

		return text
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
func (s *BoshScanner) addVMInformation(ctx context.Context, detail *DeploymentDetail, name string) {
	vms, err := s.director.GetDeploymentVMs(ctx, name)
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
func (s *BoshScanner) getDeploymentTimestamps(ctx context.Context, deploymentName string) (time.Time, time.Time, string, error) {
	events, err := s.director.GetEvents(ctx, deploymentName)
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
