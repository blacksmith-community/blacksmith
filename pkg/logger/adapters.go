package logger

// BoshLogger provides compatibility with bosh/director_adapter.Logger interface.
type BoshLogger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// ReconcilerLogger provides compatibility with pkg/reconciler.Logger interface.
type ReconcilerLogger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// ShieldLogger provides compatibility with shield.Logger interface.
type ShieldLogger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// CFLogger provides compatibility with pkg/services/cf.Logger interface.
type CFLogger interface {
	Warnf(format string, args ...interface{})
}

// Ensure our Implementation satisfies all adapter interfaces.
var (
	_ BoshLogger       = (*Implementation)(nil)
	_ ReconcilerLogger = (*Implementation)(nil)
	_ ShieldLogger     = (*Implementation)(nil)
	_ CFLogger         = (*Implementation)(nil)
)

// AsBoshLogger returns the logger as a BoshLogger interface.
func (l *Implementation) AsBoshLogger() BoshLogger {
	return l
}

// AsReconcilerLogger returns the logger as a ReconcilerLogger interface.
func (l *Implementation) AsReconcilerLogger() ReconcilerLogger {
	return l
}

// AsShieldLogger returns the logger as a ShieldLogger interface.
func (l *Implementation) AsShieldLogger() ShieldLogger {
	return l
}

// AsCFLogger returns the logger as a CFLogger interface.
func (l *Implementation) AsCFLogger() CFLogger {
	return l
}

// NoOpLogger provides a no-operation logger that satisfies all interfaces.
type NoOpLogger struct{}

func (n *NoOpLogger) Debugf(format string, args ...interface{})   {}
func (n *NoOpLogger) Infof(format string, args ...interface{})    {}
func (n *NoOpLogger) Warnf(format string, args ...interface{})    {}
func (n *NoOpLogger) Warningf(format string, args ...interface{}) {}
func (n *NoOpLogger) Errorf(format string, args ...interface{})   {}
func (n *NoOpLogger) Fatalf(format string, args ...interface{})   {}

func (n *NoOpLogger) Debug(msg string, args ...interface{})   {}
func (n *NoOpLogger) Info(msg string, args ...interface{})    {}
func (n *NoOpLogger) Warn(msg string, args ...interface{})    {}
func (n *NoOpLogger) Warning(msg string, args ...interface{}) {}
func (n *NoOpLogger) Error(msg string, args ...interface{})   {}
func (n *NoOpLogger) Fatal(msg string, args ...interface{})   {}

func (n *NoOpLogger) SetLevel(level string) error { return nil }
func (n *NoOpLogger) GetLevel() string            { return "off" }

func (n *NoOpLogger) WithField(key string, value interface{}) Logger {
	return n
}

func (n *NoOpLogger) WithFields(fields map[string]interface{}) Logger {
	return n
}

func (n *NoOpLogger) Named(name string) Logger {
	return n
}

// Ensure NoOpLogger satisfies all interfaces.
var (
	_ Logger           = (*NoOpLogger)(nil)
	_ BoshLogger       = (*NoOpLogger)(nil)
	_ ReconcilerLogger = (*NoOpLogger)(nil)
	_ ShieldLogger     = (*NoOpLogger)(nil)
	_ CFLogger         = (*NoOpLogger)(nil)
)

// NewNoOpLogger creates a new no-operation logger.
func NewNoOpLogger() Logger {
	return &NoOpLogger{}
}
