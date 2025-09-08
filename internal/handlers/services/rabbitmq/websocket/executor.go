package websocket

import (
	"context"
	"time"

	"blacksmith/internal/interfaces"
	rabbitmqssh "blacksmith/services/rabbitmq"
	gorillawebsocket "github.com/gorilla/websocket"
)

// handleStreamingExecution handles the execution of a rabbitmqctl command with streaming output.
func (h *Handler) handleStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, streamLogger interfaces.Logger) {
	if h.rabbitMQExecutorService == nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": "RabbitMQ executor service not available",
		}

		err := conn.WriteJSON(response)
		if err != nil {
			h.logger.Error("Failed to send WebSocket error response: %v", err)
		}

		return
	}

	// Create execution context
	execCtx := rabbitmqssh.ExecutionContext{
		InstanceID: instanceID,
		User:       "websocket-user", // TODO: get from auth
		ClientIP:   "websocket",      // TODO: get actual client IP
	}

	// Execute command with streaming
	result, err := h.rabbitMQExecutorService.ExecuteCommand(ctx, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		response := map[string]interface{}{
			"type":  "error",
			"error": err.Error(),
		}

		err := conn.WriteJSON(response)
		if err != nil {
			h.logger.Error("Failed to send WebSocket error response: %v", err)
		}

		return
	}

	// Send initial response
	response := map[string]interface{}{
		"type":         "execution_started",
		"execution_id": result.ExecutionID,
		"category":     result.Category,
		"command":      result.Command,
		"arguments":    result.Arguments,
		"status":       result.Status,
	}
	if err := conn.WriteJSON(response); err != nil {
		h.logger.Error("Failed to send execution started response: %v", err)

		return
	}

	// Stream output from the execution
	go func() {
		defer func() {
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
		}()

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
	}()
}

// handlePluginsStreamingExecution handles the execution of a rabbitmq-plugins command with streaming output.
func (h *Handler) handlePluginsStreamingExecution(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, category, command string, arguments []string, pluginsLogger interfaces.Logger) {
	if err := h.validatePluginsExecutorService(conn); err != nil {
		return
	}

	execCtx := h.createPluginsExecutionContext(instanceID)

	result, err := h.executePluginsCommand(ctx, conn, execCtx, deploymentName, instanceName, instanceIndex, category, command, arguments)
	if err != nil {
		return
	}

	if err := h.sendPluginsInitialResponse(conn, result, instanceID, category, command, arguments); err != nil {
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
		}

		return err
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

		return nil, err
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

	if err := conn.WriteJSON(response); err != nil {
		h.logger.Error("Failed to send initial WebSocket response: %v", err)

		return err
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
