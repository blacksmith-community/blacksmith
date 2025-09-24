package logger

import (
	"fmt"
	"os"
	"strings"
)

const (
	levelDebug   = "debug"
	levelInfo    = "info"
	levelWarn    = "warn"
	levelWarning = "warning"
	levelError   = "error"
	levelFatal   = "fatal"
)

// InitFromEnv initializes the global logger from environment variables.
func InitFromEnv() error {
	config := Config{
		Level:      getEnvOrDefault("LOG_LEVEL", "BLACKSMITH_LOG_LEVEL", "info"),
		Format:     getEnvOrDefault("LOG_FORMAT", "BLACKSMITH_LOG_FORMAT", "console"),
		OutputPath: getEnvOrDefault("LOG_OUTPUT", "BLACKSMITH_LOG_OUTPUT", "stdout"),
	}

	// Check for DEBUG environment variable as override
	if os.Getenv("DEBUG") != "" || os.Getenv("BLACKSMITH_DEBUG") != "" {
		config.Level = levelDebug
	}

	return Configure(config)
}

// getEnvOrDefault checks multiple environment variables and returns the first non-empty value or default.
func getEnvOrDefault(envVars ...string) string {
	defaultValue := envVars[len(envVars)-1]
	for i := range len(envVars) - 1 {
		if val := os.Getenv(envVars[i]); val != "" {
			return val
		}
	}

	return defaultValue
}

// NewForPackage creates a named logger for a specific package.
func NewForPackage(packageName string) Logger {
	return Get().Named(packageName)
}

// NewWithConfig creates a new logger with the given configuration.
func NewWithConfig(level, format, output string) (Logger, error) {
	config := Config{
		Level:      level,
		Format:     format,
		OutputPath: output,
	}

	return New(config)
}

// MustNewWithConfig creates a new logger with the given configuration or panics.
func MustNewWithConfig(level, format, output string) Logger {
	logger, err := NewWithConfig(level, format, output)
	if err != nil {
		panic(fmt.Sprintf("failed to create logger: %v", err))
	}

	return logger
}

// ParseLevel converts a string level to the appropriate level constant.
func ParseLevel(level string) string {
	switch strings.ToLower(level) {
	case levelDebug, "trace", "verbose":
		return levelDebug
	case levelInfo, "information":
		return levelInfo
	case levelWarn, levelWarning:
		return levelWarn
	case levelError, "err":
		return levelError
	case levelFatal, "panic", "critical":
		return levelFatal
	default:
		return levelInfo
	}
}

// SetGlobalLevel sets the global logger's level.
func SetGlobalLevel(level string) error {
	err := Get().SetLevel(ParseLevel(level))
	if err != nil {
		return fmt.Errorf("failed to set global logger level: %w", err)
	}

	return nil
}

// EnableDebug enables debug logging globally.
func EnableDebug() {
	_ = SetGlobalLevel(levelDebug)
}

// DisableDebug disables debug logging globally (sets to info level).
func DisableDebug() {
	_ = SetGlobalLevel(levelInfo)
}

// IsDebugEnabled returns true if debug logging is enabled.
func IsDebugEnabled() bool {
	return strings.ToLower(Get().GetLevel()) == levelDebug
}

// CreateTestLogger creates a logger suitable for testing.
func CreateTestLogger() Logger {
	config := Config{
		Level:      levelDebug,
		Format:     "console",
		OutputPath: "stdout",
	}
	logger, _ := New(config)

	return logger
}

// CreateSilentLogger creates a no-op logger for silent operation.
func CreateSilentLogger() Logger {
	return NewNoOpLogger()
}
