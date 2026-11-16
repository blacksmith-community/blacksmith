package deployments_test

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/settings"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// savedSettingsVault serves the index of one managed deployment and the
// saved runtime settings record.
type savedSettingsVault struct {
	*testutil.InstanceVault

	record map[string]interface{}
}

func (v *savedSettingsVault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	if path == settings.Path {
		target, _ := out.(*map[string]interface{})
		*target = v.record

		return true, nil
	}

	found, err := v.InstanceVault.Get(ctx, path, out)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	return found, nil
}

func errandHandlerWith(vault *savedSettingsVault, configured time.Duration) *deployments.Handler {
	return deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   errandTimeoutConfig{timeout: configured},
		Director: &slowErrandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), run: time.Minute},
		Vault:    vault,
	})
}

func serveErrandUntilItAnswers(t *testing.T, handler *deployments.Handler) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = 5 * time.Second
	server.Start()
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+errandPath, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	start := time.Now()

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	recorder := httptest.NewRecorder()
	recorder.Code = resp.StatusCode

	return recorder, time.Since(start)
}

// A wait saved in the Vault settings beats the environment variable and
// broker.errand_timeout.
func TestErrandWaitSavedInVaultBeatsTheEnvironmentAndTheConfig(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "1h")

	vault := &savedSettingsVault{
		InstanceVault: managingVault(errandDeployment),
		record:        map[string]interface{}{settings.ErrandTimeoutKey: "300ms"},
	}

	recorder, elapsed := serveErrandUntilItAnswers(t, errandHandlerWith(vault, time.Hour))

	if recorder.Code != http.StatusGatewayTimeout || elapsed > 3*time.Second {
		t.Fatalf("status = %d after %v, want 504 after about 300ms", recorder.Code, elapsed)
	}
}

// The environment variable beats broker.errand_timeout when Vault saves
// none, and a refused saved value falls through to it.
func TestErrandWaitFromTheEnvironmentBeatsTheConfig(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "300ms")

	for name, record := range map[string]map[string]interface{}{
		"nothing saved":      {},
		"a refused saved":    {settings.ErrandTimeoutKey: "0s"},
		"a bare number only": {settings.ErrandTimeoutKey: "600"},
	} {
		vault := &savedSettingsVault{InstanceVault: managingVault(errandDeployment), record: record}

		recorder, elapsed := serveErrandUntilItAnswers(t, errandHandlerWith(vault, time.Hour))

		if recorder.Code != http.StatusGatewayTimeout || elapsed > 3*time.Second {
			t.Errorf("%s: status = %d after %v, want 504 after about 300ms", name, recorder.Code, elapsed)
		}
	}
}

// The 504 for an errand that outlasts its wait keeps its code.
func TestErrandOutOfTimeCodeWithASavedWait(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	vault := &savedSettingsVault{
		InstanceVault: managingVault(errandDeployment),
		record:        map[string]interface{}{settings.ErrandTimeoutKey: "200ms"},
	}

	handler := errandHandlerWith(vault, time.Hour)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, errandPath, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504: %s", recorder.Code, recorder.Body.String())
	}

	if code := decodeCode(t, recorder); code != apierr.CodeOutcomeUnknown {
		t.Errorf("code = %q, want %q", code, apierr.CodeOutcomeUnknown)
	}
}

// A wait near the largest duration can't overflow when the margin for the
// write deadline is added to it, so a quick errand still answers.
func TestAnEnormousErrandWaitStillAnswers(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   errandTimeoutConfig{timeout: time.Duration(math.MaxInt64)},
		Director: &slowErrandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), run: 20 * time.Millisecond},
		Vault:    managingVault(errandDeployment),
	})

	recorder, elapsed := serveErrandUntilItAnswers(t, handler)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d after %v, want 200", recorder.Code, elapsed)
	}
}
