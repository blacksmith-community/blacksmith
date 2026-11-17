package testing_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/apierr"
	rabbitmqtesting "blacksmith/internal/handlers/services/rabbitmq/testing"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const gateRabbitPlan = "tiny"

// The RabbitMQ testing routes read the instance's credentials, so they
// answer only for a live instance. A deprovisioned or unknown ID never
// reaches its Vault path, and a live one still does.
func TestRabbitMQTestingRoutesNeedALiveInstance(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"live": {ServiceID: "rabbitmq", PlanID: gateRabbitPlan},
		"gone": {Status: vaultPkg.StatusDeleted, ServiceID: "rabbitmq", PlanID: gateRabbitPlan},
	}}
	handler := rabbitmqtesting.NewHandler(rabbitmqtesting.Dependencies{Logger: logger.Get().Named("test"), Vault: vault})

	code := func(recorder *httptest.ResponseRecorder) string {
		var body struct {
			Code string `json:"code"`
		}

		_ = json.Unmarshal(recorder.Body.Bytes(), &body)

		return body.Code
	}

	for _, badID := range []string{"gone", "unknown", "a..b"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/"+badID+"/rabbitmq/queues", nil))

		if recorder.Code != http.StatusNotFound || code(recorder) != apierr.CodeInstanceNotFound {
			t.Errorf("%s: status %d code %q, want 404 instance_not_found", badID, recorder.Code, code(recorder))
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/live/rabbitmq/queues", nil))

	if code(recorder) == apierr.CodeInstanceNotFound {
		t.Errorf("a live instance was refused: %d %s", recorder.Code, recorder.Body.String())
	}
}
