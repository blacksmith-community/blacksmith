package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	bosh "blacksmith/internal/bosh"
	boshssh "blacksmith/internal/bosh/ssh"
	"blacksmith/internal/interfaces"
	rabbitmqssh "blacksmith/internal/services/rabbitmq"
	"blacksmith/pkg/certificates"
	"blacksmith/pkg/http/response"

	"gopkg.in/yaml.v3"
)

// Constants for services handler.
const (
	// Test task ID for services operations.
	testTaskIDServices = 3000

	// SSH execution defaults.
	defaultSSHMaxOutputSize = 1024 * 1024 // 1MB
	defaultSSHTimeout       = 30          // 30 seconds

	// Default Redis port.
	defaultRedisPort = 6379

	// Maximum certificate ID length.
	maxCertificateIDLength = 24

	// Service type constants.
	serviceTypeRabbitMQ = "rabbitmq"
	serviceTypeRedis    = "redis"
)

// Error variables for err113 compliance.
var (
	errInvalidRequest                   = errors.New("invalid request")
	errCommandIsRequired                = errors.New("command is required")
	errCertificateIsRequired            = errors.New("certificate is required")
	errCertificateIDRequired            = errors.New("certificate id is required")
	errInstanceNotFound                 = errors.New("service instance not found")
	errSSHServiceUnavailable            = errors.New("ssh service not configured")
	errManifestNotFoundForInstance      = errors.New("manifest not found for instance")
	errManifestMissingInstanceGroups    = errors.New("manifest missing instance_groups")
	errManifestHasNoInstanceGroups      = errors.New("manifest has no instance groups")
	errInstanceGroupHasUnexpectedFormat = errors.New("instance group has unexpected format")
	errInstanceGroupNameNotFound        = errors.New("instance group name not found")
	errNotRabbitMQInstance              = errors.New("not a RabbitMQ instance")
	errUnknownOperation                 = errors.New("unknown operation")
	errCustomCommandPayloadRequired     = errors.New("custom command payload required")
	errFilePathRequired                 = errors.New("filePath is required")
	errInvalidFilePath                  = errors.New("invalid file path")
	errCertificateNotFound              = errors.New("certificate not found")
	errManifestNotFound                 = errors.New("manifest not found")
	errInstanceGroupsNotFound           = errors.New("instance_groups not found")
	errFailedToReadCertificateFile      = errors.New("failed to read certificate file")
)

// Handler handles generic service instance endpoints.
type Handler struct {
	logger      interfaces.Logger
	config      interfaces.Config
	vault       interfaces.Vault
	director    interfaces.Director
	ssh         interfaces.SSHService
	rabbitmqSSH interfaces.RabbitMQSSHService
}

// Dependencies contains all dependencies needed by the Services handler.
type Dependencies struct {
	Logger      interfaces.Logger
	Config      interfaces.Config
	Vault       interfaces.Vault
	Director    interfaces.Director
	SSH         interfaces.SSHService
	RabbitMQSSH interfaces.RabbitMQSSHService
}

// SSHExecuteRequest represents an SSH command execution request.
type SSHExecuteRequest struct {
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
	Timeout   int      `json:"timeout,omitempty"`
	Async     bool     `json:"async,omitempty"`
}

// NewHandler creates a new Services handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:      deps.Logger,
		config:      deps.Config,
		vault:       deps.Vault,
		director:    deps.Director,
		ssh:         deps.SSH,
		rabbitmqSSH: deps.RabbitMQSSH,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for service instance endpoints
	instancePattern := regexp.MustCompile(`^/b/[^/]+/(vms|logs|events|manifest|credentials|details|config|task/log|task/debug)$`)
	if instancePattern.MatchString(path) {
		return true
	}

	// Check for SSH execute endpoint
	sshExecutePattern := regexp.MustCompile(`^/b/[^/]+/ssh/execute$`)
	if sshExecutePattern.MatchString(path) {
		return true
	}

	// Check for certificates trusted endpoint
	certsPattern := regexp.MustCompile(`^/b/[^/]+/certificates/trusted(/file)?$`)
	if certsPattern.MatchString(path) {
		return true
	}

	// Check for RabbitMQ SSH endpoints
	rabbitMQSSHPattern := regexp.MustCompile(`^/b/[^/]+/rabbitmq/ssh/.+$`)

	return rabbitMQSSHPattern.MatchString(path)
}

// ServeHTTP handles service instance endpoints with pattern matching.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Service instance endpoints - /b/{instance}/{operation}
	if h.handleInstanceOperation(writer, req) {
		return
	}

	// SSH execute endpoint - /b/{instance}/ssh/execute
	if h.handleSSHExecute(writer, req) {
		return
	}

	// Certificates trusted file endpoint - /b/{instance}/certificates/trusted/file
	if h.handleCertificateTrustedFile(writer, req) {
		return
	}

	// Certificates trusted endpoint - /b/{instance}/certificates/trusted
	if h.handleCertificateTrusted(writer, req) {
		return
	}

	// RabbitMQ SSH endpoints - /b/{instance}/rabbitmq/ssh/*
	if h.handleRabbitMQSSH(writer, req) {
		return
	}

	// No matching endpoint
	writer.WriteHeader(http.StatusNotFound)
}

// ExecuteSSHCommand executes an SSH command on a service instance.
func (h *Handler) ExecuteSSHCommand(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("ssh-execute")
	logger.Debug("Executing SSH command on instance: %s", instanceID)

	if h.ssh == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, errSSHServiceUnavailable.Error())

		return
	}

	execReq, err := h.parseSSHExecuteRequest(writer, req)
	if err != nil {
		return
	}

	deploymentInfo, err := h.resolveDeploymentForSSH(writer, req.Context(), instanceID, logger)
	if err != nil {
		return
	}

	sshReq := h.buildSSHRequest(deploymentInfo, &execReq)
	logger.Info("Executing command '%s' on %s/%s/%d", sshReq.Command, sshReq.Deployment, sshReq.Instance, sshReq.Index)

	if execReq.Async {
		h.handleAsyncSSH(writer, logger, sshReq, instanceID, &execReq)

		return
	}

	h.handleSyncSSH(writer, logger, sshReq, instanceID, &execReq)
}

// GetTrustedCertificates fetches trusted certificate files from the service instance VM via SSH.
func (h *Handler) GetTrustedCertificates(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-get")
	logger.Debug("Getting trusted certificates for instance: %s", instanceID)

	if h.ssh == nil {
		logger.Error("SSH service not configured")
		response.WriteError(writer, http.StatusServiceUnavailable, "ssh service not configured")

		return
	}

	deploymentInfo, err := h.resolveDeploymentForSSH(writer, req.Context(), instanceID, logger)
	if err != nil {
		return
	}

	sshResp, err := h.executeCertificateListCommand(logger, deploymentInfo, instanceID)
	if err != nil {
		h.writeCertificateErrorResponse(writer, err)

		return
	}

	files := h.parseCertificateFiles(sshResp)
	h.writeCertificateSuccessResponse(writer, files)
}

// GetTrustedCertificateFile fetches a specific certificate file from the service instance VM via SSH.
func (h *Handler) GetTrustedCertificateFile(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-file")
	logger.Debug("Fetching certificate file for instance: %s", instanceID)

	if h.ssh == nil {
		logger.Error("SSH service not configured")
		response.WriteError(writer, http.StatusServiceUnavailable, "ssh service not configured")

		return
	}

	fileRequest, err := h.parseCertificateFileRequest(writer, req)
	if err != nil {
		return
	}

	deploymentInfo, err := h.resolveDeploymentForSSH(writer, req.Context(), instanceID, logger)
	if err != nil {
		return
	}

	certPEM, err := h.fetchCertificateFileContent(writer, logger, deploymentInfo, instanceID, fileRequest.FilePath)
	if err != nil {
		return
	}

	h.writeCertificateFileResponse(writer, req.Context(), logger, fileRequest.FilePath, certPEM)
}

// parseCertificateFileRequest parses and validates certificate file request.

// fetchCertificateFileContent fetches certificate file content via SSH.

// writeCertificateFileResponse writes the certificate file response.

// AddTrustedCertificate adds a trusted certificate to a service instance.
func (h *Handler) AddTrustedCertificate(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-add")
	logger.Info("Adding trusted certificate to instance: %s", instanceID)

	if h.vault == nil {
		logger.Error("Vault not configured")
		response.WriteError(writer, http.StatusInternalServerError, "vault not configured")

		return
	}

	certRequest, err := h.parseAddCertificateRequest(writer, req)
	if err != nil {
		return
	}

	certInfo, name, err := h.validateAndParseCertificate(writer, req.Context(), certRequest)
	if err != nil {
		return
	}

	recordID := generateCertificateID(certInfo.Fingerprints.SHA256)

	err = h.storeTrustedCertificate(writer, req.Context(), logger, instanceID, recordID, name, certRequest.Certificate, certInfo)
	if err != nil {
		return
	}

	h.writeAddCertificateResponse(writer, instanceID, recordID, name, certInfo)
}

// parseAddCertificateRequest parses the add certificate request.

// validateAndParseCertificate validates and parses a certificate, returning cert info and name.

// storeTrustedCertificate stores or updates a trusted certificate in vault.

// writeAddCertificateResponse writes the response for adding a certificate.

// RemoveTrustedCertificate removes a trusted certificate from a service instance.
func (h *Handler) RemoveTrustedCertificate(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-remove")

	certID := req.URL.Query().Get("id")
	if certID == "" {
		response.WriteError(writer, http.StatusBadRequest, errCertificateIDRequired.Error())

		return
	}

	logger.Info("Removing trusted certificate %s from instance: %s", certID, instanceID)

	if h.vault == nil {
		logger.Error("Vault not configured")
		response.WriteError(writer, http.StatusInternalServerError, "vault not configured")

		return
	}

	removedRecord, err := h.removeCertificateFromStore(writer, req.Context(), logger, instanceID, certID)
	if err != nil {
		return
	}

	h.writeRemoveCertificateResponse(writer, instanceID, certID, removedRecord)
}

