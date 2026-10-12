package bosh_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// instanceRow is one instance as the director writes it in the result
// output of a format=full instance listing. Like a VM row, it carries
// job_state and no process_state.
const instanceRow = `{"agent_id":"agent-0","job_name":"node","index":0,"id":"uuid-0","job_state":"failing","state":"started",` +
	`"bootstrap":true,"ips":["10.0.0.5"],"az":"z1","vm_type":"default","vm_cid":"vm-0","disk_cid":"disk-0",` +
	`"ignore":false,"vm_created_at":"2026-10-01T12:00:00Z","processes":[],"vitals":null,` +
	`"stemcell":{"name":"bosh-warden-boshlite-ubuntu-noble","version":"1.100","api_version":3}}`

// vmLessInstanceRow is an instance the director has no VM for, such as one
// a deploy has not created yet. It has no agent, no VM, and a null index.
const vmLessInstanceRow = `{"agent_id":null,"job_name":"node","index":null,"id":"uuid-1","job_state":null,"state":"started",` +
	`"bootstrap":false,"ips":[],"az":"z1","vm_type":"default","vm_cid":null,"disk_cid":null,"ignore":false,` +
	`"vm_created_at":null,"processes":[],"vitals":null,"stemcell":{"name":"","version":"","api_version":0}}`

// The instance listing's kind, and the VM type and task result the
// instance fixtures use.
const (
	instancesKind  = "instances"
	fixtureVMType  = "default"
	instanceFailed = "the instance details task failed"
)

func instanceListing() listingCall {
	return listingCall{operation: "GetInstances", kind: instancesKind, call: func(ctx context.Context, director bosh.Director) error {
		_, err := director.GetInstances(ctx, detailsDeployment)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	}}
}

// listInstances lists the instances a details director serves with output
// as the task's result.
func listInstances(t *testing.T, output string) []bosh.Instance {
	t.Helper()

	server := newDetailsDirector(t, detailsConfig{kind: instancesKind, output: output})
	director := newTaskAdapter(t, server.Director)

	var instances []bosh.Instance

	err := callWithin(t, 10*time.Second, func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%w: %v", errListingPanicked, recovered)
			}
		}()

		instances, err = director.GetInstances(t.Context(), detailsDeployment)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("expected the listing to succeed, got: %v", err)
	}

	if len(instances) != 1 {
		t.Fatalf("expected one instance, got %d: %+v", len(instances), instances)
	}

	return instances
}

// An instance without a VM has no agent and a null index. It is listed with
// its instance UUID and index -1, the index a VM without one gets, rather
// than an empty ID and index 0, which names a real instance.
func TestGetInstancesMapsAVMLessRow(t *testing.T) {
	instance := listInstances(t, vmLessInstanceRow+"\n")[0]

	if instance.ID != "uuid-1" {
		t.Errorf("ID = %q, want the instance UUID %q", instance.ID, "uuid-1")
	}

	if instance.Index != "-1" {
		t.Errorf("Index = %q, want %q", instance.Index, "-1")
	}

	if instance.Group != "node" {
		t.Errorf("Group = %q, want %q", instance.Group, "node")
	}
}

// An instance is identified by its UUID, the row's id key, and not by its
// agent's ID. Its process state comes from job_state, the key the director
// writes.
func TestGetInstancesUsesTheInstanceUUIDAndTheJobState(t *testing.T) {
	instance := listInstances(t, instanceRow+"\n")[0]

	if instance.ID != detailsInstanceUUID {
		t.Errorf("ID = %q, want the instance UUID %q, not the agent ID", instance.ID, detailsInstanceUUID)
	}

	if instance.Index != "0" {
		t.Errorf("Index = %q, want %q", instance.Index, "0")
	}

	if instance.ProcessState != "failing" {
		t.Errorf("ProcessState = %q, want %q from job_state", instance.ProcessState, "failing")
	}

	if instance.State != "started" || instance.AZ != "z1" || instance.VMType != fixtureVMType || !instance.Bootstrap ||
		len(instance.IPs) != 1 || instance.IPs[0] != "10.0.0.5" {
		t.Errorf("unexpected instance fields: %+v", instance)
	}
}

// The VM creation time is parsed, and a value that does not parse leaves
// the zero time without failing the listing.
func TestGetInstancesParsesTheVMCreationTime(t *testing.T) {
	t.Run("a valid time", func(t *testing.T) {
		instance := listInstances(t, instanceRow+"\n")[0]

		want := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
		if !instance.VMCreatedAt.Equal(want) {
			t.Errorf("VMCreatedAt = %s, want %s", instance.VMCreatedAt, want)
		}
	})

	t.Run("a value that does not parse", func(t *testing.T) {
		row := strings.Replace(instanceRow, `"2026-10-01T12:00:00Z"`, `"last tuesday"`, 1)

		instance := listInstances(t, row+"\n")[0]

		if !instance.VMCreatedAt.IsZero() {
			t.Errorf("VMCreatedAt = %s, want the zero time", instance.VMCreatedAt)
		}
	})
}

// The director's deployment-not-found answer, code 70000, gives
// ErrDeploymentNotFound and keeps the director's own answer.
func TestGetInstancesMapsCode70000ToErrDeploymentNotFound(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{kind: instancesKind, list: func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'valkey-1' doesn't exist")
	}})
	director := newTaskAdapter(t, server.Director)

	_, err := director.GetInstances(t.Context(), detailsDeployment)
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got: %v", err)
	}

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound {
		t.Fatalf("expected the error to keep the director's answer with code 70000, got %T: %v", err, err)
	}
}

// An instance details task that ends in any state but done fails the
// listing with a TaskFailedError that names the task.
func TestGetInstancesFailsWhenTheTaskEndsInError(t *testing.T) {
	server := newDetailsDirector(t, detailsConfig{kind: instancesKind, state: func(int32, int32) (string, string) {
		return batchTaskError, instanceFailed
	}})
	director := newTaskAdapter(t, server.Director)

	err := callWithin(t, 10*time.Second, func() error { return instanceListing().call(t.Context(), director) })

	var failed *bosh.TaskFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("expected a *bosh.TaskFailedError, got %T: %v", err, err)
	}

	if failed.TaskID != detailsTaskID || failed.State != batchTaskError || failed.Operation != "GetInstances" ||
		failed.Result != instanceFailed {
		t.Errorf("unexpected TaskFailedError fields: %+v", failed)
	}
}

// The instance listing cancels its director task when the caller's context
// ends, as the VM listing does.
func TestInstanceDetailsCancelsTheDirectorTaskWhenTheContextEnds(t *testing.T) {
	runCancelsTheTaskWhenTheContextEnds(t, instanceListing())
}

// A lost answer to the instance listing is not resent and does not read as
// an unknown outcome, as for the VM listing.
func TestInstanceDetailsLostAnswerIsNotOutcomeUnknown(t *testing.T) {
	runLostAnswerIsNotOutcomeUnknown(t, instanceListing())
}
