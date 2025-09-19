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

	timeout := e.resolveTimeout(request)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	startTime := time.Now()

	session, err := e.setupSession(request)
	if err != nil {
		return nil, err
	}

	defer func() { _ = session.Close() }()

	fullCmd, exitSentinel := e.buildCommandWithSentinel(request)

	err = session.SendInput([]byte(fullCmd))
	if err != nil {
		e.logger.Errorf("Failed to send command to SSH session: %v", err)

		return nil, NewSSHError(SSHErrorCodeExecution, "Failed to send command to SSH session", err)
	}

	return e.streamAndCaptureOutput(ctx, session, output, exitSentinel, timeout, startTime)
}

func (e *ExecutorImpl) resolveTimeout(request *SSHRequest) time.Duration {
	if request != nil && request.Timeout > 0 {
		return time.Duration(request.Timeout) * time.Second
	}

	return e.config.Timeout
}

func (e *ExecutorImpl) setupSession(request *SSHRequest) (SSHSession, error) {
	session, err := e.sshService.CreateSession(request)
	if err != nil {
		e.logger.Errorf("Failed to create SSH session: %v", err)

		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	err = session.Start()
	if err != nil {
		e.logger.Errorf("Failed to start SSH session: %v", err)

		return nil, fmt.Errorf("failed to start SSH session: %w", err)
	}

	return session, nil
}

func (e *ExecutorImpl) buildCommandWithSentinel(request *SSHRequest) (string, string) {
	cmdLine := request.Command
	if len(request.Args) > 0 {
		parts := make([]string, 0, len(request.Args)+1)

		parts = append(parts, cmdLine)
		for _, arg := range request.Args {
			parts = append(parts, shellSingleQuote(arg))
		}

		cmdLine = strings.Join(parts, " ")
	}

	const exitSentinel = "__BS_EXIT__:"

	fullCmd := fmt.Sprintf("set -o pipefail 2>/dev/null; %s; code=$?; echo %s$code; exit $code\n", cmdLine, exitSentinel)

	return fullCmd, exitSentinel
}

func (e *ExecutorImpl) streamAndCaptureOutput(ctx context.Context, session SSHSession, output io.Writer, exitSentinel string, timeout time.Duration, startTime time.Time) (*SSHResponse, error) {
	var (
		stdoutBuf bytes.Buffer
		tailBuf   strings.Builder
		exitCode  = -1
		writeErr  error
	)

	for {
		err := e.checkTimeout(ctx, timeout, startTime, &stdoutBuf)
		if err != nil {
			return nil, err
		}

		chunk, rerr := session.ReadOutput()
		if rerr != nil {
			if exitCode < 0 {
				e.logger.Errorf("SSH streaming read error: %v", rerr)

				return createSSHResponse(false, 1, time.Since(startTime), stdoutBuf.String(), rerr, startTime), rerr
			}

			break
		}

		if e.shouldBreakOnEmptyChunk(chunk, session) {
			break
		}

		if len(chunk) == 0 {
			continue
		}

		writeErr = e.forwardChunkToOutput(chunk, output, writeErr)
		e.accumulateChunk(chunk, &stdoutBuf, &tailBuf)
		e.trimTailBuffer(&tailBuf)

		if code := e.detectExitCode(&tailBuf, exitSentinel); code >= 0 {
			exitCode = code

			e.removeSentinelFromOutput(&stdoutBuf, exitSentinel)

			break
		}
	}

	return e.finalizeResponse(exitCode, writeErr, startTime, &stdoutBuf)
}

func (e *ExecutorImpl) checkTimeout(ctx context.Context, timeout time.Duration, startTime time.Time, stdoutBuf *bytes.Buffer) error {
	select {
	case <-ctx.Done():
		e.logger.Errorf("SSH command timed out after %v", timeout)

		resp := createSSHResponse(false, timeoutExitCode, time.Since(startTime), stdoutBuf.String(), ctx.Err(), startTime)

		return fmt.Errorf("%v: %w", resp, NewSSHError(SSHErrorCodeTimeout, "Command execution timed out", ctx.Err()))
	default:
		return nil
	}
}

func (e *ExecutorImpl) shouldBreakOnEmptyChunk(chunk []byte, session SSHSession) bool {
	if len(chunk) == 0 {
		st := session.Status()

		return st == SessionStatusClosed || st == SessionStatusError
	}

	return false
}

func (e *ExecutorImpl) forwardChunkToOutput(chunk []byte, output io.Writer, existingErr error) error {
	if output != nil && existingErr == nil {
		_, err := output.Write(chunk)
		if err != nil {
			e.logger.Errorf("Failed to write output to stream: %v", err)

			return fmt.Errorf("failed to write output: %w", err)
		}
	}

	return existingErr
}

func (e *ExecutorImpl) accumulateChunk(chunk []byte, stdoutBuf *bytes.Buffer, tailBuf *strings.Builder) {
	stdoutBuf.Write(chunk)
	tailBuf.Write(chunk)
}

func (e *ExecutorImpl) trimTailBuffer(tailBuf *strings.Builder) {
	if tailBuf.Len() > maxTailBufferSize {
		s := tailBuf.String()
		if len(s) > tailBufferTrimSize {
			tailBuf.Reset()
			tailBuf.WriteString(s[len(s)-tailBufferTrimSize:])
		}
	}
}

func (e *ExecutorImpl) detectExitCode(tailBuf *strings.Builder, exitSentinel string) int {
	idx := strings.LastIndex(tailBuf.String(), exitSentinel)
	if idx == -1 {
		return -1
	}

	sentinelSuffix := tailBuf.String()[idx+len(exitSentinel):]

	digitCount := 0
	for digitCount < len(sentinelSuffix) && sentinelSuffix[digitCount] >= '0' && sentinelSuffix[digitCount] <= '9' {
		digitCount++
	}

	if digitCount > 0 {
		var code int

		_, _ = fmt.Sscanf(sentinelSuffix[:digitCount], "%d", &code)

		return code
	}

	return -1
}

func (e *ExecutorImpl) removeSentinelFromOutput(stdoutBuf *bytes.Buffer, exitSentinel string) {
	out := stdoutBuf.String()
	if cut := strings.LastIndex(out, exitSentinel); cut != -1 {
		cleaned := out[:cut]

		stdoutBuf.Reset()
		stdoutBuf.WriteString(cleaned)
	}
}

func (e *ExecutorImpl) finalizeResponse(exitCode int, writeErr error, startTime time.Time, stdoutBuf *bytes.Buffer) (*SSHResponse, error) {
	duration := time.Since(startTime)

	if writeErr != nil {
		resp := createSSHResponse(exitCode == 0, exitCode, duration, stdoutBuf.String(), writeErr, startTime)

		return resp, NewSSHError(SSHErrorCodeInternal, "Failed to write output to stream", writeErr)
	}

	if exitCode < 0 {
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
