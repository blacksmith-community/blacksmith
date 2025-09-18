package websocket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/interfaces"
	rabbitmqssh "blacksmith/internal/services/rabbitmq"
	gorillawebsocket "github.com/gorilla/websocket"
)

// Static errors for err113 compliance.
var (
	ErrRabbitMQExecutorServiceNotAvailable = errors.New("RabbitMQ executor service not available")
)

// handleStreamingExecution handles the execution of a rabbitmqctl command with streaming output.
func (h *Handler) handleStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, _ interfaces.Logger) {
	err := h.validateRabbitMQExecutorService(conn)
	if err != nil {
		return
	}

	execCtx := h.createRabbitMQExecutionContext(instanceID)

	result, err := h.executeRabbitMQCommand(ctx, conn, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		return
	}

	err = h.sendRabbitMQInitialResponse(conn, result)
	if err != nil {
		return
	}

	h.streamRabbitMQOutput(ctx, conn, result, execCtx)
}

// handlePluginsStreamingExecution handles the execution of a rabbitmq-plugins command with streaming output.
func (h *Handler) handlePluginsStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, _ interfaces.Logger) {
	err := h.validatePluginsExecutorService(conn)
	if err != nil {
		return
	}

	execCtx := h.createPluginsExecutionContext(instanceID)

	result, err := h.executePluginsCommand(ctx, conn, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		return
	}

	err = h.sendPluginsInitialResponse(conn, result, instanceID, category, command, arguments)
	if err != nil {
		return
	}

	h.streamPluginsOutput(ctx, conn, result, execCtx, instanceID, category, command, arguments)
}

func (h *Handler) validatePluginsExecutorService(conn *gorillawebsocket.Conn) error {
	if h.rabbitMQPluginsExecutorService == nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": "RabbitMQ plugins executor service not available",
		}

		err := conn.WriteJSON(response)
		if err != nil {
			h.logger.Error("Failed to send WebSocket error response: %v", err)

			return fmt.Errorf("failed to send WebSocket error response: %w", err)
		}

		return nil
	}

	return nil
}

func (h *Handler) createPluginsExecutionContext(instanceID string) rabbitmqssh.PluginsExecutionContext {
	return rabbitmqssh.PluginsExecutionContext{
		InstanceID: instanceID,
		User:       "websocket-user", // TODO: get from auth
		ClientIP:   "websocket",      // TODO: get actual client IP
	}
}

func (h *Handler) executePluginsCommand(ctx context.Context, conn *gorillawebsocket.Conn, execCtx rabbitmqssh.PluginsExecutionContext, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string) (*rabbitmqssh.PluginsStreamingExecutionResult, error) {
	result, err := h.rabbitMQPluginsExecutorService.ExecuteCommand(ctx, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}

		writeErr := conn.WriteJSON(response)
		if writeErr != nil {
			h.logger.Error("Failed to send WebSocket error response: %v", writeErr)
		}

		return nil, fmt.Errorf("failed to execute plugins command: %w", err)
	}

	return result, nil
}

func (h *Handler) sendPluginsInitialResponse(conn *gorillawebsocket.Conn, result *rabbitmqssh.PluginsStreamingExecutionResult, instanceID, category, command string, arguments []string) error {
	response := map[string]interface{}{
		"type":         "execution_started",
		"execution_id": result.ExecutionID,
		"instance_id":  instanceID,
		"category":     category,
		"command":      command,
		"arguments":    arguments,
		"start_time":   result.StartTime,
	}

	err := conn.WriteJSON(response)
	if err != nil {
		h.logger.Error("Failed to send initial WebSocket response: %v", err)

		return fmt.Errorf("failed to send initial WebSocket response: %w", err)
	}

	return nil
}

func (h *Handler) streamPluginsOutput(ctx context.Context, conn *gorillawebsocket.Conn, result *rabbitmqssh.PluginsStreamingExecutionResult, execCtx rabbitmqssh.PluginsExecutionContext, instanceID, category, command string, arguments []string) {
	go func() {
		defer h.sendPluginsFinalResponse(ctx, conn, result, execCtx, instanceID, category, command, arguments)

		for {
			select {
			case <-ctx.Done():
				return
			case outputLine, ok := <-result.Output:
				if !ok {
					return
				}

				outputResponse := map[string]interface{}{
					"type":         "output",
					"execution_id": result.ExecutionID,
					"data":         outputLine,
				}

				err := conn.WriteJSON(outputResponse)
				if err != nil {
					h.logger.Error("Failed to send output: %v", err)

					return
				}
			}
		}
	}()
}

func (h *Handler) sendPluginsFinalResponse(ctx context.Context, conn *gorillawebsocket.Conn, result *rabbitmqssh.PluginsStreamingExecutionResult, execCtx rabbitmqssh.PluginsExecutionContext, instanceID, category, command string, arguments []string) {
	finalResponse := h.buildPluginsFinalResponse(result)

	err := conn.WriteJSON(finalResponse)
	if err != nil {
		h.logger.Error("Failed to send final WebSocket response: %v", err)
	}

	h.auditPluginsExecution(ctx, result, execCtx, instanceID, category, command, arguments)
}

func (h *Handler) buildPluginsFinalResponse(result *rabbitmqssh.PluginsStreamingExecutionResult) map[string]interface{} {
	finalResponse := map[string]interface{}{
		"type":         "execution_completed",
		"execution_id": result.ExecutionID,
		"status":       result.Status,
		"success":      result.Success,
		"exit_code":    result.ExitCode,
	}

	if result.EndTime != nil {
		finalResponse["end_time"] = *result.EndTime
		finalResponse["duration"] = result.EndTime.Sub(result.StartTime).Milliseconds()
	}

	if result.Error != "" {
		finalResponse["error"] = result.Error
	}

	return finalResponse
}

