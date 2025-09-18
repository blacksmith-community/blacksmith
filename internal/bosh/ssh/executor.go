package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Constants for SSH executor.
const (
	// Exit code for timeout.
	timeoutExitCode = 124
	// Maximum tail buffer size for sentinel detection (8KB).
	maxTailBufferSize = 8192
	// Tail buffer trim size (4KB).
	tailBufferTrimSize = 4096
)

// ExecutorImpl implements the SSHExecutor interface.
type ExecutorImpl struct {
	sshService SSHService
	config     Config
	logger     Logger
}

// NewSSHExecutor creates a new SSH executor.
func NewSSHExecutor(sshService SSHService, config Config, logger Logger) *ExecutorImpl {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ExecutorImpl{
		sshService: sshService,
		config:     config,
		logger:     logger,
	}
}

// Execute executes a command with the default timeout.
func (e *ExecutorImpl) Execute(request *SSHRequest) (*SSHResponse, error) {
	return e.ExecuteWithTimeout(request, e.config.Timeout)
}

// ExecuteWithTimeout executes a command with a custom timeout.
func (e *ExecutorImpl) ExecuteWithTimeout(request *SSHRequest, timeout time.Duration) (*SSHResponse, error) {
	e.logger.Infof("Executing SSH command with timeout %v", timeout)

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
		e.logger.Infof("SSH command completed successfully")

		return result, nil

	case err := <-errorChan:
		e.logger.Errorf("SSH command failed: %v", err)

		return nil, err

	case <-ctx.Done():
		e.logger.Errorf("SSH command timed out after %v", timeout)

		return &SSHResponse{
			Success:   false,
			ExitCode:  timeoutExitCode, // Timeout exit code
			Duration:  timeout.Milliseconds(),
			Stdout:    "",
			Stderr:    "",
			Error:     fmt.Sprintf("Command timed out after %v", timeout),
			Timestamp: time.Now(),
			RequestID: "",
		}, NewSSHError(SSHErrorCodeTimeout, "Command execution timed out", ctx.Err())
	}
}

