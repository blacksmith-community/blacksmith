package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PlanStorage handles storage of blacksmith plan files to Vault
type PlanStorage struct {
	vault  *Vault
	config *Config
}

// NewPlanStorage creates a new PlanStorage instance
func NewPlanStorage(vault *Vault, config *Config) *PlanStorage {
	return &PlanStorage{
		vault:  vault,
		config: config,
	}
}

// StorePlans stores all blacksmith plan files to Vault
func (ps *PlanStorage) StorePlans() error {
	l := Logger.Wrap("Plan Storage")
	l.Info("Starting to store blacksmith plans to Vault")
	l.Debug("Checking for blacksmith plan directories...")

	// Common paths for blacksmith plans
	planDirs := []string{
		"/var/vcap/jobs/redis-blacksmith-plans/plans",
		"/var/vcap/jobs/rabbitmq-blacksmith-plans/plans",
		"/var/vcap/jobs/postgresql-blacksmith-plans/plans",
		"/var/vcap/jobs/mysql-blacksmith-plans/plans",
		"/var/vcap/jobs/mongodb-blacksmith-plans/plans",
		"/var/vcap/jobs/elasticsearch-blacksmith-plans/plans",
		"/var/vcap/jobs/kafka-blacksmith-plans/plans",
		"/var/vcap/jobs/vault-blacksmith-plans/plans",
	}

	foundAny := false
	for _, baseDir := range planDirs {
		l.Debug("Checking plan directory: %s", baseDir)
		// Check if directory exists
		if _, err := os.Stat(baseDir); os.IsNotExist(err) {
			l.Debug("Plan directory %s does not exist, skipping", baseDir)
			continue
		}

		foundAny = true
		l.Debug("Found plan directory: %s", baseDir)

		// Extract service name from path
		// e.g. /var/vcap/jobs/redis-blacksmith-plans/plans -> redis
		pathParts := strings.Split(baseDir, "/")
		if len(pathParts) < 5 {
			l.Error("Invalid plan directory path: %s", baseDir)
			continue
		}
		jobName := pathParts[4] // e.g. "redis-blacksmith-plans"
		service := strings.TrimSuffix(jobName, "-blacksmith-plans")

		l.Info("Processing plans for service: %s from directory: %s", service, baseDir)

		// Process each plan directory
		err := ps.processServicePlans(baseDir, service)
		if err != nil {
			l.Error("Failed to process plans for service %s: %s", service, err)
			// Continue with other services even if one fails
		}
	}

	// Also check for dynamically discovered plan directories
	// Look in /var/vcap/jobs for any *-blacksmith-plans directories
	jobsDir := "/var/vcap/jobs"
	l.Debug("Scanning %s for additional blacksmith plan directories", jobsDir)
	if entries, err := os.ReadDir(jobsDir); err == nil {
		l.Debug("Found %d entries in %s", len(entries), jobsDir)
		for _, entry := range entries {
			if entry.IsDir() && strings.HasSuffix(entry.Name(), "-blacksmith-plans") {
				l.Debug("Found potential blacksmith plans job: %s", entry.Name())
				plansPath := filepath.Join(jobsDir, entry.Name(), "plans")

				// Skip if we already processed this one
				alreadyProcessed := false
				for _, knownDir := range planDirs {
					if plansPath == knownDir {
						alreadyProcessed = true
						break
					}
				}
				if alreadyProcessed {
					continue
				}

				// Check if plans directory exists
				if _, err := os.Stat(plansPath); os.IsNotExist(err) {
					l.Debug("Plans directory %s does not exist, skipping", plansPath)
					continue
				}

				foundAny = true
				service := strings.TrimSuffix(entry.Name(), "-blacksmith-plans")
				l.Info("Found additional service plans: %s at %s", service, plansPath)

				if err := ps.processServicePlans(plansPath, service); err != nil {
					l.Error("Failed to process plans for service %s: %s", service, err)
				}
			}
		}
	} else {
		l.Error("Failed to read jobs directory %s: %s", jobsDir, err)
	}

	if !foundAny {
		l.Info("No blacksmith plan directories found on this system")
	} else {
		l.Info("Completed storing blacksmith plans to Vault")
	}

	return nil
}

