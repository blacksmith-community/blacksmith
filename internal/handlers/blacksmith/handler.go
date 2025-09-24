package blacksmith

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	"blacksmith/pkg/utils"
	"gopkg.in/yaml.v3"
)

// Static errors for err113 compliance.
var (
	ErrMethodNotAllowed          = errors.New("method not allowed")
	ErrInvalidRequestBody        = errors.New("invalid request body")
	ErrCommandNotAllowed         = errors.New("command not allowed")
	ErrUnauthorizedLogFileAccess = errors.New("unauthorized log file access")
	ErrFailedToReadLogFile       = errors.New("failed to read log file")
)

const (
	deploymentNameBlacksmith = "blacksmith"
)

// credentialsSummary holds the credential information returned by GetCredentials.
type credentialsSummary struct {
	Environment string `json:"environment,omitempty"`
	BOSH        *struct {
		Address  string `json:"address,omitempty"`
		Username string `json:"username,omitempty"`
		Network  string `json:"network,omitempty"`
	} `json:"BOSH,omitempty"`
	Vault *struct {
		Address           string `json:"address,omitempty"`
		AutoUnseal        bool   `json:"auto_unseal"`
		SkipSSLValidation bool   `json:"skip_ssl_validation"`
	} `json:"Vault,omitempty"`
	Broker *struct {
		Username string `json:"username,omitempty"`
		Port     string `json:"port,omitempty"`
		BindIP   string `json:"bind_ip,omitempty"`
	} `json:"Broker,omitempty"`
}

// Handler handles Blacksmith-specific management endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	vault    interfaces.Vault
	director interfaces.Director
}

// Dependencies contains all dependencies needed by the Blacksmith handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Vault    interfaces.Vault
	Director interfaces.Director
}

// NewHandler creates a new Blacksmith management handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:   deps.Logger,
		config:   deps.Config,
		vault:    deps.Vault,
		director: deps.Director,
	}
}

// readSpecificLogFile validates and reads a specific log file.
func readSpecificLogFile(logFile string, logger interfaces.Logger) (string, error) {
	allowedLogFiles := []string{
		"/var/vcap/sys/log/blacksmith/blacksmith.stdout.log",
		"/var/vcap/sys/log/blacksmith/blacksmith.stderr.log",
		"/var/vcap/sys/log/blacksmith/vault.stdout.log",
		"/var/vcap/sys/log/blacksmith/vault.stderr.log",
		"/var/vcap/sys/log/blacksmith.vault/bpm.log",
		"/var/vcap/sys/log/blacksmith/bpm.log",
		"/var/vcap/sys/log/blacksmith/pre-start.stdout.log",
		"/var/vcap/sys/log/blacksmith/pre-start.stderr.log",
	}

	if !isLogFileAllowed(logFile, allowedLogFiles) {
		logger.Error("Unauthorized log file access attempt: %s", logFile)

		return "", ErrUnauthorizedLogFileAccess
	}

	logger.Debug("Fetching logs from file: %s", logFile)

	return readLogFileContent(logFile, logger)
}

// readDefaultLogFile reads the default log file.
func readDefaultLogFile(logger interfaces.Logger) string {
	defaultLogFile := "/var/vcap/sys/log/blacksmith/blacksmith.stdout.log"
	logger.Debug("Reading default log file: %s", defaultLogFile)

	content, err := readLogFileContent(defaultLogFile, logger)
	if err != nil {
		logger.Error("Failed to read default log file: %s", err)

		return "" // Return empty logs on error for default file
	}

	return content
}

// isLogFileAllowed checks if a log file is in the allowed list.
func isLogFileAllowed(logFile string, allowedFiles []string) bool {
	for _, allowedFile := range allowedFiles {
		if logFile == allowedFile {
			return true
		}
	}

	return false
}

// readLogFileContent reads the content of a log file.
func readLogFileContent(logFile string, logger interfaces.Logger) (string, error) {
	// #nosec G304 - logFile is validated against whitelist before calling this function
	content, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debug("Log file does not exist: %s", logFile)

			return "", nil // Empty logs if file doesn't exist
		}

		return "", fmt.Errorf("%w: %w", ErrFailedToReadLogFile, err)
	}

	return string(content), nil
}

