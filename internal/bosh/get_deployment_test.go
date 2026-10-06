package bosh_test

import (
	"errors"
	"net/http"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

const basicAuthCredential = "admin"

// A director answers 200 with a null manifest for a deployment whose first
// deploy is still running, and 404 for a deployment that does not exist. Only
// the latter may be reported as ErrDeploymentNotFound.
func TestGetDeploymentDistinguishesEmptyManifestFromMissing(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch request.URL.Path {
		case "/deployments/in-flight":
			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{"name": "in-flight", "manifest": nil, "releases": []string{}, "stemcells": []string{}, "teams": []string{}})
		case "/deployments/finished":
			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{"name": "finished", "manifest": "name: finished\n", "releases": []string{}, "stemcells": []string{}, "teams": []string{}})
		case "/deployments/missing":
			boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'missing' doesn't exist")
		default:
			return false
		}

		return true
	})

	director, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  server.URL,
		Username: basicAuthCredential,
		Password: basicAuthCredential,
		CACert:   server.CACert,
	})
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	inFlight, err := director.GetDeployment(t.Context(), "in-flight")
	if err != nil {
		t.Fatalf("expected an in-flight deployment with no manifest to exist, got error: %v", err)
	}

	if inFlight.Name != "in-flight" || inFlight.Manifest != "" {
		t.Fatalf("unexpected in-flight deployment detail: %+v", inFlight)
	}

	finished, err := director.GetDeployment(t.Context(), "finished")
	if err != nil {
		t.Fatalf("expected a finished deployment to be returned, got error: %v", err)
	}

	if finished.Manifest != "name: finished\n" {
		t.Fatalf("unexpected manifest for finished deployment: %q", finished.Manifest)
	}

	_, err = director.GetDeployment(t.Context(), "missing")
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for a 404, got: %v", err)
	}
}
