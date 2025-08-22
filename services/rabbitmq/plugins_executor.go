package rabbitmq

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	pluginListRegex   = regexp.MustCompile(`^\s*\[[E*e ]{1,2}\]`)
	pluginStatusRegex = regexp.MustCompile(`^\[([E*e ])\]\s+(\S+)`)
)

// PluginsExecutorService provides rabbitmq-plugins command execution functionality
type PluginsExecutorService struct {
	sshService      *SSHService
	metadataService *PluginsMetadataService
	logger          Logger
}

// PluginsExecutionContext provides context for command execution
type PluginsExecutionContext struct {
	Context    context.Context
	InstanceID string
	User       string
	ClientIP   string
}

// PluginsStreamingExecutionResult provides streaming execution results
type PluginsStreamingExecutionResult struct {
	ExecutionID string                 `json:"execution_id"`
	InstanceID  string                 `json:"instance_id"`
	Category    string                 `json:"category"`
	Command     string                 `json:"command"`
	Arguments   []string               `json:"arguments"`
	Status      ExecutionStatus        `json:"status"`
	Output      chan string            `json:"-"`
	Metadata    map[string]interface{} `json:"metadata"`
	StartTime   time.Time              `json:"start_time"`
	EndTime     *time.Time             `json:"end_time,omitempty"`
	Error       string                 `json:"error,omitempty"`
	ExitCode    int                    `json:"exit_code"`
	Success     bool                   `json:"success"`
}

// NewPluginsExecutorService creates a new plugins command executor service
func NewPluginsExecutorService(sshService *SSHService, metadataService *PluginsMetadataService, logger Logger) *PluginsExecutorService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &PluginsExecutorService{
		sshService:      sshService,
		metadataService: metadataService,
		logger:          logger,
	}
}

// ExecuteCommand executes a rabbitmq-plugins command with streaming output
func (e *PluginsExecutorService) ExecuteCommand(ctx PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*PluginsStreamingExecutionResult, error) {
	e.logger.Info("Executing rabbitmq-plugins command: %s.%s with args %v", category, command, arguments)

	// Validate command
	cmd, err := e.metadataService.GetCommand(command)
	if err != nil {
		return nil, fmt.Errorf("invalid command: %v", err)
	}

	// Validate arguments
	if err := e.metadataService.ValidateCommand(command, arguments); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}

	// Create execution ID
	executionID := e.generateExecutionID(ctx.InstanceID, category, command)

	// Create streaming result
	result := &PluginsStreamingExecutionResult{
		ExecutionID: executionID,
		InstanceID:  ctx.InstanceID,
		Category:    category,
		Command:     command,
		Arguments:   arguments,
		Status:      StatusPending,
		Output:      make(chan string, 100), // Buffered channel for output
		Metadata: map[string]interface{}{
			"user":      ctx.User,
			"client_ip": ctx.ClientIP,
			"dangerous": cmd.Dangerous,
		},
		StartTime: time.Now(),
	}

	// Build the rabbitmq-plugins command
	fullCommand, err := e.buildRabbitMQPluginsCommand(command, arguments)
	if err != nil {
		result.Status = StatusFailed
		result.Error = fmt.Sprintf("Failed to build command: %v", err)
		return result, err
	}

	// Execute command asynchronously
	go e.executeCommandAsync(ctx, result, deployment, instance, index, fullCommand, cmd.Timeout)

	return result, nil
}

