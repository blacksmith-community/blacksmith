package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

var errVaultDown = errors.New("dial tcp: connection refused")

// credentialVault answers a read of an instance's credentials with the
// values the test sets. Any other call panics on the nil embedded Vault.
type credentialVault struct {
	interfaces.Vault

	creds  map[string]interface{}
	exists bool
	err    error
}

func (v *credentialVault) Get(_ context.Context, _ string, result interface{}) (bool, error) {
	if v.err != nil || !v.exists {
		return v.exists, v.err
	}

	raw, err := json.Marshal(v.creds)
	if err != nil {
		return false, err //nolint:wrapcheck // A test double.
	}

	return true, json.Unmarshal(raw, result) //nolint:wrapcheck // A test double.
}

func getCredentials(t *testing.T, vault interfaces.Vault) *httptest.ResponseRecorder {
	t.Helper()

	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: vault})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/redis-abc/credentials", nil)

	handler.GetInstanceCredentials(recorder, req, "redis-abc")

	return recorder
}

// An instance whose credentials the broker cannot read never gets a made-up
// set. The answer is an error that says why.
func TestInstanceCredentialsAreNeverMadeUp(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		vault  interfaces.Vault
		status int
		code   string
	}{
		"nothing stored":    {&credentialVault{}, http.StatusNotFound, apierr.CodeNotFound},
		"an empty record":   {&credentialVault{exists: true, creds: map[string]interface{}{}}, http.StatusNotFound, apierr.CodeNotFound},
		"vault unreachable": {&credentialVault{err: errVaultDown}, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable},
		"no vault":          {nil, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := getCredentials(t, testCase.vault)
			body := decodeBody(t, recorder)

			if recorder.Code != testCase.status {
				t.Fatalf("status %d, want %d: %s", recorder.Code, testCase.status, recorder.Body.String())
			}

			if body["code"] != testCase.code {
				t.Errorf("code %v, want %s", body["code"], testCase.code)
			}

			message, _ := body["error"].(string)
			if !strings.Contains(message, "credentials are not available") {
				t.Errorf("the error %q does not say the credentials are not available", message)
			}

			for _, made := range []string{"10.0.0.1", "admin", "***"} {
				if strings.Contains(recorder.Body.String(), made) {
					t.Errorf("the answer carries the placeholder %q: %s", made, recorder.Body.String())
				}
			}

			if strings.Contains(recorder.Body.String(), errVaultDown.Error()) {
				t.Errorf("the answer repeats the Vault error text: %s", recorder.Body.String())
			}
		})
	}
}

// Stored credentials come back as they are.
func TestInstanceCredentialsAnswerTheStoredSet(t *testing.T) {
	t.Parallel()

	stored := map[string]interface{}{"host": "10.1.2.3", "username": "app"}
	recorder := getCredentials(t, &credentialVault{exists: true, creds: stored})
	body := decodeBody(t, recorder)

	if recorder.Code != http.StatusOK || body["host"] != "10.1.2.3" || body["username"] != "app" {
		t.Fatalf("got %d %s, want 200 with the stored set", recorder.Code, recorder.Body.String())
	}
}
