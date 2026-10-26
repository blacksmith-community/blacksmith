package upgrade

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/interfaces"

	gossh "golang.org/x/crypto/ssh"
)

// commandTrail keeps the command audit records a vault saved, in the order
// they were saved.
type commandTrail struct {
	mu      sync.Mutex
	records []AuditRecord
}

// watchCommandAudit has the fixture's vault keep every command audit
// record it saves. A seeded fixture calls it from its seed, before the
// manager starts.
func (f *taskFixture) watchCommandAudit() *commandTrail {
	trail := &commandTrail{}
	previous := f.vault.waitVault.onPut

	f.vault.waitVault.onPut = func(path string, raw []byte) {
		if previous != nil {
			previous(path, raw)
		}

		if !strings.HasPrefix(path, commandAuditVaultPath+"/") {
			return
		}

		var rec AuditRecord

		err := json.Unmarshal(raw, &rec)
		if err != nil {
			f.t.Errorf("a command audit record is not valid JSON: %v", err)

			return
		}

		trail.mu.Lock()
		trail.records = append(trail.records, rec)
		trail.mu.Unlock()
	}

	return trail
}

func (c *commandTrail) all() []AuditRecord {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.records)
}

func (c *commandTrail) events() []string {
	var events []string
	for _, rec := range c.all() {
		events = append(events, rec.Event)
	}

	return events
}

// of returns the records of the event, in the order they were saved.
func (c *commandTrail) of(event string) []AuditRecord {
	var found []AuditRecord

	for _, rec := range c.all() {
		if rec.Event == event {
			found = append(found, rec)
		}
	}

	return found
}

// only returns the one record of the event.
func (c *commandTrail) only(t *testing.T, event string) AuditRecord {
	t.Helper()

	found := c.of(event)
	if len(found) != 1 {
		t.Fatalf("want one %s record, got %d in %v", event, len(found), c.events())
	}

	return found[0]
}

// waitForEvent waits until the trail holds a record of the event.
func (c *commandTrail) waitForEvent(t *testing.T, event string) {
	t.Helper()

	waitUntil(t, "the "+event+" record", func() bool { return len(c.of(event)) > 0 })
}

// waitIdle waits until the queue has written every record it was given.
func (f *taskFixture) waitAuditIdle() {
	f.t.Helper()

	waitUntil(f.t, "the command audit queue", f.manager.auditIdle)
}

// auditIndex returns the IDs the command audit index names.
func (f *taskFixture) auditIndex() map[string]bool {
	f.t.Helper()

	var index map[string]interface{}

	_, err := f.vault.Get(context.Background(), commandAuditVaultPath, &index)
	if err != nil {
		f.t.Fatalf("reading the command audit index: %v", err)
	}

	ids := make(map[string]bool, len(index))
	for id := range index {
		ids[id] = true
	}

	return ids
}

// auditBody reports whether the vault holds the record's body.
func (f *taskFixture) auditBody(id string) bool {
	f.vault.waitVault.mu.Lock()
	defer f.vault.waitVault.mu.Unlock()

	_, ok := f.vault.waitVault.saved[commandAuditVaultPath+"/"+id]

	return ok
}

// holdsOutputRecord reports whether Vault holds a record of a node's output.
func (f *taskFixture) holdsOutputRecord() bool {
	f.vault.waitVault.mu.Lock()
	defer f.vault.waitVault.mu.Unlock()

	for path := range f.vault.saved {
		if strings.HasPrefix(path, commandOutputVaultPath+"/") {
			return true
		}
	}

	return false
}

// asSubmitted gives the task an actor, a reason, and the settings a create
// records, so the audit has something to carry.
func asSubmitted(task *UpgradeTask) {
	task.CreatedBy, task.CreatedFrom = "alice", "10.0.0.5:4000 via 203.0.113.9"
	task.Command.Reason = "CHG-4821 restart check"
	task.Command.Shell = "/bin/bash"
	task.Command.PreviewID = "preview-77"
	task.Command.CredentialPresented = true
}

func submitted() taskOption {
	return func(_ *taskFixture, task *UpgradeTask) { asSubmitted(task) }
}

