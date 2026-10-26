package upgrade

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh/ssh"
)

func apiStatus(err error) (int, string) {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status, apiErr.Code
	}

	return 0, ""
}

func createTwoInstanceTask(t *testing.T, env *cmdCreateEnv) *UpgradeTask {
	t.Helper()

	preview := env.previewFor(t, cmdReq("i1", "i3"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	return task
}

func TestReadCommandOutputReturnsTheNodesEntry(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	task := createTwoInstanceTask(t, env)
	ctx := context.Background()

	_, err := env.manager.storeCommandOutput(ctx, task.ID, "i1", CommandVMIdentity{Group: "redis", Index: 2},
		ssh.Result{Stdout: []byte("two"), StdoutReceived: 3})
	if err != nil {
		t.Fatalf("storing the output: %v", err)
	}

	got, err := env.manager.ReadCommandOutput(ctx, task.ID, "", "redis-2")
	if err != nil {
		t.Fatalf("ReadCommandOutput: %v", err)
	}

	if string(got.Stdout) != "two" || got.StdoutReceived != 3 {
		t.Fatalf("wrong entry: stdout %q received %d", got.Stdout, got.StdoutReceived)
	}
}

func TestReadCommandOutputRefusals(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	task := createTwoInstanceTask(t, env)
	ctx := context.Background()

	cases := map[string]struct {
		task, instance, vm string
		status             int
		code               string
	}{
		"unknown task":         {"nope", "", "redis-0", http.StatusNotFound, apierr.CodeNotFound},
		"unknown node":         {task.ID, "", "redis-9", http.StatusNotFound, apierr.CodeNotFound},
		"unknown instance":     {task.ID, "i2", "redis-0", http.StatusNotFound, apierr.CodeNotFound},
		"node without entry":   {task.ID, "i1", "redis-0", http.StatusNotFound, apierr.CodeNotFound},
		"no hyphen":            {task.ID, "", "redis", http.StatusBadRequest, apierr.CodeInvalidRequest},
		"index not a number":   {task.ID, "", "redis-x", http.StatusBadRequest, apierr.CodeInvalidRequest},
		"hyphen before index":  {task.ID, "", "redis--1", http.StatusNotFound, apierr.CodeNotFound},
		"empty group":          {task.ID, "", "-0", http.StatusBadRequest, apierr.CodeInvalidRequest},
		"two instances match":  {task.ID, "", "redis-0", http.StatusBadRequest, apierr.CodeInvalidRequest},
		"empty vm":             {task.ID, "", "", http.StatusBadRequest, apierr.CodeInvalidRequest},
		"index with plus sign": {task.ID, "", "redis-+0", http.StatusBadRequest, apierr.CodeInvalidRequest},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := env.manager.ReadCommandOutput(ctx, tc.task, tc.instance, tc.vm)

			status, code := apiStatus(err)
			if status != tc.status || code != tc.code {
				t.Fatalf("expected %d %q, got %d %q (%v)", tc.status, tc.code, status, code, err)
			}
		})
	}
}

func TestReadCommandOutputSplitsOnTheLastHyphen(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	task := createTwoInstanceTask(t, env)

	// A group with a hyphen in its name cannot be told from "redis-0" by a
	// split on the first hyphen.
	env.manager.mu.Lock()
	env.manager.tasks[task.ID].Instances[0].Command.VMs[0].Reviewed.Group = "redis-a"
	env.manager.mu.Unlock()

	_, err := env.manager.storeCommandOutput(context.Background(), task.ID, "i1", CommandVMIdentity{Group: "redis-a", Index: 0},
		ssh.Result{Stdout: []byte("x")})
	if err != nil {
		t.Fatalf("storing the output: %v", err)
	}

	if _, err := env.manager.ReadCommandOutput(context.Background(), task.ID, "i1", "redis-a-0"); err != nil {
		t.Fatalf("ReadCommandOutput: %v", err)
	}
}

func TestCheckUpgradeRequestRefusesCommandKindWithoutListingIt(t *testing.T) {
	t.Parallel()

	err := checkUpgradeRequest(CreateTaskRequest{Kind: TaskKindCommand, InstanceIDs: []string{"i1"}})
	if !errors.Is(err, ErrCommandTaskKind) {
		t.Fatalf("expected ErrCommandTaskKind, got %v", err)
	}

	if errors.Is(err, ErrInvalidTaskKind) {
		t.Fatal("a command kind must not read as an unknown kind")
	}
}