// processServicePlans processes all plan directories for a given service
func (ps *PlanStorage) processServicePlans(baseDir, service string) error {
	l := Logger.Wrap("Plan Storage")
	l.Info("Processing service: %s", service)
	l.Debug("Reading plan directory: %s", baseDir)

	// Read directory contents
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", baseDir, err)
	}
	l.Debug("Found %d entries in %s", len(entries), baseDir)

	for _, entry := range entries {
		if !entry.IsDir() {
			// Skip non-directory entries like service.yml, meta.yml
			l.Debug("Skipping non-directory entry: %s", entry.Name())
			continue
		}

		planName := entry.Name()
		planDir := filepath.Join(baseDir, planName)
		l.Debug("Processing plan: %s/%s from %s", service, planName, planDir)

		// Process the three files in each plan directory
		err := ps.storePlanFiles(planDir, service, planName)
		if err != nil {
			l.Error("Failed to store plan %s/%s: %s", service, planName, err)
			// Continue with other plans even if one fails
		}
	}

	return nil
}

// storePlanFiles stores the three plan files (manifest.yml, credentials.yml, init) to Vault
func (ps *PlanStorage) storePlanFiles(planDir, service, planName string) error {
	l := Logger.Wrap("Plan Storage")
	l.Debug("Storing plan files for %s/%s from directory %s", service, planName, planDir)

	type planFile struct {
		filename string
		vaultKey string
		dataKey  string
	}

	files := []planFile{
		{filename: "manifest.yml", vaultKey: "manifest", dataKey: "yml"},
		{filename: "credentials.yml", vaultKey: "credentials", dataKey: "yml"},
		{filename: "init", vaultKey: "init", dataKey: "script"},
	}

	for _, pf := range files {
		filePath := filepath.Join(planDir, pf.filename)

		// Check if file exists
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			l.Debug("File %s does not exist, skipping", filePath)
			continue
		}
		l.Debug("Processing file: %s", filePath)

		// Read file content
		content, err := os.ReadFile(filePath)
		if err != nil {
			l.Error("Failed to read file %s: %s", filePath, err)
			continue
		}

		// Calculate SHA256
		hash := sha256.Sum256(content)
		shasum := hex.EncodeToString(hash[:])

		// Construct Vault path using the new structure:
		// secret/plans/{service}/{plan}/{file_type}/{sha256}:{dataKey}
		vaultPath := fmt.Sprintf("plans/%s/%s/%s/%s", service, planName, pf.vaultKey, shasum)
		l.Debug("Vault path: %s", vaultPath)

		// Check if this SHA already exists
		l.Debug("Checking if SHA256 %s already exists for %s", shasum, vaultPath)
		var existingData map[string]interface{}
		exists, err := ps.vault.Get(vaultPath, &existingData)
		if err != nil {
			l.Error("Failed to check existing data for %s: %s", vaultPath, err)
		}

		if exists {
			l.Debug("Plan file %s already exists with SHA256: %s", vaultPath, shasum)
			continue
		}

		// Prepare data for Vault with the dataKey as the key
		data := map[string]interface{}{
			pf.dataKey: string(content),
		}

		// Store in Vault
		l.Debug("Storing to Vault: %s", vaultPath)
		err = ps.vault.Put(vaultPath, data)
		if err != nil {
			l.Error("Failed to store %s in Vault: %s", vaultPath, err)
			continue
		}

		l.Info("Stored plan file %s (SHA256: %s)", vaultPath, shasum)
	}

	return nil
}