func (h *Handler) auditPluginsExecution(ctx context.Context, result *rabbitmqssh.PluginsStreamingExecutionResult, execCtx rabbitmqssh.PluginsExecutionContext, instanceID, category, command string, arguments []string) {
	if h.rabbitMQPluginsAuditService == nil {
		return
	}

	execution := &rabbitmqssh.RabbitMQPluginsExecution{
		InstanceID: instanceID,
		Category:   category,
		Command:    command,
		Arguments:  arguments,
		Timestamp:  result.StartTime.UnixNano() / int64(time.Millisecond),
		Output:     "", // Will be populated during streaming
		ExitCode:   result.ExitCode,
		Success:    result.Success,
	}

	duration := int64(0)
	if result.EndTime != nil {
		duration = result.EndTime.Sub(result.StartTime).Milliseconds()
	}

	err := h.rabbitMQPluginsAuditService.LogExecution(ctx, execution, execCtx.User, execCtx.ClientIP, result.ExecutionID, duration)
	if err != nil {
		h.logger.Error("Failed to log streaming execution audit: %v", err)
	}
}

// validateRabbitMQExecutorService checks if the executor service is available.
func (h *Handler) validateRabbitMQExecutorService(conn *gorillawebsocket.Conn) error {
	if h.rabbitMQExecutorService != nil {
		return nil
	}

	response := map[string]interface{}{
		"type":  "error",
		"error": "RabbitMQ executor service not available",
	}

	err := conn.WriteJSON(response)
	if err != nil {
		h.logger.Error("Failed to send WebSocket error response: %v", err)

		return fmt.Errorf("failed to send WebSocket error response: %w", err)
	}

	return ErrRabbitMQExecutorServiceNotAvailable
}

// createRabbitMQExecutionContext creates the execution context for the command.
func (h *Handler) createRabbitMQExecutionContext(instanceID string) rabbitmqssh.ExecutionContext {
	return rabbitmqssh.ExecutionContext{
		InstanceID: instanceID,
		User:       "websocket-user", // TODO: get from auth
		ClientIP:   "websocket",      // TODO: get actual client IP
	}
}

// executeRabbitMQCommand executes the rabbitmqctl command.
func (h *Handler) executeRabbitMQCommand(ctx context.Context, conn *gorillawebsocket.Conn, execCtx rabbitmqssh.ExecutionContext, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string) (*rabbitmqssh.StreamingExecutionResult, error) {
	result, err := h.rabbitMQExecutorService.ExecuteCommand(ctx, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}

		writeErr := conn.WriteJSON(response)
		if writeErr != nil {
			h.logger.Error("Failed to send WebSocket error response: %v", writeErr)
		}

		return nil, fmt.Errorf("failed to execute rabbitmq command: %w", err)
	}

	return result, nil
}

// sendRabbitMQInitialResponse sends the initial execution started response.
func (h *Handler) sendRabbitMQInitialResponse(conn *gorillawebsocket.Conn, result *rabbitmqssh.StreamingExecutionResult) error {
	response := map[string]interface{}{
		"type":         "execution_started",
		"execution_id": result.ExecutionID,
		"category":     result.Category,
		"command":      result.Command,
		"arguments":    result.Arguments,
		"status":       result.Status,
	}

	err := conn.WriteJSON(response)
	if err != nil {
		h.logger.Error("Failed to send execution started response: %v", err)

		return fmt.Errorf("failed to send execution started response: %w", err)
	}

	return nil
}

// streamRabbitMQOutput handles streaming output from the execution.
func (h *Handler) streamRabbitMQOutput(ctx context.Context, conn *gorillawebsocket.Conn, result *rabbitmqssh.StreamingExecutionResult, execCtx rabbitmqssh.ExecutionContext) {
	go func() {
		defer h.handleRabbitMQExecutionCompletion(ctx, conn, result, execCtx)

		h.processRabbitMQOutputLines(ctx, conn, result)
	}()
}

// handleRabbitMQExecutionCompletion sends final status and logs audit.
func (h *Handler) handleRabbitMQExecutionCompletion(ctx context.Context, conn *gorillawebsocket.Conn, result *rabbitmqssh.StreamingExecutionResult, execCtx rabbitmqssh.ExecutionContext) {
	// Send final status
	finalResponse := map[string]interface{}{
		"type":         "execution_completed",
		"execution_id": result.ExecutionID,
		"status":       result.Status,
		"success":      result.Success,
		"exit_code":    result.ExitCode,
	}
	if result.Error != "" {
		finalResponse["error"] = result.Error
	}

	err := conn.WriteJSON(finalResponse)
	if err != nil {
		h.logger.Error("Failed to send final WebSocket response: %v", err)
	}

	// Log to audit if available
	if h.rabbitMQAuditService != nil {
		err := h.rabbitMQAuditService.LogStreamingExecution(ctx, result, execCtx.User, execCtx.ClientIP)
		if err != nil {
			h.logger.Error("Failed to log streaming execution audit: %v", err)
		}
	}
}

// processRabbitMQOutputLines processes output lines from the execution.
func (h *Handler) processRabbitMQOutputLines(ctx context.Context, conn *gorillawebsocket.Conn, result *rabbitmqssh.StreamingExecutionResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case outputLine, ok := <-result.Output:
			if !ok {
				// Output channel closed, execution finished
				return
			}

			// Send output line
			outputResponse := map[string]interface{}{
				"type":         "output",
				"execution_id": result.ExecutionID,
				"data":         outputLine,
			}

			err := conn.WriteJSON(outputResponse)
			if err != nil {
				h.logger.Error("Failed to send output: %v", err)

				return
			}
		}
	}
}
