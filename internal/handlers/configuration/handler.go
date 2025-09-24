package configuration

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Static errors for err113 linter compliance.
var (
	errDirectorDependencyNotAvailable = errors.New("director dependency not available")
)

// Constants for configuration defaults.
const (
	// Pagination defaults for config list endpoint.
	configListDefaultLimit = 50
	configListMaxLimit     = 200

	// Pagination defaults for config versions endpoint.
	configVersionsDefaultLimit = 20
	configVersionsMaxLimit     = 100

	// Redis defaults.
	redisDefaultTimeout      = 300
	redisDefaultTCPKeepAlive = 60
	redisDefaultDatabases    = 16
	redisDefaultPort         = 6379

	// Backup defaults.
	defaultBackupRetentionDays = 7

	// Test task ID.
	testTaskID = 2000
)

var (
	configDetailPattern   = regexp.MustCompile(`^/b/configs/([^/]+)$`)
	configVersionsPattern = regexp.MustCompile(`^/b/configs/([^/]+)/([^/]+)/versions$`)
	configDiffPattern     = regexp.MustCompile(`^/b/configs/([^/]+)/([^/]+)/diff$`)
	instanceConfigPattern = regexp.MustCompile(`^/b/([^/]+)/config$`)
)

// Error variables for err113 compliance.
var (
	errConfigurationEndpointNotFound = errors.New("configuration endpoint not found")
	errInvalidConfigurationUpdate    = errors.New("invalid configuration update")
)

// Handler handles configuration-related endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	vault    interfaces.Vault
	director interfaces.Director
}

// Dependencies contains all dependencies needed by the Configuration handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Vault    interfaces.Vault
	Director interfaces.Director
}

// ServiceFilterOption represents a service filter option.
type ServiceFilterOption struct {
	Service   string   `json:"service"`
	Plans     []string `json:"plans"`
	Tags      []string `json:"tags"`
	Bindable  bool     `json:"bindable"`
	Instances int      `json:"instances"`
}

// NewHandler creates a new Configuration handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:   deps.Logger,
		config:   deps.Config,
		vault:    deps.Vault,
		director: deps.Director,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for instance config endpoint
	configPattern := regexp.MustCompile(`^/b/[^/]+/config$`)

	return configPattern.MatchString(path)
}

// ServeHTTP handles configuration-related endpoints with pattern matching.

func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Handle config diff endpoint first to avoid matching generic patterns
	if m := configDiffPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet {
		h.GetConfigDiff(writer, req, m[1], m[2])

		return
	}

	// Handle config versions endpoint
	if m := configVersionsPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet {
		h.GetConfigVersions(writer, req, m[1], m[2])

		return
	}

	// Handle config detail endpoint
	if m := configDetailPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet {
		h.GetConfigDetails(writer, req, m[1])

		return
	}

	// Handle /b/configs endpoint
	if req.URL.Path == "/b/configs" && req.Method == http.MethodGet {
		h.GetConfigs(writer, req)

		return
	}

	// Handle /b/service-filter-options endpoint
	if req.URL.Path == "/b/service-filter-options" && req.Method == http.MethodGet {
		h.GetServiceFilterOptions(writer, req)

		return
	}

	// Handle /b/{instance}/config endpoint - service instance configuration
	if m := instanceConfigPattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]

		switch req.Method {
		case http.MethodGet:
			h.GetInstanceConfig(writer, req, instanceID)
		case http.MethodPost, http.MethodPut:
			h.UpdateInstanceConfig(writer, req, instanceID)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}

		return
	}

	// No matching endpoint
	writer.WriteHeader(http.StatusNotFound)
	response.HandleJSON(writer, nil, errConfigurationEndpointNotFound)
}