// GetLogs returns Blacksmith deployment logs.
func (h *Handler) GetLogs(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-logs")
	logger.Debug("Fetching Blacksmith logs")

	// Check if a specific log file is requested
	logFile := req.URL.Query().Get("file")

	var (
		logs string
		err  error
	)

	if logFile != "" {
		logs, err = readSpecificLogFile(logFile, logger)
	} else {
		logs = readDefaultLogFile(logger)
	}

	if err != nil {
		logsData := map[string]interface{}{
			"logs":  "",
			"error": err.Error(),
		}
		response.HandleJSON(responseWriter, logsData, nil)

		return
	}

	// Return as JSON with the logs
	logsData := map[string]interface{}{
		"logs":  logs,
		"error": nil,
	}

	response.HandleJSON(responseWriter, logsData, nil)
}

// processResurrectionConfig extracts the resurrection state from the config.
func processResurrectionConfig(resurrectionConfig interface{}, deploymentName, configName string, logger interfaces.Logger) bool {
	logger.Debug("Resurrection config found for %s, type: %T", configName, resurrectionConfig)
	logger.Debug("Resurrection config content: %+v", resurrectionConfig)

	configMap, ok := resurrectionConfig.(map[string]interface{})
	if !ok {
		logger.Error("Resurrection config is not a map: %T", resurrectionConfig)

		return false
	}

	logger.Debug("Config parsed as map with %d keys", len(configMap))

	return extractResurrectionStateFromRules(configMap, deploymentName, logger)
}

// extractResurrectionStateFromRules extracts the resurrection state from config rules.
func extractResurrectionStateFromRules(configMap map[string]interface{}, deploymentName string, logger interfaces.Logger) bool {
	rules, ok := configMap["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		logger.Debug("No rules found in config or rules is not an array")

		return false
	}

	logger.Debug("Found %d rules in config", len(rules))
	ruleMap := convertToStringMap(rules[0], logger)

	if ruleMap == nil {
		return false
	}

	logger.Debug("First rule is a map with %d keys", len(ruleMap))

	return extractResurrectionStateFromRule(ruleMap, deploymentName, logger)
}

// convertToStringMap converts a rule to a string-keyed map.
func convertToStringMap(rule interface{}, logger interfaces.Logger) map[string]interface{} {
	switch r := rule.(type) {
	case map[string]interface{}:
		return r
	case map[interface{}]interface{}:
		ruleMap := make(map[string]interface{})
		for k, v := range r {
			if ks, ok := k.(string); ok {
				ruleMap[ks] = v
			}
		}

		return ruleMap
	default:
		logger.Error("First rule is not a map: %T", rule)

		return nil
	}
}

// extractResurrectionStateFromRule extracts the enabled state from a rule.
func extractResurrectionStateFromRule(ruleMap map[string]interface{}, deploymentName string, logger interfaces.Logger) bool {
	enabled, ok := ruleMap["enabled"].(bool)
	if !ok {
		logger.Error("'enabled' field in rule is not a bool or missing")

		return false
	}

	resurrectionPaused := !enabled
	logger.Info("Resurrection config for %s: enabled=%v, setting paused=%v", deploymentName, enabled, resurrectionPaused)

	return resurrectionPaused
}

// GetVMs retrieves the VMs for the blacksmith deployment.
func (h *Handler) GetVMs(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-vms")
	logger.Debug("Fetching Blacksmith VMs")

	if !h.validateDirectorAvailable(responseWriter, logger) {
		return
	}

	deploymentName := h.getBlacksmithDeploymentName(logger)

	vms, err := h.fetchDeploymentVMs(deploymentName, logger)
	if err != nil {
		h.sendVMsErrorResponse(responseWriter, err)

		return
	}

	h.applyResurrectionStatus(vms, deploymentName, logger)
	response.HandleJSON(responseWriter, vms, nil)
}

// GetCredentials returns Blacksmith deployment credentials.
func (h *Handler) GetCredentials(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-credentials")
	logger.Debug("Fetching Blacksmith credentials")

	credentials := h.buildCredentialsSummary()
	response.HandleJSON(responseWriter, credentials, nil)
}