// A run writes a start, a staged and a settled record for each node, a
// settle for the instance, and a finish, in the order they happened. The
// records carry the actor, the settings, the command, and the nodes.
func TestCommandAuditRecordsAWholeRun(t *testing.T) {
	fix := newTaskFixture(t, 1, 2, submitted())
	trail := fix.watchCommandAudit()
	fix.exits["dep-0/1"] = 0

	fix.queue()
	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	trail.waitForEvent(t, AuditTaskFinished)
	fix.waitAuditIdle()

	want := []string{AuditTaskStarted, AuditNodeStaged, AuditNodeSettled, AuditNodeStaged, AuditNodeSettled, AuditInstanceSettled, AuditTaskFinished}
	if got := trail.events(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	started := trail.only(t, AuditTaskStarted)
	if started.ID != auditID(taskFixtureID, "", AuditTaskStarted, "") || started.TaskID != taskFixtureID {
		t.Errorf("start record id %s task %s", started.ID, started.TaskID)
	}

	if started.Actor != "alice" || started.From != "10.0.0.5:4000 via 203.0.113.9" || started.Reason != "CHG-4821 restart check" {
		t.Errorf("start record actor %q from %q reason %q", started.Actor, started.From, started.Reason)
	}

	cmd := started.Command
	if cmd == nil {
		t.Fatal("the start record has no command block")
	}

	if cmd.Text != runCommandText || cmd.Digest != commandDigest(runCommandText) || cmd.Length != len(runCommandText) {
		t.Errorf("command block text %q digest %s length %d", cmd.Text, cmd.Digest, cmd.Length)
	}

	if cmd.RunAs != "vcap" || cmd.Shell != "/bin/bash" || cmd.TimeoutSeconds != runTimeoutSecs || cmd.Parallel || cmd.PreviewID != "preview-77" || !cmd.CredentialPresented {
		t.Errorf("command block settings %+v", *cmd)
	}

	if len(cmd.Targets) != 1 || cmd.Targets[0].Deployment != "dep-0" || cmd.Targets[0].InstanceID != "inst-0" || len(cmd.Targets[0].Nodes) != 2 || cmd.Targets[0].Nodes[1].ID != "vm-0-1" {
		t.Errorf("targets %+v", cmd.Targets)
	}

	for _, rec := range trail.of(AuditNodeSettled) {
		node := rec.Node
		if node == nil || node.Phase != CommandPhaseSettled || node.Status != CommandStatusSucceeded || node.ExitCode == nil || *node.ExitCode != 0 {
			t.Fatalf("node_settled record %+v", node)
		}

		if node.Reviewed.ID == "" || node.Matched.ID != node.Reviewed.ID || node.Matched.CID != node.Reviewed.CID {
			t.Errorf("reviewed %+v matched %+v", node.Reviewed, node.Matched)
		}

		if node.StdoutKept != len("out") || node.Killed || node.Survived {
			t.Errorf("node counts and flags %+v", node)
		}

		if rec.Actor != "alice" || rec.InstanceID != "inst-0" || rec.Command == nil || rec.Command.Digest != commandDigest(runCommandText) {
			t.Errorf("node record actor %q instance %q command %+v", rec.Actor, rec.InstanceID, rec.Command)
		}
	}

	staged := trail.of(AuditNodeStaged)
	if staged[0].Node.Reviewed.Index != 0 || staged[1].Node.Reviewed.Index != 1 || staged[0].Node.Phase != CommandPhaseStaged {
		t.Errorf("staged records %+v %+v", staged[0].Node, staged[1].Node)
	}

	if settled := trail.only(t, AuditInstanceSettled); settled.Outcome != string(InstanceStatusSuccess) || settled.InstanceID != "inst-0" {
		t.Errorf("instance record %+v", settled)
	}

	if finished := trail.only(t, AuditTaskFinished); finished.Outcome != string(TaskStatusCompleted) || finished.Command == nil || len(finished.Command.Targets) != 1 {
		t.Errorf("finish record %+v", finished)
	}

	for _, rec := range trail.all() {
		if rec.Rebuilt {
			t.Errorf("a live record %s is marked rebuilt", rec.Event)
		}

		if rec.Command == nil || len(rec.Command.Targets) != 1 {
			t.Errorf("the %s record does not carry the targets of the task", rec.Event)
		}

		if !fix.auditBody(rec.ID) || !fix.auditIndex()[rec.ID] {
			t.Errorf("record %s is not in Vault and its index", rec.ID)
		}
	}
}

// Parallel nodes that settle at the same moment each get a record, and the
// index names every one, so no write is lost to another.
func TestCommandAuditParallelSettlesLoseNoWrite(t *testing.T) {
	const nodes = 6

	fix := newTaskFixture(t, 1, nodes, withParallel(nodes))
	trail := fix.watchCommandAudit()
	gate := fix.gate(opRunExec)

	fix.queue()

	for range nodes {
		gate.next()
	}

	gate.releaseAll()

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	trail.waitForEvent(t, AuditTaskFinished)
	fix.waitAuditIdle()

	// A start, a staged and a settled record for each node, an instance
	// settle, and a finish.
	wantRecords := 1 + 2*nodes + 1 + 1
	if got := len(trail.all()); got != wantRecords {
		t.Fatalf("saved %d records, want %d: %v", got, wantRecords, trail.events())
	}

	seen := map[int]bool{}

	for _, rec := range trail.of(AuditNodeSettled) {
		seen[rec.Node.Reviewed.Index] = true
	}

	if len(seen) != nodes {
		t.Errorf("node_settled covers nodes %v, want all %d", seen, nodes)
	}

	index := fix.auditIndex()
	if len(index) != wantRecords {
		t.Errorf("the index names %d records, want %d", len(index), wantRecords)
	}

	for _, rec := range trail.all() {
		if !index[rec.ID] || !fix.auditBody(rec.ID) {
			t.Errorf("record %s %s was lost from the index or from Vault", rec.Event, rec.ID)
		}
	}
}

// A cancel writes task_cancelled, the node that was running settles with a
// node_settled of its own, and nothing writes task_finished.
func TestCommandAuditCancelWritesTaskCancelled(t *testing.T) {
	fix := newTaskFixture(t, 2, 2, submitted())
	trail := fix.watchCommandAudit()
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
	script := fix.blockExec()

	fix.queue()
	script.wait(t, 1)
	fix.cancel()

	trail.waitForEvent(t, AuditTaskCancelled)
	trail.waitForEvent(t, AuditNodeSettled)
	fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })
	fix.waitAuditIdle()

	cancelled := trail.only(t, AuditTaskCancelled)
	if cancelled.Outcome != string(TaskStatusCancelled) || cancelled.Actor != "alice" || cancelled.Command == nil || cancelled.Command.Digest != commandDigest(runCommandText) {
		t.Errorf("cancel record %+v", cancelled)
	}

	settled := trail.only(t, AuditNodeSettled)
	if settled.Node.Status != CommandStatusFailed || !settled.Node.Killed || settled.Node.ExitCode != nil {
		t.Errorf("the cancelled node's record %+v", settled.Node)
	}

	if got := trail.of(AuditTaskFinished); len(got) != 0 {
		t.Errorf("a cancelled task wrote %d task_finished records", len(got))
	}
}

