package blacksmith_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/config"
)

const configRedactedPlaceholder = "(redacted)"

// fakeSecrets maps each secret-bearing config path to an obviously fake value.
// Every value is unique so a leak can be traced to one field.
func fakeSecrets() map[string]string {
	return map[string]string{
		"broker.password":                "fake-broker-password-0001",
		"broker.tls.key":                 "fake-tls-private-key-0002",
		"broker.cf.broker_pass":          "fake-cf-broker-pass-0003",
		"broker.cf.apis.dev.password":    "fake-cf-api-password-0004",
		"vault.token":                    "fake-vault-token-0005",
		"shield.token":                   "fake-shield-token-0006",
		"shield.password":                "fake-shield-password-0007",
		"bosh.password":                  "fake-bosh-password-0008",
		"credhub.client_secret":          "fake-credhub-client-secret-0009",
		"bosh.ssh.websocket.placeholder": "",
	}
}

func createSecretConfig() *config.Config {
	secrets := fakeSecrets()

	cfg := &config.Config{
		Env:     "testing",
		Broker:  config.BrokerConfig{Username: "broker-user", Password: secrets["broker.password"], Port: "8080"},
		Vault:   config.VaultConfig{Address: "https://vault.local", Token: secrets["vault.token"]},
		Shield:  config.ShieldConfig{Address: "https://shield.local", Token: secrets["shield.token"], Password: secrets["shield.password"], Username: "shield-user"},
		BOSH:    config.BOSHConfig{Address: "https://bosh.local:25555", Username: "admin", Password: secrets["bosh.password"]},
		CredHub: config.CredHubConfig{URL: "https://credhub.local:8844", ClientID: "blacksmith_credhub", ClientSecret: secrets["credhub.client_secret"], DirectorName: "bosh"},
	}
	cfg.Broker.TLS.Key = secrets["broker.tls.key"]
	cfg.Broker.CF.BrokerUser = "cf-broker-user"
	cfg.Broker.CF.BrokerPass = secrets["broker.cf.broker_pass"]
	cfg.Broker.CF.APIs = map[string]config.CFAPIConfig{
		"dev": {Name: "dev", Endpoint: "https://api.dev", Username: "cf-user", Password: secrets["broker.cf.apis.dev.password"]},
	}

	return cfg
}

func lookupConfigPath(t *testing.T, payload map[string]interface{}, path string) interface{} {
	t.Helper()

	var current interface{} = payload

	for _, part := range strings.Split(path, ".") {
		node, isMap := current.(map[string]interface{})
		if !isMap {
			t.Fatalf("config path %q: %q is not reached through a map", path, part)
		}

		next, present := node[part]
		if !present {
			t.Fatalf("config path %q is missing from the response", path)
		}

		current = next
	}

	return current
}

func TestGetConfigRedactsEverySecretField(t *testing.T) {
	t.Parallel()

	handler := createTestHandler(createSecretConfig())
	recorder := httptest.NewRecorder()

	handler.GetConfig(recorder, httptest.NewRequest(http.MethodGet, "/b/blacksmith/config", nil))
	validateStatusOK(t, recorder)

	body := recorder.Body.String()
	payload := unmarshalResponse(t, recorder)

	for path, secret := range fakeSecrets() {
		if secret == "" {
			continue
		}

		if strings.Contains(body, secret) {
			t.Errorf("secret for %s appears in the response body", path)
		}

		if got := lookupConfigPath(t, payload, path); got != configRedactedPlaceholder {
			t.Errorf("expected %s to hold %q, got %v", path, configRedactedPlaceholder, got)
		}
	}
}

func TestGetConfigKeepsNonSecretFieldsReadable(t *testing.T) {
	t.Parallel()

	handler := createTestHandler(createSecretConfig())
	recorder := httptest.NewRecorder()

	handler.GetConfig(recorder, httptest.NewRequest(http.MethodGet, "/b/blacksmith/config", nil))
	validateStatusOK(t, recorder)

	payload := unmarshalResponse(t, recorder)

	expected := map[string]string{
		"broker.username":             "broker-user",
		"broker.cf.broker_user":       "cf-broker-user",
		"broker.cf.apis.dev.username": "cf-user",
		"broker.cf.apis.dev.endpoint": "https://api.dev",
		"vault.address":               "https://vault.local",
		"bosh.username":               "admin",
		"credhub.client_id":           "blacksmith_credhub",
		"credhub.url":                 "https://credhub.local:8844",
		"credhub.director_name":       "bosh",
	}

	for path, want := range expected {
		if got := lookupConfigPath(t, payload, path); got != want {
			t.Errorf("expected %s to be %q, got %v", path, want, got)
		}
	}
}

func TestGetConfigLeavesUnsetSecretsEmpty(t *testing.T) {
	t.Parallel()

	handler := createTestHandler(&config.Config{Env: "testing"})
	recorder := httptest.NewRecorder()

	handler.GetConfig(recorder, httptest.NewRequest(http.MethodGet, "/b/blacksmith/config", nil))
	validateStatusOK(t, recorder)

	payload := unmarshalResponse(t, recorder)

	for _, path := range []string{"broker.password", "vault.token", "bosh.password", "credhub.client_secret"} {
		if got := lookupConfigPath(t, payload, path); got != "" {
			t.Errorf("expected unset %s to stay empty, got %v", path, got)
		}
	}
}