// GetConfig returns Blacksmith configuration.
func (h *Handler) GetConfig(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-config")
	logger.Debug("Fetching Blacksmith configuration")

	if req.Method != http.MethodGet {
		logger.Debug("Rejecting non-GET request with method %s", req.Method)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	// Marshal the full configuration to YAML first so we honor the yaml tags that
	// define the lowercase key names expected by the UI, then convert back to a
	// JSON-friendly map structure.
	configYAML, err := yaml.Marshal(h.config)
	if err != nil {
		logger.Error("Failed to marshal config to YAML: %v", err)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("failed to marshal config: %w", err))

		return
	}

	intermediate := make(map[interface{}]interface{})

	err = yaml.Unmarshal(configYAML, &intermediate)
	if err != nil {
		logger.Error("Failed to unmarshal config YAML: %v", err)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("failed to unmarshal config YAML: %w", err))

		return
	}

	configData := utils.DeinterfaceMap(intermediate)

	response.HandleJSON(responseWriter, configData, nil)
}

// Cleanup performs cleanup operations.
func (h *Handler) Cleanup(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-cleanup")

	// Only allow POST method
	if req.Method != http.MethodPost {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	logger.Info("Starting cleanup operation")

	// Parse request body for cleanup options
	var cleanupRequest struct {
		DryRun bool   `json:"dry_run"`
		Target string `json:"target"`
	}

	err := json.NewDecoder(req.Body).Decode(&cleanupRequest)
	if err != nil {
		// If no body, use defaults
		cleanupRequest.DryRun = false
		cleanupRequest.Target = "all"
	}

	// TODO: Implement actual cleanup logic
	// This might involve:
	// - Cleaning up orphaned VMs
	// - Removing stale service instances
	// - Clearing temporary data

	result := map[string]interface{}{
		"status":  "completed",
		"dry_run": cleanupRequest.DryRun,
		"target":  cleanupRequest.Target,
		"cleaned": map[string]int{
			"orphaned_vms":    0,
			"stale_instances": 0,
			"temporary_files": 0,
		},
	}

	response.HandleJSON(responseWriter, result, nil)
}

// GetEvents returns events for the blacksmith deployment.
func (h *Handler) GetEvents(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-events")
	logger.Debug("Fetching Blacksmith events")

	// Check if director is available
	if h.director == nil {
		logger.Error("Director not configured")

		eventsData := map[string]interface{}{
			"events": []interface{}{},
			"error":  "Director not configured",
		}
		response.HandleJSON(responseWriter, eventsData, nil)

		return
	}

	// Fetch events for the blacksmith deployment
	// Use the environment name with "-blacksmith" suffix as the deployment name
	environment := h.config.GetEnvironment()

	deploymentName := environment + "-blacksmith"
	if environment == "" {
		deploymentName = deploymentNameBlacksmith
	}

	logger.Debug("Fetching events for deployment: %s", deploymentName)

	events, err := h.director.GetEvents(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch events: %v", err)
		eventsData := map[string]interface{}{
			"events": []interface{}{},
			"error":  err.Error(),
		}
		response.HandleJSON(responseWriter, eventsData, nil)

		return
	}

	response.HandleJSON(responseWriter, events, nil)
}

// GetManifest returns the manifest for the blacksmith deployment.
func (h *Handler) GetManifest(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-manifest")
	logger.Debug("Fetching Blacksmith manifest")

	// Check if director is available
	if h.director == nil {
		logger.Error("Director not configured")

		manifestData := map[string]interface{}{
			"text":   "",
			"parsed": map[string]interface{}{},
			"error":  "Director not configured",
		}
		response.HandleJSON(responseWriter, manifestData, nil)

		return
	}

	// Fetch deployment details including manifest
	// Use the environment name with "-blacksmith" suffix as the deployment name
	environment := h.config.GetEnvironment()

	deploymentName := environment + "-blacksmith"
	if environment == "" {
		deploymentName = deploymentNameBlacksmith
	}

	logger.Debug("Fetching manifest for deployment: %s", deploymentName)

	deployment, err := h.director.GetDeployment(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch deployment: %v", err)
		manifestData := map[string]interface{}{
			"text":   "",
			"parsed": map[string]interface{}{},
			"error":  err.Error(),
		}
		response.HandleJSON(responseWriter, manifestData, nil)

		return
	}

	response.HandleJSON(responseWriter, h.formatManifestResponse(deployment.Manifest), nil)
}

// ExecuteCommand executes a command in the Blacksmith deployment context.
func (h *Handler) ExecuteCommand(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-execute")

	if req.Method != http.MethodPost {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	var cmdRequest struct {
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}

	err := json.NewDecoder(req.Body).Decode(&cmdRequest)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("%w: %w", ErrInvalidRequestBody, err))

		return
	}

	if !h.isCommandAllowed(cmdRequest.Command) {
		responseWriter.WriteHeader(http.StatusForbidden)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("%w: %s", ErrCommandNotAllowed, cmdRequest.Command))

		return
	}

	err = h.validateArguments(cmdRequest.Arguments)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, nil, err)

		return
	}

	logger.Info("Executing command: %s %s", cmdRequest.Command, strings.Join(cmdRequest.Arguments, " "))
	result := h.executeValidatedCommand(req.Context(), cmdRequest.Command, cmdRequest.Arguments)
	response.HandleJSON(responseWriter, result, nil)
}