// A task that pauses, because a node failed, is not finished, and a task
// that loads paused with nothing sent has not started.
func TestCommandAuditPauseWritesNoFinish(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		fix := newTaskFixture(t, 2, 1)
		trail := fix.watchCommandAudit()
		fix.exits["dep-0/0"] = 3

		fix.queue()
		fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })
		trail.waitForEvent(t, AuditInstanceSettled)
		fix.waitAuditIdle()

		if got := trail.of(AuditTaskFinished); len(got) != 0 {
			t.Errorf("a paused task wrote %d task_finished records", len(got))
		}

		if got := trail.of(AuditTaskCancelled); len(got) != 0 {
			t.Errorf("a paused task wrote %d task_cancelled records", len(got))
		}

		settled := trail.only(t, AuditNodeSettled)
		if settled.Node.ExitCode == nil || *settled.Node.ExitCode != 3 || settled.Node.Status != CommandStatusFailed {
			t.Errorf("the failed node's record %+v", settled.Node)
		}
	})

	t.Run("never started", func(t *testing.T) {
		var trail *commandTrail

		fix := newTaskFixtureFrom(t, 1, 2, func(f *taskFixture, task *UpgradeTask) {
			trail = f.watchCommandAudit()
			task.CreationConfirmed = true
			task.Status = TaskStatusRunning
		})

		fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })
		fix.waitAuditIdle()

		if got := trail.events(); len(got) != 0 {
			t.Errorf("a task that sent nothing wrote %v", got)
		}
	})
}

