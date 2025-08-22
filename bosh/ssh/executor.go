package ssh

import (
	"context"
	"fmt"
	"io"
	"time"
)

// ExecutorImpl implements the SSHExecutor interface
type ExecutorImpl struct {
	sshService SSHService
	config     Config
	logger     Logger
}

// NewSSHExecutor creates a new SSH executor
func NewSSHExecutor(sshService SSHService, config Config, logger Logger) SSHExecutor {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ExecutorImpl{
		sshService: sshService,
		config:     config,
		logger:     logger,
	}
}

// Execute executes a command with the default timeout
func (e *ExecutorImpl) Execute(request *SSHRequest) (*SSHResponse, error) {
	return e.ExecuteWithTimeout(request, e.config.Timeout)
}

// ExecuteWithTimeout executes a command with a custom timeout
func (e *ExecutorImpl) ExecuteWithTimeout(request *SSHRequest, timeout time.Duration) (*SSHResponse, error) {
	e.logger.Info("Executing SSH command with timeout %v", timeout)

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create a channel to receive the result
	resultChan := make(chan *SSHResponse, 1)
	errorChan := make(chan error, 1)

	// Execute the command in a goroutine
	go func() {
		result, err := e.sshService.ExecuteCommand(request)
		if err != nil {
			errorChan <- err
			return
		}
		resultChan <- result
	}()

	// Wait for either completion or timeout
	select {
	case result := <-resultChan:
		e.logger.Info("SSH command completed successfully")
		return result, nil

	case err := <-errorChan:
		e.logger.Error("SSH command failed: %v", err)
		return nil, err

	case <-ctx.Done():
		e.logger.Error("SSH command timed out after %v", timeout)
		return &SSHResponse{
			Success:   false,
			ExitCode:  124, // Timeout exit code
			Duration:  timeout.Milliseconds(),
			Error:     fmt.Sprintf("Command timed out after %v", timeout),
			Timestamp: time.Now(),
		}, NewSSHError(SSHErrorCodeTimeout, "Command execution timed out", ctx.Err())
	}
}

// ExecuteWithStream executes a command with streaming output
func (e *ExecutorImpl) ExecuteWithStream(request *SSHRequest, output io.Writer) (*SSHResponse, error) {
	e.logger.Info("Executing SSH command with streaming output")

	// For now, execute normally and write output to the writer
	// TODO: Implement actual streaming execution
	response, err := e.Execute(request)
	if err != nil {
		return response, err
	}

	// Write output to the provided writer
	if output != nil && response.Stdout != "" {
		_, writeErr := output.Write([]byte(response.Stdout))
		if writeErr != nil {
			e.logger.Error("Failed to write output to stream: %v", writeErr)
			return response, NewSSHError(SSHErrorCodeInternal, "Failed to write output to stream", writeErr)
		}
	}

	e.logger.Info("SSH command with streaming completed successfully")
	return response, nil
}

// ExecutorWithRetry wraps an executor with retry logic
type ExecutorWithRetry struct {
	executor       SSHExecutor
	maxRetries     int
	retryDelay     time.Duration
	retryPredicate func(error) bool
	logger         Logger
}

// NewExecutorWithRetry creates a new executor with retry logic
func NewExecutorWithRetry(executor SSHExecutor, maxRetries int, retryDelay time.Duration, logger Logger) SSHExecutor {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ExecutorWithRetry{
		executor:   executor,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		retryPredicate: func(err error) bool {
			// Retry on connection errors but not on timeout or permission errors
			if sshErr, ok := err.(*SSHError); ok {
				return sshErr.Code == SSHErrorCodeConnection || sshErr.Code == SSHErrorCodeInternal
			}
			return false
		},
		logger: logger,
	}
}

// Execute executes a command with retry logic
func (e *ExecutorWithRetry) Execute(request *SSHRequest) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.Execute(request)
	})
}

// ExecuteWithTimeout executes a command with timeout and retry logic
func (e *ExecutorWithRetry) ExecuteWithTimeout(request *SSHRequest, timeout time.Duration) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.ExecuteWithTimeout(request, timeout)
	})
}

// ExecuteWithStream executes a command with streaming and retry logic
func (e *ExecutorWithRetry) ExecuteWithStream(request *SSHRequest, output io.Writer) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.ExecuteWithStream(request, output)
	})
}

// executeWithRetry executes a function with retry logic
func (e *ExecutorWithRetry) executeWithRetry(fn func() (*SSHResponse, error)) (*SSHResponse, error) {
	var lastErr error
	var lastResponse *SSHResponse

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			e.logger.Info("Retrying SSH command execution, attempt %d/%d", attempt, e.maxRetries)
			time.Sleep(e.retryDelay)
		}

		response, err := fn()
		if err == nil {
			if attempt > 0 {
				e.logger.Info("SSH command succeeded after %d retries", attempt)
			}
			return response, nil
		}

		lastErr = err
		lastResponse = response

		// Check if we should retry this error
		if !e.retryPredicate(err) {
			e.logger.Debug("SSH error not retryable: %v", err)
			break
		}

		if attempt < e.maxRetries {
			e.logger.Debug("SSH command failed, will retry: %v", err)
		}
	}

	e.logger.Error("SSH command failed after %d retries: %v", e.maxRetries, lastErr)
	return lastResponse, lastErr
}

// BatchExecutor executes multiple SSH commands concurrently
type BatchExecutor struct {
	executor   SSHExecutor
	maxWorkers int
	logger     Logger
}

// NewBatchExecutor creates a new batch executor
func NewBatchExecutor(executor SSHExecutor, maxWorkers int, logger Logger) *BatchExecutor {
	if logger == nil {
		logger = &noOpLogger{}
	}

	if maxWorkers <= 0 {
		maxWorkers = 5 // Default to 5 concurrent workers
	}

	return &BatchExecutor{
		executor:   executor,
		maxWorkers: maxWorkers,
		logger:     logger,
	}
}

// BatchRequest represents a request in a batch
type BatchRequest struct {
	ID      string
	Request *SSHRequest
}

// BatchResult represents a result from a batch execution
type BatchResult struct {
	ID       string
	Response *SSHResponse
	Error    error
}

// ExecuteBatch executes multiple SSH commands concurrently
func (b *BatchExecutor) ExecuteBatch(requests []BatchRequest) []BatchResult {
	b.logger.Info("Executing batch of %d SSH commands with %d workers", len(requests), b.maxWorkers)

	// Create channels for work distribution
	workChan := make(chan BatchRequest, len(requests))
	resultChan := make(chan BatchResult, len(requests))

	// Start workers
	for i := 0; i < b.maxWorkers; i++ {
		go b.worker(workChan, resultChan)
	}

	// Send work to workers
	for _, req := range requests {
		workChan <- req
	}
	close(workChan)

	// Collect results
	results := make([]BatchResult, 0, len(requests))
	for i := 0; i < len(requests); i++ {
		result := <-resultChan
		results = append(results, result)
	}

	b.logger.Info("Batch execution completed")
	return results
}

// worker processes SSH requests from the work channel
func (b *BatchExecutor) worker(workChan <-chan BatchRequest, resultChan chan<- BatchResult) {
	for req := range workChan {
		b.logger.Debug("Worker processing request: %s", req.ID)

		response, err := b.executor.Execute(req.Request)

		resultChan <- BatchResult{
			ID:       req.ID,
			Response: response,
			Error:    err,
		}

		b.logger.Debug("Worker completed request: %s", req.ID)
	}
}
