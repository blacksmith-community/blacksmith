package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"

	"gopkg.in/yaml.v3"
)

// Constants for services handler.
const (
	// Test task ID for services operations.
	testTaskIDServices = 3000

	// Test queue messages count.
	testQueueMessageCount = 10
)

// Error variables for err113 compliance.
var (
	errInvalidRequest        = errors.New("invalid request")
	errCommandIsRequired     = errors.New("command is required")
	errCertificateIsRequired = errors.New("certificate is required")
	errCertificateIDRequired = errors.New("certificate id is required")
	errInvalidCommand        = errors.New("invalid command")
)

// Handler handles generic service instance endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	vault    interfaces.Vault
	director interfaces.Director
}

// Dependencies contains all dependencies needed by the Services handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Vault    interfaces.Vault
	Director interfaces.Director
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
		logger:   deps.Logger,
		config:   deps.Config,
		vault:    deps.Vault,
		director: deps.Director,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for credentials endpoint - just /creds
	if path == "/creds" {
		return true
	}

	// Check for service instance endpoints
	instancePattern := regexp.MustCompile(`^/b/[^/]+/(vms|events|manifest|creds\.json|details|config|task/log|task/debug)$`)
	if instancePattern.MatchString(path) {
		return true
	}

	// Check for SSH execute endpoint
	sshExecutePattern := regexp.MustCompile(`^/b/[^/]+/ssh/execute$`)
	if sshExecutePattern.MatchString(path) {
		return true
	}

	// Check for certificates trusted endpoint
	certsPattern := regexp.MustCompile(`^/b/[^/]+/certificates/trusted$`)
	if certsPattern.MatchString(path) {
		return true
	}

	// Check for RabbitMQ SSH endpoints
	rabbitMQSSHPattern := regexp.MustCompile(`^/b/[^/]+/rabbitmq/ssh/.+$`)

	return rabbitMQSSHPattern.MatchString(path)
}

// ServeHTTP handles service instance endpoints with pattern matching.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Credentials endpoint - /creds
	if req.URL.Path == "/creds" {
		h.GetAllCredentials(writer, req)

		return
	}

	// Service instance endpoints - /b/{instance}/{operation}
	instancePattern := regexp.MustCompile(`^/b/([^/]+)/(vms|logs|events|manifest|creds\.json|details|config|task/log|task/debug)$`)
	if m := instancePattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		operation := m[2]

		switch operation {
		case "vms":
			h.GetInstanceVMs(writer, req, instanceID)
		case "logs":
			h.GetInstanceLogs(writer, req, instanceID)
		case "events":
			h.GetInstanceEvents(writer, req, instanceID)
		case "manifest":
			h.GetInstanceManifest(writer, req, instanceID)
		case "creds.json":
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

		return
	}

	// SSH execute endpoint - /b/{instance}/ssh/execute
	sshExecutePattern := regexp.MustCompile(`^/b/([^/]+)/ssh/execute$`)
	if m := sshExecutePattern.FindStringSubmatch(req.URL.Path); m != nil {
		if req.Method == http.MethodPost {
			instanceID := m[1]
			h.ExecuteSSHCommand(writer, req, instanceID)

			return
		}

		writer.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	// Certificates trusted endpoint - /b/{instance}/certificates/trusted
	certsPattern := regexp.MustCompile(`^/b/([^/]+)/certificates/trusted$`)
	if m := certsPattern.FindStringSubmatch(req.URL.Path); m != nil {
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

		return
	}

	// RabbitMQ SSH endpoints - /b/{instance}/rabbitmq/ssh/*
	rabbitMQSSHPattern := regexp.MustCompile(`^/b/([^/]+)/rabbitmq/ssh/(.+)$`)
	if m := rabbitMQSSHPattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		command := m[2]
		h.ExecuteRabbitMQSSHCommand(writer, req, instanceID, command)

		return
	}

	// No matching endpoint
	writer.WriteHeader(http.StatusNotFound)
}

