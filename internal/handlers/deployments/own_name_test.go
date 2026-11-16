package deployments_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

// literalBlacksmith is the name the broker once assumed for itself.
const literalBlacksmith = "blacksmith"

// ownNameHandler is a handler whose own deployment is named by the given
// instance file, which holds fileName, or is absent when fileName is empty.
func ownNameHandler(t *testing.T, fileName string, config interfaces.Config) (*deployments.Handler, *recordingDirector) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "deployment")

	if fileName != "" {
		err := os.WriteFile(path, []byte(fileName+"\n"), 0o600)
		if err != nil {
			t.Fatalf("writing the instance file: %v", err)
		}
	}

	guard := mutation.NewGuard(time.Now)
	director := newRecordingDirector(guard, false)

	return deployments.NewHandler(deployments.Dependencies{
		Logger:      logger.NewNoOpLogger(),
		Config:      config,
		Director:    director,
		Vault:       managedVault(),
		Guard:       guard,
		OwnNameFile: path,
	}), director
}

func serve(t *testing.T, handler *deployments.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	return recorder
}

// A deployment the instance file names is the broker's own, whatever the
// environment says. It can be read and not changed, and the name the
// environment would build is another deployment.
func TestTheInstanceFileNamesTheOwnDeployment(t *testing.T) {
	t.Parallel()

	handler, director := ownNameHandler(t, "lab-bs", ownEnvironmentConfig{})

	read := serve(t, handler, http.MethodGet, "/b/deployments/lab-bs/errands")
	if read.Code == http.StatusForbidden || read.Code == http.StatusServiceUnavailable {
		t.Errorf("read of the file's name = %d, want it to run: %s", read.Code, read.Body.String())
	}

	change := serve(t, handler, http.MethodPost, "/b/deployments/lab-bs/restart")
	if change.Code != http.StatusForbidden {
		t.Errorf("change of the file's name = %d, want 403: %s", change.Code, change.Body.String())
	}

	other := serve(t, handler, http.MethodGet, "/b/deployments/"+ownDeployment+"/errands")
	if other.Code != http.StatusForbidden || decodeCode(t, other) != apierr.CodeDeploymentNotManaged {
		t.Errorf("read of the environment's name = %d, want 403 deployment_not_managed: %s", other.Code, other.Body.String())
	}

	calls, _ := director.recorded()
	if len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// With no instance file the name comes from the environment.
func TestTheEnvironmentNamesTheOwnDeploymentWithoutAFile(t *testing.T) {
	t.Parallel()

	handler, _ := ownNameHandler(t, "", ownEnvironmentConfig{})

	read := serve(t, handler, http.MethodGet, "/b/deployments/"+ownDeployment+"/errands")
	if read.Code == http.StatusForbidden || read.Code == http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the read to run: %s", read.Code, read.Body.String())
	}
}

// With neither a file nor an environment nothing is the own deployment,
// and not even the literal name blacksmith is readable.
func TestNoNameMeansNoOwnDeployment(t *testing.T) {
	t.Parallel()

	handler, _ := ownNameHandler(t, "", nil)

	for _, name := range []string{literalBlacksmith, "-blacksmith"} {
		read := serve(t, handler, http.MethodGet, "/b/deployments/"+name+"/errands")
		if read.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403: %s", name, read.Code, read.Body.String())
		}
	}
}