// ExecuteCommandSync executes a rabbitmq-plugins command synchronously
func (e *PluginsExecutorService) ExecuteCommandSync(ctx PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (string, int, error) {
	e.logger.Info("Executing rabbitmq-plugins command synchronously: %s.%s with args %v", category, command, arguments)

	// Validate command
	_, err := e.metadataService.GetCommand(command)
	if err != nil {
		return "", 1, fmt.Errorf("invalid command: %v", err)
	}

	// Validate arguments
	if err := e.metadataService.ValidateCommand(command, arguments); err != nil {
		return "", 1, fmt.Errorf("invalid arguments: %v", err)
	}

	// Build the rabbitmq-plugins command
	fullCommand, err := e.buildRabbitMQPluginsCommand(command, arguments)
	if err != nil {
		return "", 1, fmt.Errorf("failed to build command: %v", err)
	}

	// Create RabbitMQCommand for SSH service
	rabbitCmd := RabbitMQCommand{
		Name:        fullCommand,
		Args:        arguments,
		Description: fmt.Sprintf("rabbitmq-plugins %s", command),
		Timeout:     60,
	}

	// Execute command
	sshResult, err := e.sshService.ExecuteCommand(deployment, instance, index, rabbitCmd)
	if err != nil {
		e.logger.Error("Failed to execute rabbitmq-plugins command: %v", err)
		return sshResult.Output, sshResult.ExitCode, err
	}

	// Process output based on command type
	processedOutput := e.processCommandOutput(command, sshResult.Output)

	e.logger.Info("Rabbitmq-plugins command completed with exit code: %d", sshResult.ExitCode)
	return processedOutput, sshResult.ExitCode, nil
}

// buildRabbitMQPluginsCommand constructs the full rabbitmq-plugins command
func (e *PluginsExecutorService) buildRabbitMQPluginsCommand(command string, arguments []string) (string, error) {
	// Sanitize command and arguments
	sanitizedCommand := e.sanitizeInput(command)
	var sanitizedArgs []string
	for _, arg := range arguments {
		sanitized := e.sanitizeInput(arg)
		if sanitized != "" {
			sanitizedArgs = append(sanitizedArgs, sanitized)
		}
	}

	// Source RabbitMQ environment and build command
	envSource := ". /var/vcap/jobs/rabbitmq/env"

	// Build command parts
	cmdParts := []string{"rabbitmq-plugins", sanitizedCommand}
	cmdParts = append(cmdParts, sanitizedArgs...)

	// Join and escape properly
	fullCommand := fmt.Sprintf("%s && %s", envSource, strings.Join(cmdParts, " "))

	e.logger.Debug("Built rabbitmq-plugins command: %s", fullCommand)
	return fullCommand, nil
}

// processCommandOutput processes command output based on command type
func (e *PluginsExecutorService) processCommandOutput(command, output string) string {
	switch command {
	case "list":
		return e.processListOutput(output)
	case "directories":
		return e.processDirectoriesOutput(output)
	case "is_enabled":
		return e.processIsEnabledOutput(output)
	default:
		return output
	}
}

// processListOutput processes the output of the 'list' command
func (e *PluginsExecutorService) processListOutput(output string) string {
	lines := strings.Split(output, "\n")
	var processedLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing plugins") {
			continue
		}

		// Parse plugin list format: [Status] PluginName Version Description
		if pluginListRegex.MatchString(line) {
			processedLines = append(processedLines, line)
		}
	}

	if len(processedLines) == 0 {
		return "No plugins found."
	}

	return strings.Join(processedLines, "\n")
}

// processDirectoriesOutput processes the output of the 'directories' command
func (e *PluginsExecutorService) processDirectoriesOutput(output string) string {
	lines := strings.Split(output, "\n")
	var processedLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format directory paths nicely
		if strings.Contains(line, "Plugin directory:") || strings.Contains(line, "Enabled plugins file:") {
			processedLines = append(processedLines, line)
		}
	}

	return strings.Join(processedLines, "\n")
}

// processIsEnabledOutput processes the output of the 'is_enabled' command
func (e *PluginsExecutorService) processIsEnabledOutput(output string) string {
	// is_enabled typically doesn't output text, just exit codes
	// But we can add helpful text based on the result
	if strings.TrimSpace(output) == "" {
		return "Plugin status check completed (see exit code for result)"
	}
	return output
}

// executeCommandAsync executes a command asynchronously with streaming output
func (e *PluginsExecutorService) executeCommandAsync(ctx PluginsExecutionContext, result *PluginsStreamingExecutionResult, deployment, instance string, index int, fullCommand string, timeout int) {
	defer close(result.Output)

	result.Status = StatusRunning
	result.Output <- "Starting rabbitmq-plugins command execution...\n"
	result.Output <- fmt.Sprintf("Instance: %s\n", instance)
	result.Output <- fmt.Sprintf("Command: %s\n", result.Command)
	if len(result.Arguments) > 0 {
		result.Output <- fmt.Sprintf("Arguments: %s\n", strings.Join(result.Arguments, " "))
	}
	result.Output <- "\n--- Command Output ---\n"

	// Create context with timeout
	timeoutDuration := time.Duration(timeout) * time.Second
	commandCtx, cancel := context.WithTimeout(ctx.Context, timeoutDuration)
	defer cancel()

	// Create RabbitMQCommand for SSH service
	rabbitCmd := RabbitMQCommand{
		Name:        fullCommand,
		Args:        result.Arguments,
		Description: fmt.Sprintf("rabbitmq-plugins %s", result.Command),
		Timeout:     timeout,
	}

	// Execute the command
	sshResult, err := e.sshService.ExecuteCommand(deployment, instance, index, rabbitCmd)

	// Process the output
	processedOutput := e.processCommandOutput(result.Command, sshResult.Output)

	// Send output line by line
	outputLines := strings.Split(processedOutput, "\n")
	for _, line := range outputLines {
		if line != "" {
			result.Output <- line + "\n"
		}
	}

	// Set final status
	endTime := time.Now()
	result.EndTime = &endTime

	if err != nil {
		if commandCtx.Err() == context.DeadlineExceeded {
			result.Status = StatusTimeout
			result.Error = fmt.Sprintf("Command timed out after %d seconds", timeout)
			result.Output <- fmt.Sprintf("\n❌ Command timed out after %d seconds\n", timeout)
		} else {
			result.Status = StatusFailed
			result.Error = err.Error()
			result.Output <- fmt.Sprintf("\n❌ Command failed: %s\n", err.Error())
		}
	} else {
		if sshResult.ExitCode == 0 {
			result.Status = StatusCompleted
			result.Success = true
			result.Output <- "\n✅ Command completed successfully\n"
		} else {
			result.Status = StatusFailed
			result.Error = fmt.Sprintf("Command exited with code %d", sshResult.ExitCode)
			result.Output <- fmt.Sprintf("\n❌ Command exited with code %d\n", sshResult.ExitCode)
		}
	}

	result.ExitCode = sshResult.ExitCode

	executionDuration := result.EndTime.Sub(result.StartTime)
	result.Output <- fmt.Sprintf("Execution time: %v\n", executionDuration)

	e.logger.Info("Rabbitmq-plugins command execution completed: %s (exit code: %d, duration: %v)",
		result.Command, result.ExitCode, executionDuration)
}

