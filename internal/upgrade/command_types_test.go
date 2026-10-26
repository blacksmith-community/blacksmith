package upgrade

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/mutation"
)

const commandSecretText = "echo distinctive-command-text-9f3a"

func newCommandTask() *UpgradeTask {
	code := 0
	started := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)

	return &UpgradeTask{
		ID:   "command-task-1",
		Kind: TaskKindCommand,
		Command: &CommandTaskSpec{
			Command:        commandSecretText,
			CommandDigest:  "abc123",
			CommandLength:  len(commandSecretText),
			RunAs:          "vcap",
			TimeoutSeconds: CommandTimeoutDefault,
			Parallel:       true,
			MaxInFlight:    3,
		},
		Instances: []InstanceUpgrade{{
			InstanceID: "inst-1",
			Command: &InstanceCommand{
				Deployment: "dep-1",
				VMs: []CommandVM{
					{
						Reviewed:  CommandVMIdentity{Group: "redis", Index: 0, ID: "a", CID: "c1", IPs: []string{"10.0.0.1"}, State: "running"},
						Matched:   CommandVMIdentity{Group: "redis", Index: 0, ID: "a", CID: "c1", IPs: []string{"10.0.0.1"}, State: "running"},
						Phase:     CommandPhasePending,
						ExitCode:  &code,
						StartedAt: &started,
						EndedAt:   &started,
					},
					{
						Reviewed: CommandVMIdentity{Group: "redis", Index: 1, ID: "b", CID: "c2", IPs: []string{"10.0.0.2"}, State: "running"},
						Phase:    CommandPhasePending,
					},
				},
			},
		}},
	}
}

func TestCommandTaskKindConstants(t *testing.T) {
	t.Parallel()

	if TaskKindCommand != "command" {
		t.Fatalf("TaskKindCommand = %q", TaskKindCommand)
	}

	if got := tasksIndexPath(TaskKindCommand); got != "command-tasks" {
		t.Fatalf("tasksIndexPath = %q", got)
	}

	if mutation.KindCommand != "command" {
		t.Fatalf("mutation.KindCommand = %q", mutation.KindCommand)
	}

	for _, kind := range []string{"upgrade", "certificates", "command"} {
		if !strings.Contains(ErrInvalidTaskKind.Error(), kind) {
			t.Fatalf("ErrInvalidTaskKind %q does not name %q", ErrInvalidTaskKind, kind)
		}
	}
}

func TestCommandToSummary(t *testing.T) {
	t.Parallel()

	summary := newCommandTask().ToSummary()

	if summary.Kind != TaskKindCommand {
		t.Fatalf("Kind = %q", summary.Kind)
	}

	if summary.Target != "Run as vcap on 2 nodes" {
		t.Fatalf("Target = %q", summary.Target)
	}

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(raw), "distinctive-command-text-9f3a") {
		t.Fatalf("summary carries the command text: %s", raw)
	}

	if summary.Command == nil {
		t.Fatalf("summary has no command settings")
	}

	got := summary.Command
	if got.CommandDigest != "abc123" || got.CommandLength != len(commandSecretText) || got.RunAs != "vcap" ||
		got.TimeoutSeconds != CommandTimeoutDefault || !got.Parallel || got.MaxInFlight != 3 {
		t.Fatalf("command settings = %+v", got)
	}
}

func TestCommandTimeoutBounds(t *testing.T) {
	t.Parallel()

	if CommandTimeoutDefault != 600 || CommandTimeoutMin != 10 || CommandTimeoutMax != 3600 {
		t.Fatalf("timeouts %d/%d/%d", CommandTimeoutDefault, CommandTimeoutMin, CommandTimeoutMax)
	}
}

func TestCopyTaskCommandDoesNotAlias(t *testing.T) {
	t.Parallel()

	original := newCommandTask()
	copied := copyTask(original)

	if copied.Command == original.Command {
		t.Fatalf("Command spec aliases")
	}

	vms := copied.Instances[0].Command.VMs
	vms[0].Phase = CommandPhaseSettled
	*vms[0].ExitCode = 7
	*vms[0].StartedAt = time.Time{}
	*vms[0].EndedAt = time.Time{}
	vms[0].Reviewed.IPs[0] = "9.9.9.9"
	vms[0].Matched.IPs[0] = "9.9.9.9"
	vms[1].Reviewed.IPs[0] = "9.9.9.9"
	copied.Instances[0].Command.VMs = append(vms[:1], vms[1:]...)
	copied.Instances[0].Command.Deployment = "changed"
	copied.Command.Command = "changed"

	orig := original.Instances[0].Command

	if orig.Deployment != "dep-1" || orig.VMs[0].Phase != CommandPhasePending || *orig.VMs[0].ExitCode != 0 {
		t.Fatalf("original changed: %+v", orig.VMs[0])
	}

	if orig.VMs[0].StartedAt.IsZero() || orig.VMs[0].EndedAt.IsZero() {
		t.Fatalf("original times changed")
	}

	if orig.VMs[0].Reviewed.IPs[0] != "10.0.0.1" || orig.VMs[0].Matched.IPs[0] != "10.0.0.1" || orig.VMs[1].Reviewed.IPs[0] != "10.0.0.2" {
		t.Fatalf("original IPs changed")
	}

	if original.Command.Command != commandSecretText {
		t.Fatalf("original spec changed")
	}
}

func TestCommandTargetPlurals(t *testing.T) {
	t.Parallel()

	task := newCommandTask()

	if got := commandTarget(task); got != "Run as vcap on 2 nodes" {
		t.Fatalf("two nodes: %q", got)
	}

	task.Instances[0].Command.VMs = task.Instances[0].Command.VMs[:1]

	if got := commandTarget(task); got != "Run as vcap on 1 node" {
		t.Fatalf("one node: %q", got)
	}
}

// A summary carries the client request ID, so a client that lost the answer
// to a create can find its task in the list.
func TestToSummaryCarriesClientRequestID(t *testing.T) {
	task := &UpgradeTask{ID: "task-1", Kind: TaskKindCommand, ClientRequestID: "req-7"}

	summary := task.ToSummary()
	if summary.ClientRequestID != "req-7" {
		t.Fatalf("expected the summary to carry the client request ID, got %q", summary.ClientRequestID)
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}

	if !strings.Contains(string(encoded), `"client_request_id":"req-7"`) {
		t.Fatalf("expected client_request_id in the summary JSON, got %s", encoded)
	}

	if bare := (&UpgradeTask{ID: "task-2"}).ToSummary(); strings.Contains(mustJSON(t, bare), "client_request_id") {
		t.Fatal("expected a summary without a request ID to omit the field")
	}
}