// checkIfUpdateNeeded checks if the file needs to be updated based on SHA256
func (ps *PlanStorage) checkIfUpdateNeeded(vaultPath, newShasum string) (bool, error) {
	l := Logger.Wrap("Plan Storage")
	// Get existing secret from Vault
	var existing map[string]interface{}
	l.Debug("Getting existing secret from Vault: %s", vaultPath)
	exists, err := ps.vault.Get(vaultPath, &existing)
	if err != nil {
		l.Debug("Error getting secret: %s", err)
		return false, err
	}

	// If secret doesn't exist, we need to create it
	if !exists {
		l.Debug("Secret does not exist, needs creation")
		return true, nil
	}

	// Check if SHA256 exists and matches
	if existingShasum, ok := existing["sha256"].(string); ok {
		l.Debug("Existing SHA256: %s, New SHA256: %s", existingShasum, newShasum)
		if existingShasum == newShasum {
			l.Debug("SHA256 matches, no update needed")
			return false, nil // No update needed
		}
		l.Debug("SHA256 differs, update needed")
	} else {
		l.Debug("No SHA256 in existing secret, update needed")
	}

	return true, nil // Update needed
}

// calculateSHA256 calculates the SHA256 hash of a file
func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// StorePlanReferences stores the SHA256 references of plan files for a service instance
func (ps *PlanStorage) StorePlanReferences(instanceID string, serviceID string, planID string) error {
	l := Logger.Wrap("Plan Storage")
	l.Info("Storing plan references for instance %s (serviceID: %s, planID: %s)", instanceID, serviceID, planID)

	// The planID comes in the form "serviceID-planName"
	// We need to extract the actual plan name
	planName := strings.TrimPrefix(planID, serviceID+"-")
	l.Debug("Extracted plan name: %s from planID: %s", planName, planID)

	// Search for the plan directory in common locations
	// First, look for service-specific blacksmith plans directory
	possiblePaths := []string{
		fmt.Sprintf("/var/vcap/jobs/%s-blacksmith-plans/plans/%s", serviceID, planName),
	}

	// Also check for variations in the job name
	// Sometimes the service ID might not exactly match the job directory name
	jobsDir := "/var/vcap/jobs"
	if entries, err := os.ReadDir(jobsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasSuffix(entry.Name(), "-blacksmith-plans") {
				// Check if this might be our service
				jobServiceName := strings.TrimSuffix(entry.Name(), "-blacksmith-plans")
				// Check for exact match or similar names
				if strings.Contains(jobServiceName, serviceID) || strings.Contains(serviceID, jobServiceName) {
					planPath := filepath.Join(jobsDir, entry.Name(), "plans", planName)
					possiblePaths = append(possiblePaths, planPath)
				}
			}
		}
	}

	// Find the actual plan directory
	var planDir string
	for _, path := range possiblePaths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			planDir = path
			l.Debug("Found plan directory: %s", planDir)
			break
		}
	}

	if planDir == "" {
		l.Error("Could not find plan directory for service %s, plan %s", serviceID, planName)
		l.Debug("Searched paths: %v", possiblePaths)
		// Don't fail provisioning because of this
		return nil
	}

	type planRef struct {
		fileType string
		filePath string
	}

	refs := []planRef{
		{fileType: "manifest", filePath: filepath.Join(planDir, "manifest.yml")},
		{fileType: "credentials", filePath: filepath.Join(planDir, "credentials.yml")},
		{fileType: "init", filePath: filepath.Join(planDir, "init")},
	}

	shaRefs := make(map[string]string)

	for _, ref := range refs {
		// Check if file exists
		if _, err := os.Stat(ref.filePath); os.IsNotExist(err) {
			l.Debug("File %s does not exist, skipping", ref.filePath)
			continue
		}

		// Calculate SHA256
		content, err := os.ReadFile(ref.filePath)
		if err != nil {
			l.Error("Failed to read file %s: %s", ref.filePath, err)
			continue
		}

		hash := sha256.Sum256(content)
		shasum := hex.EncodeToString(hash[:])

		shaRefs[ref.fileType] = shasum
		l.Debug("Calculated SHA256 for %s: %s", ref.fileType, shasum)
	}

	// Store the SHA references in Vault at the instance path
	vaultPath := fmt.Sprintf("%s/plans/sha256", instanceID)
	l.Debug("Storing plan references at Vault path: %s", vaultPath)

	err := ps.vault.Put(vaultPath, shaRefs)
	if err != nil {
		l.Error("Failed to store plan references in Vault: %s", err)
		return fmt.Errorf("failed to store plan references: %w", err)
	}

	l.Info("Successfully stored plan references for instance %s: %+v", instanceID, shaRefs)
	return nil
}