// The cleanup that settles a node without sending the command still writes
// node_settled, whether a restart finds the node stranded, or a cancelled
// task is resumed, or a cancelled task cleans its held node on load.
func TestCommandAuditCleanupOnlySettlesWriteNodeSettled(t *testing.T) {
	cases := map[string]struct {
		seed   func(*UpgradeTask)
		resume bool
		done   func(UpgradeTask) bool
	}{
		"stranded on load": {
			seed: func(task *UpgradeTask) {
				restartedWith(map[int]CommandPhase{0: CommandPhaseSent})(nil, task)
				task.Instances[0].Command.VMs[0].StagedAt = &loadedStart
			},
			done: func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusFailed },
		},
		"cancelled and held on load": {
			seed: func(task *UpgradeTask) {
				task.CreationConfirmed = true
				task.Status = TaskStatusCancelled
				task.Instances[0].Status = InstanceStatusRunning
				task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
				task.Instances[0].Command.VMs[0].StagedAt = &loadedStart
				task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
			},
			done: func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled },
		},
		"cancelled and resumed": {
			seed: func(task *UpgradeTask) {
				task.CreationConfirmed = true
				task.Status = TaskStatusCancelled
				task.Paused = true
				task.Instances[0].Status = InstanceStatusRunning
				task.Instances[0].Command.VMs[0].Phase = CommandPhaseStaged
				task.Instances[0].Command.VMs[0].StagedAt = &loadedStart
				task.Instances[0].Command.VMs[0].StartedAt = &loadedStart
			},
			resume: true,
			done:   func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var trail *commandTrail

			fix := newTaskFixtureFrom(t, 1, 2, func(f *taskFixture, task *UpgradeTask) {
				trail = f.watchCommandAudit()

				// The load itself starts the cleanup, so the answer is set
				// before the manager starts.
				f.cleanups["dep-0/0"] = "cleanup started=yes exit=7 killed=no"

				tc.seed(task)
			})

			if tc.resume {
				err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
				if err != nil {
					t.Fatalf("resume: %v", err)
				}
			}

			fix.waitFor("the node to settle", tc.done)
			trail.waitForEvent(t, AuditNodeSettled)
			fix.waitAuditIdle()

			if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opRunCleanup}) {
				t.Fatalf("the node saw %v, want only cleanup", ops)
			}

			settled := trail.only(t, AuditNodeSettled)
			if settled.Node.Status != CommandStatusFailed || settled.Node.ExitCode == nil || *settled.Node.ExitCode != 7 || settled.Node.Reviewed.Index != 0 {
				t.Errorf("the cleanup's node record %+v", settled.Node)
			}

			staged := trail.only(t, AuditNodeStaged)
			if !staged.Rebuilt || staged.Node.Reviewed.Index != 0 {
				t.Errorf("the lost staged record came back as %+v rebuilt=%v", staged.Node, staged.Rebuilt)
			}
		})
	}
}