// ToggleResurrection toggles resurrection for a deployment (blacksmith or service instance).
func (h *Handler) ToggleResurrection(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("resurrection-toggle")

	instanceID, isValid := h.validateResurrectionPath(req, logger, responseWriter)
	if !isValid {
		return
	}

	logger.Debug("Toggling resurrection for %s", instanceID)

	if req.Method != http.MethodPut {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	enabled, success := h.decodeResurrectionRequest(req, logger, responseWriter)
	if !success {
		return
	}

	deploymentName, ok := h.resolveDeploymentName(req, instanceID, logger, responseWriter)
	if !ok {
		return
	}

	if !h.enableResurrection(deploymentName, enabled, logger, responseWriter) {
		return
	}

	h.sendResurrectionToggleResponse(responseWriter, deploymentName, enabled)
}

// DeleteResurrectionConfig deletes resurrection config for a deployment.
func (h *Handler) DeleteResurrectionConfig(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("resurrection-delete")

	instanceID, isValid := h.validateResurrectionPath(req, logger, responseWriter)
	if !isValid {
		return
	}

	logger.Debug("Deleting resurrection config for %s", instanceID)

	if req.Method != http.MethodDelete {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	deploymentName, ok := h.resolveDeploymentName(req, instanceID, logger, responseWriter)
	if !ok {
		return
	}

	if !h.deleteResurrectionConfig(deploymentName, logger, responseWriter) {
		return
	}

	h.sendResurrectionDeleteResponse(responseWriter, deploymentName)
}

func (h *Handler) validateDirectorAvailable(responseWriter http.ResponseWriter, logger interfaces.Logger) bool {
	if h.director == nil {
		logger.Error("Director not configured")

		vmsData := map[string]interface{}{
			"vms":   []interface{}{},
			"error": "Director not configured",
		}
		response.HandleJSON(responseWriter, vmsData, nil)

		return false
	}

	return true
}

func (h *Handler) getBlacksmithDeploymentName(logger interfaces.Logger) string {
	environment := h.config.GetEnvironment()

	deploymentName := environment + "-blacksmith"
	if environment == "" {
		deploymentName = deploymentNameBlacksmith
	}

	logger.Debug("Fetching VMs for deployment: %s", deploymentName)

	return deploymentName
}

func (h *Handler) fetchDeploymentVMs(deploymentName string, logger interfaces.Logger) ([]bosh.VM, error) {
	vms, err := h.director.GetDeploymentVMs(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch VMs: %v", err)

		return nil, fmt.Errorf("failed to get deployment VMs: %w", err)
	}

	return vms, nil
}

func (h *Handler) sendVMsErrorResponse(responseWriter http.ResponseWriter, err error) {
	vmsData := map[string]interface{}{
		"vms":   []interface{}{},
		"error": err.Error(),
	}
	response.HandleJSON(responseWriter, vmsData, nil)
}

func (h *Handler) applyResurrectionStatus(vms []bosh.VM, deploymentName string, logger interfaces.Logger) {
	configName := "blacksmith." + deploymentName
	logger.Debug("Checking for resurrection config: %s (deployment: %s)", configName, deploymentName)

	resurrectionPaused := false

	resurrectionConfig, err := h.director.GetConfig("resurrection", configName)
	if err != nil {
		logger.Debug("Error getting resurrection config: %v", err)
	}

	if err == nil && resurrectionConfig != nil {
		resurrectionPaused = processResurrectionConfig(resurrectionConfig, deploymentName, configName, logger)
	} else {
		logger.Debug("No resurrection config found (config: %v, err: %v), using BOSH default (resurrection active)", resurrectionConfig, err)
	}

	resurrectionConfigExists := (err == nil && resurrectionConfig != nil)

	for i := range vms {
		vms[i].ResurrectionPaused = resurrectionPaused
		vms[i].ResurrectionConfigExists = resurrectionConfigExists
		logger.Debug("VM %d (%s): resurrection_paused set to %v, config_exists=%v", i, vms[i].ID, vms[i].ResurrectionPaused, vms[i].ResurrectionConfigExists)
	}
}

func (h *Handler) buildCredentialsSummary() credentialsSummary {
	credentials := credentialsSummary{}
	credentials.Environment = h.config.GetEnvironment()
	h.populateBOSHCredentials(&credentials)
	h.populateVaultCredentials(&credentials)
	h.populateBrokerCredentials(&credentials)

	return credentials
}

func (h *Handler) populateBOSHCredentials(credentials *credentialsSummary) {
	boshCfg := h.config.GetBOSHConfig()
	if boshCfg.Address != "" || boshCfg.Username != "" || boshCfg.Network != "" {
		credentials.BOSH = &struct {
			Address  string `json:"address,omitempty"`
			Username string `json:"username,omitempty"`
			Network  string `json:"network,omitempty"`
		}{
			Address:  boshCfg.Address,
			Username: boshCfg.Username,
			Network:  boshCfg.Network,
		}
	}
}

func (h *Handler) populateVaultCredentials(credentials *credentialsSummary) {
	vaultCfg := h.config.GetVaultConfig()
	if vaultCfg.Address != "" {
		credentials.Vault = &struct {
			Address           string `json:"address,omitempty"`
			AutoUnseal        bool   `json:"auto_unseal"`
			SkipSSLValidation bool   `json:"skip_ssl_validation"`
		}{
			Address:           vaultCfg.Address,
			AutoUnseal:        vaultCfg.AutoUnseal,
			SkipSSLValidation: vaultCfg.Insecure,
		}
	}
}

func (h *Handler) populateBrokerCredentials(credentials *credentialsSummary) {
	brokerCfg := h.config.GetBrokerConfig()
	if brokerCfg.Username != "" || brokerCfg.Port != "" || brokerCfg.BindIP != "" {
		credentials.Broker = &struct {
			Username string `json:"username,omitempty"`
			Port     string `json:"port,omitempty"`
			BindIP   string `json:"bind_ip,omitempty"`
		}{
			Username: brokerCfg.Username,
			Port:     brokerCfg.Port,
			BindIP:   brokerCfg.BindIP,
		}
	}
}

func (h *Handler) validateResurrectionPath(req *http.Request, logger interfaces.Logger, responseWriter http.ResponseWriter) (string, bool) {
	pathParts := strings.Split(strings.TrimPrefix(req.URL.Path, "/b/"), "/")
	if len(pathParts) < 2 || pathParts[1] != "resurrection" {
		logger.Error("Invalid resurrection URL path: %s", req.URL.Path)
		responseWriter.WriteHeader(http.StatusNotFound)
		_, _ = responseWriter.Write([]byte(`{"error": "invalid path"}`))

		return "", false
	}

	return pathParts[0], true
}

func (h *Handler) decodeResurrectionRequest(req *http.Request, logger interfaces.Logger, responseWriter http.ResponseWriter) (bool, bool) {
	var requestBody struct {
		Enabled bool `json:"enabled"`
	}

	err := json.NewDecoder(req.Body).Decode(&requestBody)
	if err != nil {
		logger.Error("Failed to decode request body: %v", err)
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, map[string]interface{}{
			"error": "invalid JSON in request body",
		}, nil)

		return false, false
	}

	return requestBody.Enabled, true
}

func (h *Handler) resolveDeploymentName(req *http.Request, instanceID string, logger interfaces.Logger, responseWriter http.ResponseWriter) (string, bool) {
	if instanceID == deploymentNameBlacksmith {
		return h.getBlacksmithDeploymentNameForResurrection(logger), true
	}

	return h.getServiceDeploymentName(req, instanceID, logger, responseWriter)
}

func (h *Handler) getBlacksmithDeploymentNameForResurrection(logger interfaces.Logger) string {
	environment := h.config.GetEnvironment()

	deploymentName := environment + "-blacksmith"
	if environment == "" {
		deploymentName = deploymentNameBlacksmith
	}

	logger.Debug("Using blacksmith deployment name: %s", deploymentName)

	return deploymentName
}

func (h *Handler) getServiceDeploymentName(req *http.Request, instanceID string, logger interfaces.Logger, responseWriter http.ResponseWriter) (string, bool) {
	ctx := req.Context()

	inst, exists, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault index", instanceID)
		responseWriter.WriteHeader(http.StatusNotFound)
		response.HandleJSON(responseWriter, map[string]interface{}{
			"error": "service instance not found",
		}, nil)

		return "", false
	}

	deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
	logger.Debug("Resolved service deployment name: %s", deploymentName)

	return deploymentName, true
}

