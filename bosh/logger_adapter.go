package bosh

// LoggerAdapter adapts the application's Log type to the Logger interface
type LoggerAdapter struct {
	log interface {
		Info(format string, args ...interface{})
		Debug(format string, args ...interface{})
		Error(format string, args ...interface{})
	}
}

// NewLoggerAdapter creates a new logger adapter
func NewLoggerAdapter(log interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Error(format string, args ...interface{})
}) *LoggerAdapter {
	return &LoggerAdapter{log: log}
}

// Info logs an info message
func (l *LoggerAdapter) Info(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Info(format, args...)
	}
}

// Debug logs a debug message
func (l *LoggerAdapter) Debug(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Debug(format, args...)
	}
}

// Error logs an error message
func (l *LoggerAdapter) Error(format string, args ...interface{}) {
	if l.log != nil {
		l.log.Error(format, args...)
	}
}
