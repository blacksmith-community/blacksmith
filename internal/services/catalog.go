package services

import (
	"encoding/json"
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
	"github.com/fivetwenty-io/graft/pkg/yaml"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// Static errors for err113 compliance.
var (
	ErrInvalidNameFormat         = errors.New("invalid name format - names should only contain letters, numbers and hyphens")
	ErrNoServiceDirectoriesFound = errors.New("no service directories found")
	ErrPlanSchemaDeclaredTwice   = errors.New("the plan declares its parameter schema in both plan.yml and schema.json")
	ErrPlanSchemaNotAnObject     = errors.New("the parameter schema is not a JSON object")
)

// planChangesSupported reports whether this release of the broker can
// change an instance's plan. Until it can, every plan publishes
// plan_updateable false, whatever the forge declares.
const planChangesSupported = false

// contextUpdatesSupported reports whether this release of the broker can
// apply an update that changes only an instance's context. Until it can,
// the catalog publishes allow_context_updates false.
const contextUpdatesSupported = false

// maintenanceInfoSupported reports whether this release of the broker can
// run a maintenance update. Until it can, the catalog publishes no
// maintenance_info.
const maintenanceInfoSupported = false

type Plan struct {
	ID          string `json:"id"          yaml:"id"`
	Name        string `json:"name"        yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Limit       int    `json:"limit"       yaml:"limit"`
	Type        string `json:"type"        yaml:"type"`

	// The credentials and the three paths keep the key yaml.v2 gave each
	// from its lower-cased name. graft takes a key from the json tag when
	// there is no yaml tag, and json:"-" would leave them out of plan.yml.
	Manifest       map[string]interface{} `json:"-" yaml:"-"`
	Credentials    map[string]interface{} `json:"-" yaml:"credentials"`
	InitScriptPath string                 `json:"-" yaml:"initscriptpath"`

	// Store the actual file paths for SHA256 calculation
	ManifestPath    string `json:"-" yaml:"manifestpath"`
	CredentialsPath string `json:"-" yaml:"credentialspath"`

	Service *Service `json:"service" yaml:"service"`

	// Free is the plan's free flag. A plan that sets none is free, as the
	// OSB catalog's default is.
	Free *bool `json:"free,omitempty" yaml:"free"`

	// PlanUpdateable is what the plan declares about changing to another
	// plan, and when it declares nothing the service's declaration holds.
	// UpdateTo names the plans this plan can move to. The catalog
	// publishes a computed flag, never these declarations as they are.
	PlanUpdateable *bool    `json:"plan_updateable,omitempty" yaml:"plan_updateable"`
	UpdateTo       []string `json:"update_to,omitempty"       yaml:"update_to"`

	// Schemas holds the JSON schema of the plan's instance parameters,
	// from the schemas block of plan.yml or from schema.json beside it.
	Schemas *Schemas `json:"schemas,omitempty" yaml:"schemas"`

	// EphemeralUpdateOK lets a plan whose instance groups keep no
	// persistent disk take part in a plan change.
	EphemeralUpdateOK bool `json:"ephemeral_update_ok,omitempty" yaml:"ephemeral_update_ok"`

	MaintenanceInfo *MaintenanceInfo `json:"maintenance_info,omitempty" yaml:"maintenance_info"`
}

// Schemas holds the JSON schema a plan's instance parameters must match.
// The catalog publishes it for both create and update.
type Schemas struct {
	Parameters map[string]interface{} `json:"parameters,omitempty" yaml:"parameters"`
}

// MaintenanceInfo is the version a service or plan declares for its
// maintenance updates.
type MaintenanceInfo struct {
	Version     string `json:"version"               yaml:"version"`
	Description string `json:"description,omitempty" yaml:"description"`
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

	// PlanUpdateable is what service.yml declares about plan changes,
	// and a plan that declares nothing of its own takes it.
	// AllowContextUpdates is the service's declaration that it accepts an
	// update that changes only the instance's context. InitIdempotent
	// declares that the forge's init script can run again without
	// rotating a secret it already generated.
	PlanUpdateable      bool             `json:"plan_updateable,omitempty"       yaml:"plan_updateable"`
	AllowContextUpdates bool             `json:"allow_context_updates,omitempty" yaml:"allow_context_updates"`
	InitIdempotent      bool             `json:"init_idempotent,omitempty"       yaml:"init_idempotent"`
	MaintenanceInfo     *MaintenanceInfo `json:"maintenance_info,omitempty"      yaml:"maintenance_info"`
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

	err = processPlanSchema(path, &plan, loggerInstance)
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

	// plan.yml is a template, so a repeated key keeps its last value and
	// the warning names the file and the key.
	warn := duplicateKeyWarner(loggerInstance)

	err = yaml.UnmarshalWith(fileData, plan, yaml.DecodeOptions{
		OnDuplicateKey: func(key yaml.DuplicateKey) { warn(planFile, key.Path) },
	})
	if err != nil {
		loggerInstance.Error("Failed to unmarshal plan YAML from %s: %s", planFile, err)
		loggerInstance.Debug("Plan file %s (%d bytes) did not decode: %s", planFile, len(fileData), err)

		return fmt.Errorf("failed to unmarshal plan YAML %s: %w", planFile, err)
	}

	return nil
}

// duplicateKeyWarner returns the function that logs a key a plan template
// repeats. It names the file and the key and never the value.
func duplicateKeyWarner(loggerInstance logger.Logger) utils.DuplicateKeyFunc {
	return func(file, key string) {
		loggerInstance.Warn("Plan template %s repeats the key %s, and its last value wins", file, key)
	}
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

	manifest, err := utils.MergeFiles(duplicateKeyWarner(loggerInstance), manifestFile, paramsFile)
	if err != nil {
		loggerInstance.Error("Failed to merge manifest files for plan %s: %s", plan.Name, err)

		return fmt.Errorf("failed to merge manifest files: %w", err)
	}

	// A manifest.yml that holds only a null merges to a nil map, and the plan
	// keeps an empty one, as it always has.
	if manifest == nil {
		manifest = map[string]interface{}{}
	}

	plan.Manifest = manifest
	plan.ManifestPath = manifestFile

	loggerInstance.Debug("Successfully merged manifest files for plan %s", plan.Name)

	return nil
}

// processPlanSchema reads schema.json beside plan.yml, when there is one, as
// the JSON schema of the plan's instance parameters. A plan declares its
// schema in one place, so a plan.yml that has a schemas block of its own
// beside a schema.json is refused.
func processPlanSchema(path string, plan *Plan, loggerInstance logger.Logger) error {
	schemaFile := path + "/schema.json"

	data, exists, err := utils.ReadFile(schemaFile)
	switch {
	case err != nil:
		loggerInstance.Error("Error checking schema file %s: %s", schemaFile, err)

		return fmt.Errorf("failed to check schema file: %w", err)
	case !exists:
		return nil
	case plan.Schemas != nil:
		return fmt.Errorf("%s: %w", schemaFile, ErrPlanSchemaDeclaredTwice)
	}

	var parameters map[string]interface{}

	err = json.Unmarshal(data, &parameters)
	if err != nil {
		loggerInstance.Debug("Schema file %s (%d bytes) did not decode: %s", schemaFile, len(data), err)

		return fmt.Errorf("%s: %w", schemaFile, ErrPlanSchemaNotAnObject)
	}

	if parameters == nil {
		return fmt.Errorf("%s: %w", schemaFile, ErrPlanSchemaNotAnObject)
	}

	plan.Schemas = &Schemas{Parameters: parameters}
	loggerInstance.Debug("Loaded the parameter schema of plan %s from %s", plan.Name, schemaFile)

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
		plan.Credentials = make(map[string]interface{})

		return nil
	}
}

func processExistingCredentialsFile(credFileData []byte, credsFile string, plan *Plan, loggerInstance logger.Logger) error {
	loggerInstance.Debug("Found credentials file for plan %s", plan.Name)

	var credentials map[string]interface{}

	err := yaml.Unmarshal(credFileData, &credentials)
	if err != nil {
		loggerInstance.Error("Failed to unmarshal credentials YAML for plan %s: %s", plan.Name, err)

		return fmt.Errorf("failed to unmarshal credentials YAML %s: %w", credsFile, err)
	}

	plan.Credentials = mergeCredentials(plan.Credentials, credentials, utils.IsNullDocument(credFileData))

	plan.CredentialsPath = credsFile
	loggerInstance.Debug("Successfully loaded credentials for plan %s", plan.Name)

	return nil
}

// mergeCredentials adds the keys of credentials.yml to the credentials
// plan.yml gives, and a key both set takes its value from credentials.yml.
// A credentials.yml that holds nothing leaves them as they are, and one
// that holds only a null clears them.
func mergeCredentials(fromPlan, fromFile map[string]interface{}, null bool) map[string]interface{} {
	switch {
	case null:
		return nil
	case fromFile == nil:
		return fromPlan
	}

	merged := make(map[string]interface{}, len(fromPlan)+len(fromFile))

	for key, value := range fromPlan {
		merged[key] = value
	}

	for key, value := range fromFile {
		merged[key] = value
	}

	return merged
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

			dropInvalidSchema(&planData, service, log)

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

// dropInvalidSchema leaves a plan's parameter schema out of the catalog
// when it is not a schema Cloud Controller accepts. Cloud Controller
// refuses the whole catalog over one bad schema, which would block every
// service the broker offers, so the plan is still published without its
// schema, and the error names the forge and the plan.
func dropInvalidSchema(plan *Plan, service Service, log logger.Logger) {
	if plan.Schemas == nil || plan.Schemas.Parameters == nil {
		return
	}

	err := ValidateParameterSchema(plan.Schemas.Parameters)
	if err != nil {
		log.Error("forge %s plan %s publishes no parameter schema, because its schema is refused: %s", service.Name, plan.Name, err)

		plan.Schemas = nil
	}
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
		log.Debug("Service file %s (%d bytes) did not decode: %s", file, len(serviceData), err)

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

func Catalog(services []Service) []osbapi.Service {
	loggerInstance := logger.Get().Named("Catalog")
	loggerInstance.Info("Creating broker catalog from %d services", len(services))

	brokerServices := make([]osbapi.Service, len(services))
	for index, service := range services {
		loggerInstance.Debug("Processing service %d/%d - ID: %s, Name: %s", index+1, len(services), service.ID, service.Name)

		brokerServices[index].ID = service.ID
		brokerServices[index].Name = service.Name
		brokerServices[index].Description = service.Description
		brokerServices[index].Bindable = service.Bindable
		brokerServices[index].Tags = make([]string, len(service.Tags))
		brokerServices[index].Metadata = nil
		copy(brokerServices[index].Tags, service.Tags)
		loggerInstance.Debug("Service %s - Bindable: %v, Tags: %v", service.Name, service.Bindable, service.Tags)

		brokerServices[index].Plans = make([]osbapi.Plan, len(service.Plans))
		loggerInstance.Debug("Processing %d plans for service %s", len(service.Plans), service.Name)

		anyUpdateable := false

		for planIndex, servicePlan := range service.Plans {
			updateable := PlanUpdateable(planChangesSupported, service, servicePlan)
			anyUpdateable = anyUpdateable || updateable

			brokerServices[index].Plans[planIndex] = catalogPlan(servicePlan, updateable)
			loggerInstance.Debug("  Plan %d/%d - ID: %s, Name: %s, Description: %s, plan_updateable: %v",
				planIndex+1, len(service.Plans), servicePlan.ID, servicePlan.Name, servicePlan.Description, updateable)
		}

		brokerServices[index].PlanUpdateable = &anyUpdateable
		brokerServices[index].AllowContextUpdates = contextUpdatesSupported && service.AllowContextUpdates

		if maintenanceInfoSupported {
			brokerServices[index].MaintenanceInfo = catalogMaintenanceInfo(service.MaintenanceInfo)
		}

		loggerInstance.Info("Added service %s to catalog with %d plans", service.Name, len(service.Plans))
	}

	loggerInstance.Info("Successfully created broker catalog with %d services", len(brokerServices))

	return brokerServices
}

// PlanUpdateable computes the plan_updateable flag the catalog publishes for
// plan. It is true only when the broker supports plan changes, the plan
// allows them by its own declaration or, when it makes none, by its
// service's, the plan names the plans it can move to, and the forge
// declares that its init script can run again without rotating secrets.
func PlanUpdateable(supported bool, service Service, plan Plan) bool {
	declared := service.PlanUpdateable
	if plan.PlanUpdateable != nil {
		declared = *plan.PlanUpdateable
	}

	return supported && declared && service.InitIdempotent && len(plan.UpdateTo) > 0
}

// catalogPlan converts plan into the plan the catalog publishes, with
// updateable as its plan_updateable flag.
func catalogPlan(plan Plan, updateable bool) osbapi.Plan {
	free := true
	if plan.Free != nil {
		free = *plan.Free
	}

	published := osbapi.Plan{
		ID:             plan.ID,
		Name:           plan.Name,
		Description:    plan.Description,
		Free:           &free,
		PlanUpdateable: &updateable,
	}

	if plan.Schemas != nil && plan.Schemas.Parameters != nil {
		published.Schemas = &osbapi.Schemas{
			ServiceInstance: &osbapi.ServiceInstanceSchema{
				Create: &osbapi.InputParametersSchema{Parameters: plan.Schemas.Parameters},
				Update: &osbapi.InputParametersSchema{Parameters: plan.Schemas.Parameters},
			},
		}
	}

	if maintenanceInfoSupported {
		published.MaintenanceInfo = catalogMaintenanceInfo(plan.MaintenanceInfo)
	}

	return published
}

func catalogMaintenanceInfo(info *MaintenanceInfo) *osbapi.MaintenanceInfo {
	if info == nil {
		return nil
	}

	return &osbapi.MaintenanceInfo{Version: info.Version, Description: info.Description}
}
