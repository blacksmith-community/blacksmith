//nolint:testpackage // the test reads the adapter's interval source
package recovery

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
)

// A failed save of the reconciler interval leaves the adapter, its source,
// and the running manager on the interval they had, so nothing takes
// effect that Vault does not hold. The fake Vault refuses with a 403,
// which the Vault client doesn't retry, so the test fails the save at
// once.
func TestUpdateIntervalChangesNothingWhenTheSaveFails(t *testing.T) {
	t.Setenv(reconcilerIntervalEnvKey, "")

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer server.Close()

	reconcilerConfig, source := buildReconcilerConfig(&config.Config{}, &warnRecorder{})
	adapter := &ReconcilerAdapter{logger: &logger.NoOpLogger{}, config: reconcilerConfig, intervalSource: source}
	adapter.vault = internalVault.New(server.URL, "test-token", true)
	adapter.manager = adapter.buildManager()

	before := adapter.GetInterval()
	_, sourceBefore := adapter.currentInterval()

	err := adapter.UpdateInterval(t.Context(), before+20*time.Minute)
	if err == nil {
		t.Fatal("UpdateInterval succeeded against a Vault that fails every write")
	}

	if got := adapter.GetInterval(); got != before {
		t.Errorf("adapter interval = %v after a failed save, want %v", got, before)
	}

	if _, got := adapter.currentInterval(); got != sourceBefore {
		t.Errorf("interval source = %q after a failed save, want %q", got, sourceBefore)
	}

	if got := adapter.currentManager().GetInterval(); got != before {
		t.Errorf("running manager interval = %v after a failed save, want %v", got, before)
	}
}
