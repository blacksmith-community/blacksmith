package rabbitmq

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ExecutorService provides rabbitmqctl command execution functionality
type ExecutorService struct {
	sshService      *SSHService
	metadataService *MetadataService
	logger          Logger
}

// ExecutionContext provides context for command execution
type ExecutionContext struct {
	Context    context.Context
	InstanceID string
	User       string
	ClientIP   string
}

// StreamingExecutionResult provides streaming execution results
type StreamingExecutionResult struct {
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

// ExecutionStatus represents the current status of command execution
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
	StatusTimeout   ExecutionStatus = "timeout"
)

// NewExecutorService creates a new command executor service
func NewExecutorService(sshService *SSHService, metadataService *MetadataService, logger Logger) *ExecutorService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ExecutorService{
		sshService:      sshService,
		metadataService: metadataService,
		logger:          logger,
	}
}

// ExecuteCommand executes a rabbitmqctl command with streaming output
func (e *ExecutorService) ExecuteCommand(ctx ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*StreamingExecutionResult, error) {
	e.logger.Info("Executing rabbitmqctl command: %s.%s with args %v", category, command, arguments)

	// Validate command
	cmd, err := e.metadataService.GetCommand(category, command)
	if err != nil {
		return nil, fmt.Errorf("invalid command: %v", err)
	}

	// Validate arguments
	if err := e.metadataService.ValidateCommand(category, command, arguments); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}

	// Create execution ID
	executionID := e.generateExecutionID(ctx.InstanceID, category, command)

	// Create streaming result
	result := &StreamingExecutionResult{
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

	// Start execution in goroutine
	go e.executeAsync(ctx.Context, result, deployment, instance, index, *cmd)

	return result, nil
}

// ExecuteCommandSync executes a command synchronously and returns the complete result
func (e *ExecutorService) ExecuteCommandSync(ctx ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*RabbitMQCtlExecution, error) {
	e.logger.Info("Executing rabbitmqctl command synchronously: %s.%s with args %v", category, command, arguments)

	// Validate command
	cmd, err := e.metadataService.GetCommand(category, command)
	if err != nil {
		return nil, fmt.Errorf("invalid command: %v", err)
	}

	// Validate arguments
	if err := e.metadataService.ValidateCommand(category, command, arguments); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}

	// Create RabbitMQ command
	rabbitCmd := RabbitMQCommand{
		Name:        command,
		Args:        arguments,
		Description: cmd.Description,
		Timeout:     cmd.Timeout,
	}

	// Execute command
	startTime := time.Now()
	result, err := e.sshService.ExecuteCommand(deployment, instance, index, rabbitCmd)
	if err != nil {
		// BOSH SSH service returns errors for non-zero exit codes
		// Extract the detailed output from the error message
		errorMsg := err.Error()
		var extractedOutput string
		exitCode := 1

		// Parse error message to extract output and exit code
		if strings.Contains(errorMsg, "output:") {
			parts := strings.SplitN(errorMsg, "output:", 2)
			if len(parts) == 2 {
				extractedOutput = strings.TrimSpace(parts[1])
			}
		}

		// Extract exit code if present
		if strings.Contains(errorMsg, "status") {
			re := regexp.MustCompile(`status (\d+)`)
			if matches := re.FindStringSubmatch(errorMsg); len(matches) > 1 {
				if code, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
					exitCode = code
				}
			}
		}

		// Create execution record with extracted information
		execution := &RabbitMQCtlExecution{
			InstanceID: ctx.InstanceID,
			Category:   category,
			Command:    command,
			Arguments:  arguments,
			Timestamp:  startTime.Unix(),
			Output:     extractedOutput, // This contains the detailed rabbitmq error
			ExitCode:   exitCode,
			Success:    false,
			Duration:   time.Since(startTime).Milliseconds(),
			User:       ctx.User,
		}

		return execution, nil // Return the execution result, not an error
	}

	// Check if command execution was successful at the rabbitmq level
	if !result.Success {
		e.logger.Debug("RabbitMQ command failed: %s", result.Error)
	}

	// Create execution record
	execution := &RabbitMQCtlExecution{
		InstanceID: ctx.InstanceID,
		Category:   category,
		Command:    command,
		Arguments:  arguments,
		Timestamp:  startTime.Unix(),
		Output:     result.Output,
		ExitCode:   result.ExitCode,
		Success:    result.Success,
		Duration:   result.Duration,
		User:       ctx.User,
	}

	// For failed commands, include error information in output for display
	if !result.Success && result.Error != "" {
		// The SSH service already combines all error information in result.Error
		// Just use that directly
		execution.Output = result.Error
	}

	return execution, nil
}

