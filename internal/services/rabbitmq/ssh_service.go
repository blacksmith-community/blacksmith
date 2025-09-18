package rabbitmq

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/bosh/ssh"
)

// Static errors for err113 compliance.
var (
	ErrNoParserForCommand = errors.New("no parser for command")
)

// SSHService provides RabbitMQ-specific SSH operations.
type SSHService struct {
	sshService ssh.SSHService
	logger     Logger
}

// Logger interface for logging.
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// RabbitMQCommand represents a RabbitMQ management command.
type RabbitMQCommand struct {
	Name        string
	Args        []string
	Description string
	Timeout     int // Timeout in seconds
}

// RabbitMQCommandResult represents the result of a RabbitMQ command.
type RabbitMQCommandResult struct {
	Success    bool        `json:"success"`
	Command    string      `json:"command"`
	Output     string      `json:"output"`
	Error      string      `json:"error,omitempty"`
	ExitCode   int         `json:"exit_code"`
	Duration   int64       `json:"duration"`
	ParsedData interface{} `json:"parsed_data,omitempty"`
	Timestamp  time.Time   `json:"timestamp"`
}

// NewRabbitMQSSHService creates a new RabbitMQ SSH service.
func NewRabbitMQSSHService(sshService ssh.SSHService, logger Logger) *SSHService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &SSHService{
		sshService: sshService,
		logger:     logger,
	}
}

// ExecuteCommand executes a RabbitMQ command on a service instance.
func (r *SSHService) ExecuteCommand(deployment, instance string, index int, cmd RabbitMQCommand) (*RabbitMQCommandResult, error) {
	r.logger.Infof("Executing RabbitMQ command '%s' on %s/%s/%d", cmd.Name, deployment, instance, index)

	fullCommand := r.buildFullCommand(cmd)
	sshReq := r.createSSHRequest(deployment, instance, index, fullCommand, cmd.Timeout)

	r.logger.Debugf("SSH Request: %+v", sshReq)

	sshResp, err := r.sshService.ExecuteCommand(sshReq)
	if err != nil {
		return r.handleSSHError(cmd.Name, err)
	}

	result := r.createCommandResult(cmd, sshResp)
	r.enhanceResultOnFailure(result, sshResp)
	r.parseOutputOnSuccess(result, cmd)

	r.logger.Infof("RabbitMQ command '%s' completed: success=%t, exitCode=%d", cmd.Name, result.Success, result.ExitCode)

	return result, nil
}

// Common RabbitMQ commands