func (h *Handler) enableResurrection(deploymentName string, enabled bool, logger interfaces.Logger, responseWriter http.ResponseWriter) bool {
	err := h.director.EnableResurrection(deploymentName, enabled)
	if err != nil {
		logger.Error("Unable to toggle resurrection for deployment %s: %v", deploymentName, err)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(responseWriter, map[string]interface{}{
			"error": "failed to toggle resurrection: " + err.Error(),
		}, nil)

		return false
	}

	return true
}

func (h *Handler) sendResurrectionToggleResponse(responseWriter http.ResponseWriter, deploymentName string, enabled bool) {
	action := "enabled"
	if !enabled {
		action = "disabled"
	}

	response.HandleJSON(responseWriter, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Resurrection %s for deployment %s", action, deploymentName),
		"enabled": enabled,
	}, nil)
}

// deleteResurrectionConfig deletes the resurrection config for a deployment.
func (h *Handler) deleteResurrectionConfig(deploymentName string, logger interfaces.Logger, responseWriter http.ResponseWriter) bool {
	err := h.director.DeleteResurrectionConfig(deploymentName)
	if err != nil {
		logger.Error("Unable to delete resurrection config for deployment %s: %v", deploymentName, err)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(responseWriter, map[string]interface{}{
			"error": "failed to delete resurrection config: " + err.Error(),
		}, nil)

		return false
	}

	return true
}