// GetConfigs returns all available configurations.
func (h *Handler) GetConfigs(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("configs-list")
	logger.Debug("Fetching all configurations")

	if h.director == nil {
		logger.Error("BOSH director dependency is not configured")
		response.HandleJSON(writer, nil, errDirectorDependencyNotAvailable)

		return
	}

	query := req.URL.Query()
	limit := parseLimit(query.Get("limit"), configListDefaultLimit, configListMaxLimit)
	configTypes := parseTypes(query.Get("types"))

	logger.Debugf("Fetching configs with limit=%d types=%v", limit, configTypes)

	configs, err := h.director.GetConfigs(limit, configTypes)
	if err != nil {
		logger.Errorf("Failed to fetch configs: %v", err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to fetch configs: %w", err))

		return
	}

	logger.Infof("Successfully fetched %d configs", len(configs))

	payload := map[string]interface{}{
		"configs": configs,
		"count":   len(configs),
	}

	response.HandleJSON(writer, payload, nil)
}

// GetConfigDetails returns detailed information for a specific config ID.
func (h *Handler) GetConfigDetails(writer http.ResponseWriter, req *http.Request, configID string) {
	logger := h.logger.Named("config-details")
	logger.Debugf("Fetching config details for ID: %s", configID)

	if h.director == nil {
		logger.Error("BOSH director dependency is not configured")
		response.HandleJSON(writer, nil, errDirectorDependencyNotAvailable)

		return
	}

	config, err := h.director.GetConfigByID(configID)
	if err != nil {
		logger.Errorf("Failed to fetch config %s: %v", configID, err)

		switch {
		case errors.Is(err, bosh.ErrInvalidConfigIDFormat):
			response.WriteError(writer, http.StatusBadRequest, "invalid config ID: "+configID)
		case errors.Is(err, bosh.ErrConfigNotFound):
			response.WriteError(writer, http.StatusNotFound, "config not found: "+configID)
		default:
			response.HandleJSON(writer, nil, fmt.Errorf("failed to fetch config: %w", err))
		}

		return
	}

	logger.Infof("Successfully fetched config details for ID: %s", configID)
	response.HandleJSON(writer, config, nil)
}

// GetConfigVersions returns version history for a config.
func (h *Handler) GetConfigVersions(writer http.ResponseWriter, req *http.Request, configType, configName string) {
	logger := h.logger.Named("config-versions")
	logger.Debugf("Fetching config versions for type=%s name=%s", configType, configName)

	if h.director == nil {
		logger.Error("BOSH director dependency is not configured")
		response.HandleJSON(writer, nil, errDirectorDependencyNotAvailable)

		return
	}

	query := req.URL.Query()
	limit := parseLimit(query.Get("limit"), configVersionsDefaultLimit, configVersionsMaxLimit)

	logger.Debugf("Fetching versions with limit=%d", limit)

	configs, err := h.director.GetConfigVersions(configType, configName, limit)
	if err != nil {
		logger.Errorf("Failed to fetch config versions for %s/%s: %v", configType, configName, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to fetch config versions: %w", err))

		return
	}

	logger.Infof("Successfully fetched %d config versions for %s/%s", len(configs), configType, configName)

	payload := map[string]interface{}{
		"configs": configs,
		"count":   len(configs),
		"type":    configType,
		"name":    configName,
	}

	response.HandleJSON(writer, payload, nil)
}

// GetConfigDiff returns a diff between two config versions.
func (h *Handler) GetConfigDiff(writer http.ResponseWriter, req *http.Request, configType, configName string) {
	logger := h.logger.Named("config-diff")
	logger.Debugf("Computing config diff for type=%s name=%s", configType, configName)

	if h.director == nil {
		logger.Error("BOSH director dependency is not configured")
		response.HandleJSON(writer, nil, errDirectorDependencyNotAvailable)

		return
	}

	fromID, toID, parametersValid := h.parseDiffParameters(writer, req, logger)
	if !parametersValid {
		return
	}

	diff, ok := h.computeDiff(writer, logger, configType, configName, fromID, toID)
	if !ok {
		return
	}

	payload := h.buildDiffPayload(configType, configName, fromID, toID, diff)
	h.enrichDiffPayloadWithMetadata(payload, fromID, toID)

	logger.Infof("Successfully computed config diff (has_changes=%v)", diff.HasChanges)
	response.HandleJSON(writer, payload, nil)
}

func parseLimit(value string, defaultLimit, maxLimit int) int {
	if value == "" {
		return defaultLimit
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return defaultLimit
	}

	if parsed <= 0 || parsed > maxLimit {
		return defaultLimit
	}

	return parsed
}

func parseTypes(value string) []string {
	if value == "" {
		return nil
	}

	rawTypes := strings.Split(value, ",")
	types := make([]string, 0, len(rawTypes))

	for _, t := range rawTypes {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			types = append(types, trimmed)
		}
	}

	return types
}

// GetServiceFilterOptions returns service filter options for UI.
func (h *Handler) GetServiceFilterOptions(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("service-filter-options")
	logger.Debug("Fetching service filter options")

	// Return simple string options for task filtering
	// These represent different contexts for filtering BOSH tasks
	options := []string{
		"blacksmith",        // Main blacksmith deployment
		"service-instances", // All service instances
		"redis",             // Redis service
		"rabbitmq",          // RabbitMQ service
		"postgresql",        // PostgreSQL service
	}

	// Apply any query filters
	service := req.URL.Query().Get("service")
	if service != "" {
		var filtered []string

		for _, opt := range options {
			if opt == service {
				filtered = append(filtered, opt)
			}
		}

		options = filtered
	}

	response.HandleJSON(writer, map[string]interface{}{
		"options": options,
		"count":   len(options),
	}, nil)
}

// GetInstanceConfig returns configuration for a specific service instance.
func (h *Handler) GetInstanceConfig(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-config")
	logger.Debug("Fetching configuration for instance: %s", instanceID)

	// TODO: Implement actual instance configuration fetching from Vault
	// This would fetch the specific configuration for the given instance
	instanceConfig := map[string]interface{}{
		"instance_id": instanceID,
		"service":     "redis", // This would be determined from the instance
		"plan":        "standalone",
		"version":     "7.0",
		"parameters": map[string]interface{}{
			"maxmemory":        "1gb",
			"maxmemory-policy": "allkeys-lru",
			"timeout":          redisDefaultTimeout,
			"tcp-keepalive":    redisDefaultTCPKeepAlive,
			"databases":        redisDefaultDatabases,
		},
		"network": map[string]interface{}{
			"port":      redisDefaultPort,
			"bind":      "0.0.0.0",
			"protected": true,
		},
		"maintenance": map[string]interface{}{
			"backup_schedule":  "daily",
			"backup_retention": defaultBackupRetentionDays,
			"auto_update":      false,
		},
		"tags": []string{"production", "cache"},
	}

	response.HandleJSON(writer, instanceConfig, nil)
}

// UpdateInstanceConfig updates configuration for a specific service instance.
func (h *Handler) UpdateInstanceConfig(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-config-update")
	logger.Info("Updating configuration for instance: %s", instanceID)

	// Parse the configuration update from request body
	var configUpdate map[string]interface{}

	err := response.ParseJSON(req.Body, &configUpdate)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, fmt.Errorf("%w: %w", errInvalidConfigurationUpdate, err))

		return
	}

	// TODO: Implement actual instance configuration update
	// This would:
	// 1. Validate the configuration changes
	// 2. Apply the changes to the instance
	// 3. Update Vault with new configuration
	// 4. Potentially trigger instance reconfiguration

	// For now, return success response
	result := map[string]interface{}{
		"instance_id": instanceID,
		"updated":     true,
		"changes":     configUpdate,
		"message":     "Configuration updated successfully",
		"task_id":     testTaskID, // If this triggers a BOSH task
	}

	response.HandleJSON(writer, result, nil)
}

