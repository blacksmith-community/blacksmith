package bosh_test

import (
	"context"
	"net/http"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

// deployer is the part of a director the deploy entry points live on.
type deployer interface {
	UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error)
	UpdateDeploymentWithOptions(ctx context.Context, name, manifest string, opts bosh.DeployOptions) (*bosh.Task, error)
}

// deployEntry is one way to send a deploy. The slot, lost-answer, and
// cancellation tests run against every entry, so a deploy with options
// gets the same handling as a plain one.
type deployEntry struct {
	name   string
	deploy func(ctx context.Context, director deployer, name, manifest string) (*bosh.Task, error)
}

var deployEntries = []deployEntry{
	{"plain", func(ctx context.Context, director deployer, name, manifest string) (*bosh.Task, error) {
		return director.UpdateDeployment(ctx, name, manifest)
	}},
	{"options", func(ctx context.Context, director deployer, name, manifest string) (*bosh.Task, error) {
		return director.UpdateDeploymentWithOptions(ctx, name, manifest, bosh.DeployOptions{ForceLatestVariables: true})
	}},
}

func TestUpdateDeploymentWithOptionsSendsForceLatest(t *testing.T) {
	t.Parallel()

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.UpdateDeploymentWithOptions(t.Context(), deploymentName, mutationManifest, bosh.DeployOptions{ForceLatestVariables: true})
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodPost || sent.Path != deploymentsRoute || sent.Body != mutationManifest {
		t.Errorf("expected the manifest in POST /deployments, got %+v", sent)
	}

	if got := sent.Query.Get("force_latest_variables"); got != "true" {
		t.Errorf("expected force_latest_variables=true in the query, got %q (%v)", got, sent.Query)
	}

	if sent.ContextID == "" {
		t.Error("expected the deploy to carry a context ID")
	}
}

func TestUpdateDeploymentSendsNoForceLatest(t *testing.T) {
	t.Parallel()

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.UpdateDeployment(t.Context(), deploymentName, mutationManifest)
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if _, present := sent.Query["force_latest_variables"]; present {
		t.Errorf("expected a plain deploy to send no force_latest_variables, got %v", sent.Query)
	}
}

func TestListVariablesReturnsVersionIDs(t *testing.T) {
	t.Parallel()

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/deployments/"+deploymentName+"/variables" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []map[string]string{
			{"id": "8f2e", "name": "/ws-bosh/redis-7a1b/redis_cluster_crt"},
		})

		return true
	})
	director := newReadAdapter(t, server, nil)

	variables, err := director.ListVariables(t.Context(), deploymentName)
	if err != nil {
		t.Fatalf("ListVariables failed: %v", err)
	}

	want := bosh.Variable{ID: "8f2e", Name: "/ws-bosh/redis-7a1b/redis_cluster_crt"}
	if len(variables) != 1 || variables[0] != want {
		t.Fatalf("expected %+v, got %+v", want, variables)
	}
}

// checkDeployOptionsSeen checks that a base director received the options
// of the "options" entry once per deploy, and none from the plain entry.
func checkDeployOptionsSeen(t *testing.T, entry deployEntry, seen []bosh.DeployOptions, deploys int) {
	t.Helper()

	if entry.name != "options" {
		if len(seen) != 0 {
			t.Fatalf("expected the plain entry to send no options, got %+v", seen)
		}

		return
	}

	if len(seen) != deploys {
		t.Fatalf("expected %d deploys with options, got %+v", deploys, seen)
	}

	for _, opts := range seen {
		if !opts.ForceLatestVariables {
			t.Fatalf("expected ForceLatestVariables to reach the base director, got %+v", seen)
		}
	}
}
