package blacksmith_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// The broker's own deployment name comes from the shared resolver, so the
// events, manifest, and VM reads go to the name the instance file holds and
// not to one built from the environment. With no name at all they refuse.
func TestOwnDeploymentReadsUseTheOwnNameResolver(t *testing.T) {
	t.Parallel()

	named := t.TempDir() + "/deployment"

	err := os.WriteFile(named, []byte("resolved-name\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	missing := t.TempDir() + "/none"

	routes := map[string]struct {
		call   func(*handlerpkg.Handler, http.ResponseWriter, *http.Request)
		method string
	}{
		"events":   {func(h *handlerpkg.Handler, w http.ResponseWriter, r *http.Request) { h.GetEvents(w, r) }, "GetEvents"},
		"manifest": {func(h *handlerpkg.Handler, w http.ResponseWriter, r *http.Request) { h.GetManifest(w, r) }, "GetDeployment"},
		"vms":      {func(h *handlerpkg.Handler, w http.ResponseWriter, r *http.Request) { h.GetVMs(w, r) }, "GetDeploymentVMs"},
	}

	for name, route := range routes {
		for _, scenario := range []struct {
			file, env string
			want      []string
			code      int
		}{
			{named, testEnv, []string{"resolved-name"}, http.StatusOK},
			{missing, testEnv, []string{"testing-blacksmith"}, http.StatusOK},
			{missing, "", nil, http.StatusNotFound},
		} {
			director := testutil.NewScriptedBOSHDirector()
			cfg := createTestConfig()
			cfg.Env = scenario.env
			handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
				Logger: logger.Get().Named("test"), Config: cfg, Vault: &testutil.InstanceVault{}, Director: director, OwnNameFile: scenario.file,
			})

			recorder := httptest.NewRecorder()
			route.call(handler, recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/"+name, nil))

			if recorder.Code != scenario.code {
				t.Errorf("%s with env %q: status %d, want %d", name, scenario.env, recorder.Code, scenario.code)
			}

			if got := director.Calls(route.method); !slices.Equal(got, scenario.want) {
				t.Errorf("%s with env %q read %v, want %v", name, scenario.env, got, scenario.want)
			}
		}
	}
}
