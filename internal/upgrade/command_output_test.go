package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/pkg/logger"
)

const outputTaskID = "0b6f5c1e-8a52-4f0e-9d3a-1c2b3d4e5f60"

// failingPutVault refuses every write.
type failingPutVault struct {
	*waitVault
}

func (v *failingPutVault) Put(context.Context, string, interface{}) error {
	return errors.New("vault is sealed")
}

func newOutputManager() (*Manager, *waitVault) {
	vault := newWaitVault()

	return NewManager(&logger.NoOpLogger{}, newWaitDirector(), vault), vault
}

func TestCommandOutputRoundTrip(t *testing.T) {
	t.Parallel()

	m, _ := newOutputManager()
	ctx := context.Background()
	vm := CommandVMIdentity{Group: "redis", Index: 2}
	result := ssh.Result{
		Stdout:          []byte("out\x00\xff bytes"),
		Stderr:          []byte("err"),
		StdoutTruncated: true,
		StdoutReceived:  900,
		StderrReceived:  3,
	}

	path, err := m.storeCommandOutput(ctx, outputTaskID, "inst-1", vm, result)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	want := "command-output/" + outputTaskID + "/inst-1/redis-2"
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	got, found, err := m.loadCommandOutput(ctx, path)
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}

	if !bytes.Equal(got.Stdout, result.Stdout) || !bytes.Equal(got.Stderr, result.Stderr) {
		t.Errorf("streams = %q / %q", got.Stdout, got.Stderr)
	}

	if got.StdoutKept != int64(len(result.Stdout)) || got.StderrKept != 3 {
		t.Errorf("kept = %d / %d", got.StdoutKept, got.StderrKept)
	}

	if got.StdoutReceived != 900 || got.StderrReceived != 3 {
		t.Errorf("received = %d / %d", got.StdoutReceived, got.StderrReceived)
	}

	if !got.StdoutTruncated || got.StderrTruncated {
		t.Errorf("truncated = %v / %v", got.StdoutTruncated, got.StderrTruncated)
	}
}

func TestCommandOutputStoresBase64(t *testing.T) {
	t.Parallel()

	m, vault := newOutputManager()

	path, err := m.storeCommandOutput(context.Background(), outputTaskID, "inst-1",
		CommandVMIdentity{Group: "g", Index: 0}, ssh.Result{Stdout: []byte("hello")})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	vault.mu.Lock()
	raw := string(vault.saved[path])
	vault.mu.Unlock()

	if !strings.Contains(raw, `"stdout":"aGVsbG8="`) || strings.Contains(raw, "hello") {
		t.Errorf("stored entry = %s", raw)
	}
}

func TestCommandOutputEmptyStreamsStillExist(t *testing.T) {
	t.Parallel()

	m, _ := newOutputManager()
	ctx := context.Background()

	path, err := m.storeCommandOutput(ctx, outputTaskID, "inst-1", CommandVMIdentity{Group: "g", Index: 0}, ssh.Result{})
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	got, found, err := m.loadCommandOutput(ctx, path)
	if err != nil || !found {
		t.Fatalf("an empty entry must exist: found=%v err=%v", found, err)
	}

	if len(got.Stdout) != 0 || len(got.Stderr) != 0 {
		t.Errorf("streams = %q / %q", got.Stdout, got.Stderr)
	}

	_, found, err = m.loadCommandOutput(ctx, "command-output/"+outputTaskID+"/inst-1/g-1")
	if err != nil || found {
		t.Errorf("a node that never ran: found=%v err=%v", found, err)
	}
}

func TestCommandOutputWriteFailureWritesNothing(t *testing.T) {
	t.Parallel()

	inner := newWaitVault()
	m := NewManager(&logger.NoOpLogger{}, newWaitDirector(), &failingPutVault{inner})

	path, err := m.storeCommandOutput(context.Background(), outputTaskID, "inst-1",
		CommandVMIdentity{Group: "g", Index: 0}, ssh.Result{Stdout: []byte("secret-output-7c1")})
	if err == nil {
		t.Fatal("expected the write error")
	}

	if path != "" {
		t.Errorf("path = %q on failure", path)
	}

	if strings.Contains(err.Error(), "secret-output-7c1") {
		t.Errorf("error leaks output: %v", err)
	}

	if len(inner.saved) != 0 {
		t.Errorf("partial writes: %d", len(inner.saved))
	}
}

func TestCommandOutputRejectsBadSegments(t *testing.T) {
	t.Parallel()

	good := CommandVMIdentity{Group: "redis", Index: 0}

	cases := []struct {
		name     string
		taskID   string
		instance string
		vm       CommandVMIdentity
	}{
		{"task id not a uuid", "command-task-1", "inst-1", good},
		{"task id traversal", "../" + outputTaskID, "inst-1", good},
		{"task id empty", "", "inst-1", good},
		{"instance empty", outputTaskID, "", good},
		{"instance slash", outputTaskID, "a/b", good},
		{"instance dot", outputTaskID, ".", good},
		{"instance dotdot", outputTaskID, "..", good},
		{"instance newline", outputTaskID, "a\nb", good},
		{"instance nul", outputTaskID, "a\x00b", good},
		{"instance del", outputTaskID, "a\x7fb", good},
		{"group empty", outputTaskID, "inst-1", CommandVMIdentity{Group: "", Index: 0}},
		{"group slash", outputTaskID, "inst-1", CommandVMIdentity{Group: "a/b", Index: 0}},
		{"group dot", outputTaskID, "inst-1", CommandVMIdentity{Group: ".", Index: 0}},
		{"group dotdot", outputTaskID, "inst-1", CommandVMIdentity{Group: "..", Index: 0}},
		{"group control", outputTaskID, "inst-1", CommandVMIdentity{Group: "a\tb", Index: 0}},
		{"index negative", outputTaskID, "inst-1", CommandVMIdentity{Group: "redis", Index: -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, vault := newOutputManager()

			path, err := m.storeCommandOutput(context.Background(), tc.taskID, tc.instance, tc.vm,
				ssh.Result{Stdout: []byte("x")})
			if err == nil {
				t.Fatalf("accepted, path %q", path)
			}

			if path != "" || len(vault.saved) != 0 {
				t.Errorf("wrote %q / %d entries", path, len(vault.saved))
			}
		})
	}
}

func TestCommandOutputConcurrentNodes(t *testing.T) {
	t.Parallel()

	m, _ := newOutputManager()
	ctx := context.Background()

	var wg sync.WaitGroup

	paths := make([]string, 2)
	errs := make([]error, 2)

	for i := range 2 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			paths[i], errs[i] = m.storeCommandOutput(ctx, outputTaskID, "inst-1",
				CommandVMIdentity{Group: "redis", Index: i},
				ssh.Result{Stdout: []byte(fmt.Sprintf("node-%d", i))})
		}()
	}

	wg.Wait()

	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("node %d: %v", i, errs[i])
		}

		got, found, err := m.loadCommandOutput(ctx, paths[i])
		if err != nil || !found {
			t.Fatalf("node %d: found=%v err=%v", i, found, err)
		}

		if string(got.Stdout) != fmt.Sprintf("node-%d", i) {
			t.Errorf("node %d stdout = %q", i, got.Stdout)
		}
	}

	if paths[0] == paths[1] {
		t.Errorf("both nodes wrote %q", paths[0])
	}
}