// executeAsync executes a command asynchronously with streaming output
func (e *ExecutorService) executeAsync(ctx context.Context, result *StreamingExecutionResult, deployment, instance string, index int, cmd RabbitMQCtlCommand) {
	defer close(result.Output)

	// Update status to running
	result.Status = StatusRunning
	result.Output <- fmt.Sprintf("Starting execution of %s.%s...\n", result.Category, result.Command)

	// Create RabbitMQ command with arguments
	rabbitCmd := RabbitMQCommand{
		Name:        result.Command,
		Args:        result.Arguments,
		Description: cmd.Description,
		Timeout:     cmd.Timeout,
	}

	// Check for context cancellation
	select {
	case <-ctx.Done():
		result.Status = StatusCancelled
		result.Error = "execution cancelled"
		result.Output <- "Execution cancelled by user\n"
		e.finalizeExecution(result)
		return
	default:
	}

	// Execute command
	sshResult, err := e.sshService.ExecuteCommand(deployment, instance, index, rabbitCmd)
	if err != nil {
		result.Status = StatusFailed
		result.Error = err.Error()
		result.Output <- fmt.Sprintf("Error: %s\n", err.Error())
		e.finalizeExecution(result)
		return
	}

	// Stream output
	if sshResult.Output != "" {
		// Split output into lines and stream them
		lines := strings.Split(sshResult.Output, "\n")
		for _, line := range lines {
			select {
			case <-ctx.Done():
				result.Status = StatusCancelled
				result.Error = "execution cancelled during output streaming"
				result.Output <- "Execution cancelled during output streaming\n"
				e.finalizeExecution(result)
				return
			case result.Output <- line + "\n":
				// Line sent successfully
			}
		}
	}

	// Update final status
	result.ExitCode = sshResult.ExitCode
	result.Success = sshResult.Success

	if sshResult.Success {
		result.Status = StatusCompleted
		result.Output <- "Command completed successfully\n"
	} else {
		result.Status = StatusFailed
		if sshResult.Error != "" {
			result.Error = sshResult.Error
			result.Output <- fmt.Sprintf("Command failed: %s\n", sshResult.Error)
		} else {
			result.Error = fmt.Sprintf("Command failed with exit code %d", sshResult.ExitCode)
			result.Output <- fmt.Sprintf("Command failed with exit code %d\n", sshResult.ExitCode)
		}
	}

	e.finalizeExecution(result)
}

// finalizeExecution sets the end time and logs completion
func (e *ExecutorService) finalizeExecution(result *StreamingExecutionResult) {
	endTime := time.Now()
	result.EndTime = &endTime

	duration := result.EndTime.Sub(result.StartTime)
	e.logger.Info("Command execution completed: %s.%s, status=%s, duration=%v",
		result.Category, result.Command, result.Status, duration)
}

// generateExecutionID generates a unique execution ID
func (e *ExecutorService) generateExecutionID(instanceID, category, command string) string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("%s-%s-%s-%d", instanceID, category, command, timestamp)
}

// BuildRabbitMQCtlCommand builds the full rabbitmqctl command with arguments
// This is a public version of the existing buildRabbitMQCtlCommand method for use by other services
func (e *ExecutorService) BuildRabbitMQCtlCommand(command string, args []string) []string {
	// Build the rabbitmqctl command with environment sourcing
	// Must run as user vcap and use --longnames option before the command

	// Build rabbitmqctl command parts
	var cmdParts []string
	cmdParts = append(cmdParts, "source /var/vcap/jobs/rabbitmq/env &&")
	cmdParts = append(cmdParts, "rabbitmqctl")
	cmdParts = append(cmdParts, "--longnames")
	cmdParts = append(cmdParts, command)
	cmdParts = append(cmdParts, args...)

	// Create the inner command that will be run as vcap user
	innerCommand := strings.Join(cmdParts, " ")

	// Wrap with su - vcap -c to run as vcap user
	fullCmd := []string{"/bin/sudo", "su", "-", "vcap", "-c", innerCommand}

	e.logger.Debug("Built RabbitMQ command: %v", fullCmd)
	return fullCmd
}

// ValidateCommandExecution performs pre-execution validation
func (e *ExecutorService) ValidateCommandExecution(category, command string, arguments []string) error {
	// Get command metadata
	cmd, err := e.metadataService.GetCommand(category, command)
	if err != nil {
		return fmt.Errorf("command not found: %v", err)
	}

	// Check if command is dangerous and requires additional confirmation
	if cmd.Dangerous {
		e.logger.Info("WARNING: Command %s.%s is marked as dangerous", category, command)
	}

	// Validate arguments
	return e.metadataService.ValidateCommand(category, command, arguments)
}

// GetCommandHelp returns help information for a specific command
func (e *ExecutorService) GetCommandHelp(category, command string) (*RabbitMQCtlCommand, error) {
	return e.metadataService.GetCommand(category, command)
}

// ListAvailableCommands returns all available commands organized by category
func (e *ExecutorService) ListAvailableCommands() ([]RabbitMQCtlCategory, error) {
	return e.metadataService.GetCategories(), nil
}

// SanitizeArguments sanitizes command arguments to prevent injection attacks
func (e *ExecutorService) SanitizeArguments(args []string) []string {
	sanitized := make([]string, len(args))
	for i, arg := range args {
		// Remove dangerous characters and escape quotes
		clean := strings.ReplaceAll(arg, ";", "")
		clean = strings.ReplaceAll(clean, "&", "")
		clean = strings.ReplaceAll(clean, "|", "")
		clean = strings.ReplaceAll(clean, "`", "")
		clean = strings.ReplaceAll(clean, "$", "")
		clean = strings.ReplaceAll(clean, "$(", "")
		clean = strings.ReplaceAll(clean, "../", "")
		clean = strings.TrimSpace(clean)

		// Escape single and double quotes
		clean = strings.ReplaceAll(clean, "'", "\\'")
		clean = strings.ReplaceAll(clean, "\"", "\\\"")

		sanitized[i] = clean
	}
	return sanitized
}