// sanitizeInput sanitizes user input to prevent command injection
func (e *PluginsExecutorService) sanitizeInput(input string) string {
	// Remove dangerous characters and sequences
	dangerous := []string{";", "&", "|", "`", "$", "(", ")", "<", ">", "\"", "'", "\\", "\n", "\r", "\t"}

	sanitized := input
	for _, char := range dangerous {
		sanitized = strings.ReplaceAll(sanitized, char, "")
	}

	// Trim whitespace
	sanitized = strings.TrimSpace(sanitized)

	// Only allow alphanumeric, dash, underscore, dot, and spaces
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-_.= ]`)
	sanitized = reg.ReplaceAllString(sanitized, "")

	return sanitized
}

// generateExecutionID generates a unique execution ID
func (e *PluginsExecutorService) generateExecutionID(instanceID, category, command string) string {
	timestamp := time.Now().UnixNano()
	return fmt.Sprintf("%s-%s-%s-%d", instanceID, category, command, timestamp)
}

// ValidatePluginOperation validates plugin operations for dangerous commands
func (e *PluginsExecutorService) ValidatePluginOperation(command string, arguments []string) (bool, string, error) {
	cmd, err := e.metadataService.GetCommand(command)
	if err != nil {
		return false, "", err
	}

	// Check for dangerous operations
	isDangerous := false
	warning := ""

	switch command {
	case "disable":
		for _, arg := range arguments {
			if arg == "--all" {
				isDangerous = true
				warning = "Disabling all plugins will remove all functionality from RabbitMQ. This operation cannot be easily undone."
			}
		}
	case "set":
		if len(arguments) == 0 {
			isDangerous = true
			warning = "Setting no plugins will disable all currently enabled plugins. This will remove all functionality from RabbitMQ."
		}
	case "enable":
		for _, arg := range arguments {
			if arg == "--all" {
				warning = "Enabling all plugins may consume significant system resources and could affect performance."
			}
		}
	}

	// Override with command's dangerous flag if set
	if cmd.Dangerous {
		isDangerous = true
		if warning == "" {
			warning = "This command may affect RabbitMQ functionality. Please review the operation before proceeding."
		}
	}

	return isDangerous, warning, nil
}

// GetPluginStatus parses plugin list output to extract status information
func (e *PluginsExecutorService) GetPluginStatus(listOutput string) map[string]string {
	status := make(map[string]string)
	lines := strings.Split(listOutput, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "[") {
			continue
		}

		// Parse format: [E*] plugin_name version description
		// [E*] = explicitly enabled, [e*] = implicitly enabled, [ ] = disabled
		matches := pluginStatusRegex.FindStringSubmatch(line)

		if len(matches) >= 3 {
			statusSymbol := matches[1]
			pluginName := matches[2]

			switch statusSymbol {
			case "E*":
				status[pluginName] = "enabled"
			case "e*":
				status[pluginName] = "implicitly_enabled"
			default:
				status[pluginName] = "disabled"
			}
		}
	}

	return status
}

// FormatPluginList formats plugin list output for better display
func (e *PluginsExecutorService) FormatPluginList(output string, verbose bool) string {
	lines := strings.Split(output, "\n")
	var formattedLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Listing plugins") {
			continue
		}

		if pluginListRegex.MatchString(line) {
			if verbose {
				formattedLines = append(formattedLines, line)
			} else {
				// Extract just plugin name and status for compact view
				matches := pluginStatusRegex.FindStringSubmatch(line)
				if len(matches) >= 3 {
					statusSymbol := matches[1]
					pluginName := matches[2]

					statusText := "disabled"
					switch statusSymbol {
					case "E*":
						statusText = "enabled"
					case "e*":
						statusText = "implicitly enabled"
					}

					formattedLines = append(formattedLines, fmt.Sprintf("%-30s %s", pluginName, statusText))
				}
			}
		}
	}

	if len(formattedLines) == 0 {
		return "No plugins found."
	}

	if !verbose {
		// Add header for compact view
		header := fmt.Sprintf("%-30s %s", "Plugin Name", "Status")
		separator := strings.Repeat("-", len(header))
		return header + "\n" + separator + "\n" + strings.Join(formattedLines, "\n")
	}

	return strings.Join(formattedLines, "\n")
}
