package bosh_test

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
)

const basicAuthCredential = "admin"

// A director answers 200 with a null manifest for a deployment whose first
// deploy is still running, and 404 for a deployment that does not exist. Only
// the latter may be reported as ErrDeploymentNotFound.
func TestGetDeploymentDistinguishesEmptyManifestFromMissing(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/deployments/in-flight":
			_, _ = writer.Write([]byte(`{"name":"in-flight","manifest":null,"releases":[],"stemcells":[],"teams":[]}`))
		case "/deployments/finished":
			_, _ = writer.Write([]byte(`{"name":"finished","manifest":"name: finished\n","releases":[],"stemcells":[],"teams":[]}`))
		case "/deployments/missing":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"code":70000,"description":"Deployment 'missing' doesn't exist"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})

	director, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  server.URL,
		Username: basicAuthCredential,
		Password: basicAuthCredential,
		CACert:   string(caPEM),
	})
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	inFlight, err := director.GetDeployment("in-flight")
	if err != nil {
		t.Fatalf("expected an in-flight deployment with no manifest to exist, got error: %v", err)
	}

	if inFlight.Name != "in-flight" || inFlight.Manifest != "" {
		t.Fatalf("unexpected in-flight deployment detail: %+v", inFlight)
	}

	finished, err := director.GetDeployment("finished")
	if err != nil {
		t.Fatalf("expected a finished deployment to be returned, got error: %v", err)
	}

	if finished.Manifest != "name: finished\n" {
		t.Fatalf("unexpected manifest for finished deployment: %q", finished.Manifest)
	}

	_, err = director.GetDeployment("missing")
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound for a 404, got: %v", err)
	}
}