// settledSeed is a task a broker left finished, with its two nodes settled
// and no audit record written.
func settledSeed(f *taskFixture, task *UpgradeTask) {
	asSubmitted(task)

	staged := loadedStart.Add(-time.Second)
	started := loadedStart
	ended := loadedStart.Add(time.Minute)
	exit := 0

	task.CreationConfirmed = true
	task.Status = TaskStatusCompleted
	task.StartedAt = &started
	task.CompletedAt = &ended
	task.CompletedCount = 1
	task.Instances[0].Status = InstanceStatusSuccess
	task.Instances[0].CompletedAt = &ended

	for i := range task.Instances[0].Command.VMs {
		node := &task.Instances[0].Command.VMs[i]
		node.Matched = node.Reviewed.clone()
		node.Phase = CommandPhaseSettled
		node.Status = CommandStatusSucceeded
		node.ExitCode = &exit
		node.StagedAt = &staged
		node.StartedAt = &started
		node.EndedAt = &ended
	}
}

// A broker that went down between the saves of a run and their audit
// writes writes the missing records at the next load, once. A record that
// already landed is not written again, and a second load writes nothing.
func TestCommandAuditBackfillWritesWhatACrashSkippedOnce(t *testing.T) {
	startedID := auditID(taskFixtureID, "", AuditTaskStarted, "")

	var trail *commandTrail

	fix := newTaskFixtureFrom(t, 1, 2, func(f *taskFixture, task *UpgradeTask) {
		settledSeed(f, task)

		// The start record landed before the crash.
		ctx := context.Background()

		err := f.vault.confirmVault.Put(ctx, commandAuditVaultPath+"/"+startedID, AuditRecord{ID: startedID, Event: AuditTaskStarted, TaskID: taskFixtureID, Command: &CommandAudit{}})
		if err != nil {
			t.Fatalf("storing the landed record: %v", err)
		}

		err = f.vault.confirmVault.Put(ctx, commandAuditVaultPath, map[string]interface{}{startedID: map[string]interface{}{"event": AuditTaskStarted}})
		if err != nil {
			t.Fatalf("storing the index: %v", err)
		}

		trail = f.watchCommandAudit()
	})

	wantEvents := []string{AuditNodeStaged, AuditNodeSettled, AuditNodeStaged, AuditNodeSettled, AuditInstanceSettled, AuditTaskFinished}

	waitUntil(t, "the rebuilt records", func() bool { return fix.manager.auditIdle() && len(trail.all()) >= len(wantEvents) })

	if got := trail.events(); !slices.Equal(got, wantEvents) {
		t.Fatalf("rebuilt %v, want %v and no second start", got, wantEvents)
	}

	for _, rec := range trail.all() {
		if !rec.Rebuilt || rec.Actor != "alice" || rec.Command == nil || rec.Command.Text != runCommandText {
			t.Errorf("rebuilt %s record rebuilt=%v actor=%q command=%+v", rec.Event, rec.Rebuilt, rec.Actor, rec.Command)
		}
	}

	settled := trail.of(AuditNodeSettled)
	if !settled[0].At.Equal(loadedStart.Add(time.Minute)) || settled[0].Node.Matched.ID != "vm-0-0" {
		t.Errorf("the rebuilt settle has time %v and matched %+v", settled[0].At, settled[0].Node.Matched)
	}

	if staged := trail.of(AuditNodeStaged)[0]; staged.Node.Phase != CommandPhaseStaged || staged.Node.Status != "" || !staged.At.Equal(loadedStart.Add(-time.Second)) {
		t.Errorf("the rebuilt staged record %+v at %v", staged.Node, staged.At)
	}

	if fix.vault.putCount(commandAuditVaultPath+"/"+startedID) != 1 {
		t.Error("the start record that had landed was written again")
	}

	index := fix.auditIndex()
	if len(index) != len(wantEvents)+1 {
		t.Errorf("the index names %d records, want %d", len(index), len(wantEvents)+1)
	}

	for id := range index {
		if puts := fix.vault.putCount(commandAuditVaultPath + "/" + id); puts != 1 {
			t.Errorf("record %s was written %d times", id, puts)
		}
	}

	fix.manager.Stop()

	second := NewManager(fix.log, fix.director, fix.vault)
	t.Cleanup(second.Stop)
	second.Start()
	waitForLoad(t, second)
	waitUntil(t, "the second load's audit queue", second.auditIdle)

	if got := len(trail.all()); got != len(wantEvents) {
		t.Errorf("the second load wrote %d more records", got-len(wantEvents))
	}

	for id := range index {
		if puts := fix.vault.putCount(commandAuditVaultPath + "/" + id); puts != 1 {
			t.Errorf("after the second load record %s was written %d times", id, puts)
		}
	}
}

