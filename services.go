package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pivotal-cf/brokerapi/v8/domain"
	"gopkg.in/yaml.v2"
)

type Plan struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Limit       int    `yaml:"limit" json:"limit"`
	Type        string `yaml:"type" json:"type"`

	Manifest       map[interface{}]interface{} `json:"-"`
	Credentials    map[interface{}]interface{} `json:"-"`
	InitScriptPath string                      `json:"-"`

	// Store the actual file paths for SHA256 calculation
	ManifestPath    string `json:"-"`
	CredentialsPath string `json:"-"`

	Service *Service `yaml:"service" json:"service"`
}

type Service struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Type        string   `yaml:"type" json:"type"`
	Description string   `yaml:"description" json:"description"`
	Bindable    bool     `yaml:"bindable" json:"bindable"`
	Tags        []string `yaml:"tags" json:"tags"`
	Limit       int      `yaml:"limit" json:"limit"`
	Plans       []Plan   `yaml:"plans" json:"plans"`
}

var ValidName *regexp.Regexp

func init() {
	ValidName = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func CheckNames(names ...string) error {
	for _, s := range names {
		if !ValidName.MatchString(s) {
			return fmt.Errorf("'%s' is invalid; names should only contain letters, numbers and hyphens", s)
		}
	}
	return nil
}

func (p Plan) String() string {
	return fmt.Sprintf("%s/%s", p.Service.ID, p.ID)
}

func (p Plan) OverLimit(db *VaultIndex) bool {
	if p.Limit == 0 && p.Service.Limit == 0 {
		return false
	}

	if p.Limit < 0 || p.Service.Limit < 0 {
		return true
	}

	existingPlan := 0
	existingService := 0

	for _, s := range db.Data {
		if ss, ok := s.(map[string]interface{}); ok {
			service, haveService := ss["service_id"]
			plan, havePlan := ss["plan_id"]
			if havePlan && haveService {
				if v, ok := service.(string); ok && v == p.Service.ID {
					existingService += 1
					if v, ok := plan.(string); ok && v == p.ID {
						existingPlan += 1
					}
				}
			}
		}
	}

	if p.Limit > 0 && existingPlan >= p.Limit {
		return true
	}

	if p.Service.Limit > 0 && existingService >= p.Service.Limit {
		return true
	}

	return false
}

func ReadPlan(path string) (p Plan, err error) {
	l := Logger.Wrap("ReadPlan")
	l.Debug("Reading plan from path: %s", path)
	planFile := fmt.Sprintf("%s/plan.yml", path)
	l.Debug("Reading plan file: %s", planFile)
	b, err := os.ReadFile(planFile)
	if err != nil {
		l.Error("Failed to read plan file %s: %s", planFile, err)
		return
	}
	err = yaml.Unmarshal(b, &p)
	if err != nil {
		l.Error("Failed to unmarshal plan YAML from %s: %s", planFile, err)
		l.Debug("Raw YAML content (first 500 chars): %s", string(b[:min(len(b), 500)]))
		return
	}
	if p.ID == "" && p.Name != "" {
		l.Debug("Plan ID was empty, using Name: %s", p.Name)
		p.ID = p.Name
	}
	if p.Name == "" && p.ID != "" {
		l.Debug("Plan Name was empty, using ID: %s", p.ID)
		p.Name = p.ID
	}
	l.Debug("Plan details - ID: %s, Name: %s, Description: %s, Limit: %d, Type: %s", p.ID, p.Name, p.Description, p.Limit, p.Type)
	if err = CheckNames(p.ID, p.Name); err != nil {
		l.Error("Invalid plan names - ID: %s, Name: %s: %s", p.ID, p.Name, err)
		return
	}

	manifestFile := fmt.Sprintf("%s/manifest.yml", path)
	paramsFile := fmt.Sprintf("%s/params.yml", path)
	l.Debug("Merging manifest files: %s and %s", manifestFile, paramsFile)
	p.Manifest, err = mergeFiles(manifestFile, paramsFile)
	if err != nil {
		l.Error("Failed to merge manifest files for plan %s: %s", p.Name, err)
		return
	}
	l.Debug("Successfully merged manifest files for plan %s", p.Name)

	// Store the manifest file path for later SHA256 calculation
	p.ManifestPath = manifestFile

	credsFile := fmt.Sprintf("%s/credentials.yml", path)
	l.Debug("Checking for credentials file: %s", credsFile)
	b, exists, err := readFile(credsFile)
	if exists {
		l.Debug("Found credentials file for plan %s", p.Name)
		if err != nil {
			l.Error("Error reading credentials file %s: %s", credsFile, err)
			return
		}
		err = yaml.Unmarshal(b, &p.Credentials)
		if err != nil {
			l.Error("Failed to unmarshal credentials YAML for plan %s: %s", p.Name, err)
			return
		}
		l.Debug("Successfully loaded credentials for plan %s", p.Name)
		// Store the credentials file path for later SHA256 calculation
		p.CredentialsPath = credsFile
	} else if err != nil {
		l.Error("Error checking credentials file %s: %s", credsFile, err)
		return
	} else {
		l.Debug("No credentials file found for plan %s, using empty credentials", p.Name)
		p.Credentials = make(map[interface{}]interface{})
		// No credentials file path to store
	}

	p.InitScriptPath = fmt.Sprintf("%s/init", path)
	l.Info("Successfully read plan %s from %s", p.Name, path)
	return
}

func ReadPlans(dir string, service Service) ([]Plan, error) {
	l := Logger.Wrap("ReadPlans")
	l.Debug("Reading plans from directory: %s for service: %s", dir, service.Name)
	pp := make([]Plan, 0)

	ls, err := os.ReadDir(dir)
	if err != nil {
		l.Error("Failed to read plans directory %s: %s", dir, err)
		return pp, err
	}
	l.Debug("Found %d entries in plans directory", len(ls))

	for _, f := range ls {
		if f.IsDir() {
			planPath := fmt.Sprintf("%s/%s", dir, f.Name())
			l.Debug("Processing plan directory: %s", planPath)
			p, err := ReadPlan(planPath)
			if err != nil {
				l.Error("Failed to read plan from %s: %s", planPath, err)
				return pp, err
			}
			p.ID = service.ID + "-" + p.ID
			l.Debug("Set plan ID to: %s (service.ID=%s + plan.ID=%s)", p.ID, service.ID, p.ID)
			p.Service = &service
			pp = append(pp, p)
			l.Info("Added plan %s to service %s", p.Name, service.Name)
		} else {
			l.Debug("Skipping non-directory entry: %s", f.Name())
		}
	}
	l.Info("Successfully read %d plans for service %s", len(pp), service.Name)
	return pp, err
}

func ReadService(path string) (Service, error) {
	l := Logger.Wrap("ReadService")
	l.Debug("Reading service from path: %s", path)
	var s Service
	file := fmt.Sprintf("%s/service.yml", path)
	l.Debug("Reading service file: %s", file)

	b, err := os.ReadFile(file)
	if err != nil {
		l.Error("Failed to read service file %s: %s", file, err)
		return s, fmt.Errorf("%s: %s", file, err)
	}

	err = yaml.Unmarshal(b, &s)
	if err != nil {
		l.Error("Failed to unmarshal service YAML from %s: %s", file, err)
		l.Debug("Raw YAML content (first 500 chars): %s", string(b[:min(len(b), 500)]))
		return s, fmt.Errorf("%s: %s", file, err)
	}
	if s.ID == "" && s.Name != "" {
		l.Debug("Service ID was empty, using Name: %s", s.Name)
		s.ID = s.Name
	}
	if s.Name == "" && s.ID != "" {
		l.Debug("Service Name was empty, using ID: %s", s.ID)
		s.Name = s.ID
	}
	l.Debug("Service details - ID: %s, Name: %s, Type: %s, Description: %s, Bindable: %v, Limit: %d",
		s.ID, s.Name, s.Type, s.Description, s.Bindable, s.Limit)
	if err = CheckNames(s.ID, s.Name); err != nil {
		l.Error("Invalid service names - ID: %s, Name: %s: %s", s.ID, s.Name, err)
		return s, fmt.Errorf("%s: %s", file, err)
	}

	l.Debug("Reading plans for service %s from %s", s.Name, path)
	pp, err := ReadPlans(path, s)
	if err != nil {
		l.Error("Failed to read plans for service %s: %s", s.Name, err)
		return s, fmt.Errorf("%s: %s", file, err)
	}
	s.Plans = pp
	l.Info("Successfully read service %s with %d plans", s.Name, len(s.Plans))
	return s, nil
}

func ReadServices(dirs ...string) ([]Service, error) {
	l := Logger.Wrap("ReadServices")
	l.Info("Starting to read services from %d directories", len(dirs))
	for i, dir := range dirs {
		l.Debug("Service directory %d: %s", i+1, dir)
	}

	if len(dirs) == 0 {
		l.Error("No service directories provided")
		return nil, fmt.Errorf("no service directories found")
	}

	ss := make([]Service, 0)
	for _, dir := range dirs {
		l.Debug("Processing service directory: %s", dir)
		ls, err := os.ReadDir(dir)
		if err != nil {
			l.Error("Failed to read service directory %s: %s", dir, err)
			return nil, fmt.Errorf("%s: %s", dir, err)
		}
		l.Debug("Found %d entries in directory %s", len(ls), dir)

		for _, f := range ls {
			if f.IsDir() {
				servicePath := fmt.Sprintf("%s/%s", dir, f.Name())
				l.Debug("Processing service directory: %s", servicePath)
				s, err := ReadService(servicePath)
				if err != nil {
					l.Error("Failed to read service from %s: %s", servicePath, err)
					return nil, fmt.Errorf("%s/%s: %s", dir, f.Name(), err)
				}
				ss = append(ss, s)
				l.Info("Added service %s (ID: %s) with %d plans", s.Name, s.ID, len(s.Plans))
			} else {
				l.Debug("Skipping non-directory entry: %s", f.Name())
			}
		}
	}
	l.Info("Successfully read %d services total", len(ss))
	for _, s := range ss {
		l.Debug("Service summary - ID: %s, Name: %s, Plans: %d", s.ID, s.Name, len(s.Plans))
	}
	return ss, nil
}

func AutoScanForgeDirectories(config *Config) ([]string, error) {
	l := Logger.Wrap("AutoScanForgeDirectories")
	l.Info("Starting auto-scan for forge directories")

	var forgeDirs []string

	// Default scan paths if none configured
	scanPaths := config.Forges.ScanPaths
	if len(scanPaths) == 0 {
		scanPaths = []string{
			"/var/vcap/jobs",
			"/var/vcap/data/blacksmith",
		}
	}

	// Default scan patterns if none configured
	scanPatterns := config.Forges.ScanPatterns
	if len(scanPatterns) == 0 {
		scanPatterns = []string{
			"*-forge/templates",
			"*-forge",
		}
	}

	l.Debug("Scanning paths: %v", scanPaths)
	l.Debug("Using patterns: %v", scanPatterns)

	for _, basePath := range scanPaths {
		l.Debug("Scanning base path: %s", basePath)

		// Check if base path exists
		if _, err := os.Stat(basePath); os.IsNotExist(err) {
			l.Debug("Skipping non-existent path: %s", basePath)
			continue
		}

		// Read directory entries
		entries, err := os.ReadDir(basePath)
		if err != nil {
			l.Error("Failed to read directory %s: %s", basePath, err)
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			entryPath := fmt.Sprintf("%s/%s", basePath, entry.Name())
			l.Debug("Checking directory: %s", entryPath)

			// Check against scan patterns
			for _, pattern := range scanPatterns {
				matched, err := filepath.Match(pattern, entry.Name())
				if err != nil {
					l.Error("Error matching pattern %s against %s: %s", pattern, entry.Name(), err)
					continue
				}

				if matched {
					l.Debug("Found forge directory matching pattern %s: %s", pattern, entryPath)

					// For template paths, check if they contain service definitions
					if strings.Contains(pattern, "templates") {
						if hasServiceDefinitions(entryPath) {
							l.Info("Adding forge templates directory: %s", entryPath)
							forgeDirs = append(forgeDirs, entryPath)
						}
					} else {
						// For data paths, check if they contain service definitions
						if hasServiceDefinitions(entryPath) {
							l.Info("Adding forge data directory: %s", entryPath)
							forgeDirs = append(forgeDirs, entryPath)
						}
					}
					break // Stop checking other patterns for this entry
				}
			}
		}
	}

	l.Info("Auto-scan completed. Found %d forge directories", len(forgeDirs))
	for i, dir := range forgeDirs {
		l.Debug("Forge directory %d: %s", i+1, dir)
	}

	return forgeDirs, nil
}

func hasServiceDefinitions(path string) bool {
	l := Logger.Wrap("hasServiceDefinitions")
	l.Debug("Checking for service definitions in: %s", path)

	entries, err := os.ReadDir(path)
	if err != nil {
		l.Debug("Could not read directory %s: %s", path, err)
		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		servicePath := fmt.Sprintf("%s/%s", path, entry.Name())
		serviceFile := fmt.Sprintf("%s/service.yml", servicePath)

		if _, err := os.Stat(serviceFile); err == nil {
			l.Debug("Found service definition at: %s", serviceFile)
			return true
		}
	}

	l.Debug("No service definitions found in: %s", path)
	return false
}

func Catalog(ss []Service) []domain.Service {
	l := Logger.Wrap("Catalog")
	l.Info("Creating broker catalog from %d services", len(ss))

	bb := make([]domain.Service, len(ss))
	for i, s := range ss {
		l.Debug("Processing service %d/%d - ID: %s, Name: %s", i+1, len(ss), s.ID, s.Name)
		var md domain.ServiceMetadata
		bb[i].ID = s.ID
		bb[i].Name = s.Name
		bb[i].Description = s.Description
		bb[i].Bindable = s.Bindable
		bb[i].Tags = make([]string, len(s.Tags))
		bb[i].Metadata = &md
		copy(bb[i].Tags, s.Tags)
		l.Debug("Service %s - Bindable: %v, Tags: %v", s.Name, s.Bindable, s.Tags)

		bb[i].Plans = make([]domain.ServicePlan, len(s.Plans))
		l.Debug("Processing %d plans for service %s", len(s.Plans), s.Name)
		for j, p := range s.Plans {
			bb[i].Plans[j].ID = p.ID
			bb[i].Plans[j].Name = p.Name
			bb[i].Plans[j].Description = p.Description
			l.Debug("  Plan %d/%d - ID: %s, Name: %s, Description: %s",
				j+1, len(s.Plans), p.ID, p.Name, p.Description)
			/* FIXME: support free */
		}
		l.Info("Added service %s to catalog with %d plans", s.Name, len(s.Plans))
	}

	l.Info("Successfully created broker catalog with %d services", len(bb))
	return bb
}
