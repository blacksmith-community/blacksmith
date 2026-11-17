package redis_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/services/redis"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const gateRedisPlan = "tiny"

func codeOf(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	return body.Code
}

// The Redis testing routes read the instance's credentials, so they answer
// only for a live instance. A deprovisioned or unknown ID never reaches its
// Vault path, and a live one still does.
func TestRedisRoutesNeedALiveInstance(t *testing.T) {
	t.Parallel()

	vault := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"live": {ServiceID: "redis", PlanID: gateRedisPlan},
		"gone": {Status: vaultPkg.StatusDeleted, ServiceID: "redis", PlanID: gateRedisPlan},
	}}
	handler := redis.NewHandler(logger.Get().Named("test"), vault, nil)

	for _, badID := range []string{"gone", "unknown", "a..b"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/"+badID+"/redis/info", nil))

		if recorder.Code != http.StatusNotFound || codeOf(t, recorder) != apierr.CodeInstanceNotFound {
			t.Errorf("%s: status %d code %q, want 404 instance_not_found", badID, recorder.Code, codeOf(t, recorder))
		}
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/live/redis/info", nil))

	if codeOf(t, recorder) == apierr.CodeInstanceNotFound {
		t.Errorf("a live instance was refused: %d %s", recorder.Code, recorder.Body.String())
	}
}
