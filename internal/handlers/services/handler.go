package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
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
	logger interfaces.Logger
	config interfaces.Config
	vault  interfaces.Vault
}

// Dependencies contains all dependencies needed by the Services handler.
type Dependencies struct {
	Logger interfaces.Logger
	Config interfaces.Config
	Vault  interfaces.Vault
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
		logger: deps.Logger,
		config: deps.Config,
		vault:  deps.Vault,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
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