// A node that was staged and then settled before it was sent has no start
// time, and its lost node_staged record is still rebuilt, at the time the
// node was staged. A node that settled without ever being staged gets none.
func TestCommandAuditBackfillRebuildsStagedNodeThatWasNeverSent(t *testing.T) {
	var trail *commandTrail

	staged := loadedStart.Add(-time.Second)

	fix := newTaskFixtureFrom(t, 1, 2, func(f *taskFixture, task *UpgradeTask) {
		settledSeed(f, task)

		failed := &task.Instances[0].Command.VMs[0]
		failed.Status = CommandStatusFailed
		failed.ExitCode = nil
		failed.StartedAt = nil
		failed.StagedAt = &staged

		never := &task.Instances[0].Command.VMs[1]
		never.Status = CommandStatusFailed
		never.ExitCode = nil
		never.StartedAt = nil
		never.StagedAt = nil

		trail = f.watchCommandAudit()
	})

	waitUntil(t, "the rebuilt records", func() bool { return fix.manager.auditIdle() && len(trail.all()) >= 6 })

	rebuilt := trail.of(AuditNodeStaged)
	if len(rebuilt) != 1 {
		t.Fatalf("rebuilt %d node_staged records, want one for the node that was staged", len(rebuilt))
	}

	if rebuilt[0].Node.Reviewed.ID != "vm-0-0" || !rebuilt[0].At.Equal(staged) {
		t.Errorf("the rebuilt staged record is for %q at %v, want vm-0-0 at %v", rebuilt[0].Node.Reviewed.ID, rebuilt[0].At, staged)
	}

	for _, rec := range trail.all() {
		if len(rec.Command.Targets) != 1 || len(rec.Command.Targets[0].Nodes) != 2 {
			t.Errorf("the rebuilt %s record carries targets %+v", rec.Event, rec.Command.Targets)
		}
	}
}