// removeCertificateFromStore removes a certificate from the vault store.

// writeRemoveCertificateResponse writes the response for removing a certificate.

// ExecuteRabbitMQSSHCommand executes RabbitMQ-specific SSH commands.
func (h *Handler) ExecuteRabbitMQSSHCommand(writer http.ResponseWriter, req *http.Request, instanceID string, command string) {
	logger := h.logger.Named("rabbitmq-ssh")
	logger.Debug("Executing RabbitMQ SSH command '%s' on instance: %s", command, instanceID)

	err := h.validateRabbitMQRequest(writer, req)
	if err != nil {
		return
	}

	bodyBytes, err := h.readRequestBody(writer, req, logger)
	if err != nil {
		return
	}

	deploymentName, instanceName, err := h.resolveRabbitMQDeployment(writer, req, instanceID, logger)
	if err != nil {
		return
	}

	result, err := h.executeRabbitMQCommand(writer, command, bodyBytes, deploymentName, instanceName, 0)
	if err != nil {
		logger.Error("RabbitMQ SSH command '%s' failed for %s: %v", command, instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, err.Error())

		return
	}

	response.HandleJSON(writer, result, nil)
}

// processResurrectionConfigWithDebug extracts the resurrection state from the config with debug logging.
func processResurrectionConfigWithDebug(resurrectionConfig interface{}, deploymentName, configName string, logger interfaces.Logger) bool {
	logger.Debug("Resurrection config found for %s, type: %T", configName, resurrectionConfig)
	logger.Debug("Resurrection config content: %+v", resurrectionConfig)

	configMap, ok := resurrectionConfig.(map[string]interface{})
	if !ok {
		logger.Error("Resurrection config is not a map: %T", resurrectionConfig)

		return false
	}

	logger.Debug("Config parsed as map with %d keys", len(configMap))

	for k, v := range configMap {
		logger.Debug("Config key '%s': %T = %+v", k, v, v)
	}

	return extractResurrectionStateFromRulesWithDebug(configMap, deploymentName, logger)
}

// extractResurrectionStateFromRulesWithDebug extracts the resurrection state from config rules with debug logging.
func extractResurrectionStateFromRulesWithDebug(configMap map[string]interface{}, deploymentName string, logger interfaces.Logger) bool {
	rules, ok := configMap["rules"].([]interface{})
	if !ok || len(rules) == 0 {
		logger.Debug("No rules found in config or rules is not an array")

		return false
	}

	logger.Debug("Found %d rules in config", len(rules))
	ruleMap := convertToStringMapWithDebug(rules[0], logger)

	if ruleMap == nil {
		return false
	}

	logger.Debug("First rule is a map with %d keys", len(ruleMap))

	return extractResurrectionStateFromRuleWithDebug(ruleMap, deploymentName, logger)
}

// convertToStringMapWithDebug converts a rule to a string-keyed map with debug logging.
func convertToStringMapWithDebug(rule interface{}, logger interfaces.Logger) map[string]interface{} {
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

// extractResurrectionStateFromRuleWithDebug extracts the enabled state from a rule with debug logging.
func extractResurrectionStateFromRuleWithDebug(ruleMap map[string]interface{}, deploymentName string, logger interfaces.Logger) bool {
	enabled, ok := ruleMap["enabled"].(bool)
	if !ok {
		logger.Error("'enabled' field in rule is not a bool or missing")

		return false
	}

	resurrectionPaused := !enabled
	logger.Info("Resurrection config for %s: enabled=%v, setting paused=%v", deploymentName, enabled, resurrectionPaused)

	return resurrectionPaused
}

// GetInstanceVMs returns VMs for a service instance.
func (h *Handler) GetInstanceVMs(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-vms")
	logger.Debug("Getting VMs for instance: %s", instanceID)

	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{
			"vms":   []interface{}{},
			"error": "Director not configured",
		}, nil)

		return
	}

	deploymentName, err := h.getDeploymentNameFromVault(writer, req.Context(), logger, instanceID)
	if err != nil {
		return
	}

	vms, err := h.fetchDeploymentVMs(writer, logger, deploymentName)
	if err != nil {
		return
	}

	h.applyResurrectionConfig(logger, deploymentName, vms)
	h.cacheVMData(req.Context(), logger, instanceID, deploymentName, vms)

	response.HandleJSON(writer, vms, nil)
}

// getDeploymentNameFromVault retrieves deployment name from vault.

// fetchDeploymentVMs fetches VMs for a deployment.

// applyResurrectionConfig applies resurrection configuration to VMs.

// cacheVMData caches VM data in vault.

// parseLogContent parses logs and extracts individual files if structured.
func parseLogContent(logs string) map[string]string {
	logFiles := make(map[string]string)

	if !strings.Contains(logs, "===") {
		// Unstructured logs - just use raw content
		logFiles["raw"] = logs

		return logFiles
	}

	// Parse structured logs
	lines := strings.Split(logs, "\n")

	var (
		currentFile    string
		currentContent strings.Builder
	)

	for _, line := range lines {
		if strings.HasPrefix(line, "===") && strings.HasSuffix(line, "===") {
			// Save previous file if exists
			if currentFile != "" {
				logFiles[currentFile] = currentContent.String()
				currentContent.Reset()
			}
			// Extract new file name
			currentFile = strings.TrimSpace(strings.Trim(line, "="))
		} else {
			currentContent.WriteString(line + "\n")
		}
	}

	// Save last file
	if currentFile != "" {
		logFiles[currentFile] = currentContent.String()
	}

	return logFiles
}

// GetInstanceLogs returns logs for a service instance.
func (h *Handler) GetInstanceLogs(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("service-instance-logs")
	logger.Debug("Fetching instance logs for instance %s", instanceID)

	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "Director not configured",
		}, nil)

		return
	}

	ctx := req.Context()

	deploymentName, err := h.getDeploymentNameForLogs(writer, ctx, logger, instanceID)
	if err != nil {
		return
	}

	instanceGroups, err := h.getInstanceGroupsFromManifest(writer, ctx, logger, instanceID)
	if err != nil {
		return
	}

	allLogs := h.fetchLogsForInstanceGroups(logger, deploymentName, instanceGroups)

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"logs":       allLogs,
		"error":      nil,
	}, nil)
}

// getDeploymentNameForLogs retrieves deployment name for log fetching.

// getInstanceGroupsFromManifest extracts instance groups from manifest.

// fetchLogsForInstanceGroups fetches logs for all instance groups.

// getInstanceCount extracts instance count from group.

// fetchLogsForJob fetches logs for a specific job.

// GetInstanceEvents returns events for a service instance.
func (h *Handler) GetInstanceEvents(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-events")
	logger.Debug("Getting events for instance: %s", instanceID)

	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{
			"events": []interface{}{},
			"error":  "Director not configured",
		}, nil)

		return
	}

	// Get instance data from vault to get deployment name
	ctx := req.Context()

	var inst struct {
		ServiceID      string `json:"service_id"`
		PlanID         string `json:"plan_id"`
		DeploymentName string `json:"deployment_name"`
	}

	// Try to get instance data from vault
	exists, err := h.vault.Get(ctx, instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"events": []interface{}{},
			"error":  "service instance not found",
		}, nil)

		return
	}

	deploymentName := inst.DeploymentName
	logger.Debug("Getting events for deployment: %s", deploymentName)

	events, err := h.director.GetEvents(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch events: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"events": []interface{}{},
			"error":  err.Error(),
		}, nil)

		return
	}

	response.HandleJSON(writer, events, nil)
}

// GetInstanceManifest returns the manifest for a service instance.
func (h *Handler) GetInstanceManifest(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-manifest")
	logger.Debug("Getting manifest for instance: %s", instanceID)

	ctx := req.Context()

	// Try Vault first for immediate availability during provisioning
	if h.vault != nil {
		var manifestData map[string]interface{}

		path := instanceID + "/manifest"

		exists, err := h.vault.Get(ctx, path, &manifestData)
		if err == nil && exists && manifestData != nil {
			if manifestText, ok := manifestData["manifest"].(string); ok && manifestText != "" {
				logger.Info("Retrieved manifest from Vault for instance %s (size: %d bytes)", instanceID, len(manifestText))
				h.writeManifestFromText(writer, logger, manifestText)

				return
			}
		}

		logger.Debug("No manifest in Vault for instance %s, falling back to BOSH director", instanceID)
	}

	// Fall back to BOSH director (for backward compatibility and deployed instances)
	if h.director == nil {
		logger.Error("Director not configured and no manifest in Vault")
		response.HandleJSON(writer, map[string]interface{}{
			"text":   "",
			"parsed": nil,
			"error":  "Director not configured",
		}, nil)

		return
	}

	deploymentName, err := h.getDeploymentNameForManifest(writer, ctx, logger, instanceID)
	if err != nil {
		return
	}

	deployment, err := h.fetchDeploymentManifest(writer, logger, deploymentName)
	if err != nil {
		return
	}

	h.writeManifestResponse(writer, logger, deployment)
}

// getDeploymentNameForManifest retrieves deployment name for manifest fetching.

// fetchDeploymentManifest fetches deployment manifest from director.

// writeManifestResponse writes the manifest response.