// ListQueues lists all queues.
func (r *SSHService) ListQueues(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "list_queues",
		Args:        []string{"name", "messages", "consumers", "state"},
		Description: "List all queues with messages and consumers",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// ListConnections lists all connections.
func (r *SSHService) ListConnections(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "list_connections",
		Args:        []string{"name", "state", "user", "protocol"},
		Description: "List all connections",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// ListChannels lists all channels.
func (r *SSHService) ListChannels(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "list_channels",
		Args:        []string{"name", "connection", "user", "consumer_count"},
		Description: "List all channels",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// ListUsers lists all users.
func (r *SSHService) ListUsers(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "list_users",
		Args:        []string{},
		Description: "List all users",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// ClusterStatus gets cluster status.
func (r *SSHService) ClusterStatus(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "cluster_status",
		Args:        []string{},
		Description: "Get cluster status",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// NodeHealth checks node health.
func (r *SSHService) NodeHealth(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "node_health_check",
		Args:        []string{},
		Description: "Check node health",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// Status gets overall status.
func (r *SSHService) Status(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "status",
		Args:        []string{},
		Description: "Get RabbitMQ status",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// Environment gets environment information.
func (r *SSHService) Environment(deployment, instance string, index int) (*RabbitMQCommandResult, error) {
	cmd := RabbitMQCommand{
		Name:        "environment",
		Args:        []string{},
		Description: "Get RabbitMQ environment",
		Timeout:     DefaultSSHTimeout,
	}

	return r.ExecuteCommand(deployment, instance, index, cmd)
}

// IsRabbitMQSSHService implements the interfaces.RabbitMQSSHService interface.
func (r *SSHService) IsRabbitMQSSHService() bool {
	return true
}

func (r *SSHService) createSSHRequest(deployment, instance string, index int, fullCommand []string, timeout int) *ssh.SSHRequest {
	sshReq := &ssh.SSHRequest{
		Deployment: deployment,
		Instance:   instance,
		Index:      index,
		Command:    fullCommand[0],
		Args:       fullCommand[1:],
		Timeout:    timeout,
		Options: &ssh.SSHOptions{
			BufferOutput:  true,
			MaxOutputSize: MaxSSHOutputSize, // 1MB max output
		},
	}

	if sshReq.Timeout == 0 {
		sshReq.Timeout = 30
	}

	return sshReq
}

func (r *SSHService) handleSSHError(cmdName string, err error) (*RabbitMQCommandResult, error) {
	r.logger.Errorf("SSH command failed: %v", err)

	errorMsg := err.Error()
	extractedOutput := r.extractOutputFromError(errorMsg)
	exitCode := r.extractExitCodeFromError(errorMsg)

	return &RabbitMQCommandResult{
		Success:   false,
		Command:   cmdName,
		Output:    extractedOutput,
		Error:     errorMsg,
		ExitCode:  exitCode,
		Timestamp: time.Now(),
	}, nil // Don't return error, return result with Success=false
}

func (r *SSHService) extractOutputFromError(errorMsg string) string {
	if strings.Contains(errorMsg, "output:") {
		parts := strings.SplitN(errorMsg, "output:", ErrorOutputParts)
		if len(parts) == ErrorOutputParts {
			return strings.TrimSpace(parts[1])
		}
	}

	return ""
}

func (r *SSHService) extractExitCodeFromError(errorMsg string) int {
	if strings.Contains(errorMsg, "status") {
		re := regexp.MustCompile(`status (\d+)`)
		if matches := re.FindStringSubmatch(errorMsg); len(matches) > 1 {
			code, parseErr := strconv.Atoi(matches[1])
			if parseErr == nil {
				return code
			}
		}
	}

	return 1
}

func (r *SSHService) createCommandResult(cmd RabbitMQCommand, sshResp *ssh.SSHResponse) *RabbitMQCommandResult {
	return &RabbitMQCommandResult{
		Success:   sshResp.Success,
		Command:   cmd.Name,
		Output:    sshResp.Stdout,
		Error:     sshResp.Error,
		ExitCode:  sshResp.ExitCode,
		Duration:  sshResp.Duration,
		Timestamp: sshResp.Timestamp,
	}
}

func (r *SSHService) enhanceResultOnFailure(result *RabbitMQCommandResult, sshResp *ssh.SSHResponse) {
	if !result.Success {
		errorParts := r.collectErrorParts(result, sshResp)
		if len(errorParts) > 0 {
			result.Error = strings.Join(errorParts, "\n\n")
		} else {
			result.Error = fmt.Sprintf("Command failed with exit code %d", result.ExitCode)
		}
	}
}

func (r *SSHService) collectErrorParts(result *RabbitMQCommandResult, sshResp *ssh.SSHResponse) []string {
	var errorParts []string

	if result.Error != "" {
		errorParts = append(errorParts, result.Error)
	}

	if result.Output != "" {
		errorParts = append(errorParts, "Command Output:\n"+result.Output)
	}

	if sshResp.Stderr != "" {
		errorParts = append(errorParts, "Stderr:\n"+sshResp.Stderr)
	}

	return errorParts
}

func (r *SSHService) parseOutputOnSuccess(result *RabbitMQCommandResult, cmd RabbitMQCommand) {
	if result.Success && result.Output != "" && !strings.Contains(cmd.Name, "rabbitmq-plugins") {
		parsedData, parseErr := r.parseCommandOutput(cmd.Name, result.Output)
		if parseErr == nil {
			result.ParsedData = parsedData
		} else {
			r.logger.Debugf("Failed to parse output for command %s: %v", cmd.Name, parseErr)
		}
	}
}

func (r *SSHService) buildFullCommand(cmd RabbitMQCommand) []string {
	if strings.Contains(cmd.Name, "rabbitmq-plugins") || strings.Contains(cmd.Name, "/var/vcap/jobs/rabbitmq/env") {
		r.logger.Debugf("Detected pre-built rabbitmq-plugins command, using directly")

		innerCommand := cmd.Name

		return []string{"/bin/sudo", "su", "-", "vcap", "-c", innerCommand}
	}

	return r.buildRabbitMQCtlCommand(cmd)
}

// buildRabbitMQCtlCommand builds the full rabbitmqctl command with arguments.
func (r *SSHService) buildRabbitMQCtlCommand(cmd RabbitMQCommand) []string {
	// Build the rabbitmqctl command with environment sourcing
	// Must run as user vcap and use --longnames option before the command

	// Build rabbitmqctl command parts
	var cmdParts []string

	cmdParts = append(cmdParts, "source /var/vcap/jobs/rabbitmq/env &&")
	cmdParts = append(cmdParts, "rabbitmqctl")
	cmdParts = append(cmdParts, "--longnames")
	cmdParts = append(cmdParts, cmd.Name)
	cmdParts = append(cmdParts, cmd.Args...)

	// Create the inner command that will be run as vcap user
	innerCommand := strings.Join(cmdParts, " ")

	// Wrap with su - vcap -c to run as vcap user
	fullCmd := []string{"/bin/sudo", "su", "-", "vcap", "-c", innerCommand}

	r.logger.Debugf("Built RabbitMQ command: %v", fullCmd)

	return fullCmd
}

// parseCommandOutput parses the output of rabbitmqctl commands into structured data.
func (r *SSHService) parseCommandOutput(commandName, output string) (interface{}, error) {
	r.logger.Debugf("Parsing output for command: %s", commandName)

	switch commandName {
	case "list_queues":
		return r.parseListQueues(output)
	case "list_connections":
		return r.parseListConnections(output)
	case "list_channels":
		return r.parseListChannels(output)
	case "list_users":
		return r.parseListUsers(output)
	case "cluster_status":
		return r.parseClusterStatus(output)
	case "status":
		return r.parseStatus(output)
	case "node_health_check":
		return r.parseNodeHealth(output)
	case "environment":
		return r.parseEnvironment(output)
	default:
		r.logger.Debugf("No parser available for command: %s", commandName)

		return nil, fmt.Errorf("%w: %s", ErrNoParserForCommand, commandName)
	}
}

// parseListQueues parses the output of list_queues command.
func (r *SSHService) parseListQueues(output string) (interface{}, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return []map[string]interface{}{}, nil
	}

	var queues []map[string]interface{}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Split by tabs (rabbitmqctl uses tabs as separators)
		fields := strings.Split(line, "\t")
		if len(fields) >= MinFieldsForQueue {
			queue := map[string]interface{}{
				"name":      strings.TrimSpace(fields[0]),
				"messages":  r.parseIntSafe(strings.TrimSpace(fields[1])),
				"consumers": r.parseIntSafe(strings.TrimSpace(fields[2])),
				"state":     strings.TrimSpace(fields[3]),
			}
			queues = append(queues, queue)
		}
	}

	return map[string]interface{}{
		"queues": queues,
		"count":  len(queues),
	}, nil
}

// parseListConnections parses the output of list_connections command.
func (r *SSHService) parseListConnections(output string) (interface{}, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return []map[string]interface{}{}, nil
	}

	var connections []map[string]interface{}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) >= MinFieldsForQueue {
			connection := map[string]interface{}{
				"name":     strings.TrimSpace(fields[0]),
				"state":    strings.TrimSpace(fields[1]),
				"user":     strings.TrimSpace(fields[2]),
				"protocol": strings.TrimSpace(fields[3]),
			}
			connections = append(connections, connection)
		}
	}

	return map[string]interface{}{
		"connections": connections,
		"count":       len(connections),
	}, nil
}

// parseListChannels parses the output of list_channels command.
func (r *SSHService) parseListChannels(output string) (interface{}, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return []map[string]interface{}{}, nil
	}

	var channels []map[string]interface{}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) >= MinFieldsForQueue {
			channel := map[string]interface{}{
				"name":           strings.TrimSpace(fields[0]),
				"connection":     strings.TrimSpace(fields[1]),
				"user":           strings.TrimSpace(fields[2]),
				"consumer_count": r.parseIntSafe(strings.TrimSpace(fields[3])),
			}
			channels = append(channels, channel)
		}
	}

	return map[string]interface{}{
		"channels": channels,
		"count":    len(channels),
	}, nil
}

