package broker_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/broker"
	"blacksmith/pkg/logger"
)

const (
	authTestPassword = "s3cret"
	authTestGuess    = "guess"
)

// The credential check must refuse a wrong username, a wrong password, and
// a pair that is wrong in both fields, and accept only the exact pair. The
// check compares in constant time, which a test cannot observe, so this
// covers the behaviour and the code carries the timing guarantee.
func TestBasicAuthAcceptsOnlyTheExactPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		password string
		want     int
	}{
		{"right pair", installUser, authTestPassword, http.StatusNoContent},
		{"wrong username", "root", authTestPassword, http.StatusForbidden},
		{"wrong password", installUser, authTestGuess, http.StatusForbidden},
		{"both wrong", "root", authTestGuess, http.StatusForbidden},
		{"username prefix", "adm", authTestPassword, http.StatusForbidden},
		{"password prefix", installUser, "s3c", http.StatusForbidden},
		{"password with extra tail", installUser, authTestPassword + "!", http.StatusForbidden},
		{"empty pair", "", "", http.StatusForbidden},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			inner := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			})
			api := broker.API{Username: installUser, Password: authTestPassword, Internal: inner, Primary: broker.NullHandler{}}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/status", nil)
			req.SetBasicAuth(testCase.username, testCase.password)

			recorder := httptest.NewRecorder()
			api.ServeHTTP(recorder, req)

			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

// A person sometimes types a password into the username field, so a failed
// login must leave neither field in the log at any level.
func TestFailedLoginLogsNoSubmittedCredential(t *testing.T) {
	t.Parallel()

	const (
		typedUser = "hunter2-typed-as-username"
		typedPass = "another-typed-secret"
	)

	recorder := &levelRecordingLogger{NoOpLogger: &logger.NoOpLogger{}}
	api := broker.API{
		Username: installUser,
		Password: authTestPassword,
		Internal: broker.NullHandler{},
		Primary:  broker.NullHandler{},
		Logger:   recorder,
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/status", nil)
	req.SetBasicAuth(typedUser, typedPass)
	req.RemoteAddr = "10.0.0.7:5000"

	response := httptest.NewRecorder()
	api.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	sawFailure := false

	for _, entry := range recorder.entries {
		if strings.Contains(entry, typedUser) || strings.Contains(entry, typedPass) {
			t.Fatalf("log line holds a submitted credential: %q", entry)
		}

		if strings.Contains(entry, "authentication failed") &&
			strings.Contains(entry, "10.0.0.7:5000") && strings.Contains(entry, "/b/status") {
			sawFailure = true
		}
	}

	if !sawFailure {
		t.Fatalf("no failed-login line with the address and route in %q", recorder.entries)
	}
}