// convertToJSONCompatible converts map[interface{}]interface{} to map[string]interface{}
// This is necessary because YAML unmarshaling creates map[interface{}]interface{} which
// cannot be directly marshaled to JSON.
func convertToJSONCompatible(value interface{}) interface{} {
	switch typedValue := value.(type) {
	case map[interface{}]interface{}:
		m := make(map[string]interface{})
		for k, v := range typedValue {
			m[fmt.Sprint(k)] = convertToJSONCompatible(v)
		}

		return m
	case []interface{}:
		for i, v := range typedValue {
			typedValue[i] = convertToJSONCompatible(v)
		}

		return typedValue
	default:
		return value
	}
}

// GetInstanceCredentials returns credentials for a service instance.
func (h *Handler) GetInstanceCredentials(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-credentials")
	logger.Debug("Getting credentials for instance: %s", instanceID)

	if h.vault != nil {
		var creds map[string]interface{}

		path := instanceID + "/credentials"

		exists, err := h.vault.Get(req.Context(), path, &creds)
		if err != nil {
			logger.Error("Failed to fetch credentials from Vault: %v", err)
			response.HandleJSON(writer, map[string]interface{}{
				"error": fmt.Sprintf("Failed to fetch credentials: %v", err),
			}, nil)

			return
		}

		if exists && creds != nil {
			logger.Debug("Retrieved credentials for instance %s from Vault", instanceID)
			response.HandleJSON(writer, creds, nil)

			return
		}

		logger.Debug("No credentials found in Vault for instance %s", instanceID)
	}

	// Return placeholder if Vault is not configured or credentials not found
	credentials := map[string]interface{}{
		"host":     "10.0.0.1",
		"port":     defaultRedisPort,
		"username": "admin",
		"password": "***",
		"uri":      fmt.Sprintf("%s://admin:***@10.0.0.1:%d", extractServiceType(instanceID), defaultRedisPort),
	}

	response.HandleJSON(writer, credentials, nil)
}

// GetInstanceDetails returns detailed info for a service instance.
func (h *Handler) GetInstanceDetails(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-details")
	logger.Debug("Getting details for instance: %s", instanceID)

	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{
			"error": "Director not configured",
		}, nil)

		return
	}

	ctx := req.Context()

	inst, deploymentName, err := h.getInstanceDataForDetails(writer, ctx, logger, instanceID)
	if err != nil {
		return
	}

	deployment, err := h.fetchDeploymentForDetails(writer, logger, deploymentName, instanceID)
	if err != nil {
		return
	}

	details := h.buildInstanceDetails(instanceID, deployment, inst)
	response.HandleJSON(writer, details, nil)
}

// getInstanceDataForDetails retrieves instance data for details endpoint.

// fetchDeploymentForDetails fetches deployment for details endpoint.

// buildInstanceDetails builds instance details response.

// GetInstanceConfig returns configuration for a service instance.
func (h *Handler) GetInstanceConfig(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-config")
	logger.Debug("Getting config for instance: %s", instanceID)

	if h.vault == nil {
		logger.Error("Vault not configured")
		response.WriteError(writer, http.StatusInternalServerError, "vault not configured")

		return
	}

	ctx := req.Context()

	configData, err := h.loadInstanceConfig(ctx, instanceID)
	if err != nil {
		if errors.Is(err, errInstanceNotFound) {
			response.WriteError(writer, http.StatusNotFound, errInstanceNotFound.Error())
		} else {
			logger.Error("Failed to load config for %s: %v", instanceID, err)
			response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to load config: %v", err))
		}

		return
	}

	response.HandleJSON(writer, configData, nil)
}

// GetInstanceTaskLog returns task logs for a service instance.
func (h *Handler) GetInstanceTaskLog(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-task-log")
	logger.Debug("Getting task log for instance: %s", instanceID)

	if h.director == nil {
		logger.Error("Director not configured")
		response.WriteError(writer, http.StatusInternalServerError, "director not configured")

		return
	}

	ctx := req.Context()

	deploymentInfo, err := h.resolveDeploymentInfo(ctx, instanceID)
	if err != nil {
		if errors.Is(err, errInstanceNotFound) {
			response.WriteError(writer, http.StatusNotFound, errInstanceNotFound.Error())
		} else {
			logger.Error("Failed to resolve deployment info for %s: %v", instanceID, err)
			response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to resolve deployment info: %v", err))
		}

		return
	}

	deploymentName := deploymentInfo.Deployment
	logger.Debug("Resolved deployment name %s for task log", deploymentName)

	events, err := h.director.GetEvents(deploymentName)
	if err != nil {
		logger.Error("Unable to get events for deployment %s: %v", deploymentName, err)
		response.HandleJSON(writer, []bosh.TaskEvent{}, nil)

		return
	}

	taskID := latestDeploymentTaskID(events)
	if taskID == 0 {
		logger.Debug("No task ID found for deployment %s", deploymentName)
		response.HandleJSON(writer, []bosh.TaskEvent{}, nil)

		return
	}

	logger.Debug("Fetching task output for task %d", taskID)

	taskOutput := fetchTaskOutput(logger, h.director, taskID)
	if strings.TrimSpace(taskOutput) == "" {
		logger.Debug("Task output empty for task %d", taskID)
		response.HandleJSON(writer, []bosh.TaskEvent{}, nil)

		return
	}

	response.HandleJSON(writer, parseResultOutputToEvents(taskOutput), nil)
}

func (h *Handler) parseCertificateFileRequest(writer http.ResponseWriter, req *http.Request) (struct{ FilePath string }, error) {
	var fileRequest struct {
		FilePath string `json:"filePath"`
	}

	err := json.NewDecoder(req.Body).Decode(&fileRequest)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))

		return struct{ FilePath string }{}, fmt.Errorf("failed to decode certificate file request: %w", err)
	}

	if fileRequest.FilePath == "" {
		response.WriteError(writer, http.StatusBadRequest, "filePath is required")

		return struct{ FilePath string }{}, errFilePathRequired
	}

	if !strings.HasPrefix(fileRequest.FilePath, "/etc/ssl/certs/bosh-trusted-cert-") || !strings.HasSuffix(fileRequest.FilePath, ".pem") {
		response.WriteError(writer, http.StatusBadRequest, "invalid file path: must be a BOSH trusted certificate")

		return struct{ FilePath string }{}, errInvalidFilePath
	}

	return struct{ FilePath string }{FilePath: fileRequest.FilePath}, nil
}

func (h *Handler) fetchCertificateFileContent(writer http.ResponseWriter, logger interfaces.Logger, deploymentInfo *deploymentInfo, instanceID, filePath string) (string, error) {
	sshReq := &boshssh.SSHRequest{
		Deployment: deploymentInfo.Deployment,
		Instance:   deploymentInfo.InstanceGroup,
		Index:      0,
		Command:    "/bin/cat",
		Args:       []string{filePath},
		Timeout:    defaultSSHTimeout,
		Options: &boshssh.SSHOptions{
			BufferOutput:  true,
			MaxOutputSize: defaultSSHMaxOutputSize,
		},
	}

	logger.Debug("Reading certificate file via SSH: %s on %s/%s/%d", filePath, sshReq.Deployment, sshReq.Instance, sshReq.Index)

	sshResp, err := h.ssh.ExecuteCommand(sshReq)
	if err != nil {
		logger.Error("SSH certificate file read failed for %s: %v", instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to read certificate file: %v", err))

		return "", fmt.Errorf("failed to read certificate file via SSH: %w", err)
	}

	if !sshResp.Success || strings.TrimSpace(sshResp.Stdout) == "" {
		errorMsg := sshResp.Error
		if errorMsg == "" && sshResp.Stderr != "" {
			errorMsg = sshResp.Stderr
		}

		if errorMsg == "" {
			errorMsg = "No output returned from certificate file"
		}

		response.WriteError(writer, http.StatusInternalServerError, "failed to read certificate file: "+errorMsg)

		return "", fmt.Errorf("%w: %s", errFailedToReadCertificateFile, errorMsg)
	}

	return strings.TrimSpace(sshResp.Stdout), nil
}

func (h *Handler) writeCertificateFileResponse(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, filePath, certPEM string) {
	certInfo, err := certificates.ParseCertificateFromPEM(ctx, certPEM)
	if err != nil {
		logger.Error("Failed to parse certificate from %s: %v", filePath, err)
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("failed to parse certificate: %v", err))

		return
	}

	fileName := filePath
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		fileName = filePath[idx+1:]
	}

	certItem := certificates.CertificateListItem{
		Name:    fileName,
		Path:    filePath,
		Details: *certInfo,
	}

	response.HandleJSON(writer, map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"certificates": []certificates.CertificateListItem{certItem},
			"metadata": map[string]interface{}{
				"source":    "service-trusted-file",
				"timestamp": time.Now(),
				"count":     1,
			},
		},
	}, nil)
}

func (h *Handler) parseAddCertificateRequest(writer http.ResponseWriter, req *http.Request) (struct {
	Certificate string
	Name        string
}, error) {
	var certRequest struct {
		Certificate string `json:"certificate"`
		Name        string `json:"name"`
	}

	err := json.NewDecoder(req.Body).Decode(&certRequest)
	if err != nil {
		errorMsg := fmt.Sprintf("%s: %v", errInvalidRequest.Error(), err)
		response.WriteError(writer, http.StatusBadRequest, errorMsg)

		return struct {
			Certificate string
			Name        string
		}{}, fmt.Errorf("failed to decode certificate request: %w", err)
	}

	if certRequest.Certificate == "" {
		response.WriteError(writer, http.StatusBadRequest, errCertificateIsRequired.Error())

		return struct {
			Certificate string
			Name        string
		}{}, errCertificateIsRequired
	}

	return struct {
		Certificate string
		Name        string
	}{Certificate: certRequest.Certificate, Name: certRequest.Name}, nil
}