// ExecuteWithStream executes a command with streaming output.
func (e *ExecutorImpl) ExecuteWithStream(request *SSHRequest, output io.Writer) (*SSHResponse, error) {
	e.logger.Infof("Executing SSH command with streaming output")

	// Resolve timeout: prefer request.Timeout if provided, else executor config
	timeout := e.config.Timeout
	if request != nil && request.Timeout > 0 {
		timeout = time.Duration(request.Timeout) * time.Second
	}

	// Create timeout context for the whole streaming execution
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	startTime := time.Now()

	// Create interactive SSH session
	session, err := e.sshService.CreateSession(request)
	if err != nil {
		e.logger.Errorf("Failed to create SSH session: %v", err)

		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	// Ensure cleanup
	defer func() { _ = session.Close() }()

	// Start the session
	err = session.Start()
	if err != nil {
		e.logger.Errorf("Failed to start SSH session: %v", err)

		return nil, fmt.Errorf("failed to start SSH session: %w", err)
	}

	// Build the command line with a sentinel to capture the exit code, then exit the shell
	// Use robust single-quote escaping for each argument
	cmdLine := request.Command
	if len(request.Args) > 0 {
		parts := make([]string, 0, len(request.Args)+1)

		parts = append(parts, cmdLine)
		for _, a := range request.Args {
			parts = append(parts, shellSingleQuote(a))
		}

		cmdLine = strings.Join(parts, " ")
	}

	const exitSentinel = "__BS_EXIT__:"

	fullCmd := fmt.Sprintf("set -o pipefail 2>/dev/null; %s; code=$?; echo %s$code; exit $code\n", cmdLine, exitSentinel)

	err = session.SendInput([]byte(fullCmd))
	if err != nil {
		e.logger.Errorf("Failed to send command to SSH session: %v", err)

		return nil, NewSSHError(SSHErrorCodeExecution, "Failed to send command to SSH session", err)
	}

	// Stream output, detect sentinel and capture exit code
	var (
		stdoutBuf bytes.Buffer
		tailBuf   strings.Builder // small sliding window to detect sentinel across chunk boundaries
		exitCode  = -1
		writeErr  error
	)

	for {
		// Check timeout
		select {
		case <-ctx.Done():
			e.logger.Errorf("SSH command timed out after %v", timeout)

			return createSSHResponse(false, timeoutExitCode, time.Since(startTime), stdoutBuf.String(), ctx.Err(), startTime),
				NewSSHError(SSHErrorCodeTimeout, "Command execution timed out", ctx.Err())
		default:
		}

		chunk, rerr := session.ReadOutput()
		if rerr != nil {
			// Treat read error as execution failure if we haven't seen an exit code
			if exitCode < 0 {
				e.logger.Errorf("SSH streaming read error: %v", rerr)

				return createSSHResponse(false, 1, time.Since(startTime), stdoutBuf.String(), rerr, startTime), rerr
			}

			break
		}

		if len(chunk) == 0 {
			// No data this interval; check if session has closed
			st := session.Status()
			if st == SessionStatusClosed || st == SessionStatusError {
				break
			}

			continue
		}

		// Forward to provided writer
		if output != nil && writeErr == nil {
			_, writeErr = output.Write(chunk)
			if writeErr != nil {
				e.logger.Errorf("Failed to write output to stream: %v", writeErr)
				// We will still try to finish capturing exit code, but return error later
			}
		}

		// Accumulate for response and sentinel detection
		stdoutBuf.Write(chunk)
		tailBuf.Write(chunk)

		// Keep tailBuf reasonably small to avoid unbounded growth
		if tailBuf.Len() > maxTailBufferSize {
			s := tailBuf.String()
			if len(s) > tailBufferTrimSize {
				tailBuf.Reset()
				tailBuf.WriteString(s[len(s)-tailBufferTrimSize:])
			}
		}

		// Detect exit sentinel
		if idx := strings.LastIndex(tailBuf.String(), exitSentinel); idx != -1 {
			// Extract digits following sentinel up to first non-digit
			sentinelSuffix := tailBuf.String()[idx+len(exitSentinel):]

			digitCount := 0
			for digitCount < len(sentinelSuffix) && sentinelSuffix[digitCount] >= '0' && sentinelSuffix[digitCount] <= '9' {
				digitCount++
			}

			if digitCount > 0 {
				// Parse exit code
				var code int

				_, _ = fmt.Sscanf(sentinelSuffix[:digitCount], "%d", &code)
				exitCode = code

				// Remove sentinel from accumulated stdout
				out := stdoutBuf.String()
				if cut := strings.LastIndex(out, exitSentinel); cut != -1 {
					// Drop from sentinel start to end of line
					cleaned := out[:cut]

					stdoutBuf.Reset()
					stdoutBuf.WriteString(cleaned)
				}

				break
			}
		}
	}

	duration := time.Since(startTime)

	if writeErr != nil {
		// Return response with streaming write error surfaced
		resp := createSSHResponse(exitCode == 0, exitCode, duration, stdoutBuf.String(), writeErr, startTime)

		return resp, NewSSHError(SSHErrorCodeInternal, "Failed to write output to stream", writeErr)
	}

	if exitCode < 0 {
		// Session ended without sentinel; treat as unknown failure
		exitCode = 1
	}

	success := exitCode == 0
	if success {
		e.logger.Infof("SSH command with streaming completed successfully in %v", duration)
	} else {
		e.logger.Errorf("SSH command with streaming failed (exit %d) in %v", exitCode, duration)
	}

	return createSSHResponse(success, exitCode, duration, stdoutBuf.String(), nil, startTime), nil
}

// shellSingleQuote returns a shell-safe single-quoted representation of the argument.
// It encloses the string in single quotes and escapes any existing single quotes.
func shellSingleQuote(input string) string {
	if input == "" {
		return "''"
	}

	// Replace each ' with '\'' (close, escape, reopen)
	return "'" + strings.ReplaceAll(input, "'", "'\"'\"'") + "'"
}

// ExecutorWithRetry wraps an executor with retry logic.
type ExecutorWithRetry struct {
	executor       SSHExecutor
	maxRetries     int
	retryDelay     time.Duration
	retryPredicate func(error) bool
	logger         Logger
}

// NewExecutorWithRetry creates a new executor with retry logic.
func NewExecutorWithRetry(executor SSHExecutor, maxRetries int, retryDelay time.Duration, logger Logger) *ExecutorWithRetry {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ExecutorWithRetry{
		executor:   executor,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
		retryPredicate: func(err error) bool {
			// Retry on connection errors but not on timeout or permission errors
			var sshErr *SSHError
			if errors.As(err, &sshErr) {
				return sshErr.Code == SSHErrorCodeConnection || sshErr.Code == SSHErrorCodeInternal
			}

			return false
		},
		logger: logger,
	}
}

// Execute executes a command with retry logic.
func (e *ExecutorWithRetry) Execute(request *SSHRequest) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.Execute(request)
	})
}

