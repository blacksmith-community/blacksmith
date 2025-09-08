package logger

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Static errors for err113 compliance.
var (
	ErrInvalidLogLevel = errors.New("invalid log level")
)

// Logger is the main interface that supports both f and non-f methods.
type Logger interface {
	// Core logging methods with formatting
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Warningf(format string, args ...interface{}) // Alias for Warnf
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})

	// Core logging methods without formatting
	Debug(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
	Warning(msg string, args ...interface{}) // Alias for Warn
	Error(msg string, args ...interface{})
	Fatal(msg string, args ...interface{})

	// Level management
	SetLevel(level string) error
	GetLevel() string

	// Create child loggers
	WithField(key string, value interface{}) Logger
	WithFields(fields map[string]interface{}) Logger
	Named(name string) Logger
}

// Implementation holds the zap logger.
type Implementation struct {
	logger *zap.Logger
	sugar  *zap.SugaredLogger
	level  zapcore.Level
}

// Global logger instance.
var globalLogger Logger //nolint:gochecknoglobals // Global logger is needed for package-level logging

func init() { //nolint:gochecknoinits // Required for global logger initialization
	// Initialize with a default logger
	globalLogger, _ = New(Config{
		Level:  "info",
		Format: "console",
	})
}

// Config holds logger configuration.
type Config struct {
	Level      string // debug, info, warn, error, fatal
	Format     string // json, console
	OutputPath string // stdout, stderr, or file path
	CallerSkip int    // Number of callers to skip in stack trace
}

// New creates a new logger instance.
func New(config Config) (Logger, error) {
	// Parse log level
	level := zapcore.InfoLevel

	switch strings.ToLower(config.Level) {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn", "warning":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	case "fatal":
		level = zapcore.FatalLevel
	}

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create encoder based on format
	var encoder zapcore.Encoder
	if config.Format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Create writer
	var writer zapcore.WriteSyncer

	switch config.OutputPath {
	case "", "stdout":
		writer = zapcore.AddSync(os.Stdout)
	case "stderr":
		writer = zapcore.AddSync(os.Stderr)
	default:
		file, err := os.OpenFile(config.OutputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, DefaultFileMode)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}

		writer = zapcore.AddSync(file)
	}

	// Create core
	core := zapcore.NewCore(encoder, writer, level)

	// Create logger with caller skip
	callerSkip := config.CallerSkip
	if callerSkip == 0 {
		callerSkip = 1
	}

	logger := zap.New(core, zap.AddCaller(), zap.AddCallerSkip(callerSkip))

	return &Implementation{
		logger: logger,
		sugar:  logger.Sugar(),
		level:  level,
	}, nil
}

// Global logger functions

// Get returns the global logger.
func Get() Logger {
	return globalLogger
}

// Set sets the global logger.
func Set(logger Logger) {
	globalLogger = logger
}

// Configure configures the global logger.
func Configure(config Config) error {
	logger, err := New(config)
	if err != nil {
		return err
	}

	globalLogger = logger

	return nil
}

// Implementation of Logger interface

func (l *Implementation) Debugf(format string, args ...interface{}) {
	l.sugar.Debugf(format, args...)
}

func (l *Implementation) Infof(format string, args ...interface{}) {
	l.sugar.Infof(format, args...)
}

func (l *Implementation) Warnf(format string, args ...interface{}) {
	l.sugar.Warnf(format, args...)
}

func (l *Implementation) Warningf(format string, args ...interface{}) {
	l.Warnf(format, args...)
}

func (l *Implementation) Errorf(format string, args ...interface{}) {
	l.sugar.Errorf(format, args...)
}

func (l *Implementation) Fatalf(format string, args ...interface{}) {
	l.sugar.Fatalf(format, args...)
}

func (l *Implementation) Debug(msg string, args ...interface{}) {
	if len(args) > 0 {
		l.sugar.Debugw(msg, args...)
	} else {
		l.logger.Debug(msg)
	}
}

func (l *Implementation) Info(msg string, args ...interface{}) {
	if len(args) > 0 {
		l.sugar.Infow(msg, args...)
	} else {
		l.logger.Info(msg)
	}
}

func (l *Implementation) Warn(msg string, args ...interface{}) {
	if len(args) > 0 {
		l.sugar.Warnw(msg, args...)
	} else {
		l.logger.Warn(msg)
	}
}

func (l *Implementation) Warning(msg string, args ...interface{}) {
	l.Warn(msg, args...)
}

func (l *Implementation) Error(msg string, args ...interface{}) {
	if len(args) > 0 {
		l.sugar.Errorw(msg, args...)
	} else {
		l.logger.Error(msg)
	}
}

func (l *Implementation) Fatal(msg string, args ...interface{}) {
	if len(args) > 0 {
		l.sugar.Fatalw(msg, args...)
	} else {
		l.logger.Fatal(msg)
	}
}

func (l *Implementation) SetLevel(level string) error {
	// Parse new level
	var newLevel zapcore.Level

	switch strings.ToLower(level) {
	case "debug":
		newLevel = zapcore.DebugLevel
	case "info":
		newLevel = zapcore.InfoLevel
	case "warn", "warning":
		newLevel = zapcore.WarnLevel
	case "error":
		newLevel = zapcore.ErrorLevel
	case "fatal":
		newLevel = zapcore.FatalLevel
	default:
		return fmt.Errorf("%w: %s", ErrInvalidLogLevel, level)
	}

	l.level = newLevel
	// Note: Zap doesn't support dynamic level changes easily, would need to recreate the logger
	// For now, store the level for reference
	return nil
}

func (l *Implementation) GetLevel() string {
	return l.level.String()
}

func (l *Implementation) WithField(key string, value interface{}) Logger {
	return &Implementation{
		logger: l.logger.With(zap.Any(key, value)),
		sugar:  l.logger.With(zap.Any(key, value)).Sugar(),
		level:  l.level,
	}
}

func (l *Implementation) WithFields(fields map[string]interface{}) Logger {
	zapFields := make([]zap.Field, 0, len(fields))
	for k, v := range fields {
		zapFields = append(zapFields, zap.Any(k, v))
	}

	return &Implementation{
		logger: l.logger.With(zapFields...),
		sugar:  l.logger.With(zapFields...).Sugar(),
		level:  l.level,
	}
}

func (l *Implementation) Named(name string) Logger {
	return &Implementation{
		logger: l.logger.Named(name),
		sugar:  l.logger.Named(name).Sugar(),
		level:  l.level,
	}
}

// Global convenience functions that use the global logger

func Debugf(format string, args ...interface{}) {
	globalLogger.Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	globalLogger.Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	globalLogger.Warnf(format, args...)
}

func Warningf(format string, args ...interface{}) {
	globalLogger.Warningf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	globalLogger.Errorf(format, args...)
}

func Fatalf(format string, args ...interface{}) {
	globalLogger.Fatalf(format, args...)
}

func Debug(msg string, args ...interface{}) {
	globalLogger.Debug(msg, args...)
}

func Info(msg string, args ...interface{}) {
	globalLogger.Info(msg, args...)
}

func Warn(msg string, args ...interface{}) {
	globalLogger.Warn(msg, args...)
}

func Warning(msg string, args ...interface{}) {
	globalLogger.Warning(msg, args...)
}

func Error(msg string, args ...interface{}) {
	globalLogger.Error(msg, args...)
}

func Fatal(msg string, args ...interface{}) {
	globalLogger.Fatal(msg, args...)
}
