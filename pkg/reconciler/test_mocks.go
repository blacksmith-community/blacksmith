package reconciler_test

import "sync"

// Mock implementations for testing

type MockLoggerLocal struct {
	mu        sync.Mutex
	debugLogs []string
	infoLogs  []string
	warnLogs  []string
	errorLogs []string
}

func NewMockLoggerLocal() *MockLoggerLocal {
	return &MockLoggerLocal{
		debugLogs: []string{},
		infoLogs:  []string{},
		warnLogs:  []string{},
		errorLogs: []string{},
	}
}

func (l *MockLoggerLocal) Debugf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.debugLogs = append(l.debugLogs, format)
}

func (l *MockLoggerLocal) Infof(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.infoLogs = append(l.infoLogs, format)
}

func (l *MockLoggerLocal) Warningf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.warnLogs = append(l.warnLogs, format)
}

func (l *MockLoggerLocal) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errorLogs = append(l.errorLogs, format)
}