// parseListUsers parses the output of list_users command.
func (r *SSHService) parseListUsers(output string) (interface{}, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return []map[string]interface{}{}, nil
	}

	var users []map[string]interface{}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Users output format: username tags
		fields := strings.SplitN(line, "\t", TabSeparatorParts)
		if len(fields) >= 1 {
			user := map[string]interface{}{
				"username": strings.TrimSpace(fields[0]),
			}
			if len(fields) >= TabSeparatorParts {
				user["tags"] = strings.TrimSpace(fields[1])
			}

			users = append(users, user)
		}
	}

	return map[string]interface{}{
		"users": users,
		"count": len(users),
	}, nil
}

// parseClusterStatus parses the output of cluster_status command.
func (r *SSHService) parseClusterStatus(output string) (interface{}, error) {
	// Cluster status output is complex and varies by version
	// For now, return the raw output
	return map[string]interface{}{
		"raw_output": output,
		"summary":    "Cluster status information",
	}, nil
}

// parseStatus parses the output of status command.
func (r *SSHService) parseStatus(output string) (interface{}, error) {
	// Status output is complex and varies by version
	// For now, return the raw output
	return map[string]interface{}{
		"raw_output": output,
		"summary":    "RabbitMQ status information",
	}, nil
}

// parseNodeHealth parses the output of node_health_check command.
func (r *SSHService) parseNodeHealth(output string) (interface{}, error) {
	// Health check typically returns simple status
	isHealthy := strings.Contains(strings.ToLower(output), "health check passed") ||
		strings.Contains(strings.ToLower(output), "ok") ||
		!strings.Contains(strings.ToLower(output), "error")

	return map[string]interface{}{
		"healthy":    isHealthy,
		"raw_output": output,
	}, nil
}

// parseEnvironment parses the output of environment command.
func (r *SSHService) parseEnvironment(output string) (interface{}, error) {
	// Environment output is complex and varies by version
	// For now, return the raw output
	return map[string]interface{}{
		"raw_output": output,
		"summary":    "RabbitMQ environment information",
	}, nil
}

// parseIntSafe safely parses an integer from a string, returning 0 if parsing fails.
func (r *SSHService) parseIntSafe(s string) int {
	i, err := strconv.Atoi(s)
	if err == nil {
		return i
	}

	return 0
}

// noOpLogger is a no-operation logger implementation.
type noOpLogger struct{}

func (l *noOpLogger) Infof(format string, args ...interface{})  {}
func (l *noOpLogger) Debugf(format string, args ...interface{}) {}
func (l *noOpLogger) Errorf(format string, args ...interface{}) {}