// The log line of an audit record holds the digest and the length, and
// never the command text, while Vault keeps the whole record.
func TestCommandAuditLogLineHoldsTheDigestAndNeverTheText(t *testing.T) {
	const textCanary = "text-canary-5d1e8a07"

	fix := newTaskFixture(t, 1, 1)
	trail := fix.watchCommandAudit()

	command := "echo " + textCanary
	exit := 0
	rec := AuditRecord{
		ID: "rec-1", At: loadedStart, Event: AuditNodeSettled, TaskID: taskFixtureID, InstanceID: "inst-0", Actor: "alice", From: "10.0.0.5",
		Reason: "CHG-1",
		Command: &CommandAudit{
			Text: command, Digest: commandDigest(command), Length: len(command), RunAs: "vcap", CredentialPresented: true,
			Targets: []CommandAuditTarget{{Deployment: "dep-0", InstanceID: "inst-0"}},
		},
		Node: &CommandNodeAudit{Reviewed: CommandVMIdentity{Group: "redis", Index: 0, ID: "vm-0-0"}, Phase: CommandPhaseSettled, Status: CommandStatusSucceeded, ExitCode: &exit, StdoutKept: 12},
	}

	if !fix.manager.audit(context.Background(), rec) {
		t.Fatal("the record was not saved")
	}

	logs := fix.log.joined()

	if strings.Contains(logs, textCanary) {
		t.Error("a log line holds the command text")
	}

	line := auditLine(t, fix.log)
	for _, want := range []string{"digest=" + commandDigest(command), "length=" + strconv.Itoa(len(command)), "node=redis/0", "actor=\"alice\"", "credential=true", `"digest":"` + commandDigest(command) + `"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line lacks %q: %s", want, line)
		}
	}

	if got := trail.only(t, AuditNodeSettled); got.Command.Text != command {
		t.Errorf("Vault kept the text %q, want %q verbatim", got.Command.Text, command)
	}
}

// auditLine returns the one command audit line a logger kept.
func auditLine(t *testing.T, log *captureLog) string {
	t.Helper()

	log.mu.Lock()
	defer log.mu.Unlock()

	var found []string

	for _, line := range log.lines {
		if strings.HasPrefix(line, "audit=command-run ") {
			found = append(found, line)
		}
	}

	if len(found) != 1 {
		t.Fatalf("want one command audit line, got %d", len(found))
	}

	return found[0]
}

// opSession tells the fake VM which operation the runner is about to send,
// so its handler can answer as the wrapper scripts would. The nodes of the
// test run one at a time, so one operation is in flight.
type opSession struct {
	PatchSession

	op *atomic.Value
}

func (s *opSession) Run(ctx context.Context, host int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	s.op.Store(op)

	return s.PatchSession.Run(ctx, host, op, args, stdin) //nolint:wrapcheck // the test passes the answer on
}

// groupDirector hands a session one host for each node of the group.
type groupDirector struct {
	*sshDirector
}

func (d *groupDirector) SSHGroupSession(_ context.Context, deployment, group string, indices []int) (*sshconn.Session, error) {
	session := &sshconn.Session{Deployment: deployment}

	for _, index := range indices {
		session.Hosts = append(session.Hosts, sshconn.Host{Job: group, IndexOrID: strconv.Itoa(index), Address: d.vm.listener.Addr().String()})
	}

	return session, nil
}

// A whole two-node command task runs through the manager and the real SSH
// runner over a fake VM, with one logger in every package that logs on the
// path. A canary in the command text and another in each stream reach no
// line any package logged and no error the task shows, and the audit line
// carries the digest and the length.
func TestCommandRunLogsNoCanaryInAnyPackage(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) { checkNoCanaries(t, mode) })
	}
}

// checkNoCanaries runs the task to the end the mode names, which is a
// success, a first node that exits non-zero, or a cancel while the first
// node's command is running.
func checkNoCanaries(t *testing.T, mode string) {
	t.Helper()

	const (
		textCanary   = "text-canary-b3c9e1f4"
		stdoutCanary = "stdout-canary-72ad90c1"
		stderrCanary = "stderr-canary-1e4f6b28"
	)

	command := "echo " + textCanary
	digest := commandDigest(command)

	var op atomic.Value

	op.Store("")

	running := make(chan struct{}, 4)
	hold := make(chan struct{})

	var release sync.Once

	releaseHold := func() { release.Do(func() { close(hold) }) }

	t.Cleanup(releaseHold)

	vm := startSSHVM(t, func(channel gossh.Channel, _ <-chan struct{}) uint32 {
		_, _ = io.Copy(io.Discard, channel)

		switch op.Load() {
		case opRunPrepare:
			_, _ = io.WriteString(channel, "staged bytes="+strconv.Itoa(len(command))+" sha256="+digest+"\n")
		case opRunExec:
			_, _ = io.WriteString(channel, stdoutCanary)
			_, _ = io.WriteString(channel.Stderr(), stderrCanary)

			switch mode {
			case "failure":
				return 3
			case "cancel":
				running <- struct{}{}

				<-hold
			}
		case opRunCleanup:
			record := runCleanupDone
			if mode == "cancel" {
				record = "cleanup started=yes exit=- killed=yes"
			}

			_, _ = io.WriteString(channel, record+"\n")
		}

		return 0
	})

	fix := newTaskFixtureFrom(t, 1, 2, nil, submitted(), func(f *taskFixture, task *UpgradeTask) {
		task.Command.Command = command
		task.Command.CommandDigest = digest
		task.Command.CommandLength = len(command)

		director := &groupDirector{sshDirector: &sshDirector{vm: vm, t: t}}
		open := NewCommandRunner(director, f.log)

		f.runner = func(ctx context.Context, deployment, group string, indices []int) (PatchSession, error) {
			sess, err := open(ctx, deployment, group, indices)
			if err != nil {
				return nil, err
			}

			return &opSession{PatchSession: sess, op: &op}, nil
		}
	})

	trail := fix.watchCommandAudit()

	fix.queue()

	var done UpgradeTask

	switch mode {
	case "success":
		done = fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
		trail.waitForEvent(t, AuditTaskFinished)
	case "failure":
		done = fix.waitFor("the task to fail", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })
		trail.waitForEvent(t, AuditTaskFinished)
	case "cancel":
		<-running
		fix.cancel()
		releaseHold()

		done = fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })
		trail.waitForEvent(t, AuditTaskCancelled)
	}

	fix.waitAuditIdle()

	logs := fix.log.joined()

	// The controls show the check can see what it looks for. The runner
	// logged its quiet line, the audit line carries the digest, and Vault
	// holds the text and the output on purpose.
	if !strings.Contains(logs, "ended with") {
		t.Error("the SSH runner logged nothing, so the check does not cover it")
	}

	if !strings.Contains(logs, "digest="+digest) || !strings.Contains(logs, "length="+strconv.Itoa(len(command))) {
		t.Error("no audit line carries the digest and the length")
	}

	if got := trail.only(t, AuditTaskStarted); got.Command.Text != command {
		t.Error("the audit record in Vault does not hold the command text")
	}

	if !fix.holdsOutputRecord() {
		t.Error("Vault holds no output record, so the stream canaries were never kept anywhere")
	}

	for _, canary := range []string{textCanary, stdoutCanary, stderrCanary} {
		if strings.Contains(logs, canary) {
			t.Errorf("a captured log line holds the canary %q", canary[:strings.Index(canary, "-canary")+7])
		}
	}

	// No error the API shows for the task holds a canary either.
	for _, inst := range done.Instances {
		texts := []string{inst.Error}

		for _, node := range inst.Command.VMs {
			texts = append(texts, node.Error, node.Reason)
		}

		for _, text := range texts {
			for _, canary := range []string{textCanary, stdoutCanary, stderrCanary} {
				if strings.Contains(text, canary) {
					t.Errorf("an error string of the task holds a canary: %q", text)
				}
			}
		}
	}

	for _, rec := range trail.all() {
		raw, _ := json.Marshal(rec)
		if strings.Contains(string(raw), stdoutCanary) || strings.Contains(string(raw), stderrCanary) {
			t.Error("an audit record holds output")
		}
	}

}

// indexRefusingVault refuses to read any audit index.
type indexRefusingVault struct {
	interfaces.Vault
}

func (v indexRefusingVault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	if path == commandAuditVaultPath || path == certificateAuditVaultPath {
		return false, errPutRefused
	}

	return v.Vault.Get(ctx, path, out)
}

// The lines that say records were given up name the trail the records
// belong to, one line for each trail, and carry no text of the command.
func TestCommandAuditGiveUpLinesNameTheirTrail(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	fix.manager.auditQ.mu.Lock()
	fix.manager.auditQ.pending = []AuditRecord{
		{ID: "c1", Event: AuditNodeSettled, TaskID: taskFixtureID, Command: &CommandAudit{}},
		{ID: "c2", Event: AuditTaskFinished, TaskID: taskFixtureID, Command: &CommandAudit{}},
		{ID: "r1", Event: AuditSkipped, TaskID: "cert-task"},
	}
	fix.manager.auditQ.mu.Unlock()

	fix.manager.dropPendingAudit()

	logs := fix.log.joined()

	for _, want := range []string{"audit=command-run 2 queued records were not saved", "audit=certificate-rotation 1 queued records were not saved"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q:\n%s", want, logs)
		}
	}

	fix.assertNoCommandText()
}

func TestCommandAuditUnreadableIndexLineNamesTheCommandTrail(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	fix.manager.auditRetry = time.Nanosecond
	fix.manager.vault = indexRefusingVault{Vault: fix.manager.vault}

	fix.manager.retryBackfill([]string{taskFixtureID})

	logs := fix.log.joined()
	if !strings.Contains(logs, "audit=command-run the audit index could not be read, so lost records of 1 tasks were not rebuilt") {
		t.Errorf("the log lacks the command trail line:\n%s", logs)
	}

	if strings.Contains(logs, "audit=certificate-rotation") {
		t.Errorf("the line of a command task names the certificate trail:\n%s", logs)
	}

	fix.assertNoCommandText()
}