// GetPlanReferences retrieves the SHA256 references for a service instance
func (ps *PlanStorage) GetPlanReferences(instanceID string) (map[string]string, error) {
	l := Logger.Wrap("Plan Storage")
	l.Debug("Retrieving plan references for instance %s", instanceID)

	vaultPath := fmt.Sprintf("%s/plans/sha256", instanceID)

	var refs map[string]string
	exists, err := ps.vault.Get(vaultPath, &refs)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve plan references: %w", err)
	}

	if !exists {
		return nil, fmt.Errorf("no plan references found for instance %s", instanceID)
	}

	l.Debug("Retrieved plan references for instance %s: %+v", instanceID, refs)
	return refs, nil
}

// GetPlanFileByReference retrieves a plan file by its SHA256 reference
func (ps *PlanStorage) GetPlanFileByReference(service string, planName string, fileType string, sha256 string) (string, error) {
	l := Logger.Wrap("Plan Storage")
	l.Debug("Retrieving plan file for %s/%s/%s with SHA256: %s", service, planName, fileType, sha256)

	vaultPath := fmt.Sprintf("plans/%s/%s/%s/%s", service, planName, fileType, sha256)

	var data map[string]interface{}
	exists, err := ps.vault.Get(vaultPath, &data)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve plan file: %w", err)
	}

	if !exists {
		return "", fmt.Errorf("plan file not found at path: %s", vaultPath)
	}

	// Determine the data key based on file type
	var dataKey string
	switch fileType {
	case "manifest", "credentials":
		dataKey = "yml"
	case "init":
		dataKey = "script"
	default:
		return "", fmt.Errorf("unknown file type: %s", fileType)
	}

	content, ok := data[dataKey].(string)
	if !ok {
		return "", fmt.Errorf("invalid data format for plan file")
	}

	l.Debug("Successfully retrieved plan file from %s", vaultPath)
	return content, nil
}

// GetPlanFilesForUpgrade retrieves all plan files for an instance upgrade
// It uses the stored SHA256 references to get the exact plan files used during provisioning
func (ps *PlanStorage) GetPlanFilesForUpgrade(instanceID string, service string, planName string) (map[string]string, error) {
	l := Logger.Wrap("Plan Storage")
	l.Info("Retrieving plan files for upgrade of instance %s", instanceID)

	// Get the SHA256 references for this instance
	refs, err := ps.GetPlanReferences(instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan references for instance %s: %w", instanceID, err)
	}

	// Retrieve each plan file using its SHA256 reference
	planFiles := make(map[string]string)

	for fileType, sha256 := range refs {
		content, err := ps.GetPlanFileByReference(service, planName, fileType, sha256)
		if err != nil {
			l.Error("Failed to retrieve %s file for instance %s: %s", fileType, instanceID, err)
			// Try to fallback to current plan file if historical version not found
			l.Info("Attempting to use current plan file for %s", fileType)
			continue
		}
		planFiles[fileType] = content
	}

	if len(planFiles) == 0 {
		return nil, fmt.Errorf("no plan files could be retrieved for instance %s", instanceID)
	}

	l.Info("Successfully retrieved %d plan files for instance %s upgrade", len(planFiles), instanceID)
	return planFiles, nil
}
