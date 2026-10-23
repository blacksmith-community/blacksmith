package bosh_test

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

// deploymentTasksServer answers every task list with tasks 42, a deploy
// that finished, and 41, a failed recreate.
func deploymentTasksServer(t *testing.T) *boshtest.Director {
	t.Helper()

	return boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		recreate := directorTask(41, "error")
		recreate["description"] = "recreate deployment"

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{directorTask(42, "done"), recreate})

		return true
	})
}

// DeploymentTasks lists the deployment's recent tasks in every state,
// newest first, with the end time of each finished one.
func TestDeploymentTasksListsTheRecentTasksOfOneDeployment(t *testing.T) {
	server := deploymentTasksServer(t)
	lister, ok := newReadAdapter(t, server, nil).(bosh.DeploymentTaskLister)

	if !ok {
		t.Fatal("the director adapter does not list a deployment's tasks")
	}

	tasks, err := lister.DeploymentTasks(t.Context(), "valkey-1", 5)
	if err != nil {
		t.Fatalf("DeploymentTasks failed: %v", err)
	}

	if len(tasks) != 2 || tasks[0].ID != 42 || tasks[0].Description != bosh.DeployTaskDescription || tasks[1].ID != 41 {
		t.Fatalf("expected tasks 42 and 41, got %+v", tasks)
	}

	if tasks[0].EndedAt == nil || !tasks[0].EndedAt.Equal(time.Unix(1759766400, 0)) {
		t.Errorf("the finished deploy has end time %v", tasks[0].EndedAt)
	}

	lists := requestsTo(server, http.MethodGet, "/tasks")
	if len(lists) != 1 {
		t.Fatalf("expected one task list request, got %d", len(lists))
	}

	query, err := url.ParseQuery(lists[0].RawQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", lists[0].RawQuery, err)
	}

	want := url.Values{"deployment": {"valkey-1"}, "limit": {"5"}, "verbose": {"1"}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("the task list asked for %q, want %q", query.Encode(), want.Encode())
	}
}

// An empty deployment name would list every deployment's tasks, so it is
// refused before any request.
func TestDeploymentTasksRefusesAnEmptyDeployment(t *testing.T) {
	server := deploymentTasksServer(t)
	lister, _ := newReadAdapter(t, server, nil).(bosh.DeploymentTaskLister)

	if lister == nil {
		t.Fatal("the director adapter does not list a deployment's tasks")
	}

	if _, err := lister.DeploymentTasks(t.Context(), "", 5); err == nil {
		t.Error("an empty deployment name was accepted")
	}

	if got := requestsTo(server, http.MethodGet, "/tasks"); len(got) != 0 {
		t.Errorf("an empty deployment name sent %d requests", len(got))
	}
}

// directorOnly has the Director methods and nothing more.
type directorOnly struct{ bosh.Director }

// The pooled director passes the call through when its director lists a
// deployment's tasks, and says it cannot when it does not.
func TestPooledDeploymentTasks(t *testing.T) {
	server := deploymentTasksServer(t)
	pooled := bosh.NewPooledDirector(newReadAdapter(t, server, nil), 1, time.Second, nil)

	tasks, err := pooled.DeploymentTasks(t.Context(), "valkey-1", 5)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("the pooled director listed %+v, %v", tasks, err)
	}

	bare := bosh.NewPooledDirector(directorOnly{}, 1, time.Second, nil)

	if _, err := bare.DeploymentTasks(t.Context(), "valkey-1", 5); !errors.Is(err, bosh.ErrDeploymentTasksUnsupported) {
		t.Errorf("a director without the call returned %v, want ErrDeploymentTasksUnsupported", err)
	}
}
