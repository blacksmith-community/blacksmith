package blacksmith

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Static errors for err113 compliance.
var (
	ErrCredentialFetchingNotYetImplemented = errors.New("credential fetching not yet implemented")
	ErrMethodNotAllowed                    = errors.New("method not allowed")
	ErrInvalidRequestBody                  = errors.New("invalid request body")
	ErrCommandNotAllowed                   = errors.New("command not allowed")
)

// Handler handles Blacksmith-specific management endpoints.
type Handler struct {
	logger interfaces.Logger
	config interfaces.Config
	vault  interfaces.Vault
}

// Dependencies contains all dependencies needed by the Blacksmith handler.
type Dependencies struct {
	Logger interfaces.Logger
	Config interfaces.Config
	Vault  interfaces.Vault
}

// NewHandler creates a new Blacksmith management handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger: deps.Logger,
		config: deps.Config,
		vault:  deps.Vault,
	}
}

// GetLogs returns Blacksmith deployment logs.
func (h *Handler) GetLogs(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-logs")
	logger.Debug("Fetching Blacksmith logs")

	// Parse query parameters
	lines := req.URL.Query().Get("lines")
	if lines == "" {
		lines = "100"
	}

	// TODO: Implement actual log fetching via BOSH
	// For now, return placeholder response
	logsData := map[string]interface{}{
		"logs":  "Blacksmith logs would appear here",
		"lines": lines,
		"error": nil,
	}

	response.HandleJSON(responseWriter, logsData, nil)
}

// GetCredentials returns Blacksmith deployment credentials.
func (h *Handler) GetCredentials(w http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-credentials")
	logger.Debug("Fetching Blacksmith credentials")

	// TODO: Implement actual credential fetching from Vault
	// For now, return error indicating not implemented
	w.WriteHeader(http.StatusNotImplemented)
	response.HandleJSON(w, nil, ErrCredentialFetchingNotYetImplemented)
}

// GetConfig returns Blacksmith configuration.
func (h *Handler) GetConfig(w http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-config")
	logger.Debug("Fetching Blacksmith configuration")

	// TODO: Get actual configuration
	// For now, return basic config structure
	config := map[string]interface{}{
		"environment": "development",
		"version":     "0.0.0",
		"features": map[string]bool{
			"ssh_ui_terminal": true,
			"websocket":       true,
		},
		"services": []string{
			"redis",
			"rabbitmq",
			"postgresql",
		},
	}

	response.HandleJSON(w, config, nil)
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

	if err := json.NewDecoder(req.Body).Decode(&cleanupRequest); err != nil {
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

// ExecuteCommand executes a command in the Blacksmith deployment context.
func (h *Handler) ExecuteCommand(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-execute")

	// Only allow POST method
	if req.Method != http.MethodPost {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	// Parse command from request
	var cmdRequest struct {
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}

	if err := json.NewDecoder(req.Body).Decode(&cmdRequest); err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("%w: %w", ErrInvalidRequestBody, err))

		return
	}

	// Validate command (whitelist allowed commands)
	allowedCommands := []string{"status", "version", "health"}
	allowed := false

	for _, cmd := range allowedCommands {
		if cmdRequest.Command == cmd {
			allowed = true

			break
		}
	}

	if !allowed {
		responseWriter.WriteHeader(http.StatusForbidden)
		response.HandleJSON(responseWriter, nil, fmt.Errorf("%w: %s", ErrCommandNotAllowed, cmdRequest.Command))

		return
	}

	logger.Info("Executing command: %s %s", cmdRequest.Command, strings.Join(cmdRequest.Arguments, " "))

	// Execute the command
	cmd := exec.Command(cmdRequest.Command, cmdRequest.Arguments...)
	output, err := cmd.CombinedOutput()

	result := map[string]interface{}{
		"command": cmdRequest.Command,
		"args":    cmdRequest.Arguments,
		"output":  string(output),
		"success": err == nil,
	}
	if err != nil {
		result["error"] = err.Error()
	}

	response.HandleJSON(responseWriter, result, nil)
}
