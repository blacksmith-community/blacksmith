package reconciler

// Mock implementations for testing

type mockLogger struct {
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func newMockLogger() *mockLogger {
	return &mockLogger{
		debugLogs: []string{},
		infoLogs:  []string{},
		warnLogs:  []string{},
		errorLogs: []string{},
	}
}

func (l *mockLogger) Debug(format string, args ...interface{}) {
	l.debugLogs = append(l.debugLogs, format)
}

func (l *mockLogger) Info(format string, args ...interface{}) {
	l.infoLogs = append(l.infoLogs, format)
}

func (l *mockLogger) Warning(format string, args ...interface{}) {
	l.warnLogs = append(l.warnLogs, format)
}

func (l *mockLogger) Error(format string, args ...interface{}) {
	l.errorLogs = append(l.errorLogs, format)
}