func (h *Handler) validateAndParseCertificate(writer http.ResponseWriter, ctx context.Context, certRequest struct {
	Certificate string
	Name        string
}) (*certificates.CertificateInfo, string, error) {
	err := certificates.ValidateCertificateFormat(certRequest.Certificate)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("invalid certificate format: %v", err))

		return nil, "", fmt.Errorf("invalid certificate format: %w", err)
	}

	certInfo, err := certificates.ParseCertificateFromPEM(ctx, certRequest.Certificate)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("failed to parse certificate: %v", err))

		return nil, "", fmt.Errorf("failed to parse certificate: %w", err)
	}

	name := strings.TrimSpace(certRequest.Name)
	if name == "" {
		name = certInfo.Subject.CommonName
	}

	if name == "" {
		name = "Trusted certificate"
	}

	return certInfo, name, nil
}

func (h *Handler) storeTrustedCertificate(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID, recordID, name, certPEM string, certInfo *certificates.CertificateInfo) error {
	storePath := instanceID + "/certificates/trusted"

	var store trustedCertificateStore

	exists, err := h.vault.Get(ctx, storePath, &store)
	if err != nil {
		logger.Error("Failed to load trusted certificate store for %s: %v", instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to load certificates: %v", err))

		return fmt.Errorf("failed to load trusted certificate store: %w", err)
	}

	if !exists {
		store = trustedCertificateStore{}
	}

	record := trustedCertificateRecord{
		ID:          recordID,
		Name:        name,
		PEM:         certPEM,
		AddedAt:     time.Now().UTC(),
		Fingerprint: certInfo.Fingerprints,
	}

	updated := false

	for i, existing := range store.Certificates {
		if existing.ID == recordID {
			store.Certificates[i] = record
			updated = true

			break
		}
	}

	if !updated {
		store.Certificates = append(store.Certificates, record)
	}

	err = h.vault.Put(ctx, storePath, store)
	if err != nil {
		logger.Error("Failed to persist trusted certificates for %s: %v", instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to store certificate: %v", err))

		return fmt.Errorf("failed to persist trusted certificates: %w", err)
	}

	return nil
}

func (h *Handler) writeAddCertificateResponse(writer http.ResponseWriter, instanceID, recordID, name string, certInfo *certificates.CertificateInfo) {
	listItem := certificates.CertificateListItem{
		Name:    name,
		Path:    recordID,
		Details: *certInfo,
	}

	result := map[string]interface{}{
		"instance_id":    instanceID,
		"certificate":    listItem,
		"certificate_id": recordID,
		"message":        "Certificate added successfully",
	}

	response.HandleJSON(writer, result, nil)
}

func (h *Handler) removeCertificateFromStore(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID, certID string) (trustedCertificateRecord, error) {
	var emptyRecord trustedCertificateRecord

	store, storeLoaded := h.loadCertificateStore(writer, ctx, logger, instanceID)
	if !storeLoaded {
		return emptyRecord, errCertificateNotFound
	}

	removedRecord, ok := h.findAndRemoveCertificate(writer, logger, &store, instanceID, certID)
	if !ok {
		return emptyRecord, errCertificateNotFound
	}

	err := h.saveCertificateStoreAfterRemoval(writer, ctx, logger, instanceID, &store)
	if err != nil {
		return emptyRecord, err
	}

	return removedRecord, nil
}

func (h *Handler) loadCertificateStore(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) (trustedCertificateStore, bool) {
	var store trustedCertificateStore

	storePath := instanceID + "/certificates/trusted"

	exists, err := h.vault.Get(ctx, storePath, &store)
	if err != nil {
		logger.Error("Failed to load trusted certificate store for %s: %v", instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to load certificates: %v", err))

		return store, false
	}

	if !exists || len(store.Certificates) == 0 {
		logger.Debug("No trusted certificates stored for %s", instanceID)
		response.WriteError(writer, http.StatusNotFound, "certificate not found")

		return store, false
	}

	return store, true
}

func (h *Handler) findAndRemoveCertificate(writer http.ResponseWriter, logger interfaces.Logger, store *trustedCertificateStore, instanceID, certID string) (trustedCertificateRecord, bool) {
	var emptyRecord trustedCertificateRecord

	index := -1

	for i, record := range store.Certificates {
		if record.ID == certID {
			index = i

			break
		}
	}

	if index == -1 {
		logger.Debug("Certificate %s not found in store for %s", certID, instanceID)
		response.WriteError(writer, http.StatusNotFound, "certificate not found")

		return emptyRecord, false
	}

	removedRecord := store.Certificates[index]
	store.Certificates = append(store.Certificates[:index], store.Certificates[index+1:]...)

	return removedRecord, true
}

func (h *Handler) saveCertificateStoreAfterRemoval(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string, store *trustedCertificateStore) error {
	storePath := instanceID + "/certificates/trusted"

	if len(store.Certificates) == 0 {
		err := h.vault.Delete(ctx, storePath)
		if err != nil {
			logger.Error("Failed to delete empty certificate store for %s: %v", instanceID, err)
			response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to update certificates: %v", err))

			return fmt.Errorf("failed to delete empty certificate store: %w", err)
		}
	} else {
		err := h.vault.Put(ctx, storePath, *store)
		if err != nil {
			logger.Error("Failed to update certificate store for %s: %v", instanceID, err)
			response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to update certificates: %v", err))

			return fmt.Errorf("failed to update certificate store: %w", err)
		}
	}

	return nil
}

func (h *Handler) writeRemoveCertificateResponse(writer http.ResponseWriter, instanceID, certID string, removedRecord trustedCertificateRecord) {
	result := map[string]interface{}{
		"instance_id":      instanceID,
		"certificate_id":   certID,
		"removed":          true,
		"removed_at":       time.Now().UTC(),
		"certificate_name": removedRecord.Name,
		"message":          "Certificate removed successfully",
	}

	response.HandleJSON(writer, result, nil)
}

func (h *Handler) validateRabbitMQRequest(writer http.ResponseWriter, req *http.Request) error {
	if req.Method != http.MethodPost {
		response.WriteError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use POST.")

		return errInvalidRequest
	}

	if h.rabbitmqSSH == nil {
		response.WriteError(writer, http.StatusServiceUnavailable, "rabbitmq ssh service not configured")

		return errSSHServiceUnavailable
	}

	return nil
}

func (h *Handler) readRequestBody(writer http.ResponseWriter, req *http.Request, logger interfaces.Logger) ([]byte, error) {
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error("Failed to read request body: %v", err)
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))

		return nil, fmt.Errorf("failed to read request body: %w", err)
	}

	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	return bodyBytes, nil
}

func (h *Handler) resolveRabbitMQDeployment(writer http.ResponseWriter, req *http.Request, instanceID string, logger interfaces.Logger) (string, string, error) {
	deploymentInfo, err := h.resolveDeploymentInfo(req.Context(), instanceID)
	if err != nil {
		if errors.Is(err, errInstanceNotFound) {
			response.WriteError(writer, http.StatusNotFound, errInstanceNotFound.Error())
		} else {
			logger.Error("Failed to resolve deployment info for %s: %v", instanceID, err)
			response.WriteError(writer, http.StatusInternalServerError, fmt.Sprintf("failed to resolve deployment info: %v", err))
		}

		return "", "", err
	}

	if !isRabbitMQService(deploymentInfo.ServiceID) {
		response.WriteError(writer, http.StatusBadRequest, "not a RabbitMQ instance")

		return "", "", errNotRabbitMQInstance
	}

	return deploymentInfo.Deployment, deploymentInfo.InstanceGroup, nil
}

func (h *Handler) executeRabbitMQCommand(writer http.ResponseWriter, command string, bodyBytes []byte, deploymentName, instanceName string, instanceIndex int) (*rabbitmqssh.RabbitMQCommandResult, error) {
	baseCmd := h.extractBaseCommand(command)

	return h.dispatchRabbitMQCommand(writer, baseCmd, bodyBytes, deploymentName, instanceName, instanceIndex)
}

func (h *Handler) extractBaseCommand(command string) string {
	if idx := strings.Index(command, "/"); idx >= 0 {
		return command[:idx]
	}

	return command
}

func (h *Handler) dispatchRabbitMQCommand(writer http.ResponseWriter, baseCmd string, bodyBytes []byte, deploymentName, instanceName string, instanceIndex int) (*rabbitmqssh.RabbitMQCommandResult, error) {
	commandExecutors := map[string]func() (*rabbitmqssh.RabbitMQCommandResult, error){
		"list_queues": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.ListQueues(deploymentName, instanceName, instanceIndex)
		},
		"list_connections": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.ListConnections(deploymentName, instanceName, instanceIndex)
		},
		"list_channels": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.ListChannels(deploymentName, instanceName, instanceIndex)
		},
		"list_users": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.ListUsers(deploymentName, instanceName, instanceIndex)
		},
		"cluster_status": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.ClusterStatus(deploymentName, instanceName, instanceIndex)
		},
		"node_health": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.NodeHealth(deploymentName, instanceName, instanceIndex)
		},
		"status": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.Status(deploymentName, instanceName, instanceIndex)
		},
		"environment": func() (*rabbitmqssh.RabbitMQCommandResult, error) {
			return h.rabbitmqSSH.Environment(deploymentName, instanceName, instanceIndex)
		},
	}

	if baseCmd == "custom" {
		return h.executeCustomRabbitMQCommand(writer, bodyBytes, deploymentName, instanceName, instanceIndex)
	}

	executor, exists := commandExecutors[baseCmd]
	if !exists {
		response.WriteError(writer, http.StatusNotFound, "unknown RabbitMQ SSH operation: "+baseCmd)

		return nil, fmt.Errorf("%w: %s", errUnknownOperation, baseCmd)
	}

	result, err := executor()
	if err != nil {
		return nil, fmt.Errorf("failed to execute %s: %w", baseCmd, err)
	}

	return result, nil
}

