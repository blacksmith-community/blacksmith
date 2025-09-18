package planstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
)

// Static errors for err113 compliance.
var (
	ErrNoPlanReferencesFound       = errors.New("no plan references found for instance")
	ErrPlanFileNotFound            = errors.New("plan file not found at path")
	ErrUnknownFileType             = errors.New("unknown file type")
	ErrInvalidDataFormatForPlan    = errors.New("invalid data format for plan file")
	ErrNoPlanFilesCouldBeRetrieved = errors.New("no plan files could be retrieved for instance")
)

// Store handles storage of blacksmith plan files to Vault.
type Store struct {
	vault  *internalVault.Vault
	config interface{}
}

// New creates a new Store instance.
func New(vault *internalVault.Vault, config interface{}) *Store {
	return &Store{
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

func (ps *Store) StorePlans(ctx context.Context) {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Info("Starting to store blacksmith plans to Vault")
	loggerInstance.Debug("Checking for blacksmith plan directories...")

	planDirs := ps.getCommonPlanDirs()
	foundAny := ps.processCommonPlanDirs(ctx, planDirs, loggerInstance)

	// Check for dynamically discovered plan directories
	foundAdditional, err := ps.processDynamicPlanDirs(ctx, planDirs, loggerInstance)
	if err != nil {
		loggerInstance.Error("Failed to process dynamic plan directories: %s", err)
	}

	foundAny = foundAny || foundAdditional

	ps.logStorageCompletion(foundAny, loggerInstance)
}

// StorePlanReferences stores the SHA256 references of plan files for a service instance.
func (ps *Store) StorePlanReferences(ctx context.Context, instanceID string, plan services.Plan) error {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Info("Storing plan references for instance %s (plan: %s)", instanceID, plan.Name)

	refs := ps.buildPlanReferences(plan)
	if len(refs) == 0 {
		loggerInstance.Info("No plan files found to store references for instance %s", instanceID)

		return nil
	}

	shaRefs := ps.calculateSHAReferences(refs, loggerInstance)

	if len(shaRefs) == 0 {
		loggerInstance.Info("No SHA256 references calculated for instance %s", instanceID)

		return nil
	}

	return ps.storeSHAReferences(ctx, instanceID, shaRefs, loggerInstance)
}

type planRef struct {
	fileType string
	filePath string
}

// GetPlanReferences retrieves the SHA256 references for a service instance.
func (ps *Store) GetPlanReferences(ctx context.Context, instanceID string) (map[string]string, error) {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Debug("Retrieving plan references for instance %s", instanceID)

	vaultPath := instanceID + "/plans/sha256"

	var refs map[string]string

	exists, err := ps.vault.Get(ctx, vaultPath, &refs)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve plan references: %w", err)
	}

	if !exists {
		return nil, fmt.Errorf("no plan references found for instance %s: %w", instanceID, ErrNoPlanReferencesFound)
	}

	loggerInstance.Debug("Retrieved plan references for instance %s: %+v", instanceID, refs)

	return refs, nil
}

// GetPlanFileByReference retrieves a plan file by its SHA256 reference.
func (ps *Store) GetPlanFileByReference(ctx context.Context, service string, planName string, fileType string, sha256 string) (string, error) {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Debug("Retrieving plan file for %s/%s/%s with SHA256: %s", service, planName, fileType, sha256)

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

	loggerInstance.Debug("Successfully retrieved plan file from %s", vaultPath)

	return content, nil
}

// GetPlanFilesForUpgrade retrieves all plan files for an instance upgrade
// It uses the stored SHA256 references to get the exact plan files used during provisioning.
func (ps *Store) GetPlanFilesForUpgrade(ctx context.Context, instanceID string, service string, planName string) (map[string]string, error) {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Info("Retrieving plan files for upgrade of instance %s", instanceID)

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
			loggerInstance.Error("Failed to retrieve %s file for instance %s: %s", fileType, instanceID, err)
			// Try to fallback to current plan file if historical version not found
			loggerInstance.Info("Attempting to use current plan file for %s", fileType)

			continue
		}

		planFiles[fileType] = content
	}

	if len(planFiles) == 0 {
		return nil, fmt.Errorf("no plan files could be retrieved for instance %s: %w", instanceID, ErrNoPlanFilesCouldBeRetrieved)
	}

	loggerInstance.Info("Successfully retrieved %d plan files for instance %s upgrade", len(planFiles), instanceID)

	return planFiles, nil
}

func (ps *Store) getCommonPlanDirs() []string {
	return []string{
		"/var/vcap/jobs/redis-blacksmith-plans/plans",
		"/var/vcap/jobs/rabbitmq-blacksmith-plans/plans",
		"/var/vcap/jobs/postgresql-blacksmith-plans/plans",
		"/var/vcap/jobs/mysql-blacksmith-plans/plans",
		"/var/vcap/jobs/mongodb-blacksmith-plans/plans",
		"/var/vcap/jobs/elasticsearch-blacksmith-plans/plans",
		"/var/vcap/jobs/kafka-blacksmith-plans/plans",
		"/var/vcap/jobs/vault-blacksmith-plans/plans",
	}
}