func (h *Handler) parseDiffParameters(writer http.ResponseWriter, req *http.Request, logger interfaces.Logger) (string, string, bool) {
	query := req.URL.Query()
	fromID := strings.TrimSpace(query.Get("from"))
	toID := strings.TrimSpace(query.Get("to"))

	if fromID == "" || toID == "" {
		logger.Errorf("Missing required diff parameters: from=%s to=%s", fromID, toID)
		response.WriteError(writer, http.StatusBadRequest, "missing required parameters: 'from' and 'to'")

		return "", "", false
	}

	logger.Debugf("Computing diff from %s to %s", fromID, toID)

	return fromID, toID, true
}

func (h *Handler) computeDiff(writer http.ResponseWriter, logger interfaces.Logger, configType, configName, fromID, toID string) (*bosh.ConfigDiff, bool) {
	diff, err := h.director.ComputeConfigDiff(fromID, toID)
	if err != nil {
		logger.Errorf("Failed to compute config diff for %s/%s: %v", configType, configName, err)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to compute config diff: %w", err))

		return nil, false
	}

	return diff, true
}

func (h *Handler) buildDiffPayload(configType, configName, fromID, toID string, diff *bosh.ConfigDiff) map[string]interface{} {
	return map[string]interface{}{
		"type":        configType,
		"name":        configName,
		"from_id":     fromID,
		"to_id":       toID,
		"has_changes": diff.HasChanges,
		"changes":     diff.Changes,
		"diff_string": diff.DiffString,
	}
}

func (h *Handler) enrichDiffPayloadWithMetadata(payload map[string]interface{}, fromID, toID string) {
	fromConfig, err := h.director.GetConfigByID(fromID)
	if err == nil && fromConfig != nil {
		payload["from_metadata"] = map[string]interface{}{
			"id":         fromConfig.ID,
			"created_at": fromConfig.CreatedAt,
			"is_active":  fromConfig.IsActive,
		}
	}

	toConfig, err := h.director.GetConfigByID(toID)
	if err == nil && toConfig != nil {
		payload["to_metadata"] = map[string]interface{}{
			"id":         toConfig.ID,
			"created_at": toConfig.CreatedAt,
			"is_active":  toConfig.IsActive,
		}
	}
}
