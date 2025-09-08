package adapters

import (
	"fmt"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

// LoggerAdapter adapts the existing StandardLog to the interfaces.Logger interface.
type LoggerAdapter struct {
	standardLog *logger.StandardLog
}

// NewLoggerAdapter creates a new logger adapter.
func NewLoggerAdapter(standardLog *logger.StandardLog) interfaces.Logger {
	return &LoggerAdapter{
		standardLog: standardLog,
	}
}

// Wrap creates a new logger with additional context.
func (l *LoggerAdapter) Wrap(ctxString string) interfaces.Logger {
	return NewLoggerAdapter(l.standardLog.Wrap("%s", ctxString))
}

// Debug logs a debug message.
func (l *LoggerAdapter) Debug(format string, args ...interface{}) {
	l.standardLog.Debug(format, args...)
}

// Info logs an info message.
func (l *LoggerAdapter) Info(format string, args ...interface{}) {
	l.standardLog.Info(format, args...)
}

// Error logs an error message.
func (l *LoggerAdapter) Error(format string, args ...interface{}) {
	l.standardLog.Error(format, args...)
}

// Debugf logs a debug message with formatting.
func (l *LoggerAdapter) Debugf(format string, args ...interface{}) {
	l.standardLog.Debugf(format, args...)
}

// Infof logs an info message with formatting.
func (l *LoggerAdapter) Infof(format string, args ...interface{}) {
	l.standardLog.Infof(format, args...)
}

// Errorf logs an error message with formatting.
func (l *LoggerAdapter) Errorf(format string, args ...interface{}) {
	l.standardLog.Errorf(format, args...)
}

// Warnf logs a warning message with formatting.
func (l *LoggerAdapter) Warnf(format string, args ...interface{}) {
	l.standardLog.Warnf(format, args...)
}

// Warningf is an alias for Warnf.
func (l *LoggerAdapter) Warningf(format string, args ...interface{}) {
	l.standardLog.Warnf(format, args...)
}

// Fatalf logs a fatal message and exits.
func (l *LoggerAdapter) Fatalf(format string, args ...interface{}) {
	l.standardLog.Errorf(format, args...)
	panic("fatal error")
}

// Warn logs a warning message.
func (l *LoggerAdapter) Warn(msg string, args ...interface{}) {
	l.standardLog.Error(msg, args...) // StandardLog doesn't have Warn, use Error
}

// Warning is an alias for Warn.
func (l *LoggerAdapter) Warning(msg string, args ...interface{}) {
	l.standardLog.Error(msg, args...) // StandardLog doesn't have Warn, use Error
}

// Fatal logs a fatal message and exits.
func (l *LoggerAdapter) Fatal(msg string, args ...interface{}) {
	l.standardLog.Error(msg, args...)
	panic("fatal error")
}

// SetLevel sets the logging level (not supported by StandardLog).
func (l *LoggerAdapter) SetLevel(level string) error {
	// StandardLog doesn't support dynamic level setting
	return nil
}

// GetLevel gets the logging level (not supported by StandardLog).
func (l *LoggerAdapter) GetLevel() string {
	return "info" // Default level
}

// WithField creates a logger with a field (simplified for StandardLog).
func (l *LoggerAdapter) WithField(key string, value interface{}) interfaces.Logger {
	ctx := fmt.Sprintf("%s=%v", key, value)

	return NewLoggerAdapter(l.standardLog.Wrap("%s", ctx))
}

// WithFields creates a logger with multiple fields (simplified for StandardLog).
func (l *LoggerAdapter) WithFields(fields map[string]interface{}) interfaces.Logger {
	ctx := ""
	for k, v := range fields {
		if ctx != "" {
			ctx += ","
		}

		ctx += fmt.Sprintf("%s=%v", k, v)
	}

	return NewLoggerAdapter(l.standardLog.Wrap("%s", ctx))
}

// Named creates a named logger.
func (l *LoggerAdapter) Named(name string) interfaces.Logger {
	return NewLoggerAdapter(l.standardLog.Wrap("%s", name))
}
