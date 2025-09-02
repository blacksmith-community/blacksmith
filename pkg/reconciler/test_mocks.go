package reconciler

import "sync"

// Mock implementations for testing

type mockLogger struct {
	mu        sync.Mutex
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugLogs = append(l.debugLogs, format)
}

func (l *mockLogger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infoLogs = append(l.infoLogs, format)
}

func (l *mockLogger) Warning(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnLogs = append(l.warnLogs, format)
}

func (l *mockLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errorLogs = append(l.errorLogs, format)
}