func (h *Handler) executeCustomRabbitMQCommand(writer http.ResponseWriter, bodyBytes []byte, deploymentName, instanceName string, instanceIndex int) (*rabbitmqssh.RabbitMQCommandResult, error) {
	var customReq struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Timeout int      `json:"timeout"`
	}

	if len(bodyBytes) == 0 {
		response.WriteError(writer, http.StatusBadRequest, "custom command payload required")

		return nil, errCustomCommandPayloadRequired
	}

	err := json.Unmarshal(bodyBytes, &customReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))

		return nil, fmt.Errorf("failed to unmarshal custom command request: %w", err)
	}

	if strings.TrimSpace(customReq.Command) == "" {
		response.WriteError(writer, http.StatusBadRequest, "command is required")

		return nil, errCommandIsRequired
	}

	cmd := rabbitmqssh.RabbitMQCommand{
		Name:        customReq.Command,
		Args:        customReq.Args,
		Description: "Custom RabbitMQ command",
		Timeout:     customReq.Timeout,
	}

	result, err := h.rabbitmqSSH.ExecuteCommand(deploymentName, instanceName, instanceIndex, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to execute custom RabbitMQ command: %w", err)
	}

	return result, nil
}

func (h *Handler) getDeploymentNameFromVault(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) (string, error) {
	var inst struct {
		ServiceID      string `json:"service_id"`
		PlanID         string `json:"plan_id"`
		DeploymentName string `json:"deployment_name"`
	}

	exists, err := h.vault.Get(ctx, instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"vms":   []interface{}{},
			"error": "service instance not found",
		}, nil)

		return "", errInstanceNotFound
	}

	logger.Debug("Getting VMs for deployment: %s", inst.DeploymentName)

	return inst.DeploymentName, nil
}

func (h *Handler) fetchDeploymentVMs(writer http.ResponseWriter, logger interfaces.Logger, deploymentName string) ([]bosh.VM, error) {
	vms, err := h.director.GetDeploymentVMs(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch VMs: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"vms":   []interface{}{},
			"error": err.Error(),
		}, nil)

		return nil, fmt.Errorf("failed to fetch VMs for deployment: %w", err)
	}

	return vms, nil
}

func (h *Handler) applyResurrectionConfig(logger interfaces.Logger, deploymentName string, vms []bosh.VM) {
	configName := "blacksmith." + deploymentName
	logger.Debug("Checking for resurrection config: %s (deployment: %s)", configName, deploymentName)

	resurrectionPaused := false

	resurrectionConfig, err := h.director.GetConfig("resurrection", configName)
	if err != nil {
		logger.Debug("Error getting resurrection config: %v", err)
	}

	if err == nil && resurrectionConfig != nil {
		resurrectionPaused = processResurrectionConfigWithDebug(resurrectionConfig, deploymentName, configName, logger)
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

func (h *Handler) cacheVMData(ctx context.Context, logger interfaces.Logger, instanceID, deploymentName string, vms []bosh.VM) {
	if instanceID == "blacksmith" {
		return
	}

	cacheEntry := map[string]interface{}{
		"vms":        vms,
		"cached_at":  time.Now().Format(time.RFC3339),
		"deployment": deploymentName,
	}

	err := h.vault.Put(ctx, instanceID+"/vms", cacheEntry)
	if err != nil {
		logger.Error("failed to cache VM data for instance %s: %v", instanceID, err)
	}
}

func (h *Handler) getDeploymentNameForLogs(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) (string, error) {
	var inst struct {
		ServiceID      string `json:"service_id"`
		PlanID         string `json:"plan_id"`
		DeploymentName string `json:"deployment_name"`
	}

	exists, err := h.vault.Get(ctx, instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "service instance not found",
		}, nil)

		return "", errInstanceNotFound
	}

	logger.Debug("Fetching logs for deployment %s", inst.DeploymentName)

	return inst.DeploymentName, nil
}

func (h *Handler) getInstanceGroupsFromManifest(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) ([]interface{}, error) {
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil || !exists {
		logger.Error("Unable to find manifest for instance %s: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to find manifest",
		}, nil)

		return nil, errManifestNotFound
	}

	var manifest map[string]interface{}

	err = yaml.Unmarshal([]byte(manifestData.Manifest), &manifest)
	if err != nil {
		logger.Error("Unable to parse manifest: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to parse manifest",
		}, nil)

		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok {
		logger.Error("Unable to find instance_groups in manifest")
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to find instance groups",
		}, nil)

		return nil, errInstanceGroupsNotFound
	}

	return instanceGroups, nil
}

func (h *Handler) fetchLogsForInstanceGroups(logger interfaces.Logger, deploymentName string, instanceGroups []interface{}) map[string]interface{} {
	allLogs := make(map[string]interface{})

	logger.Debug("Found %d instance groups in manifest", len(instanceGroups))

	for idx, ig := range instanceGroups {
		group, found := ig.(map[string]interface{})
		if !found {
			logger.Debug("Instance group %d is not map[string]interface{}, skipping (type: %T)", idx, ig)

			continue
		}

		jobName, ok := group["name"].(string)
		if !ok {
			logger.Debug("Instance group %d has no 'name' field or wrong type, skipping", idx)

			continue
		}

		logger.Debug("Found job: %s", jobName)

		instances := h.getInstanceCount(group)
		logger.Debug("Job %s has %d instances", jobName, instances)

		h.fetchLogsForJob(logger, allLogs, deploymentName, jobName, instances)
	}

	return allLogs
}

func (h *Handler) getInstanceCount(group map[string]interface{}) int {
	instances := 1

	switch v := group["instances"].(type) {
	case int:
		instances = v
	case float64:
		instances = int(v)
	}

	return instances
}

func (h *Handler) fetchLogsForJob(logger interfaces.Logger, allLogs map[string]interface{}, deploymentName, jobName string, instances int) {
	for i := range instances {
		jobIndex := strconv.Itoa(i)
		logKey := fmt.Sprintf("%s/%s", jobName, jobIndex)

		logger.Debug("Fetching logs for job %s/%s", jobName, jobIndex)

		logs, err := h.director.FetchLogs(deploymentName, jobName, jobIndex)
		if err != nil {
			logger.Error("Failed to fetch logs for %s/%s: %v", jobName, jobIndex, err)
			allLogs[logKey] = map[string]interface{}{
				"error": fmt.Sprintf("Failed to fetch logs: %s", err),
			}
		} else {
			allLogs[logKey] = parseLogContent(logs)
		}
	}
}

func (h *Handler) getDeploymentNameForManifest(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) (string, error) {
	var inst struct {
		ServiceID      string `json:"service_id"`
		PlanID         string `json:"plan_id"`
		DeploymentName string `json:"deployment_name"`
	}

	exists, err := h.vault.Get(ctx, instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"text":   "",
			"parsed": nil,
			"error":  "service instance not found",
		}, nil)

		return "", errInstanceNotFound
	}

	logger.Debug("Getting manifest for deployment: %s", inst.DeploymentName)

	return inst.DeploymentName, nil
}

func (h *Handler) fetchDeploymentManifest(writer http.ResponseWriter, logger interfaces.Logger, deploymentName string) (*bosh.DeploymentDetail, error) {
	deployment, err := h.director.GetDeployment(deploymentName)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "doesn't exist") {
			logger.Info("Deployment %s doesn't exist (404), returning success for deleted deployment", deploymentName)
			response.HandleJSON(writer, map[string]interface{}{
				"text": "",
				"parsed": map[string]interface{}{
					"name":    deploymentName,
					"deleted": true,
					"message": "Deployment has been deleted",
				},
				"deleted": true,
			}, nil)

			return nil, fmt.Errorf("deployment not found: %w", err)
		}

		logger.Error("Failed to fetch deployment: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"text":   "",
			"parsed": nil,
			"error":  err.Error(),
		}, nil)

		return nil, fmt.Errorf("failed to fetch deployment: %w", err)
	}

	return deployment, nil
}

func (h *Handler) writeManifestResponse(writer http.ResponseWriter, logger interfaces.Logger, deployment *bosh.DeploymentDetail) {
	h.writeManifestFromText(writer, logger, deployment.Manifest)
}

// writeManifestFromText writes a manifest response from raw manifest text.
func (h *Handler) writeManifestFromText(writer http.ResponseWriter, logger interfaces.Logger, manifestText string) {
	var manifestParsed interface{}

	err := yaml.Unmarshal([]byte(manifestText), &manifestParsed)
	if err != nil {
		logger.Error("Unable to parse manifest YAML: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"text":   manifestText,
			"parsed": nil,
			"error":  "unable to parse manifest",
		}, nil)

		return
	}

	manifestParsed = convertToJSONCompatible(manifestParsed)

	response.HandleJSON(writer, map[string]interface{}{
		"text":   manifestText,
		"parsed": manifestParsed,
	}, nil)
}

func (h *Handler) getInstanceDataForDetails(writer http.ResponseWriter, ctx context.Context, logger interfaces.Logger, instanceID string) (struct {
	ServiceID      string `json:"service_id"`
	PlanID         string `json:"plan_id"`
	DeploymentName string `json:"deployment_name"`
	InstanceName   string `json:"instance_name"`
}, string, error) {
	var inst struct {
		ServiceID      string `json:"service_id"`
		PlanID         string `json:"plan_id"`
		DeploymentName string `json:"deployment_name"`
		InstanceName   string `json:"instance_name"`
	}

	exists, err := h.vault.Get(ctx, instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"error": "service instance not found",
		}, nil)

		return inst, "", errInstanceNotFound
	}

	logger.Debug("Getting details for deployment: %s", inst.DeploymentName)

	return inst, inst.DeploymentName, nil
}

