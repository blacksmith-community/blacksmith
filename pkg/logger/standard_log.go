package logger

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// StandardLog provides a standard logging implementation with context and audit features
// It wraps the centralized logger while providing additional functionality.
type StandardLog struct {
	logger    Logger
	ctx       []string
	up        *StandardLog
	debugging bool

	// Audit ring buffer
	mu    sync.Mutex
	index int
	ring  []string
	max   int
}

// NewStandardLog creates a new standard log adapter.
func NewStandardLog(logger Logger, maxEntries int) *StandardLog {
	return &StandardLog{
		logger:    logger,
		max:       maxEntries,
		index:     0,
		ring:      make([]string, 0),
		debugging: IsDebugEnabled(),
	}
}

// GetLogger returns the underlying logger (for compatibility).
func (l *StandardLog) GetLogger() Logger {
	return l.logger
}

// Full returns true if the ring buffer is full.
func (l *StandardLog) Full() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.ring) == l.max
}

// String returns the accumulated audit log.
func (l *StandardLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ring == nil {
		return ""
	}

	if len(l.ring) == l.max {
		return strings.Join(l.ring[l.index:l.max], "") +
			strings.Join(l.ring[0:l.index], "")
	}

	return strings.Join(l.ring, "")
}

// Audit adds a message to the ring buffer.
func (l *StandardLog) Audit(msg string) {
	if l.up != nil {
		l.up.Audit(msg)

		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ring == nil {
		return
	}

	if len(l.ring) == l.max {
		l.ring[l.index] = msg
		l.index = (l.index + 1) % l.max

		return
	}

	l.ring = append(l.ring, msg)
}

// Wrap creates a child logger with additional context.
func (l *StandardLog) Wrap(f string, args ...interface{}) *StandardLog {
	contextStr := fmt.Sprintf(f, args...)

	// Create a named logger with the context
	var namedLogger Logger

	if len(l.ctx) > 0 {
		// Build full context path
		fullContext := strings.Join(append(l.ctx, contextStr), ".")
		namedLogger = l.logger.Named(fullContext)
	} else {
		namedLogger = l.logger.Named(contextStr)
	}

	return &StandardLog{
		up:        l,
		logger:    namedLogger,
		ctx:       append(l.ctx, contextStr),
		debugging: l.debugging,
	}
}

// Debug logs a debug message.
func (l *StandardLog) Debug(f string, args ...interface{}) {
	if l.debugging || (l.up != nil && l.up.debugging) {
		l.printf("DEBUG", f, args...)
	}
}

// Info logs an info message.
func (l *StandardLog) Info(f string, args ...interface{}) {
	l.printf("INFO", f, args...)
}

// Error logs an error message.
func (l *StandardLog) Error(f string, args ...interface{}) {
	l.printf("ERROR", f, args...)
}

// Format method aliases for compatibility.
func (l *StandardLog) Debugf(f string, args ...interface{}) {
	l.Debug(f, args...)
}

func (l *StandardLog) Infof(f string, args ...interface{}) {
	l.Info(f, args...)
}

func (l *StandardLog) Errorf(f string, args ...interface{}) {
	l.Error(f, args...)
}

func (l *StandardLog) Warnf(f string, args ...interface{}) {
	l.Info("[WARN] "+f, args...)
}

// SetDebugging enables or disables debug mode.
func (l *StandardLog) SetDebugging(enabled bool) {
	l.debugging = enabled
	if enabled {
		_ = l.logger.SetLevel("debug")
	} else {
		_ = l.logger.SetLevel("info")
	}
}

// printf formats and logs a message with level.
func (l *StandardLog) printf(lvl, f string, args ...interface{}) {
	message := fmt.Sprintf(f, args...)

	// Add to audit ring
	now := time.Now().Format("2006-01-02 15:04:05.000")

	var auditMsg string
	if len(l.ctx) == 0 {
		auditMsg = fmt.Sprintf("%s %-5s  %s\n", now, lvl, message)
	} else {
		auditMsg = fmt.Sprintf("%s %-5s  [%s] %s\n", now, lvl, strings.Join(l.ctx, " / "), message)
	}

	l.Audit(auditMsg)

	// Log using the new logger (context is already in the named logger)
	switch lvl {
	case "DEBUG":
		l.logger.Debug(message)
	case "INFO":
		l.logger.Info(message)
	case "ERROR":
		l.logger.Error(message)
	default:
		l.logger.Info(message)
	}
}

// CreateStandardLogger creates a standard logger using the global logger.
func CreateStandardLogger(maxEntries int) *StandardLog {
	return NewStandardLog(Get(), maxEntries)
}

// CreateStandardLoggerWithConfig creates a standard logger with specific configuration.
func CreateStandardLoggerWithConfig(debug bool, maxEntries int) *StandardLog {
	config := Config{
		Level:  levelInfo,
		Format: "console",
	}
	if debug {
		config.Level = levelDebug
	}

	logger, _ := New(config)
	standardLog := NewStandardLog(logger, maxEntries)
	standardLog.debugging = debug

	return standardLog
}
