package deployments_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// A missing index, as a fresh broker has, manages nothing.
func TestAMissingIndexManagesNoDeployment(t *testing.T) {
	t.Parallel()

	handler, director := ownershipHandler(&testutil.InstanceVault{})

	for _, action := range deploymentActions() {
		recorder := serveAction(t, handler, action)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403: %s", action.name, recorder.Code, recorder.Body.String())
		}
	}

	if calls, _ := director.recorded(); len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// A padded environment still names the own deployment, and it is trimmed,
// so the read of the trimmed name runs and its change is refused.
func TestPaddedEnvironmentNamesTheOwnDeployment(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)
	director := newRecordingDirector(guard, false)
	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:      logger.NewNoOpLogger(),
		Config:      paddedEnvironmentConfig{},
		Director:    director,
		Vault:       managedVault(),
		Guard:       guard,
		OwnNameFile: filepath.Join(t.TempDir(), "absent"),
	})

	read := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+ownDeployment+"/errands", nil)
	readRecorder := httptest.NewRecorder()

	handler.ServeHTTP(readRecorder, read)

	if readRecorder.Code == http.StatusForbidden || readRecorder.Code == http.StatusServiceUnavailable {
		t.Errorf("read status = %d, want the read to run: %s", readRecorder.Code, readRecorder.Body.String())
	}

	change := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/"+ownDeployment+"/restart", nil)
	changeRecorder := httptest.NewRecorder()

	handler.ServeHTTP(changeRecorder, change)

	if changeRecorder.Code != http.StatusForbidden {
		t.Errorf("change status = %d, want 403: %s", changeRecorder.Code, changeRecorder.Body.String())
	}
}

// paddedEnvironmentConfig names the prod environment with padding around it.
type paddedEnvironmentConfig struct{ writeTimeoutConfig }

func (paddedEnvironmentConfig) GetEnvironment() string { return "  prod\n" }