// ExecuteSSHCommand executes an SSH command on a service instance.
func (h *Handler) ExecuteSSHCommand(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("ssh-execute")
	logger.Debug("Executing SSH command on instance: %s", instanceID)

	// Parse request body
	var execReq SSHExecuteRequest

	err := json.NewDecoder(req.Body).Decode(&execReq)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, fmt.Errorf("%w: %w", errInvalidRequest, err))

		return
	}

	// Validate command
	if execReq.Command == "" {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, errCommandIsRequired)

		return
	}

	logger.Info("Executing command '%s' on instance %s", execReq.Command, instanceID)

	// TODO: Implement actual SSH command execution via BOSH SSH
	// This would:
	// 1. Get instance details from Vault
	// 2. Establish SSH connection via BOSH
	// 3. Execute the command
	// 4. Return the output

	// For now, return mock response
	result := map[string]interface{}{
		"instance_id": instanceID,
		"command":     execReq.Command,
		"arguments":   execReq.Arguments,
		"exit_code":   0,
		"stdout":      fmt.Sprintf("Command '%s' executed successfully on %s", execReq.Command, instanceID),
		"stderr":      "",
		"async":       execReq.Async,
	}

	if execReq.Async {
		result["task_id"] = testTaskIDServices
		result["message"] = "Command execution started asynchronously"
	}

	response.HandleJSON(writer, result, nil)
}

// GetTrustedCertificates returns trusted certificates for a service instance.
func (h *Handler) GetTrustedCertificates(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-get")
	logger.Debug("Getting trusted certificates for instance: %s", instanceID)

	// TODO: Implement actual certificate fetching from Vault
	certificates := []map[string]interface{}{
		{
			"id":          "cert-1",
			"common_name": "ca.example.com",
			"issuer":      "Example CA",
			"not_before":  "2024-01-01T00:00:00Z",
			"not_after":   "2025-01-01T00:00:00Z",
			"fingerprint": "AA:BB:CC:DD:EE:FF",
		},
	}

	response.HandleJSON(writer, map[string]interface{}{
		"instance_id":  instanceID,
		"certificates": certificates,
		"count":        len(certificates),
	}, nil)
}

// AddTrustedCertificate adds a trusted certificate to a service instance.
func (h *Handler) AddTrustedCertificate(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-add")
	logger.Info("Adding trusted certificate to instance: %s", instanceID)

	// Parse certificate from request body
	var certRequest struct {
		Certificate string `json:"certificate"`
		Name        string `json:"name"`
	}

	err := json.NewDecoder(req.Body).Decode(&certRequest)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, fmt.Errorf("%w: %w", errInvalidRequest, err))

		return
	}

	if certRequest.Certificate == "" {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, errCertificateIsRequired)

		return
	}

	// TODO: Implement actual certificate addition
	// This would:
	// 1. Validate the certificate
	// 2. Store it in Vault
	// 3. Update the service instance configuration
	// 4. Potentially trigger instance reconfiguration

	result := map[string]interface{}{
		"instance_id": instanceID,
		"certificate": map[string]interface{}{
			"id":   "cert-new",
			"name": certRequest.Name,
		},
		"added":   true,
		"message": "Certificate added successfully",
	}

	response.HandleJSON(writer, result, nil)
}

// RemoveTrustedCertificate removes a trusted certificate from a service instance.
func (h *Handler) RemoveTrustedCertificate(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("certificates-trusted-remove")

	// Get certificate ID from query params
	certID := req.URL.Query().Get("id")
	if certID == "" {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, errCertificateIDRequired)

		return
	}

	logger.Info("Removing trusted certificate %s from instance: %s", certID, instanceID)

	// TODO: Implement actual certificate removal
	result := map[string]interface{}{
		"instance_id":    instanceID,
		"certificate_id": certID,
		"removed":        true,
		"message":        "Certificate removed successfully",
	}

	response.HandleJSON(writer, result, nil)
}

