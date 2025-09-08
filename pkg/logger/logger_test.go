package logger_test

import (
	"testing"

	. "blacksmith/pkg/logger"
)

func TestLoggerCreation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "debug level console",
			config: Config{
				Level:  "debug",
				Format: "console",
			},
		},
		{
			name: "info level json",
			config: Config{
				Level:  "info",
				Format: "json",
			},
		},
		{
			name: "error level",
			config: Config{
				Level:  "error",
				Format: "console",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, err := New(tt.config)
			if err != nil {
				t.Fatalf("Failed to create logger: %v", err)
			}

			if logger == nil {
				t.Fatal("Logger is nil")
			}
		})
	}
}

func TestLoggerMethods(t *testing.T) {
	t.Parallel()
	// Create a test logger
	logger, err := New(Config{
		Level:  "debug",
		Format: "console",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Test formatted methods
	logger.Debugf("Debug message: %s", "test")
	logger.Infof("Info message: %s", "test")
	logger.Warnf("Warn message: %s", "test")
	logger.Warningf("Warning message: %s", "test")
	logger.Errorf("Error message: %s", "test")

	// Test non-formatted methods
	logger.Debug("Debug message")
	logger.Info("Info message")
	logger.Warn("Warn message")
	logger.Warning("Warning message")
	logger.Error("Error message")

	// Test with fields
	logger.Debug("Message with fields", "key", "value", "number", 42)
	logger.Info("Info with fields", "key", "value")
}

func TestLoggerLevels(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{
		Level:  "info",
		Format: "console",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Test level getters
	level := logger.GetLevel()
	if level != "info" {
		t.Errorf("Expected level 'info', got '%s'", level)
	}

	// Test level setters
	err = logger.SetLevel("debug")
	if err != nil {
		t.Errorf("Failed to set level: %v", err)
	}

	err = logger.SetLevel("invalid")
	if err == nil {
		t.Error("Expected error for invalid level")
	}
}

func TestLoggerWithField(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{
		Level:  "debug",
		Format: "console",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	// Test WithField
	childLogger := logger.WithField("component", "test")
	if childLogger == nil {
		t.Fatal("WithField returned nil")
	}

	childLogger.Info("Message from child logger")

	// Test WithFields
	childLogger2 := logger.WithFields(map[string]interface{}{
		"component": "test2",
		"version":   "1.0",
	})
	if childLogger2 == nil {
		t.Fatal("WithFields returned nil")
	}

	childLogger2.Info("Message from child logger 2")
}

func TestLoggerNamed(t *testing.T) {
	t.Parallel()

	logger, err := New(Config{
		Level:  "debug",
		Format: "console",
	})
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	namedLogger := logger.Named("subsystem")
	if namedLogger == nil {
		t.Fatal("Named returned nil")
	}

	namedLogger.Info("Message from named logger")
}

func TestGlobalLogger(t *testing.T) {
	// Test InitFromEnv
	t.Setenv("LOG_LEVEL", "debug")

	err := InitFromEnv()
	if err != nil {
		t.Fatalf("Failed to initialize from env: %v", err)
	}

	// Test global logger functions
	globalLogger := Get()
	if globalLogger == nil {
		t.Fatal("Global logger is nil")
	}

	// Test global convenience functions
	Debugf("Debug: %s", "test")
	Infof("Info: %s", "test")
	Warnf("Warn: %s", "test")
	Warningf("Warning: %s", "test")
	Errorf("Error: %s", "test")

	Debug("Debug message")
	Info("Info message")
	Warn("Warn message")
	Warning("Warning message")
	Error("Error message")
}

func TestNoOpLogger(t *testing.T) {
	t.Parallel()

	logger := NewNoOpLogger()
	if logger == nil {
		t.Fatal("NoOpLogger is nil")
	}

	// These should not panic
	logger.Debugf("Debug: %s", "test")
	logger.Infof("Info: %s", "test")
	logger.Warnf("Warn: %s", "test")
	logger.Warningf("Warning: %s", "test")
	logger.Errorf("Error: %s", "test")

	logger.Debug("Debug")
	logger.Info("Info")
	logger.Warn("Warn")
	logger.Warning("Warning")
	logger.Error("Error")

	_ = logger.SetLevel("debug")

	level := logger.GetLevel()
	if level != "off" {
		t.Errorf("Expected NoOpLogger level 'off', got '%s'", level)
	}

	child := logger.WithField("key", "value")
	if child != logger {
		t.Error("NoOpLogger WithField should return self")
	}

	child = logger.WithFields(map[string]interface{}{"key": "value"})
	if child != logger {
		t.Error("NoOpLogger WithFields should return self")
	}

	child = logger.Named("test")
	if child != logger {
		t.Error("NoOpLogger Named should return self")
	}
}

func TestStandardLogAdapter(t *testing.T) {
	t.Parallel()
	// Test standard logger creation
	standard := CreateStandardLogger(1024)
	if standard == nil {
		t.Fatal("Standard logger is nil")
	}

	// Test standard methods
	standard.Debug("Debug message")
	standard.Info("Info message")
	standard.Error("Error message")

	standard.Debugf("Debug: %s", "test")
	standard.Infof("Info: %s", "test")
	standard.Errorf("Error: %s", "test")
	standard.Warnf("Warn: %s", "test")

	// Test wrap
	wrapped := standard.Wrap("component")
	if wrapped == nil {
		t.Fatal("Wrapped logger is nil")
	}

	wrapped.Info("Message from wrapped logger")

	// Test audit ring buffer
	audit := standard.String()
	if audit == "" {
		t.Log("Audit log is empty (expected for small buffer)")
	}
}