func (ps *Store) processCommonPlanDirs(ctx context.Context, planDirs []string, loggerInstance logger.Logger) bool {
	foundAny := false

	for _, baseDir := range planDirs {
		loggerInstance.Debug("Checking plan directory: %s", baseDir)

		if !ps.directoryExists(baseDir, loggerInstance) {
			continue
		}

		foundAny = true

		service := ps.extractServiceName(baseDir, loggerInstance)
		if service == "" {
			continue
		}

		loggerInstance.Info("Processing plans for service: %s from directory: %s", service, baseDir)

		err := ps.processServicePlans(ctx, baseDir, service)
		if err != nil {
			loggerInstance.Error("Failed to process plans for service %s: %s", service, err)
		}
	}

	return foundAny
}

func (ps *Store) directoryExists(baseDir string, loggerInstance logger.Logger) bool {
	_, err := os.Stat(baseDir)
	if os.IsNotExist(err) {
		loggerInstance.Debug("Plan directory %s does not exist, skipping", baseDir)

		return false
	}

	loggerInstance.Debug("Found plan directory: %s", baseDir)

	return true
}

func (ps *Store) extractServiceName(baseDir string, loggerInstance logger.Logger) string {
	pathParts := strings.Split(baseDir, "/")

	const minPathPartsRequired = 5
	if len(pathParts) < minPathPartsRequired {
		loggerInstance.Error("Invalid plan directory path: %s", baseDir)

		return ""
	}

	jobName := pathParts[4] // e.g. "redis-blacksmith-plans"

	return strings.TrimSuffix(jobName, "-blacksmith-plans")
}

func (ps *Store) processDynamicPlanDirs(ctx context.Context, planDirs []string, loggerInstance logger.Logger) (bool, error) {
	jobsDir := "/var/vcap/jobs"
	loggerInstance.Debug("Scanning %s for additional blacksmith plan directories", jobsDir)

	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		loggerInstance.Error("Failed to read jobs directory %s: %s", jobsDir, err)

		return false, fmt.Errorf("failed to read jobs directory %s: %w", jobsDir, err)
	}

	loggerInstance.Debug("Found %d entries in %s", len(entries), jobsDir)

	foundAny := false

	for _, entry := range entries {
		if isBlacksmithPlanDir(entry) {
			loggerInstance.Debug("Found potential blacksmith plans job: %s", entry.Name())

			processed, err := ps.processBlacksmithPlanEntry(ctx, entry, jobsDir, planDirs, loggerInstance)
			if processed {
				foundAny = true
			}

			if err != nil {
				loggerInstance.Error("Failed to process plans for service %s: %s", entry.Name(), err)
			}
		}
	}

	return foundAny, nil
}

func (ps *Store) logStorageCompletion(foundAny bool, loggerInstance logger.Logger) {
	if !foundAny {
		loggerInstance.Info("No blacksmith plan directories found on this system")
	} else {
		loggerInstance.Info("Completed storing blacksmith plans to Vault")
	}
}

func (ps *Store) buildPlanReferences(plan services.Plan) []planRef {
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
		_, err := os.Stat(plan.InitScriptPath)
		if err == nil {
			refs = append(refs, planRef{fileType: "init", filePath: plan.InitScriptPath})
		}
	}

	return refs
}

func (ps *Store) calculateSHAReferences(refs []planRef, loggerInstance logger.Logger) map[string]string {
	shaRefs := make(map[string]string)

	for _, ref := range refs {
		_, err := os.Stat(ref.filePath)
		if os.IsNotExist(err) {
			loggerInstance.Debug("File %s does not exist, skipping", ref.filePath)

			continue
		}

		content, err := os.ReadFile(ref.filePath)
		if err != nil {
			loggerInstance.Error("Failed to read file %s: %s", ref.filePath, err)

			continue
		}

		hash := sha256.Sum256(content)
		shasum := hex.EncodeToString(hash[:])

		shaRefs[ref.fileType] = shasum
		loggerInstance.Debug("Calculated SHA256 for %s: %s", ref.fileType, shasum)
	}

	return shaRefs
}

func (ps *Store) storeSHAReferences(ctx context.Context, instanceID string, shaRefs map[string]string, loggerInstance logger.Logger) error {
	vaultPath := instanceID + "/plans/sha256"
	loggerInstance.Debug("Storing plan references at Vault path: %s", vaultPath)

	err := ps.vault.Put(ctx, vaultPath, shaRefs)
	if err != nil {
		loggerInstance.Error("Failed to store plan references in Vault: %s", err)

		return fmt.Errorf("failed to store plan references: %w", err)
	}

	loggerInstance.Info("Successfully stored plan references for instance %s: %+v", instanceID, shaRefs)

	return nil
}

