package main

import (
	"blacksmith/pkg/logger"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Static errors for err113 compliance.
var (
	ErrNoPlanReferencesFound       = errors.New("no plan references found for instance")
	ErrPlanFileNotFound            = errors.New("plan file not found at path")
	ErrUnknownFileType             = errors.New("unknown file type")
	ErrInvalidDataFormatForPlan    = errors.New("invalid data format for plan file")
	ErrNoPlanFilesCouldBeRetrieved = errors.New("no plan files could be retrieved for instance")
)

// PlanStorage handles storage of blacksmith plan files to Vault.
type PlanStorage struct {
	vault  *Vault
	config *Config
}

// NewPlanStorage creates a new PlanStorage instance.
func NewPlanStorage(vault *Vault, config *Config) *PlanStorage {
	return &PlanStorage{
		vault:  vault,
		config: config,
	}
}

// StorePlans stores all blacksmith plan files to Vault.
// isBlacksmithPlanDir checks if an entry is a blacksmith plans directory.
func isBlacksmithPlanDir(entry os.DirEntry) bool {
	return entry.IsDir() && strings.HasSuffix(entry.Name(), "-blacksmith-plans")
}

// isAlreadyProcessed checks if a plans path has already been processed.
func isAlreadyProcessed(plansPath string, planDirs []string) bool {
	for _, knownDir := range planDirs {
		if plansPath == knownDir {
			return true
		}
	}

	return false
}

// processBlacksmithPlanEntry processes a single blacksmith plan entry.
func (ps *PlanStorage) processBlacksmithPlanEntry(ctx context.Context, entry os.DirEntry, jobsDir string, planDirs []string, l logger.Logger) (bool, error) {
	plansPath := filepath.Join(jobsDir, entry.Name(), "plans")

	// Skip if we already processed this one
	if isAlreadyProcessed(plansPath, planDirs) {
		return false, nil
	}

	// Check if plans directory exists
	if _, err := os.Stat(plansPath); os.IsNotExist(err) {
		l.Debug("Plans directory %s does not exist, skipping", plansPath)

		return false, nil
	}

	service := strings.TrimSuffix(entry.Name(), "-blacksmith-plans")
	l.Info("Found additional service plans: %s at %s", service, plansPath)

	err := ps.processServicePlans(ctx, plansPath, service)
	if err != nil {
		l.Warn("Failed to process additional service plans from %s: %s", plansPath, err)

		return false, err
	}

	return true, nil
}

func (ps *PlanStorage) StorePlans(ctx context.Context) {
	l := logger.Get().Named("Plan Storage")
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
		const minPathPartsRequired = 5
		if len(pathParts) < minPathPartsRequired {
			l.Error("Invalid plan directory path: %s", baseDir)

			continue
		}

		jobName := pathParts[4] // e.g. "redis-blacksmith-plans"
		service := strings.TrimSuffix(jobName, "-blacksmith-plans")

		l.Info("Processing plans for service: %s from directory: %s", service, baseDir)

		// Process each plan directory
		err := ps.processServicePlans(ctx, baseDir, service)
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
			if isBlacksmithPlanDir(entry) {
				l.Debug("Found potential blacksmith plans job: %s", entry.Name())

				processed, err := ps.processBlacksmithPlanEntry(ctx, entry, jobsDir, planDirs, l)
				if processed {
					foundAny = true
				}

				if err != nil {
					l.Error("Failed to process plans for service %s: %s", entry.Name(), err)
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
}

// calculateSHA256 calculates the SHA256 hash of a file.
func calculateSHA256(filePath string) (string, error) {
	file, err := safeOpenFile(filePath)
	if err != nil {
		return "", err
	}

	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("failed to hash file: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// StorePlanReferences stores the SHA256 references of plan files for a service instance.
func (ps *PlanStorage) StorePlanReferences(ctx context.Context, instanceID string, plan Plan) error {
	l := logger.Get().Named("Plan Storage")
	l.Info("Storing plan references for instance %s (plan: %s)", instanceID, plan.Name)

	type planRef struct {
		fileType string
		filePath string
	}

	refs := []planRef{}

	// Add manifest file if path is set
	if plan.ManifestPath != "" {
		refs = append(refs, planRef{fileType: "manifest", filePath: plan.ManifestPath})
	}

	// Add credentials file if path is set
	if plan.CredentialsPath != "" {
		refs = append(refs, planRef{fileType: "credentials", filePath: plan.CredentialsPath})
	}

	// Add init script if path is set and file exists
	if plan.InitScriptPath != "" {
		if _, err := os.Stat(plan.InitScriptPath); err == nil {
			refs = append(refs, planRef{fileType: "init", filePath: plan.InitScriptPath})
		}
	}

	if len(refs) == 0 {
		l.Info("No plan files found to store references for instance %s", instanceID)

		return nil
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

	if len(shaRefs) == 0 {
		l.Info("No SHA256 references calculated for instance %s", instanceID)

		return nil
	}

	// Store the SHA references in Vault at the instance path
	vaultPath := instanceID + "/plans/sha256"
	l.Debug("Storing plan references at Vault path: %s", vaultPath)

	err := ps.vault.Put(ctx, vaultPath, shaRefs)
	if err != nil {
		l.Error("Failed to store plan references in Vault: %s", err)

		return fmt.Errorf("failed to store plan references: %w", err)
	}

	l.Info("Successfully stored plan references for instance %s: %+v", instanceID, shaRefs)

	return nil
}

// GetPlanReferences retrieves the SHA256 references for a service instance.
func (ps *PlanStorage) GetPlanReferences(ctx context.Context, instanceID string) (map[string]string, error) {
	l := logger.Get().Named("Plan Storage")
	l.Debug("Retrieving plan references for instance %s", instanceID)

	vaultPath := instanceID + "/plans/sha256"

	var refs map[string]string

	exists, err := ps.vault.Get(ctx, vaultPath, &refs)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve plan references: %w", err)
	}

	if !exists {
		return nil, fmt.Errorf("no plan references found for instance %s: %w", instanceID, ErrNoPlanReferencesFound)
	}

	l.Debug("Retrieved plan references for instance %s: %+v", instanceID, refs)

	return refs, nil
}

// GetPlanFileByReference retrieves a plan file by its SHA256 reference.
func (ps *PlanStorage) GetPlanFileByReference(ctx context.Context, service string, planName string, fileType string, sha256 string) (string, error) {
	l := logger.Get().Named("Plan Storage")
	l.Debug("Retrieving plan file for %s/%s/%s with SHA256: %s", service, planName, fileType, sha256)

	vaultPath := fmt.Sprintf("plans/%s/%s/%s/%s", service, planName, fileType, sha256)

	var data map[string]interface{}

	exists, err := ps.vault.Get(ctx, vaultPath, &data)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve plan file: %w", err)
	}

	if !exists {
		return "", fmt.Errorf("plan file not found at path %s: %w", vaultPath, ErrPlanFileNotFound)
	}

	// Determine the data key based on file type
	var dataKey string

	switch fileType {
	case "manifest", "credentials":
		dataKey = "yml"
	case "init":
		dataKey = "script"
	default:
		return "", fmt.Errorf("unknown file type %s: %w", fileType, ErrUnknownFileType)
	}

	content, ok := data[dataKey].(string)
	if !ok {
		return "", ErrInvalidDataFormatForPlan
	}

	l.Debug("Successfully retrieved plan file from %s", vaultPath)

	return content, nil
}

// GetPlanFilesForUpgrade retrieves all plan files for an instance upgrade
// It uses the stored SHA256 references to get the exact plan files used during provisioning.
func (ps *PlanStorage) GetPlanFilesForUpgrade(ctx context.Context, instanceID string, service string, planName string) (map[string]string, error) {
	l := logger.Get().Named("Plan Storage")
	l.Info("Retrieving plan files for upgrade of instance %s", instanceID)

	// Get the SHA256 references for this instance
	refs, err := ps.GetPlanReferences(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan references for instance %s: %w", instanceID, err)
	}

	// Retrieve each plan file using its SHA256 reference
	planFiles := make(map[string]string)

	for fileType, sha256 := range refs {
		content, err := ps.GetPlanFileByReference(ctx, service, planName, fileType, sha256)
		if err != nil {
			l.Error("Failed to retrieve %s file for instance %s: %s", fileType, instanceID, err)
			// Try to fallback to current plan file if historical version not found
			l.Info("Attempting to use current plan file for %s", fileType)

			continue
		}

		planFiles[fileType] = content
	}

	if len(planFiles) == 0 {
		return nil, fmt.Errorf("no plan files could be retrieved for instance %s: %w", instanceID, ErrNoPlanFilesCouldBeRetrieved)
	}

	l.Info("Successfully retrieved %d plan files for instance %s upgrade", len(planFiles), instanceID)

	return planFiles, nil
}

// processServicePlans processes all plan directories for a given service.
func (ps *PlanStorage) processServicePlans(ctx context.Context, baseDir, service string) error {
	log := logger.Get().Named("Plan Storage")
	log.Info("Processing service: %s", service)
	log.Debug("Reading plan directory: %s", baseDir)

	// Read directory contents
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", baseDir, err)
	}

	log.Debug("Found %d entries in %s", len(entries), baseDir)

	for _, entry := range entries {
		if !entry.IsDir() {
			// Skip non-directory entries like service.yml, meta.yml
			log.Debug("Skipping non-directory entry: %s", entry.Name())

			continue
		}

		planName := entry.Name()
		planDir := filepath.Join(baseDir, planName)
		log.Debug("Processing plan: %s/%s from %s", service, planName, planDir)

		// Process the three files in each plan directory
		ps.storePlanFiles(ctx, planDir, service, planName)
	}

	return nil
}

// storePlanFiles stores the three plan files (manifest.yml, credentials.yml, init) to Vault.
func (ps *PlanStorage) storePlanFiles(ctx context.Context, planDir, service, planName string) {
	l := logger.Get().Named("Plan Storage")
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
		content, err := safeReadFile(filePath)
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

		exists, err := ps.vault.Get(ctx, vaultPath, &existingData)
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

		err = ps.vault.Put(ctx, vaultPath, data)
		if err != nil {
			l.Error("Failed to store %s in Vault: %s", vaultPath, err)

			continue
		}

		l.Info("Stored plan file %s (SHA256: %s)", vaultPath, shasum)
	}
}