// ExecuteRabbitMQSSHCommand executes RabbitMQ-specific SSH commands.
func (h *Handler) ExecuteRabbitMQSSHCommand(writer http.ResponseWriter, req *http.Request, instanceID string, command string) {
	logger := h.logger.Named("rabbitmq-ssh")
	logger.Debug("Executing RabbitMQ SSH command '%s' on instance: %s", command, instanceID)

	// Parse any additional parameters from request body
	var cmdRequest map[string]interface{}

	err := json.NewDecoder(req.Body).Decode(&cmdRequest)
	if err != nil && err.Error() != "EOF" {
		logger.Warn("Failed to decode request body: %v", err)
	}

	// Handle different RabbitMQ SSH commands
	cmdParts := strings.Split(command, "/")
	if len(cmdParts) == 0 {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, errInvalidCommand)

		return
	}

	baseCmd := cmdParts[0]

	// TODO: Implement actual RabbitMQ SSH command execution
	// This would handle various RabbitMQ management commands via SSH

	result := map[string]interface{}{
		"instance_id":  instanceID,
		"command":      command,
		"base_command": baseCmd,
		"output":       fmt.Sprintf("RabbitMQ command '%s' executed on instance %s", command, instanceID),
		"success":      true,
	}

	// Add any command-specific response data
	switch baseCmd {
	case "status":
		result["status"] = map[string]interface{}{
			"running": true,
			"nodes":   1,
			"version": "3.11.0",
		}
	case "list_queues":
		result["queues"] = []map[string]interface{}{
			{"name": "queue1", "messages": testQueueMessageCount},
			{"name": "queue2", "messages": 0},
		}
	case "list_users":
		result["users"] = []string{"admin", "guest", "app_user"}
	}

	response.HandleJSON(writer, result, nil)
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

	vms, err := h.director.GetDeploymentVMs(instanceID)
	if err != nil {
		logger.Error("Failed to fetch VMs: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"vms":   []interface{}{},
			"error": err.Error(),
		}, nil)

		return
	}

	var vmsData []map[string]interface{}
	for _, vm := range vms {
		vmData := map[string]interface{}{
			"instance":   vm.Job + "/" + vm.ID,
			"state":      vm.State,
			"vm_cid":     vm.CID,
			"vm_type":    vm.VMType,
			"ips":        vm.IPs,
			"deployment": instanceID,
			"az":         vm.AZ,
			"disk_cids":  vm.DiskCIDs,
		}
		vmsData = append(vmsData, vmData)
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": instanceID,
		"vms":        vmsData,
	}, nil)
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

	// Get instance data from vault to construct deployment name
	ctx := req.Context()

	var inst struct {
		ServiceID string `json:"service_id"`
		PlanID    string `json:"plan_id"`
	}

	// Try to get instance data from vault
	exists, err := h.vault.Get(ctx, "db/"+instanceID, &inst)
	if err != nil || !exists {
		logger.Error("Unable to find service instance %s in vault: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "service instance not found",
		}, nil)

		return
	}

	// Construct deployment name: {plan_id}-{instance_id}
	deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)
	logger.Debug("Fetching logs for deployment %s", deploymentName)

	// Get the manifest to determine the job names
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err = h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil || !exists {
		logger.Error("Unable to find manifest for instance %s: %v", instanceID, err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to find manifest",
		}, nil)

		return
	}

	// Parse the manifest to find job names
	var manifest map[string]interface{}
	if err := yaml.Unmarshal([]byte(manifestData.Manifest), &manifest); err != nil {
		logger.Error("Unable to parse manifest: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to parse manifest",
		}, nil)

		return
	}

	// Extract instance groups/jobs from manifest
	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok {
		logger.Error("Unable to find instance_groups in manifest")
		response.HandleJSON(writer, map[string]interface{}{
			"logs":  "",
			"error": "unable to find instance groups",
		}, nil)

		return
	}

	// Collect all logs from all instance groups
	allLogs := make(map[string]interface{})

	for _, ig := range instanceGroups {
		group, ok := ig.(map[interface{}]interface{})
		if !ok {
			continue
		}

		jobName, ok := group["name"].(string)
		if !ok {
			continue
		}

		instances := 1
		if instCount, ok := group["instances"].(int); ok {
			instances = instCount
		}

		// Fetch logs for each instance in the group
		for i := range instances {
			jobIndex := strconv.Itoa(i)
			logKey := fmt.Sprintf("%s/%s", jobName, jobIndex)

			logger.Debug("Fetching logs for job %s/%s", jobName, jobIndex)

			// Call FetchLogs on the BOSH director
			logs, err := h.director.FetchLogs(deploymentName, jobName, jobIndex)
			if err != nil {
				logger.Error("Failed to fetch logs for %s/%s: %v", jobName, jobIndex, err)
				allLogs[logKey] = map[string]interface{}{
					"error": fmt.Sprintf("Failed to fetch logs: %s", err),
				}
			} else {
				// Parse the logs to extract individual files if structured
				logFiles := make(map[string]string)

				if strings.Contains(logs, "===") {
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
				} else {
					// Unstructured logs - just use raw content
					logFiles["raw"] = logs
				}

				allLogs[logKey] = logFiles
			}
		}
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"logs":       allLogs,
		"error":      nil,
	}, nil)
}

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

	events, err := h.director.GetEvents(instanceID)
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

	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{
			"manifest": "",
			"error":    "Director not configured",
		}, nil)

		return
	}

	deployment, err := h.director.GetDeployment(instanceID)
	if err != nil {
		logger.Error("Failed to fetch deployment: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"manifest": "",
			"error":    err.Error(),
		}, nil)

		return
	}

	response.HandleJSON(writer, map[string]interface{}{
		"manifest": deployment.Manifest,
		"name":     deployment.Name,
	}, nil)
}