func (h *Handler) fetchDeploymentForDetails(writer http.ResponseWriter, logger interfaces.Logger, deploymentName, instanceID string) (*bosh.DeploymentDetail, error) {
	deployment, err := h.director.GetDeployment(deploymentName)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "doesn't exist") {
			logger.Info("Deployment %s doesn't exist (404), returning success for deleted instance", deploymentName)
			response.HandleJSON(writer, map[string]interface{}{
				"instance_id": instanceID,
				"deleted":     true,
				"message":     "Instance has been deleted",
			}, nil)

			return nil, fmt.Errorf("deployment not found: %w", err)
		}

		logger.Error("Failed to fetch deployment: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"error": err.Error(),
		}, nil)

		return nil, fmt.Errorf("failed to fetch deployment for details: %w", err)
	}

	return deployment, nil
}

func (h *Handler) buildInstanceDetails(instanceID string, deployment *bosh.DeploymentDetail, inst struct {
	ServiceID      string `json:"service_id"`
	PlanID         string `json:"plan_id"`
	DeploymentName string `json:"deployment_name"`
	InstanceName   string `json:"instance_name"`
}) map[string]interface{} {
	return map[string]interface{}{
		"instance_id":     instanceID,
		"service_id":      inst.ServiceID,
		"plan_id":         inst.PlanID,
		"deployment_name": inst.DeploymentName,
		"instance_name":   inst.InstanceName,
		"manifest": map[string]interface{}{
			"text": deployment.Manifest,
		},
	}
}

// handleInstanceOperation handles service instance operation endpoints.
func (h *Handler) handleInstanceOperation(writer http.ResponseWriter, req *http.Request) bool {
	instancePattern := regexp.MustCompile(`^/b/([^/]+)/(vms|logs|events|manifest|credentials|details|config|task/log|task/debug)$`)

	matches := instancePattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		return false
	}

	instanceID := matches[1]
	operation := matches[2]

	switch operation {
	case "vms":
		h.GetInstanceVMs(writer, req, instanceID)
	case "logs":
		h.GetInstanceLogs(writer, req, instanceID)
	case "events":
		h.GetInstanceEvents(writer, req, instanceID)
	case "manifest":
		h.GetInstanceManifest(writer, req, instanceID)
	case "credentials":
		h.GetInstanceCredentials(writer, req, instanceID)
	case "details":
		h.GetInstanceDetails(writer, req, instanceID)
	case "config":
		h.GetInstanceConfig(writer, req, instanceID)
	case "task/log", "task/debug":
		h.GetInstanceTaskLog(writer, req, instanceID)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}

	return true
}

func (h *Handler) handleSSHExecute(writer http.ResponseWriter, req *http.Request) bool {
	sshExecutePattern := regexp.MustCompile(`^/b/([^/]+)/ssh/execute$`)

	matches := sshExecutePattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		return false
	}

	if req.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)

		return true
	}

	instanceID := matches[1]
	h.ExecuteSSHCommand(writer, req, instanceID)

	return true
}

func (h *Handler) handleCertificateTrustedFile(writer http.ResponseWriter, req *http.Request) bool {
	certFilePattern := regexp.MustCompile(`^/b/([^/]+)/certificates/trusted/file$`)

	matches := certFilePattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		return false
	}

	if req.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)

		return true
	}

	instanceID := matches[1]
	h.GetTrustedCertificateFile(writer, req, instanceID)

	return true
}

func (h *Handler) handleCertificateTrusted(writer http.ResponseWriter, req *http.Request) bool {
	certsPattern := regexp.MustCompile(`^/b/([^/]+)/certificates/trusted$`)

	m := certsPattern.FindStringSubmatch(req.URL.Path)
	if m == nil {
		return false
	}

	instanceID := m[1]

	switch req.Method {
	case http.MethodGet:
		h.GetTrustedCertificates(writer, req, instanceID)
	case http.MethodPost:
		h.AddTrustedCertificate(writer, req, instanceID)
	case http.MethodDelete:
		h.RemoveTrustedCertificate(writer, req, instanceID)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}

	return true
}

func (h *Handler) handleRabbitMQSSH(writer http.ResponseWriter, req *http.Request) bool {
	rabbitMQSSHPattern := regexp.MustCompile(`^/b/([^/]+)/rabbitmq/ssh/(.+)$`)

	matches := rabbitMQSSHPattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		return false
	}

	instanceID := matches[1]
	command := matches[2]
	h.ExecuteRabbitMQSSHCommand(writer, req, instanceID, command)

	return true
}

func (h *Handler) parseSSHExecuteRequest(writer http.ResponseWriter, req *http.Request) (SSHExecuteRequest, error) {
	var execReq SSHExecuteRequest

	err := json.NewDecoder(req.Body).Decode(&execReq)
	if err != nil {
		errorMsg := fmt.Sprintf("%s: %v", errInvalidRequest.Error(), err)
		response.WriteError(writer, http.StatusBadRequest, errorMsg)

		return execReq, fmt.Errorf("%w: %w", errInvalidRequest, err)
	}

	if execReq.Command == "" {
		response.WriteError(writer, http.StatusBadRequest, errCommandIsRequired.Error())

		return execReq, errCommandIsRequired
	}

	return execReq, nil
}

func (h *Handler) resolveDeploymentForSSH(writer http.ResponseWriter, ctx context.Context, instanceID string, logger interfaces.Logger) (*deploymentInfo, error) {
	deploymentInfo, err := h.resolveDeploymentInfo(ctx, instanceID)
	if err != nil {
		if errors.Is(err, errInstanceNotFound) {
			response.WriteError(writer, http.StatusNotFound, errInstanceNotFound.Error())
		} else {
			logger.Error("Failed to resolve deployment info for %s: %v", instanceID, err)
			errorMsg := fmt.Sprintf("failed to resolve deployment info: %v", err)
			response.WriteError(writer, http.StatusInternalServerError, errorMsg)
		}

		return nil, err
	}

	return deploymentInfo, nil
}

func (h *Handler) buildSSHRequest(deploymentInfo *deploymentInfo, execReq *SSHExecuteRequest) *boshssh.SSHRequest {
	sshReq := &boshssh.SSHRequest{
		Deployment: deploymentInfo.Deployment,
		Instance:   deploymentInfo.InstanceGroup,
		Index:      0,
		Command:    execReq.Command,
		Args:       append([]string(nil), execReq.Arguments...),
		Timeout:    execReq.Timeout,
		Options: &boshssh.SSHOptions{
			BufferOutput:  true,
			MaxOutputSize: defaultSSHMaxOutputSize,
		},
	}

	if sshReq.Timeout <= 0 {
		sshReq.Timeout = defaultSSHTimeout
	}

	return sshReq
}

func (h *Handler) handleAsyncSSH(writer http.ResponseWriter, logger interfaces.Logger, sshReq *boshssh.SSHRequest, instanceID string, execReq *SSHExecuteRequest) {
	h.executeSSHAsync(logger, sshReq, instanceID)

	result := map[string]interface{}{
		"instance_id": instanceID,
		"command":     execReq.Command,
		"arguments":   execReq.Arguments,
		"async":       true,
		"task_id":     testTaskIDServices,
		"message":     "Command execution started asynchronously",
	}

	response.HandleJSON(writer, result, nil)
}

func (h *Handler) handleSyncSSH(writer http.ResponseWriter, logger interfaces.Logger, sshReq *boshssh.SSHRequest, instanceID string, execReq *SSHExecuteRequest) {
	sshResp, err := h.ssh.ExecuteCommand(sshReq)
	if err != nil {
		logger.Error("SSH command execution failed for %s: %v", instanceID, err)
		response.WriteError(writer, http.StatusInternalServerError, err.Error())

		return
	}

	result := h.buildSSHResponse(instanceID, execReq, sshResp)
	response.HandleJSON(writer, result, nil)
}

func (h *Handler) buildSSHResponse(instanceID string, execReq *SSHExecuteRequest, sshResp *boshssh.SSHResponse) map[string]interface{} {
	result := map[string]interface{}{
		"instance_id": instanceID,
		"command":     execReq.Command,
		"arguments":   execReq.Arguments,
		"exit_code":   sshResp.ExitCode,
		"stdout":      sshResp.Stdout,
		"stderr":      sshResp.Stderr,
		"success":     sshResp.Success,
		"duration":    sshResp.Duration,
		"timestamp":   sshResp.Timestamp,
		"async":       false,
	}

	if sshResp.Error != "" {
		result["error"] = sshResp.Error
	}

	if sshResp.RequestID != "" {
		result["request_id"] = sshResp.RequestID
	}

	return result
}

func (h *Handler) executeCertificateListCommand(logger interfaces.Logger, deploymentInfo *deploymentInfo, instanceID string) (*boshssh.SSHResponse, error) {
	sshReq := &boshssh.SSHRequest{
		Deployment: deploymentInfo.Deployment,
		Instance:   deploymentInfo.InstanceGroup,
		Index:      0,
		Command:    "/bin/bash",
		Args:       []string{"-c", "/bin/ls /etc/ssl/certs/bosh-trusted-cert-*.pem"},
		Timeout:    defaultSSHTimeout,
		Options: &boshssh.SSHOptions{
			BufferOutput:  true,
			MaxOutputSize: defaultSSHMaxOutputSize,
		},
	}

	logger.Debug("Listing certificates via SSH command on %s/%s/%d", sshReq.Deployment, sshReq.Instance, sshReq.Index)

	sshResp, err := h.ssh.ExecuteCommand(sshReq)
	if err != nil {
		logger.Error("SSH certificate listing failed for %s: %v", instanceID, err)

		return nil, fmt.Errorf("SSH certificate listing failed: %w", err)
	}

	return sshResp, nil
}

