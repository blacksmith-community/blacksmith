package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/pivotal-cf/brokerapi/v8/domain"
	"gopkg.in/yaml.v2"
)

// Constants for services operations.
const (
	DebugYAMLContentLimit = 500
)

// Static errors for err113 compliance.
var (
	ErrInvalidNameFormat         = errors.New("invalid name format - names should only contain letters, numbers and hyphens")
	ErrNoServiceDirectoriesFound = errors.New("no service directories found")
)

type Plan struct {
	ID          string `json:"id"          yaml:"id"`
	Name        string `json:"name"        yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Limit       int    `json:"limit"       yaml:"limit"`
	Type        string `json:"type"        yaml:"type"`

	Manifest       map[interface{}]interface{} `json:"-"`
	Credentials    map[interface{}]interface{} `json:"-"`
	InitScriptPath string                      `json:"-"`

	// Store the actual file paths for SHA256 calculation
	ManifestPath    string `json:"-"`
	CredentialsPath string `json:"-"`

	Service *Service `json:"service" yaml:"service"`
}

type Service struct {
	ID          string   `json:"id"          yaml:"id"`
	Name        string   `json:"name"        yaml:"name"`
	Type        string   `json:"type"        yaml:"type"`
	Description string   `json:"description" yaml:"description"`
	Bindable    bool     `json:"bindable"    yaml:"bindable"`
	Tags        []string `json:"tags"        yaml:"tags"`
	Limit       int      `json:"limit"       yaml:"limit"`
	Plans       []Plan   `json:"plans"       yaml:"plans"`
}

// Job represents a BOSH deployment job/instance.
type Job struct {
	Name       string   `json:"name"       yaml:"name"`
	Deployment string   `json:"deployment" yaml:"deployment"`
	ID         string   `json:"id"         yaml:"id"`
	PlanID     string   `json:"plan_id"    yaml:"plan_id"`
	PlanName   string   `json:"plan_name"  yaml:"plan_name"`
	FQDN       string   `json:"fqdn"       yaml:"fqdn"`
	IPs        []string `json:"ips"        yaml:"ips"`
	DNS        []string `json:"dns"        yaml:"dns"`
}

var ValidName = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func CheckNames(names ...string) error {
	for _, s := range names {
		if !ValidName.MatchString(s) {
			return fmt.Errorf("'%s' is invalid; names should only contain letters, numbers and hyphens: %w", s, ErrInvalidNameFormat)
		}
	}

	return nil
}

func (p Plan) String() string {
	return fmt.Sprintf("%s/%s", p.Service.ID, p.ID)
}

func (p Plan) OverLimit(indexDB *vaultPkg.Index) bool {
	if p.hasUnlimitedLimits() {
		return false
	}

	if p.hasNegativeLimits() {
		return true
	}

	existingPlan, existingService := p.countExistingInstances(indexDB)

	return p.exceedsLimits(existingPlan, existingService)
}

func (p Plan) hasUnlimitedLimits() bool {
	return p.Limit == 0 && p.Service.Limit == 0
}

func (p Plan) hasNegativeLimits() bool {
	return p.Limit < 0 || p.Service.Limit < 0
}

func (p Plan) countExistingInstances(db *vaultPkg.Index) (int, int) {
	existingPlan := 0
	existingService := 0

	for _, s := range db.Data {
		if ss, ok := s.(map[string]interface{}); ok {
			service, haveService := ss["service_id"]
			plan, havePlan := ss["plan_id"]

			if p.shouldCountInstance(haveService, havePlan, service, plan) {
				existingService++

				if p.isPlanMatch(plan) {
					existingPlan++
				}
			}
		}
	}

	return existingPlan, existingService
}

func (p Plan) shouldCountInstance(haveService, havePlan bool, service, _ interface{}) bool {
	if !havePlan || !haveService {
		return false
	}

	if v, ok := service.(string); ok && v == p.Service.ID {
		return true
	}

	return false
}

func (p Plan) isPlanMatch(plan interface{}) bool {
	if v, ok := plan.(string); ok && v == p.ID {
		return true
	}

	return false
}

func (p Plan) exceedsLimits(existingPlan, existingService int) bool {
	if p.Limit > 0 && existingPlan >= p.Limit {
		return true
	}

	if p.Service.Limit > 0 && existingService >= p.Service.Limit {
		return true
	}

	return false
}

func ReadPlan(path string) (Plan, error) {
	var plan Plan

	loggerInstance := logger.Get().Named("ReadPlan")
	loggerInstance.Debug("Reading plan from path: %s", path)

	err := readAndParsePlanFile(path, &plan, loggerInstance)
	if err != nil {
		return plan, err
	}

	normalizePlanIDAndName(&plan, loggerInstance)

	err = CheckNames(plan.ID, plan.Name)
	if err != nil {
		loggerInstance.Error("Invalid plan names - ID: %s, Name: %s: %s", plan.ID, plan.Name, err)

		return plan, err
	}

	err = processPlanManifest(path, &plan, loggerInstance)
	if err != nil {
		return plan, err
	}

	err = processPlanCredentials(path, &plan, loggerInstance)
	if err != nil {
		return plan, err
	}

	plan.InitScriptPath = path + "/init"
	loggerInstance.Info("Successfully read plan %s from %s", plan.Name, path)

	return plan, nil
}

func readAndParsePlanFile(path string, plan *Plan, loggerInstance logger.Logger) error {
	planFile := path + "/plan.yml"
	loggerInstance.Debug("Reading plan file: %s", planFile)

	fileData, err := utils.SafeReadFile(planFile)
	if err != nil {
		loggerInstance.Error("Failed to read plan file %s: %s", planFile, err)

		return fmt.Errorf("failed to read plan file: %w", err)
	}

	err = yaml.Unmarshal(fileData, plan)
	if err != nil {
		loggerInstance.Error("Failed to unmarshal plan YAML from %s: %s", planFile, err)
		loggerInstance.Debug("Raw YAML content (first 500 chars): %s", string(fileData[:minInt(len(fileData), DebugYAMLContentLimit)]))

		return fmt.Errorf("failed to unmarshal plan YAML: %w", err)
	}

	return nil
}

func normalizePlanIDAndName(plan *Plan, loggerInstance logger.Logger) {
	if plan.ID == "" && plan.Name != "" {
		loggerInstance.Debug("Plan ID was empty, using Name: %s", plan.Name)
		plan.ID = plan.Name
	}

	if plan.Name == "" && plan.ID != "" {
		loggerInstance.Debug("Plan Name was empty, using ID: %s", plan.ID)
		plan.Name = plan.ID
	}

	loggerInstance.Debug("Plan details - ID: %s, Name: %s, Description: %s, Limit: %d, Type: %s", plan.ID, plan.Name, plan.Description, plan.Limit, plan.Type)
}

func processPlanManifest(path string, plan *Plan, loggerInstance logger.Logger) error {
	manifestFile := path + "/manifest.yml"
	paramsFile := path + "/params.yml"
	loggerInstance.Debug("Merging manifest files: %s and %s", manifestFile, paramsFile)

	manifest, err := utils.MergeFiles(manifestFile, paramsFile)
	if err != nil {
		loggerInstance.Error("Failed to merge manifest files for plan %s: %s", plan.Name, err)

		return fmt.Errorf("failed to merge manifest files: %w", err)
	}

	plan.Manifest = manifest
	plan.ManifestPath = manifestFile

	loggerInstance.Debug("Successfully merged manifest files for plan %s", plan.Name)

	return nil
}

func processPlanCredentials(path string, plan *Plan, loggerInstance logger.Logger) error {
	credsFile := path + "/credentials.yml"
	loggerInstance.Debug("Checking for credentials file: %s", credsFile)

	credFileData, exists, err := utils.ReadFile(credsFile)
	switch {
	case exists:
		return processExistingCredentialsFile(credFileData, credsFile, plan, loggerInstance)
	case err != nil:
		loggerInstance.Error("Error checking credentials file %s: %s", credsFile, err)

		return fmt.Errorf("failed to check credentials file: %w", err)
	default:
		loggerInstance.Debug("No credentials file found for plan %s, using empty credentials", plan.Name)
		plan.Credentials = make(map[interface{}]interface{})

		return nil
	}
}

func processExistingCredentialsFile(credFileData []byte, credsFile string, plan *Plan, loggerInstance logger.Logger) error {
	loggerInstance.Debug("Found credentials file for plan %s", plan.Name)

	err := yaml.Unmarshal(credFileData, &plan.Credentials)
	if err != nil {
		loggerInstance.Error("Failed to unmarshal credentials YAML for plan %s: %s", plan.Name, err)

		return fmt.Errorf("failed to unmarshal credentials YAML: %w", err)
	}

	plan.CredentialsPath = credsFile
	loggerInstance.Debug("Successfully loaded credentials for plan %s", plan.Name)

	return nil
}

func ReadPlans(dir string, service Service) ([]Plan, error) {
	log := logger.Get().Named("ReadPlans")
	log.Debug("Reading plans from directory: %s for service: %s", dir, service.Name)

	plans := make([]Plan, 0)

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Error("Failed to read plans directory %s: %s", dir, err)

		return plans, fmt.Errorf("failed to read plans directory: %w", err)
	}

	log.Debug("Found %d entries in plans directory", len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			planPath := fmt.Sprintf("%s/%s", dir, entry.Name())
			log.Debug("Processing plan directory: %s", planPath)

			planData, err := ReadPlan(planPath)
			if err != nil {
				log.Error("Failed to read plan from %s: %s", planPath, err)

				return plans, err
			}

			planData.ID = service.ID + "-" + planData.ID
			log.Debug("Set plan ID to: %s (service.ID=%s + plan.ID=%s)", planData.ID, service.ID, planData.ID)
			planData.Service = &service
			plans = append(plans, planData)
			log.Info("Added plan %s to service %s", planData.Name, service.Name)
		} else {
			log.Debug("Skipping non-directory entry: %s", entry.Name())
		}
	}

	log.Info("Successfully read %d plans for service %s", len(plans), service.Name)

	return plans, nil
}

func ReadService(path string) (Service, error) {
	log := logger.Get().Named("ReadService")
	log.Debug("Reading service from path: %s", path)

	var service Service

	file := path + "/service.yml"
	log.Debug("Reading service file: %s", file)

	serviceData, err := utils.SafeReadFile(file)
	if err != nil {
		log.Error("Failed to read service file %s: %s", file, err)

		return service, fmt.Errorf("%s: %w", file, err)
	}

	err = yaml.Unmarshal(serviceData, &service)
	if err != nil {
		log.Error("Failed to unmarshal service YAML from %s: %s", file, err)
		log.Debug("Raw YAML content (first 500 chars): %s", string(serviceData[:minInt(len(serviceData), DebugYAMLContentLimit)]))

		return service, fmt.Errorf("%s: %w", file, err)
	}

	if service.ID == "" && service.Name != "" {
		log.Debug("Service ID was empty, using Name: %s", service.Name)
		service.ID = service.Name
	}

	if service.Name == "" && service.ID != "" {
		log.Debug("Service Name was empty, using ID: %s", service.ID)
		service.Name = service.ID
	}

	log.Debug("Service details - ID: %s, Name: %s, Type: %s, Description: %s, Bindable: %v, Limit: %d",
		service.ID, service.Name, service.Type, service.Description, service.Bindable, service.Limit)

	err = CheckNames(service.ID, service.Name)
	if err != nil {
		log.Error("Invalid service names - ID: %s, Name: %s: %s", service.ID, service.Name, err)

		return service, fmt.Errorf("%s: %w", file, err)
	}

	log.Debug("Reading plans for service %s from %s", service.Name, path)

	servicePlans, err := ReadPlans(path, service)
	if err != nil {
		log.Error("Failed to read plans for service %s: %s", service.Name, err)

		return service, fmt.Errorf("%s: %w", file, err)
	}

	service.Plans = servicePlans
	log.Info("Successfully read service %s with %d plans", service.Name, len(service.Plans))

	return service, nil
}

func ReadServices(dirs ...string) ([]Service, error) {
	log := logger.Get().Named("ReadServices")
	log.Info("Starting to read services from %d directories", len(dirs))

	for i, dir := range dirs {
		log.Debug("Service directory %d: %s", i+1, dir)
	}

	if len(dirs) == 0 {
		log.Error("No service directories provided")

		return nil, ErrNoServiceDirectoriesFound
	}

	services := make([]Service, 0)

	for _, dir := range dirs {
		log.Debug("Processing service directory: %s", dir)

		serviceEntries, err := os.ReadDir(dir)
		if err != nil {
			log.Error("Failed to read service directory %s: %s", dir, err)

			return nil, fmt.Errorf("%s: %w", dir, err)
		}

		log.Debug("Found %d entries in directory %s", len(serviceEntries), dir)

		for _, serviceEntry := range serviceEntries {
			if serviceEntry.IsDir() {
				servicePath := fmt.Sprintf("%s/%s", dir, serviceEntry.Name())
				log.Debug("Processing service directory: %s", servicePath)

				serviceData, err := ReadService(servicePath)
				if err != nil {
					log.Error("Failed to read service from %s: %s", servicePath, err)

					return nil, fmt.Errorf("%s/%s: %w", dir, serviceEntry.Name(), err)
				}

				services = append(services, serviceData)
				log.Info("Added service %s (ID: %s) with %d plans", serviceData.Name, serviceData.ID, len(serviceData.Plans))
			} else {
				log.Debug("Skipping non-directory entry: %s", serviceEntry.Name())
			}
		}
	}

	log.Info("Successfully read %d services total", len(services))

	for _, s := range services {
		log.Debug("Service summary - ID: %s, Name: %s, Plans: %d", s.ID, s.Name, len(s.Plans))
	}

	return services, nil
}

func getDefaultScanPaths(configPaths []string) []string {
	if len(configPaths) > 0 {
		return configPaths
	}

	return []string{
		"/var/vcap/jobs",
		"/var/vcap/data/blacksmith",
	}
}

func getDefaultScanPatterns(configPatterns []string) []string {
	if len(configPatterns) > 0 {
		return configPatterns
	}

	return []string{
		"*-forge/templates",
		"*-forge",
	}
}

func scanBasePath(basePath string, scanPatterns []string, log logger.Logger) []string {
	log.Debug("Scanning base path: %s", basePath)

	if !pathExists(basePath) {
		log.Debug("Skipping non-existent path: %s", basePath)

		return nil
	}

	entries, err := os.ReadDir(basePath)
	if err != nil {
		log.Error("Failed to read directory %s: %s", basePath, err)

		return nil
	}

	var foundDirs []string

	for _, entry := range entries {
		if entry.IsDir() {
			dirs := checkEntryAgainstPatterns(basePath, entry, scanPatterns, log)
			foundDirs = append(foundDirs, dirs...)
		}
	}

	return foundDirs
}

func pathExists(path string) bool {
	_, err := os.Stat(path)

	return !os.IsNotExist(err)
}

func checkEntryAgainstPatterns(basePath string, entry os.DirEntry, scanPatterns []string, log logger.Logger) []string {
	entryPath := fmt.Sprintf("%s/%s", basePath, entry.Name())
	log.Debug("Checking directory: %s", entryPath)

	for _, pattern := range scanPatterns {
		if matchesPattern(pattern, entry.Name(), log) {
			log.Debug("Found forge directory matching pattern %s: %s", pattern, entryPath)

			return processMatchingDirectory(entryPath, pattern, log)
		}
	}

	return nil
}

func matchesPattern(pattern, name string, log logger.Logger) bool {
	matched, err := filepath.Match(pattern, name)
	if err != nil {
		log.Error("Error matching pattern %s against %s: %s", pattern, name, err)

		return false
	}

	return matched
}

func processMatchingDirectory(entryPath, pattern string, log logger.Logger) []string {
	if strings.Contains(pattern, "templates") {
		return processTemplateDirectory(entryPath, log)
	}

	return processDataDirectory(entryPath, log)
}

func processTemplateDirectory(entryPath string, log logger.Logger) []string {
	if hasServiceDefinitions(entryPath) {
		log.Info("Adding forge templates directory: %s", entryPath)

		return []string{entryPath}
	}

	return nil
}

func processDataDirectory(entryPath string, log logger.Logger) []string {
	if hasServiceDefinitions(entryPath) {
		log.Info("Adding forge data directory: %s", entryPath)

		return []string{entryPath}
	}

	return nil
}

func logFoundDirectories(forgeDirs []string, log logger.Logger) {
	for i, dir := range forgeDirs {
		log.Debug("Forge directory %d: %s", i+1, dir)
	}
}

func AutoScanForgeDirectories(config *config.Config) []string {
	log := logger.Get().Named("AutoScanForgeDirectories")
	log.Info("Starting auto-scan for forge directories")

	scanPaths := getDefaultScanPaths(config.Forges.ScanPaths)
	scanPatterns := getDefaultScanPatterns(config.Forges.ScanPatterns)

	log.Debug("Scanning paths: %v", scanPaths)
	log.Debug("Using patterns: %v", scanPatterns)

	var forgeDirs []string

	for _, basePath := range scanPaths {
		foundDirs := scanBasePath(basePath, scanPatterns, log)
		forgeDirs = append(forgeDirs, foundDirs...)
	}

	log.Info("Auto-scan completed. Found %d forge directories", len(forgeDirs))
	logFoundDirectories(forgeDirs, log)

	return forgeDirs
}

func hasServiceDefinitions(path string) bool {
	log := logger.Get().Named("hasServiceDefinitions")
	log.Debug("Checking for service definitions in: %s", path)

	entries, err := os.ReadDir(path)
	if err != nil {
		log.Debug("Could not read directory %s: %s", path, err)

		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		servicePath := fmt.Sprintf("%s/%s", path, entry.Name())
		serviceFile := servicePath + "/service.yml"

		_, err := os.Stat(serviceFile)
		if err == nil {
			log.Debug("Found service definition at: %s", serviceFile)

			return true
		}
	}

	log.Debug("No service definitions found in: %s", path)

	return false
}

func Catalog(services []Service) []domain.Service {
	loggerInstance := logger.Get().Named("Catalog")
	loggerInstance.Info("Creating broker catalog from %d services", len(services))

	brokerServices := make([]domain.Service, len(services))
	for index, service := range services {
		loggerInstance.Debug("Processing service %d/%d - ID: %s, Name: %s", index+1, len(services), service.ID, service.Name)

		var metadata domain.ServiceMetadata

		brokerServices[index].ID = service.ID
		brokerServices[index].Name = service.Name
		brokerServices[index].Description = service.Description
		brokerServices[index].Bindable = service.Bindable
		brokerServices[index].Tags = make([]string, len(service.Tags))
		brokerServices[index].Metadata = &metadata
		copy(brokerServices[index].Tags, service.Tags)
		loggerInstance.Debug("Service %s - Bindable: %v, Tags: %v", service.Name, service.Bindable, service.Tags)

		brokerServices[index].Plans = make([]domain.ServicePlan, len(service.Plans))
		loggerInstance.Debug("Processing %d plans for service %s", len(service.Plans), service.Name)

		for planIndex, servicePlan := range service.Plans {
			brokerServices[index].Plans[planIndex].ID = servicePlan.ID
			brokerServices[index].Plans[planIndex].Name = servicePlan.Name
			brokerServices[index].Plans[planIndex].Description = servicePlan.Description
			loggerInstance.Debug("  Plan %d/%d - ID: %s, Name: %s, Description: %s",
				planIndex+1, len(service.Plans), servicePlan.ID, servicePlan.Name, servicePlan.Description)
			/* FIXME: support free */
		}

		loggerInstance.Info("Added service %s to catalog with %d plans", service.Name, len(service.Plans))
	}

	loggerInstance.Info("Successfully created broker catalog with %d services", len(brokerServices))

	return brokerServices
}
