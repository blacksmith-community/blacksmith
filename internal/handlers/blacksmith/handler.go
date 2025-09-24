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

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Static errors for err113 compliance.
var (
	ErrMethodNotAllowed   = errors.New("method not allowed")
	ErrInvalidRequestBody = errors.New("invalid request body")
	ErrCommandNotAllowed  = errors.New("command not allowed")
)

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

// GetLogs returns Blacksmith deployment logs.
func (h *Handler) GetLogs(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-logs")
	logger.Debug("Fetching Blacksmith logs")

	// Check if a specific log file is requested
	logFile := req.URL.Query().Get("file")

	var logs string

	if logFile != "" {
		// Validate the requested log file path for security
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

		// Check if the requested file is in the allowed list
		isAllowed := false

		for _, allowedFile := range allowedLogFiles {
			if logFile == allowedFile {
				isAllowed = true

				break
			}
		}

		if !isAllowed {
			logger.Error("Unauthorized log file access attempt: %s", logFile)

			logsData := map[string]interface{}{
				"logs":  "",
				"error": "unauthorized log file access",
			}
			response.HandleJSON(responseWriter, logsData, nil)

			return
		}

		logger.Debug("Fetching logs from file: %s", logFile)

		// Read the log file
		// #nosec G304 - logFile is validated against whitelist above
		content, err := os.ReadFile(logFile)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Debug("Log file does not exist: %s", logFile)

				logs = "" // Empty logs if file doesn't exist
			} else {
				logger.Error("Failed to read log file %s: %s", logFile, err)
				logsData := map[string]interface{}{
					"logs":  "",
					"error": fmt.Sprintf("failed to read log file: %s", err),
				}
				response.HandleJSON(responseWriter, logsData, nil)

				return
			}
		} else {
			logs = string(content)
		}
	} else {
		// Default behavior: read the default log file
		defaultLogFile := "/var/vcap/sys/log/blacksmith/blacksmith.stdout.log"
		// #nosec G304 - defaultLogFile is a hardcoded path
		content, err := os.ReadFile(defaultLogFile)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Debug("Default log file does not exist: %s", defaultLogFile)

				logs = "" // Empty logs if file doesn't exist
			} else {
				logger.Error("Failed to read default log file: %s", err)

				logs = "" // Return empty logs on error
			}
		} else {
			logs = string(content)
		}
	}

	// Return as JSON with the logs
	logsData := map[string]interface{}{
		"logs":  logs,
		"error": nil,
	}

	response.HandleJSON(responseWriter, logsData, nil)
}

// GetVMs retrieves the VMs for the blacksmith deployment.
func (h *Handler) GetVMs(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-vms")
	logger.Debug("Fetching Blacksmith VMs")

	// Check if director is available
	if h.director == nil {
		logger.Error("Director not configured")

		vmsData := map[string]interface{}{
			"vms":   []interface{}{},
			"error": "Director not configured",
		}
		response.HandleJSON(responseWriter, vmsData, nil)

		return
	}

	// Fetch VMs for the blacksmith deployment
	// Use the environment name as the deployment name
	environment := h.config.GetEnvironment()

	deploymentName := environment
	if deploymentName == "" {
		deploymentName = "blacksmith"
	}

	logger.Debug("Fetching VMs for deployment: %s", deploymentName)

	vms, err := h.director.GetDeploymentVMs(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch VMs: %v", err)
		vmsData := map[string]interface{}{
			"vms":   []interface{}{},
			"error": err.Error(),
		}
		response.HandleJSON(responseWriter, vmsData, nil)

		return
	}

	// Format VMs data
	var vmsData []map[string]interface{}
	for _, vm := range vms {
		vmData := map[string]interface{}{
			"instance":   vm.Job + "/" + vm.ID,
			"state":      vm.State,
			"vm_cid":     vm.CID,
			"vm_type":    vm.VMType,
			"ips":        vm.IPs,
			"deployment": deploymentName,
			"az":         vm.AZ,
			"disk_cids":  vm.DiskCIDs,
		}
		vmsData = append(vmsData, vmData)
	}

	response.HandleJSON(responseWriter, map[string]interface{}{
		"deployment": deploymentName,
		"vms":        vmsData,
	}, nil)
}

// GetCredentials returns Blacksmith deployment credentials.
func (h *Handler) GetCredentials(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-credentials")
	logger.Debug("Fetching Blacksmith credentials")

	// Build credential summary from configuration.
	credentials := struct {
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
	}{}

	credentials.Environment = h.config.GetEnvironment()

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

	response.HandleJSON(responseWriter, credentials, nil)
}

// GetConfig returns Blacksmith configuration.
func (h *Handler) GetConfig(responseWriter http.ResponseWriter, req *http.Request) {
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

	response.HandleJSON(responseWriter, config, nil)
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

// isCommandAllowed checks if the command is in the allowed list.

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
	// Use the environment name as the deployment name
	environment := h.config.GetEnvironment()

	deploymentName := environment
	if deploymentName == "" {
		deploymentName = "blacksmith"
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
			"manifest": "",
			"error":    "Director not configured",
		}
		response.HandleJSON(responseWriter, manifestData, nil)

		return
	}

	// Fetch deployment details including manifest
	// Use the environment name as the deployment name
	environment := h.config.GetEnvironment()

	deploymentName := environment
	if deploymentName == "" {
		deploymentName = "blacksmith"
	}

	logger.Debug("Fetching manifest for deployment: %s", deploymentName)

	deployment, err := h.director.GetDeployment(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch deployment: %v", err)
		manifestData := map[string]interface{}{
			"manifest": "",
			"error":    err.Error(),
		}
		response.HandleJSON(responseWriter, manifestData, nil)

		return
	}

	response.HandleJSON(responseWriter, map[string]interface{}{
		"manifest": deployment.Manifest,
		"name":     deployment.Name,
	}, nil)
}

// extractLogFile extracts the requested log file content from the fetched logs.
// The logs are typically in a tar archive format containing multiple log files.
func (h *Handler) extractLogFile(logs string, logFilePath string) string {
	// For now, return the full logs content
	// TODO: Parse tar archive and extract specific log file
	return logs
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