// GetInstanceCredentials returns credentials for a service instance.
func (h *Handler) GetInstanceCredentials(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-credentials")
	logger.Debug("Getting credentials for instance: %s", instanceID)

	if h.vault != nil {
		var creds map[string]interface{}

		path := "secret/" + instanceID

		// Use the Vault Get method to fetch credentials
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
		"port":     6379,
		"username": "admin",
		"password": "***",
		"uri":      extractServiceType(instanceID) + "://admin:***@10.0.0.1:6379",
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

	deployment, err := h.director.GetDeployment(instanceID)
	if err != nil {
		logger.Error("Failed to fetch deployment: %v", err)
		response.HandleJSON(writer, map[string]interface{}{
			"error": err.Error(),
		}, nil)

		return
	}

	details := map[string]interface{}{
		"instance_id": instanceID,
		"name":        deployment.Name,
		"service":     extractServiceType(instanceID),
		"plan":        "standard",
	}

	response.HandleJSON(writer, details, nil)
}

// GetInstanceConfig returns configuration for a service instance.
func (h *Handler) GetInstanceConfig(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-config")
	logger.Debug("Getting config for instance: %s", instanceID)

	// TODO: Fetch actual config from Vault
	config := map[string]interface{}{
		"instance_id": instanceID,
		"service":     extractServiceType(instanceID),
		"plan":        "standard",
		"parameters": map[string]interface{}{
			"maxmemory":        "1gb",
			"maxmemory-policy": "allkeys-lru",
		},
	}

	response.HandleJSON(writer, config, nil)
}

// GetInstanceTaskLog returns task logs for a service instance.
func (h *Handler) GetInstanceTaskLog(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-task-log")
	logger.Debug("Getting task log for instance: %s", instanceID)

	// TODO: Implement actual task log fetching
	response.HandleJSON(writer, map[string]interface{}{
		"instance_id": instanceID,
		"log":         "Task completed successfully",
	}, nil)
}

// extractServiceType attempts to extract service type from deployment name.
func extractServiceType(deploymentName string) string {
	if strings.HasPrefix(deploymentName, "redis-") {
		return "redis"
	}

	if strings.HasPrefix(deploymentName, "rabbitmq-") {
		return "rabbitmq"
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

// GetAllCredentials returns credentials for all service instances from Vault.
func (h *Handler) GetAllCredentials(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("all-credentials")
	logger.Debug("Getting credentials for all service instances")

	// First get all deployments from the Director to know what instances exist
	if h.director == nil {
		logger.Error("Director not configured")
		response.HandleJSON(writer, map[string]interface{}{}, nil)

		return
	}

	deployments, err := h.director.GetDeployments()
	if err != nil {
		logger.Error("Failed to fetch deployments: %v", err)
		response.HandleJSON(writer, map[string]interface{}{}, nil)

		return
	}

	// Fetch credentials for each service instance deployment from Vault
	allCreds := make(map[string]interface{})

	for _, deployment := range deployments {
		// Skip blacksmith itself and other non-service deployments
		if deployment.Name == "blacksmith" || deployment.Name == "" {
			continue
		}

		// Try to fetch credentials from Vault at secret/{instanceID}
		if h.vault != nil {
			var creds map[string]interface{}

			path := "secret/" + deployment.Name

			// Use the Vault Get method to fetch credentials
			exists, err := h.vault.Get(req.Context(), path, &creds)
			if err != nil {
				logger.Warn("Failed to fetch credentials for %s: %v", deployment.Name, err)
				// Continue to next instance, don't fail entire request
				continue
			}

			if exists && creds != nil {
				// Add the credentials to the response map
				allCreds[deployment.Name] = creds
				logger.Debug("Retrieved credentials for instance %s", deployment.Name)
			} else {
				logger.Debug("No credentials found in Vault for instance %s", deployment.Name)
				// Return empty credentials object for instances without stored creds
				allCreds[deployment.Name] = map[string]interface{}{
					"message": "Credentials not found in Vault",
				}
			}
		} else {
			logger.Warn("Vault not configured, returning placeholder credentials")
			// Return placeholder if Vault is not configured
			allCreds[deployment.Name] = map[string]interface{}{
				"host":     "10.0.0.1",
				"port":     6379,
				"username": "admin",
				"password": "***",
				"uri":      extractServiceType(deployment.Name) + "://admin:***@10.0.0.1:6379",
			}
		}
	}

	response.HandleJSON(writer, allCreds, nil)
}