func (h *Handler) parseCertificateFiles(sshResp *boshssh.SSHResponse) []map[string]string {
	var files []map[string]string

	if !sshResp.Success || strings.TrimSpace(sshResp.Stdout) == "" {
		return files
	}

	lines := strings.Split(strings.TrimSpace(sshResp.Stdout), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(line, "No certificates found") && strings.HasSuffix(line, ".pem") {
			fileName := line
			if idx := strings.LastIndex(line, "/"); idx >= 0 {
				fileName = line[idx+1:]
			}

			files = append(files, map[string]string{
				"name": fileName,
				"path": line,
			})
		}
	}

	return files
}

func (h *Handler) writeCertificateErrorResponse(writer http.ResponseWriter, err error) {
	response.HandleJSON(writer, map[string]interface{}{
		"success": false,
		"error":   err.Error(),
		"data": map[string]interface{}{
			"files": []interface{}{},
			"metadata": map[string]interface{}{
				"source":    "service-trusted",
				"timestamp": time.Now(),
				"count":     0,
			},
		},
	}, nil)
}

func (h *Handler) writeCertificateSuccessResponse(writer http.ResponseWriter, files []map[string]string) {
	response.HandleJSON(writer, map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"files": files,
			"metadata": map[string]interface{}{
				"source":    "service-trusted",
				"timestamp": time.Now(),
				"count":     len(files),
			},
		},
	}, nil)
}

type deploymentInfo struct {
	Deployment    string
	InstanceGroup string
	ServiceID     string
	PlanID        string
}

func (h *Handler) executeSSHAsync(logger interfaces.Logger, request *boshssh.SSHRequest, instanceID string) {
	if h.ssh == nil {
		logger.Warnf("SSH service not configured for async execution on instance %s", instanceID)

		return
	}

	asyncReq := *request
	if request.Args != nil {
		asyncReq.Args = append([]string(nil), request.Args...)
	}

	go func(req *boshssh.SSHRequest) {
		_, sshErr := h.ssh.ExecuteCommand(req)
		if sshErr != nil {
			logger.Warnf("Async SSH command failed for instance %s: %v", instanceID, sshErr)

			return
		}

		logger.Debugf("Async SSH command completed for instance %s", instanceID)
	}(&asyncReq)
}

func (h *Handler) resolveDeploymentInfo(ctx context.Context, instanceID string) (*deploymentInfo, error) {
	logger := h.logger.Named("resolve-deployment")

	inst, exists, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to load instance metadata from vault: %w", err)
	}

	if !exists || inst.PlanID == "" {
		return nil, errInstanceNotFound
	}

	deployment := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

	instanceGroup, err := h.extractInstanceGroupName(ctx, instanceID)
	if err != nil {
		logger.Debug("Falling back to default instance group for %s: %v", instanceID, err)

		instanceGroup = defaultInstanceGroupName(inst.ServiceID, inst.PlanID)
	}

	if instanceGroup == "" {
		instanceGroup = inst.PlanID
	}

	return &deploymentInfo{
		Deployment:    deployment,
		InstanceGroup: instanceGroup,
		ServiceID:     inst.ServiceID,
		PlanID:        inst.PlanID,
	}, nil
}

func (h *Handler) extractInstanceGroupName(ctx context.Context, instanceID string) (string, error) {
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve manifest from vault: %w", err)
	}

	if !exists || strings.TrimSpace(manifestData.Manifest) == "" {
		return "", errManifestNotFoundForInstance
	}

	name, parseErr := firstInstanceGroupName(manifestData.Manifest)
	if parseErr != nil {
		return "", parseErr
	}

	return name, nil
}

func firstInstanceGroupName(manifestYAML string) (string, error) {
	var manifest map[string]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	groupsRaw, exists := manifest["instance_groups"]
	if !exists {
		return "", errManifestMissingInstanceGroups
	}

	groups, validSlice := groupsRaw.([]interface{})
	if !validSlice || len(groups) == 0 {
		return "", errManifestHasNoInstanceGroups
	}

	firstGroup, validMap := groups[0].(map[string]interface{})
	if !validMap {
		return "", errInstanceGroupHasUnexpectedFormat
	}

	name, ok := firstGroup["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", errInstanceGroupNameNotFound
	}

	return name, nil
}

func defaultInstanceGroupName(serviceID, planID string) string {
	service := strings.ToLower(serviceID)
	switch {
	case strings.Contains(service, serviceTypeRabbitMQ) || strings.Contains(service, "rabbit"):
		return serviceTypeRabbitMQ
	case strings.Contains(service, serviceTypeRedis):
		if strings.Contains(strings.ToLower(planID), "standalone") {
			return "standalone"
		}

		return serviceTypeRedis
	case strings.Contains(service, "postgres") || strings.Contains(service, "pgsql"):
		return "postgres"
	}

	if planID != "" {
		return planID
	}

	return "instance"
}

func generateCertificateID(fingerprint string) string {
	sanitized := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	if sanitized == "" {
		sanitized = fmt.Sprintf("%x", time.Now().UnixNano())
	}

	if len(sanitized) > maxCertificateIDLength {
		sanitized = sanitized[:maxCertificateIDLength]
	}

	return "cert-" + sanitized
}

func isRabbitMQService(serviceID string) bool {
	service := strings.ToLower(serviceID)

	return strings.Contains(service, serviceTypeRabbitMQ) || strings.Contains(service, "rabbit")
}

func guessServiceType(serviceID, planID string) string {
	serviceID = strings.ToLower(serviceID)
	planID = strings.ToLower(planID)

	switch {
	case strings.Contains(serviceID, serviceTypeRabbitMQ) || strings.Contains(planID, "rabbit"):
		return serviceTypeRabbitMQ
	case strings.Contains(serviceID, serviceTypeRedis) || strings.Contains(planID, serviceTypeRedis):
		return serviceTypeRedis
	case strings.Contains(serviceID, "postgres") || strings.Contains(serviceID, "pgsql") || strings.Contains(planID, "postgres"):
		return "postgresql"
	}

	return ""
}

type trustedCertificateStore struct {
	Certificates []trustedCertificateRecord `json:"certificates"`
}

type trustedCertificateRecord struct {
	ID          string                               `json:"id"`
	Name        string                               `json:"name"`
	PEM         string                               `json:"pem"`
	AddedAt     time.Time                            `json:"added_at"`
	Fingerprint certificates.CertificateFingerprints `json:"fingerprint"`
}