// processBlacksmithPlanEntry processes a single blacksmith plan entry.
func (ps *Store) processBlacksmithPlanEntry(ctx context.Context, entry os.DirEntry, jobsDir string, planDirs []string, loggerInstance logger.Logger) (bool, error) {
	plansPath := filepath.Join(jobsDir, entry.Name(), "plans")

	// Skip if we already processed this one
	if isAlreadyProcessed(plansPath, planDirs) {
		return false, nil
	}

	// Check if plans directory exists
	_, err := os.Stat(plansPath)
	if os.IsNotExist(err) {
		loggerInstance.Debug("Plans directory %s does not exist, skipping", plansPath)

		return false, nil
	}

	service := strings.TrimSuffix(entry.Name(), "-blacksmith-plans")
	loggerInstance.Info("Found additional service plans: %s at %s", service, plansPath)

	err = ps.processServicePlans(ctx, plansPath, service)
	if err != nil {
		loggerInstance.Warn("Failed to process additional service plans from %s: %s", plansPath, err)

		return false, err
	}

	return true, nil
}

// processServicePlans processes all plan directories for a given service.
func (ps *Store) processServicePlans(ctx context.Context, baseDir, service string) error {
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
func (ps *Store) storePlanFiles(ctx context.Context, planDir, service, planName string) {
	loggerInstance := logger.Get().Named("Plan Storage")
	loggerInstance.Debug("Storing plan files for %s/%s from directory %s", service, planName, planDir)

	files := ps.getPlanFileDefinitions()

	for _, planFile := range files {
		ps.processPlanFile(ctx, planDir, service, planName, planFile, loggerInstance)
	}
}

type planFile struct {
	filename string
	vaultKey string
	dataKey  string
}

func (ps *Store) getPlanFileDefinitions() []planFile {
	return []planFile{
		{filename: "manifest.yml", vaultKey: "manifest", dataKey: "yml"},
		{filename: "credentials.yml", vaultKey: "credentials", dataKey: "yml"},
		{filename: "init", vaultKey: "init", dataKey: "script"},
	}
}

func (ps *Store) processPlanFile(ctx context.Context, planDir, service, planName string, planFile planFile, loggerInstance logger.Logger) {
	filePath := filepath.Join(planDir, planFile.filename)

	if !ps.planFileExists(filePath, loggerInstance) {
		return
	}

	content, err := ps.readPlanFileContent(filePath, loggerInstance)
	if err != nil {
		return
	}

	shasum := ps.calculateFileSHA(content)
	vaultPath := ps.buildVaultPath(service, planName, planFile.vaultKey, shasum)

	if ps.planFileAlreadyStored(ctx, vaultPath, shasum, loggerInstance) {
		return
	}

	ps.storePlanFileToVault(ctx, vaultPath, content, planFile.dataKey, shasum, loggerInstance)
}

func (ps *Store) planFileExists(filePath string, loggerInstance logger.Logger) bool {
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		loggerInstance.Debug("File %s does not exist, skipping", filePath)

		return false
	}

	loggerInstance.Debug("Processing file: %s", filePath)

	return true
}

func (ps *Store) readPlanFileContent(filePath string, loggerInstance logger.Logger) ([]byte, error) {
	content, err := utils.SafeReadFile(filePath)
	if err != nil {
		loggerInstance.Error("Failed to read file %s: %s", filePath, err)

		return nil, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	return content, nil
}

func (ps *Store) calculateFileSHA(content []byte) string {
	hash := sha256.Sum256(content)

	return hex.EncodeToString(hash[:])
}

func (ps *Store) buildVaultPath(service, planName, vaultKey, shasum string) string {
	return fmt.Sprintf("plans/%s/%s/%s/%s", service, planName, vaultKey, shasum)
}

func (ps *Store) planFileAlreadyStored(ctx context.Context, vaultPath, shasum string, loggerInstance logger.Logger) bool {
	loggerInstance.Debug("Checking if SHA256 %s already exists for %s", shasum, vaultPath)

	var existingData map[string]interface{}

	exists, err := ps.vault.Get(ctx, vaultPath, &existingData)
	if err != nil {
		loggerInstance.Error("Failed to check existing data for %s: %s", vaultPath, err)

		return false
	}

	if exists {
		loggerInstance.Debug("Plan file %s already exists with SHA256: %s", vaultPath, shasum)

		return true
	}

	return false
}

func (ps *Store) storePlanFileToVault(ctx context.Context, vaultPath string, content []byte, dataKey, shasum string, loggerInstance logger.Logger) {
	data := map[string]interface{}{
		dataKey: string(content),
	}

	loggerInstance.Debug("Storing to Vault: %s", vaultPath)

	err := ps.vault.Put(ctx, vaultPath, data)
	if err != nil {
		loggerInstance.Error("Failed to store %s in Vault: %s", vaultPath, err)

		return
	}

	loggerInstance.Info("Stored plan file %s (SHA256: %s)", vaultPath, shasum)
}