// ExecuteWithTimeout executes a command with timeout and retry logic.
func (e *ExecutorWithRetry) ExecuteWithTimeout(request *SSHRequest, timeout time.Duration) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.ExecuteWithTimeout(request, timeout)
	})
}

// ExecuteWithStream executes a command with streaming and retry logic.
func (e *ExecutorWithRetry) ExecuteWithStream(request *SSHRequest, output io.Writer) (*SSHResponse, error) {
	return e.executeWithRetry(func() (*SSHResponse, error) {
		return e.executor.ExecuteWithStream(request, output)
	})
}

// executeWithRetry executes a function with retry logic.
func (e *ExecutorWithRetry) executeWithRetry(retryFunc func() (*SSHResponse, error)) (*SSHResponse, error) {
	var lastErr error

	var lastResponse *SSHResponse

	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			e.logger.Infof("Retrying SSH command execution, attempt %d/%d", attempt, e.maxRetries)
			time.Sleep(e.retryDelay)
		}

		response, err := retryFunc()
		if err == nil {
			if attempt > 0 {
				e.logger.Infof("SSH command succeeded after %d retries", attempt)
			}

			return response, nil
		}

		lastErr = err
		lastResponse = response

		// Check if we should retry this error
		if !e.retryPredicate(err) {
			e.logger.Debugf("SSH error not retryable: %v", err)

			break
		}

		if attempt < e.maxRetries {
			e.logger.Debugf("SSH command failed, will retry: %v", err)
		}
	}

	e.logger.Errorf("SSH command failed after %d retries: %v", e.maxRetries, lastErr)

	return lastResponse, lastErr
}

// BatchExecutor executes multiple SSH commands concurrently.
type BatchExecutor struct {
	executor   SSHExecutor
	maxWorkers int
	logger     Logger
}

// NewBatchExecutor creates a new batch executor.
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

// BatchRequest represents a request in a batch.
type BatchRequest struct {
	ID      string
	Request *SSHRequest
}

// BatchResult represents a result from a batch execution.
type BatchResult struct {
	ID       string
	Response *SSHResponse
	Error    error
}

// ExecuteBatch executes multiple SSH commands concurrently.
func (b *BatchExecutor) ExecuteBatch(requests []BatchRequest) []BatchResult {
	b.logger.Infof("Executing batch of %d SSH commands with %d workers", len(requests), b.maxWorkers)

	// Create channels for work distribution
	workChan := make(chan BatchRequest, len(requests))
	resultChan := make(chan BatchResult, len(requests))

	// Start workers
	for range b.maxWorkers {
		go b.worker(workChan, resultChan)
	}

	// Send work to workers
	for _, req := range requests {
		workChan <- req
	}

	close(workChan)

	// Collect results
	results := make([]BatchResult, 0, len(requests))

	for range requests {
		result := <-resultChan
		results = append(results, result)
	}

	b.logger.Infof("Batch execution completed")

	return results
}

// worker processes SSH requests from the work channel.
func (b *BatchExecutor) worker(workChan <-chan BatchRequest, resultChan chan<- BatchResult) {
	for req := range workChan {
		b.logger.Debugf("Worker processing request: %s", req.ID)

		response, err := b.executor.Execute(req.Request)

		resultChan <- BatchResult{
			ID:       req.ID,
			Response: response,
			Error:    err,
		}

		b.logger.Debugf("Worker completed request: %s", req.ID)
	}
}