// sendResurrectionDeleteResponse sends the success response for resurrection config deletion.
func (h *Handler) sendResurrectionDeleteResponse(responseWriter http.ResponseWriter, deploymentName string) {
	response.HandleJSON(responseWriter, map[string]interface{}{
		"success": true,
		"message": "Resurrection config deleted for deployment " + deploymentName,
	}, nil)
}

// isCommandAllowed checks if the command is in the allowed list.
func (h *Handler) isCommandAllowed(command string) bool {
	allowedCommands := []string{"status", "version", "health"}
	for _, cmd := range allowedCommands {
		if command == cmd {
			return true
		}
	}

	return false
}

func (h *Handler) validateArguments(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, ";|&$`(){}[]<>*?~!") {
			return fmt.Errorf("%w: argument contains invalid characters: %s", ErrInvalidRequestBody, arg)
		}
	}

	return nil
}

func (h *Handler) executeValidatedCommand(ctx context.Context, command string, args []string) map[string]interface{} {
	// #nosec G204 - Command and arguments are validated with whitelist and sanitized
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()

	result := map[string]interface{}{
		"command": command,
		"args":    args,
		"output":  string(output),
		"success": err == nil,
	}
	if err != nil {
		result["error"] = err.Error()
	}

	return result
}

// formatManifestResponse formats the manifest data for UI consumption.
func (h *Handler) formatManifestResponse(manifestYAML string) map[string]interface{} {
	// If no manifest text, return empty response
	if manifestYAML == "" {
		return map[string]interface{}{
			"text":   "",
			"parsed": map[string]interface{}{},
		}
	}

	// Parse the YAML manifest into a structured format
	var parsed map[string]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &parsed)
	if err != nil {
		// If parsing fails, return text only with empty parsed section
		return map[string]interface{}{
			"text":   manifestYAML,
			"parsed": map[string]interface{}{},
		}
	}

	return map[string]interface{}{
		"text":   manifestYAML,
		"parsed": parsed,
	}
}
