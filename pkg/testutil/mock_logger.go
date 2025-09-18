package testutil

// IntegrationMockLogger implements reconciler.Logger interface for testing.
type IntegrationMockLogger struct{}

// Debugf implements the Logger interface.
func (ml *IntegrationMockLogger) Debugf(format string, args ...interface{}) {}

// Infof implements the Logger interface.
func (ml *IntegrationMockLogger) Infof(format string, args ...interface{}) {}

// Errorf implements the Logger interface.
func (ml *IntegrationMockLogger) Errorf(format string, args ...interface{}) {}

// Warningf implements the Logger interface.
func (ml *IntegrationMockLogger) Warningf(format string, args ...interface{}) {}
