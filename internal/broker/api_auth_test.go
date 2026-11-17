package broker_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/broker"
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
