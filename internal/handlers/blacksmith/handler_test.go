package blacksmith_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/config"
	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
)

func TestGetConfigReturnsFullConfiguration(t *testing.T) {
	t.Parallel()

	cfg := createTestConfig()
	handler := createTestHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/b/blacksmith/config", nil)
	recorder := httptest.NewRecorder()

	handler.GetConfig(recorder, req)

	validateStatusOK(t, recorder)
	payload := unmarshalResponse(t, recorder)
	validateConfigPayload(t, payload)
}

func createTestConfig() *config.Config {
	return &config.Config{
		Env: "testing",
		Broker: config.BrokerConfig{
			Username: "user",
			Password: "pass",
			Port:     "8080",
			BindIP:   "0.0.0.0",
			CF: config.CFBrokerConfig{
				Enabled: true,
				APIs: map[string]config.CFAPIConfig{
					"dev": {
						Name:     "dev",
						Endpoint: "https://api.dev",
						Username: "cf-user",
						Password: "cf-pass",
					},
				},
			},
		},
		Vault: config.VaultConfig{
			Address: "https://vault.local",
			Token:   "token",
		},
		Services: config.ServicesConfig{
			SkipTLSVerify: []string{"rabbitmq"},
		},
	}
}

func createTestHandler(cfg *config.Config) *handlerpkg.Handler {
	return handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("blacksmith-handler-test"),
		Config: cfg,
	})
}

func validateStatusOK(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
}

func unmarshalResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var payload map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	return payload
}

func validateConfigPayload(t *testing.T, payload map[string]interface{}) {
	t.Helper()

	validateEnvironment(t, payload)
	validateBrokerData(t, payload)
	validateServicesData(t, payload)
}

func validateEnvironment(t *testing.T, payload map[string]interface{}) {
	t.Helper()

	if payload["env"] != "testing" {
		t.Fatalf("expected env to be 'testing', got %v", payload["env"])
	}
}

func validateBrokerData(t *testing.T, payload map[string]interface{}) {
	t.Helper()

	brokerData, found := payload["broker"].(map[string]interface{})
	if !found {
		t.Fatalf("expected broker to be a map, got %T", payload["broker"])
	}

	if brokerData["username"] != "user" {
		t.Fatalf("expected broker.username to be 'user', got %v", brokerData["username"])
	}

	validateCFData(t, brokerData)
}

func validateCFData(t *testing.T, brokerData map[string]interface{}) {
	t.Helper()

	cfData, found := brokerData["cf"].(map[string]interface{})
	if !found {
		t.Fatalf("expected broker.cf to be a map, got %T", brokerData["cf"])
	}

	apis, exists := cfData["apis"].(map[string]interface{})
	if !exists {
		t.Fatalf("expected broker.cf.apis to be a map, got %T", cfData["apis"])
	}

	devAPI, found := apis["dev"].(map[string]interface{})
	if !found {
		t.Fatalf("expected broker.cf.apis.dev to be a map, got %T", apis["dev"])
	}

	if devAPI["endpoint"] != "https://api.dev" {
		t.Fatalf("expected broker.cf.apis.dev.endpoint to be 'https://api.dev', got %v", devAPI["endpoint"])
	}
}

func validateServicesData(t *testing.T, payload map[string]interface{}) {
	t.Helper()

	servicesData, exists := payload["services"].(map[string]interface{})
	if !exists {
		t.Fatalf("expected services to be a map, got %T", payload["services"])
	}

	skipTLS, ok := servicesData["skip_tls_verify"].([]interface{})
	if !ok {
		t.Fatalf("expected services.skip_tls_verify to be a list, got %T", servicesData["skip_tls_verify"])
	}

	if len(skipTLS) != 1 || skipTLS[0] != "rabbitmq" {
		t.Fatalf("unexpected skip_tls_verify contents: %+v", skipTLS)
	}
}