type instanceConfigResponse struct {
	InstanceID   string                 `json:"instance_id"`
	ServiceID    string                 `json:"service_id,omitempty"`
	PlanID       string                 `json:"plan_id,omitempty"`
	ServiceType  string                 `json:"service_type,omitempty"`
	PlanName     string                 `json:"plan_name,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Parameters   interface{}            `json:"parameters,omitempty"`
	Manifest     map[string]interface{} `json:"manifest,omitempty"`
	Credentials  map[string]interface{} `json:"credentials,omitempty"`
	Deployment   map[string]interface{} `json:"deployment,omitempty"`
	VaultSources []string               `json:"vault_sources,omitempty"`
}

func (h *Handler) loadInstanceConfig(ctx context.Context, instanceID string) (*instanceConfigResponse, error) {
	resp := &instanceConfigResponse{
		InstanceID:   instanceID,
		Metadata:     make(map[string]interface{}),
		VaultSources: []string{},
	}

	err := h.loadRootInstanceData(ctx, instanceID, resp)
	if err != nil {
		return nil, err
	}

	err = h.loadInstanceIndex(ctx, instanceID, resp)
	if err != nil {
		return nil, err
	}

	err = h.loadInstanceSecrets(ctx, instanceID, resp)
	if err != nil {
		return nil, err
	}

	h.normalizeInstanceConfig(resp)

	return resp, nil
}

func (h *Handler) loadRootInstanceData(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	var root map[string]interface{}

	exists, err := h.vault.Get(ctx, instanceID, &root)
	if err != nil {
		return fmt.Errorf("failed to load instance data: %w", err)
	}

	if !exists {
		return errInstanceNotFound
	}

	resp.VaultSources = append(resp.VaultSources, instanceID)
	resp.ServiceID = getStringValue(root, "service_id")
	resp.PlanID = getStringValue(root, "plan_id")
	resp.PlanName = getStringValue(root, "plan_name")
	resp.ServiceType = getStringValue(root, "service_type")

	if ctxMap := normalizeToStringMap(root["context"]); len(ctxMap) > 0 {
		resp.Metadata = mergeStringMaps(resp.Metadata, ctxMap)
	}

	if params, ok := root["parameters"]; ok {
		resp.Parameters = params
	}

	if resp.ServiceType == "" {
		resp.ServiceType = guessServiceType(resp.ServiceID, resp.PlanID)
	}

	return nil
}

func (h *Handler) loadInstanceIndex(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	indexInst, indexExists, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to load instance index: %w", err)
	}

	if indexExists {
		if resp.ServiceID == "" {
			resp.ServiceID = indexInst.ServiceID
		}

		if resp.PlanID == "" {
			resp.PlanID = indexInst.PlanID
		}
	}

	return nil
}

func (h *Handler) loadInstanceSecrets(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	logger := h.logger.Named("load-instance-config")

	err := h.loadMetadataSecret(ctx, instanceID, resp)
	if err != nil {
		return err
	}

	err = h.loadDeploymentSecret(ctx, instanceID, resp)
	if err != nil {
		return err
	}

	err = h.loadManifestSecret(ctx, instanceID, resp, logger)
	if err != nil {
		return err
	}

	err = h.loadParametersSecret(ctx, instanceID, resp)
	if err != nil {
		return err
	}

	err = h.loadCredentialsSecret(ctx, instanceID, resp)
	if err != nil {
		return err
	}

	return nil
}

func (h *Handler) loadMetadataSecret(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	metadataSecret, metadataSource, err := h.loadMapSecret(ctx, instanceID+"/metadata")
	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}

	if metadataSecret != nil {
		resp.Metadata = mergeStringMaps(resp.Metadata, metadataSecret)
		resp.VaultSources = appendUnique(resp.VaultSources, metadataSource)
	}

	return nil
}

func (h *Handler) loadDeploymentSecret(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	deploymentSecret, deploymentSource, err := h.loadMapSecret(ctx, instanceID+"/deployment")
	if err != nil {
		return fmt.Errorf("failed to load deployment info: %w", err)
	}

	if deploymentSecret != nil {
		resp.Deployment = deploymentSecret

		resp.VaultSources = appendUnique(resp.VaultSources, deploymentSource)
		if resp.ServiceID == "" {
			resp.ServiceID = getStringValue(deploymentSecret, "service_id")
		}

		if resp.PlanID == "" {
			resp.PlanID = getStringValue(deploymentSecret, "plan_id")
		}
	}

	return nil
}

func (h *Handler) loadManifestSecret(ctx context.Context, instanceID string, resp *instanceConfigResponse, logger interfaces.Logger) error {
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	manifestExists, err := h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	if manifestExists && strings.TrimSpace(manifestData.Manifest) != "" {
		var manifestMap map[string]interface{}

		unmarshalErr := yaml.Unmarshal([]byte(manifestData.Manifest), &manifestMap)
		if unmarshalErr != nil {
			logger.Warnf("Failed to parse manifest YAML for %s: %v", instanceID, unmarshalErr)
		} else {
			resp.Manifest = normalizeToStringMap(manifestMap)
		}

		resp.VaultSources = appendUnique(resp.VaultSources, instanceID+"/manifest")
	}

	return nil
}

func (h *Handler) loadParametersSecret(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	paramsSecret, paramsSource, err := h.loadSecret(ctx, instanceID+"/parameters")
	if err != nil {
		return fmt.Errorf("failed to load parameters: %w", err)
	}

	if paramsSecret != nil {
		resp.Parameters = paramsSecret
		resp.VaultSources = appendUnique(resp.VaultSources, paramsSource)
	}

	return nil
}

func (h *Handler) loadCredentialsSecret(ctx context.Context, instanceID string, resp *instanceConfigResponse) error {
	credsSecret, credsSource, err := h.loadMapSecret(ctx, instanceID+"/credentials")
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	if credsSecret != nil {
		resp.Credentials = credsSecret
		resp.VaultSources = appendUnique(resp.VaultSources, credsSource)
	}

	return nil
}

func (h *Handler) normalizeInstanceConfig(resp *instanceConfigResponse) {
	if resp.ServiceType == "" {
		resp.ServiceType = guessServiceType(resp.ServiceID, resp.PlanID)
	}

	if len(resp.Metadata) == 0 {
		resp.Metadata = nil
	}

	if resp.Manifest != nil && len(resp.Manifest) == 0 {
		resp.Manifest = nil
	}

	if resp.Credentials != nil && len(resp.Credentials) == 0 {
		resp.Credentials = nil
	}

	if resp.Deployment != nil && len(resp.Deployment) == 0 {
		resp.Deployment = nil
	}

	if resp.Parameters != nil {
		if m := normalizeToStringMap(resp.Parameters); m != nil {
			resp.Parameters = m
		} else if arr, ok := resp.Parameters.([]interface{}); ok && len(arr) == 0 {
			resp.Parameters = nil
		}
	}

	if len(resp.VaultSources) == 0 {
		resp.VaultSources = nil
	}
}

func (h *Handler) loadMapSecret(ctx context.Context, path string) (map[string]interface{}, string, error) {
	var data map[string]interface{}

	exists, err := h.vault.Get(ctx, path, &data)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load secret from %s: %w", path, err)
	}

	if !exists {
		return nil, "", nil
	}

	return normalizeToStringMap(data), path, nil
}

func (h *Handler) loadSecret(ctx context.Context, path string) (interface{}, string, error) {
	var raw interface{}

	exists, err := h.vault.Get(ctx, path, &raw)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load secret from %s: %w", path, err)
	}

	if !exists {
		return nil, "", nil
	}

	return raw, path, nil
}

func getStringValue(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}

	if value, ok := data[key]; ok {
		switch typedValue := value.(type) {
		case string:
			return typedValue
		case fmt.Stringer:
			return typedValue.String()
		default:
			return fmt.Sprintf("%v", typedValue)
		}
	}

	return ""
}

func normalizeToStringMap(value interface{}) map[string]interface{} {
	switch typedValue := value.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		return copyStringInterfaceMap(typedValue)
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typedValue))
		for key, val := range typedValue {
			out[fmt.Sprintf("%v", key)] = val
		}

		return out
	default:
		return nil
	}
}

func copyStringInterfaceMap(inputMap map[string]interface{}) map[string]interface{} {
	if inputMap == nil {
		return nil
	}

	out := make(map[string]interface{}, len(inputMap))
	for k, v := range inputMap {
		out[k] = v
	}

	return out
}

func mergeStringMaps(base map[string]interface{}, other map[string]interface{}) map[string]interface{} {
	if len(other) == 0 {
		return base
	}

	if base == nil {
		base = make(map[string]interface{}, len(other))
	}

	for k, v := range other {
		base[k] = v
	}

	return base
}

func appendUnique(slice []string, item string) []string {
	for _, existing := range slice {
		if existing == item {
			return slice
		}
	}

	return append(slice, item)
}

func latestDeploymentTaskID(events []bosh.Event) int {
	var taskID int

	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.TaskID == "" || event.Action == "acquire" {
			continue
		}

		if event.ObjectType == "deployment" || event.Action == "create" || event.Action == "update" || event.Action == "deploy" {
			id, convErr := strconv.Atoi(event.TaskID)
			if convErr == nil {
				return id
			}
		}
	}

	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.TaskID == "" || event.Action == "acquire" || event.Action == "release" {
			continue
		}

		id, convErr := strconv.Atoi(event.TaskID)
		if convErr == nil {
			return id
		}
	}

	return taskID
}

func fetchTaskOutput(logger interfaces.Logger, director interfaces.Director, taskID int) string {
	eventOutput, err := director.GetTaskOutput(taskID, "event")
	if err != nil {
		logger.Debug("Unable to get event output for task %d: %v", taskID, err)
	}

	if strings.TrimSpace(eventOutput) != "" {
		return eventOutput
	}

	resultOutput, err := director.GetTaskOutput(taskID, "result")
	if err != nil {
		logger.Error("Unable to get result output for task %d: %v", taskID, err)

		return ""
	}

	return resultOutput
}

func parseResultOutputToEvents(resultOutput string) []bosh.TaskEvent {
	events := make([]bosh.TaskEvent, 0)
	if strings.TrimSpace(resultOutput) == "" {
		return events
	}

	lines := strings.Split(resultOutput, "\n")
	for _, line := range lines {
		if event, ok := parseSingleEventLine(line); ok {
			events = append(events, event)
		}
	}

	return events
}

func parseSingleEventLine(line string) (bosh.TaskEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return bosh.TaskEvent{}, false
	}

	boshEvent, err := unmarshalBOSHEvent(line)
	if err != nil {
		return bosh.TaskEvent{}, false
	}

	return buildTaskEvent(boshEvent), true
}

func unmarshalBOSHEvent(line string) (boshEventData, error) {
	var boshEvent boshEventData

	err := json.Unmarshal([]byte(line), &boshEvent)
	if err != nil {
		return boshEvent, fmt.Errorf("failed to unmarshal BOSH event: %w", err)
	}

	return boshEvent, nil
}

type boshEventData struct {
	Time     int64    `json:"time"`
	Stage    string   `json:"stage"`
	Tags     []string `json:"tags"`
	Total    int      `json:"total"`
	Task     string   `json:"task"`
	Index    int      `json:"index"`
	State    string   `json:"state"`
	Progress int      `json:"progress"`
	Data     struct {
		Status string `json:"status"`
	} `json:"data,omitempty"`
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func buildTaskEvent(boshEvent boshEventData) bosh.TaskEvent {
	event := bosh.TaskEvent{
		Time:     time.Unix(boshEvent.Time, 0),
		Stage:    boshEvent.Stage,
		Tags:     boshEvent.Tags,
		Total:    boshEvent.Total,
		Task:     boshEvent.Task,
		Index:    boshEvent.Index,
		State:    boshEvent.State,
		Progress: boshEvent.Progress,
	}

	if boshEvent.Data.Status != "" {
		event.Data = map[string]interface{}{"status": boshEvent.Data.Status}
	}

	if boshEvent.Error.Message != "" {
		event.Error = &bosh.TaskEventError{
			Code:    boshEvent.Error.Code,
			Message: boshEvent.Error.Message,
		}
	}

	return event
}

// extractServiceType attempts to extract service type from deployment name.
func extractServiceType(deploymentName string) string {
	if strings.HasPrefix(deploymentName, serviceTypeRedis+"-") {
		return serviceTypeRedis
	}

	if strings.HasPrefix(deploymentName, serviceTypeRabbitMQ+"-") {
		return serviceTypeRabbitMQ
	}

	if strings.HasPrefix(deploymentName, "postgresql-") {
		return "postgresql"
	}

	parts := strings.Split(deploymentName, "-")
	if len(parts) > 0 {
		return parts[0]
	}

	return "unknown"
}
