package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

const cmdRequestID = "7c1d"

// cmdLogger keeps every line the manager logs, formatted, so a test can
// prove that none of them holds the command.
type cmdLogger struct {
	logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *cmdLogger) Named(string) logger.Logger { return l }

func (l *cmdLogger) keep(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}

func (l *cmdLogger) Debug(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *cmdLogger) Info(msg string, args ...interface{})    { l.keep(msg, args...) }
func (l *cmdLogger) Warn(msg string, args ...interface{})    { l.keep(msg, args...) }
func (l *cmdLogger) Error(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *cmdLogger) Debugf(msg string, args ...interface{})  { l.keep(msg, args...) }
func (l *cmdLogger) Infof(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *cmdLogger) Warnf(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *cmdLogger) Errorf(msg string, args ...interface{})  { l.keep(msg, args...) }
func (l *cmdLogger) Warning(msg string, args ...interface{}) { l.keep(msg, args...) }

func (l *cmdLogger) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// cmdCreateVault answers instance records and saves everything else the way
// waitVault does. Writes of command task records are counted from 1. A write
// the refuse rule picks stores nothing and fails, and one the land rule picks
// stores the record and fails anyway. While blind is set, every read of a
// command task record fails, so the manager can't tell that a write landed.
type cmdCreateVault struct {
	*waitVault

	records map[string]map[string]any

	script sync.Mutex
	writes int
	refuse func(n int) bool
	land   func(n int) bool
	blind  bool
}

// script sets the rules under the lock.
func (v *cmdCreateVault) setRules(refuse, land func(n int) bool, blind bool) {
	v.script.Lock()
	defer v.script.Unlock()

	v.refuse, v.land, v.blind = refuse, land, blind
}

func (v *cmdCreateVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	v.script.Lock()
	blind := v.blind && strings.HasPrefix(path, commandTasksVaultPath+"/")
	v.script.Unlock()

	if blind {
		return false, errGetRefused
	}

	record, ok := v.records[path]
	if !ok {
		return v.waitVault.Get(ctx, path, result)
	}

	raw, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", path, err)
	}

	return true, json.Unmarshal(raw, result)
}

func (v *cmdCreateVault) Put(ctx context.Context, path string, data interface{}) error {
	if !strings.HasPrefix(path, commandTasksVaultPath+"/") {
		return v.waitVault.Put(ctx, path, data)
	}

	v.script.Lock()
	v.writes++
	number := v.writes
	refused := v.refuse != nil && v.refuse(number)
	unacknowledged := v.land != nil && v.land(number)
	v.script.Unlock()

	if refused {
		return errPutRefused
	}

	err := v.waitVault.Put(ctx, path, data)
	if err == nil && unacknowledged {
		return errAnswerLost
	}

	return err
}

// record returns the command task record as the vault holds it.
func (v *cmdCreateVault) record(t *testing.T, taskID string) (UpgradeTask, bool) {
	t.Helper()

	v.mu.Lock()
	raw, ok := v.saved[commandTasksVaultPath+"/"+taskID]
	v.mu.Unlock()

	var task UpgradeTask
	if ok {
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatalf("the saved record of task %s is not a task: %v", taskID, err)
		}
	}

	return task, ok
}

// countSaved returns how many saved paths start with the prefix.
func (v *cmdCreateVault) countSaved(prefix string) int {
	v.mu.Lock()
	defer v.mu.Unlock()

	count := 0

	for path := range v.saved {
		if strings.HasPrefix(path, prefix) {
			count++
		}
	}

	return count
}

// confirmedRecordsFrom counts the saved command task records that say their
// creation was confirmed and came from the preview.
func (v *cmdCreateVault) confirmedRecordsFrom(t *testing.T, previewID string) int {
	t.Helper()

	v.mu.Lock()
	defer v.mu.Unlock()

	count := 0

	for path, raw := range v.saved {
		if !strings.HasPrefix(path, commandTasksVaultPath+"/") {
			continue
		}

		var task UpgradeTask
		if err := json.Unmarshal(raw, &task); err != nil {
			t.Fatalf("the saved record at %s is not a task: %v", path, err)
		}

		if task.CreationConfirmed && task.Command != nil && task.Command.PreviewID == previewID {
			count++
		}
	}

	return count
}

type cmdCreateEnv struct {
	manager *Manager
	vault   *cmdCreateVault
	log     *cmdLogger
	now     *atomic.Value
}

// stubAuthorizer answers every stored check with one error.
type stubAuthorizer struct {
	ConfigAuthorizer

	stored error
	calls  atomic.Int32
}

func (a *stubAuthorizer) AuthorizeStored(context.Context, CommandTaskSpec) error {
	a.calls.Add(1)

	return a.stored
}

// flippingAuthorizer allows every stored task until off is set, and refuses
// every one after.
type flippingAuthorizer struct {
	ConfigAuthorizer

	off *atomic.Bool
}

func (a *flippingAuthorizer) AuthorizeStored(context.Context, CommandTaskSpec) error {
	if a.off.Load() {
		return errCommandsDisabled()
	}

	return nil
}

// newCreateCmdEnv returns a loaded manager over a director with three
// instances. Commands are on and root is off unless change says otherwise.
func newCreateCmdEnv(t *testing.T, change func(*Deps)) *cmdCreateEnv {
	t.Helper()

	director := &cmdDirector{vms: map[string][]bosh.VM{
		"redis-i1": {
			{ID: "id-0", CID: "cid-0", Job: "redis", Index: 0, AZ: "z1", IPs: []string{"10.0.0.4"}, State: "running", JobState: "running"},
			{ID: "id-1", CID: "", Job: "redis", Index: 1, AZ: "z2", IPs: []string{"10.0.0.5"}, State: "missing"},
			{ID: "id-2", CID: "cid-2", Job: "redis", Index: 2, AZ: "z3", IPs: []string{"10.0.0.6"}, State: "stopped", JobState: "stopped"},
		},
		"pg-i2":    {{ID: "id-9", CID: "cid-9", Job: "pg", Index: 0, AZ: "z1", State: "running", JobState: "running"}},
		"redis-i3": {{ID: "id-7", CID: "cid-7", Job: "redis", Index: 0, State: "running", JobState: "running"}},
	}}

	vault := &cmdCreateVault{waitVault: newWaitVault(), records: map[string]map[string]any{
		"i1": {"deployment_name": "redis-i1", "instance_name": "one", "service_id": "redis", "plan_id": "small"},
		"i2": {"deployment_name": "pg-i2", "service_id": "postgres", "plan_id": "tiny"},
		"i3": {"deployment_name": "redis-i3", "service_id": "redis", "plan_id": "small"},
	}}

	log := &cmdLogger{}

	manager := NewManager(log, director, vault)
	t.Cleanup(manager.Stop)

	now := &atomic.Value{}
	now.Store(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))

	deps := Deps{
		Now:         func() time.Time { return now.Load().(time.Time) },
		Commands:    func() config.CommandsConfig { return config.CommandsConfig{} },
		WorkerCount: func(context.Context) (int, bool) { return 4, true },
	}
	if change != nil {
		change(&deps)
	}

	if err := manager.Configure(deps); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	manager.Start()
	waitForLoad(t, manager)

	manager.createRetry = 5 * time.Millisecond

	return &cmdCreateEnv{manager: manager, vault: vault, log: log, now: now}
}

// previewFor stores a command preview for the instances with the settings of
// req and returns its ID.
func (e *cmdCreateEnv) previewFor(t *testing.T, req CommandPreviewRequest) *CommandPreview {
	t.Helper()

	preview, err := e.manager.PreviewCommand(context.Background(), req)
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	return preview
}

func createCmdReq(previewID string) CommandCreateRequest {
	return CommandCreateRequest{
		PreviewID:       previewID,
		ClientRequestID: cmdRequestID,
		Command:         secretCommand,
		RunAs:           "vcap",
		TimeoutSeconds:  600,
		Name:            "CHG-77 flush caches",
		Reason:          "CHG-77",
	}
}

// expectNoCommandText fails the test when a string holds the command or its
// digest. It never prints the string it checked.
func expectNoCommandText(t *testing.T, where, text string) {
	t.Helper()

	if strings.Contains(text, secretCommand) || strings.Contains(text, commandDigest(secretCommand)) {
		t.Fatalf("%s holds the command or its digest", where)
	}
}

// expectCommandBytes fails the test unless got is the command, and prints
// only lengths when it is not.
func expectCommandBytes(t *testing.T, where, got string) {
	t.Helper()

	if got != secretCommand {
		t.Fatalf("%s holds a command of %d bytes that differs from the %d bytes sent", where, len(got), len(secretCommand))
	}
}

// expectPreviewKept fails the test unless the preview can still be spent.
func expectPreviewKept(t *testing.T, e *cmdCreateEnv, previewID string) {
	t.Helper()

	if _, err := e.manager.certDeps().Previews.GetCommand(previewID); err != nil {
		t.Fatalf("expected the preview to be unspent, got %v", err)
	}
}

// expectNoSpend fails the test when the preview store was ever asked to
// spend a preview, so a peek can't pass for a spend that was put back.
func expectNoSpend(t *testing.T, e *cmdCreateEnv) {
	t.Helper()

	if spends := e.manager.certDeps().Previews.spends.Load(); spends != 0 {
		t.Fatalf("expected no spend, got %d", spends)
	}
}

// expectPreviewSpent fails the test unless the preview is gone.
func expectPreviewSpent(t *testing.T, e *cmdCreateEnv, previewID string) {
	t.Helper()

	_, err := e.manager.certDeps().Previews.GetCommand(previewID)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
}

// expectTasksNone fails the test when the manager holds a task or the guard
// holds an instance.
func expectTasksNone(t *testing.T, e *cmdCreateEnv) {
	t.Helper()

	expectNothingHeld(t, e.manager)

	if tasks := e.manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no task, got %d", len(tasks))
	}

	if count := e.vault.countSaved(commandTasksVaultPath); count != 0 {
		t.Fatalf("expected nothing saved under command-tasks, got %d entries", count)
	}
}

// A create from a spent preview makes a command task that carries the
// command and the settings, holds every instance under the command kind,
// lists each reviewed node as pending, is saved under command-tasks, and
// records who created it.
func TestCreateCommandTaskBuildsTask(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1", "i3"))
	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "admin", RemoteAddr: "10.0.0.5:4000"})

	req := createCmdReq(preview.ID)
	req.CredentialPresented = true

	task, existing, err := env.manager.CreateCommandTask(ctx, req)
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	if existing {
		t.Fatal("a new create should not report an existing task")
	}

	if task.Kind != TaskKindCommand || task.Status != TaskStatusPending || task.Name != req.Name || task.ClientRequestID != cmdRequestID {
		t.Fatalf("wrong task header: kind %q status %q name %q request %q", task.Kind, task.Status, task.Name, task.ClientRequestID)
	}

	if task.CreatedBy != "admin" || task.CreatedFrom != "10.0.0.5:4000" || !task.CreationConfirmed {
		t.Fatalf("wrong actor or confirmation: by %q from %q confirmed %v", task.CreatedBy, task.CreatedFrom, task.CreationConfirmed)
	}

	spec := task.Command
	if spec == nil {
		t.Fatal("the task has no command spec")
	}

	expectCommandBytes(t, "the spec", spec.Command)

	if spec.CommandDigest != commandDigest(secretCommand) || spec.CommandLength != len(secretCommand) {
		t.Fatalf("wrong digest or length: %d", spec.CommandLength)
	}

	if spec.RunAs != "vcap" || spec.Shell != commandShell || spec.PreviewID != preview.ID ||
		spec.TimeoutSeconds != 600 || spec.Parallel || spec.MaxInFlight != 0 || spec.Reason != "CHG-77" || !spec.CredentialPresented {
		t.Fatalf("wrong spec settings: %+v", CommandSummary{RunAs: spec.RunAs, TimeoutSeconds: spec.TimeoutSeconds, Parallel: spec.Parallel, MaxInFlight: spec.MaxInFlight})
	}

	if task.TotalCount != 2 || len(task.Instances) != 2 {
		t.Fatalf("expected two instances, got %d with total %d", len(task.Instances), task.TotalCount)
	}

	first := task.Instances[0]
	if first.InstanceID != "i1" || first.DeploymentName != "redis-i1" || first.Status != InstanceStatusPending || first.Command == nil {
		t.Fatalf("wrong first instance: %+v", first)
	}

	if first.Command.Deployment != "redis-i1" || len(first.Command.VMs) != 3 {
		t.Fatalf("expected the three reviewed nodes of redis-i1, got %+v", first.Command)
	}

	node := first.Command.VMs[0]
	if node.Reviewed.Group != "redis" || node.Reviewed.Index != 0 || node.Reviewed.ID != "id-0" || node.Reviewed.CID != "cid-0" ||
		node.Reviewed.AZ != "z1" || node.Phase != CommandPhasePending {
		t.Fatalf("wrong first node: %+v", node)
	}

	if skipped := first.Command.VMs[1]; skipped.Reason != noVMReason || skipped.Error != "" {
		t.Fatalf("a skipped node keeps its reason out of Error: reason %q error %q", skipped.Reason, skipped.Error)
	}

	if first.Command.VMs[1].Phase != CommandPhaseSkipped || first.Command.VMs[2].Phase != CommandPhasePending {
		t.Fatalf("a node with no VM is skipped and every other node is pending: %q %q", first.Command.VMs[1].Phase, first.Command.VMs[2].Phase)
	}

	for _, id := range []string{"i1", "i3"} {
		holder, held := env.manager.Guard().Holder(id, "")
		if !held || holder.Kind != mutation.KindCommand || holder.TaskID != task.ID {
			t.Fatalf("expected the task to hold %s as a command, got %+v (held %v)", id, holder, held)
		}
	}

	stored, ok := env.vault.record(t, task.ID)
	if !ok {
		t.Fatal("the task is not saved under command-tasks")
	}

	if !stored.CreationConfirmed || stored.Kind != TaskKindCommand || stored.Command == nil {
		t.Fatalf("the saved record is not a confirmed command task: %+v", stored.Kind)
	}

	expectCommandBytes(t, "the saved record", stored.Command.Command)

	var index map[string]any

	if exists, err := env.vault.Get(context.Background(), commandTasksVaultPath, &index); err != nil || !exists || index[task.ID] == nil {
		t.Fatalf("the task is not in the command-tasks index: %v", err)
	}
}

// The task takes its settings from the preview. A serial preview and a
// create that sends parallel false with a max in flight of 99 make a spec
// with a max in flight of 0, and a timeout of 0 becomes the default.
func TestCreateCommandTaskSettingsComeFromPreview(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	serial := cmdReq("i1")
	serial.TimeoutSeconds = 0
	preview := env.previewFor(t, serial)

	req := createCmdReq(preview.ID)
	req.TimeoutSeconds = 0
	req.Parallel = false
	req.MaxInFlight = 99

	task, _, err := env.manager.CreateCommandTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	if task.Command.MaxInFlight != 0 || task.Command.Parallel || task.Command.TimeoutSeconds != CommandTimeoutDefault {
		t.Fatalf("expected a serial spec with the default timeout, got parallel %v max %d timeout %d",
			task.Command.Parallel, task.Command.MaxInFlight, task.Command.TimeoutSeconds)
	}

	parallel := cmdReq("i3")
	parallel.Parallel = true
	parallel.MaxInFlight = 2
	second := env.previewFor(t, parallel)

	req = createCmdReq(second.ID)
	req.ClientRequestID = "other"
	req.Parallel = true
	req.MaxInFlight = 2

	task, _, err = env.manager.CreateCommandTask(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateCommandTask parallel: %v", err)
	}

	if !task.Command.Parallel || task.Command.MaxInFlight != 2 {
		t.Fatalf("expected the parallel settings of the preview, got %v %d", task.Command.Parallel, task.Command.MaxInFlight)
	}
}

// A create whose command, user, timeout, or parallel settings differ from
// the preview's is a 409 preview_mismatch that spends nothing, reserves
// nothing, and saves nothing.
func TestCreateCommandTaskMismatchSpendsNothing(t *testing.T) {
	t.Parallel()

	changes := map[string]func(*CommandCreateRequest){
		"command text":   func(r *CommandCreateRequest) { r.Command = "echo another-marker" },
		"command length": func(r *CommandCreateRequest) { r.Command += " " },
		"user":           func(r *CommandCreateRequest) { r.RunAs = "root" },
		"timeout":        func(r *CommandCreateRequest) { r.TimeoutSeconds = 30 },
		"parallel":       func(r *CommandCreateRequest) { r.Parallel = true; r.MaxInFlight = 2 },
	}

	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env := newCreateCmdEnv(t, func(d *Deps) {
				d.Commands = func() config.CommandsConfig {
					on := true

					return config.CommandsConfig{Root: &on}
				}
			})
			preview := env.previewFor(t, cmdReq("i1"))

			req := createCmdReq(preview.ID)
			change(&req)

			_, _, err := env.manager.CreateCommandTask(context.Background(), req)
			expectAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)
			expectNoCommandText(t, "the error", err.Error())
			expectNoSpend(t, env)
			expectPreviewKept(t, env, preview.ID)
			expectTasksNone(t, env)
		})
	}
}

// A create across two services succeeds.
func TestCreateCommandTaskAcrossServices(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1", "i2"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	if len(task.Instances) != 2 {
		t.Fatalf("expected two instances, got %d", len(task.Instances))
	}
}

// An unknown, expired, or already spent preview answers 410 preview_expired
// the way the certificate create does, and a certificate preview answers
// 409 preview_mismatch.
func TestCreateCommandTaskPreviewGone(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq("pv-unknown"))
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	preview := env.previewFor(t, cmdReq("i1"))
	req := createCmdReq(preview.ID)
	req.ClientRequestID = ""

	if _, _, err = env.manager.CreateCommandTask(context.Background(), req); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, _, err = env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	env.manager.certDeps().Previews.Put(&Preview{ID: "pv-cert"}, time.Hour)

	req.PreviewID = "pv-cert"
	_, _, err = env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)
}

// A create that AuthorizeStored refuses fails with the authorizer's error
// before anything is spent, reserved, or saved.
func TestCreateCommandTaskAuthorizerRefuses(t *testing.T) {
	t.Parallel()

	stub := &stubAuthorizer{stored: apierr.New(http.StatusForbidden, apierr.CodeCommandsDisabled, "running commands is disabled")}
	env := newCreateCmdEnv(t, func(d *Deps) { d.Authorizer = stub })
	preview := env.previewFor(t, cmdReq("i1"))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeCommandsDisabled)

	if stub.calls.Load() == 0 {
		t.Fatal("the authorizer was never asked")
	}

	expectNoSpend(t, env)
	expectPreviewKept(t, env, preview.ID)
	expectTasksNone(t, env)
}

// With no authorizer configured, the manager's own refuses a root command
// while root runs are off, and a refusal spends nothing.
func TestCreateCommandTaskDefaultAuthorizerRefusesRoot(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	root := cmdReq("i1")
	root.RunAs = "root"
	preview := env.previewFor(t, root)

	req := createCmdReq(preview.ID)
	req.RunAs = "root"

	_, _, err := env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRootDisabled)
	expectPreviewKept(t, env, preview.ID)
	expectTasksNone(t, env)
}

// A second create for an instance a command task holds is a 409
// instance_busy, and so is a certificate task for it. A busy instance in the
// middle of a create frees what the create took and keeps the preview.
func TestCreateCommandTaskBusy(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	// Both previews are taken while nothing is held, since a preview marks a
	// held instance blocked.
	first := env.previewFor(t, cmdReq("i1"))
	second := env.previewFor(t, cmdReq("i3", "i1"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(first.ID))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err = env.manager.Guard().Acquire("i1", "redis-i1", mutation.Holder{Kind: mutation.KindCertificates, TaskID: "cert-1", Description: "certificate rotation"})
	if !errors.Is(err, mutation.ErrInstanceBusy) {
		t.Fatalf("a certificate task should find i1 busy, got %v", err)
	}

	req := createCmdReq(second.ID)
	req.ClientRequestID = "second"

	_, _, err = env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeInstanceBusy)
	expectNoCommandText(t, "the error", err.Error())

	if _, held := env.manager.Guard().Holder("i3", ""); held {
		t.Fatal("the refused create still holds i3")
	}

	if holder, held := env.manager.Guard().Holder("i1", ""); !held || holder.TaskID != task.ID {
		t.Fatalf("the first task should still hold i1, got %+v", holder)
	}

	if len(env.manager.ListTasks()) != 1 || env.vault.countSaved(commandTasksVaultPath+"/") != 1 {
		t.Fatal("the refused create left a task or a record behind")
	}

	expectPreviewKept(t, env, second.ID)
}

// expectCommandHolder fails the test unless the guard holds the instance for the
// task as a command.
func expectCommandHolder(t *testing.T, e *cmdCreateEnv, instanceID, taskID string) {
	t.Helper()

	holder, held := e.manager.Guard().Holder(instanceID, "")
	if !held || holder.Kind != mutation.KindCommand || holder.TaskID != taskID {
		t.Fatalf("expected task %s to hold %s as a command, got %+v (held %v)", taskID, instanceID, holder, held)
	}
}

// onlyTask returns the one task the manager holds.
func onlyTask(t *testing.T, e *cmdCreateEnv) UpgradeTask {
	t.Helper()

	tasks := e.manager.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected one task in memory, got %d", len(tasks))
	}

	return *tasks[0]
}

// expectUnconfirmedCommandCreate checks what a create whose save was not
// confirmed leaves behind and returns the task's ID. The error is a 500 that
// holds no command text, the task is failed in memory, its instances stay
// held, its preview stays spent, and a create for the same instances is
// refused.
func expectUnconfirmedCommandCreate(t *testing.T, env *cmdCreateEnv, err error, previewID string, instances ...string) string {
	t.Helper()

	expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)
	expectNoCommandText(t, "the error", err.Error())
	expectNoCommandText(t, "the log", env.log.text())

	task := onlyTask(t, env)
	if task.Status != TaskStatusFailed || task.CreationConfirmed {
		t.Fatalf("expected a failed, unconfirmed task, got %q confirmed %v", task.Status, task.CreationConfirmed)
	}

	for i := range task.Instances {
		if task.Instances[i].Status != InstanceStatusFailed || task.Instances[i].Error != createUnconfirmedMessage {
			t.Fatalf("expected failed instances with the unconfirmed message, got %+v", task.Instances[i].Status)
		}
	}

	for _, id := range instances {
		expectCommandHolder(t, env, id, task.ID)
	}

	expectPreviewSpent(t, env, previewID)

	return task.ID
}

// expectFailedStatusSaved heals the vault, waits until the failed status of
// the task is saved, and checks that the instances are then free while the
// record in Vault can never run.
func expectFailedStatusSaved(t *testing.T, env *cmdCreateEnv, taskID string) {
	t.Helper()

	env.vault.setRules(nil, nil, false)

	waitUntil(t, "the failed status of task "+taskID+" to be saved", func() bool {
		stored, ok := env.vault.record(t, taskID)

		return ok && stored.Status == TaskStatusFailed && !stored.CreationConfirmed
	})

	waitUntil(t, "the instances of task "+taskID+" to be freed", func() bool {
		return len(env.manager.Guard().Snapshot()) == 0
	})
}

// A first save that is refused fails the create with a 500 and leaves the
// task failed in memory, still holding its instances, with the preview
// spent, until a save of the failed status is confirmed. Only then are the
// instances freed. Nothing in Vault could ever run.
func TestCreateCommandTaskFirstSaveRefusedHoldsUntilFailedSaved(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	env.vault.setRules(func(int) bool { return true }, nil, false)

	preview := env.previewFor(t, cmdReq("i1", "i3"))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err == nil {
		t.Fatal("expected the create to fail")
	}

	taskID := expectUnconfirmedCommandCreate(t, env, err, preview.ID, "i1", "i3")

	if _, ok := env.vault.record(t, taskID); ok {
		t.Fatal("a refused save left a record in Vault")
	}

	expectFailedStatusSaved(t, env, taskID)
}

// A first save that is confirmed and a second that is refused outright leave
// an unconfirmed record in Vault, which can't run. The task stays failed in
// memory and holds its instances until the failed status is saved over it.
func TestCreateCommandTaskSecondSaveRefused(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	env.vault.setRules(func(n int) bool { return n >= 2 }, nil, false)

	preview := env.previewFor(t, cmdReq("i1", "i3"))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err == nil {
		t.Fatal("expected the create to fail")
	}

	taskID := expectUnconfirmedCommandCreate(t, env, err, preview.ID, "i1", "i3")

	stored, ok := env.vault.record(t, taskID)
	if !ok || stored.CreationConfirmed || stored.Status != TaskStatusPending {
		t.Fatalf("expected the first save's unconfirmed pending record in Vault, got present %v confirmed %v status %q", ok, stored.CreationConfirmed, stored.Status)
	}

	expectFailedStatusSaved(t, env, taskID)
}

// The confirming save lands in Vault but is reported as an error, and its
// read-back fails too, so the manager can't tell that a confirmed, runnable
// record is stored. The task stays failed in memory with its instances held
// and its preview spent, so no second task can come from the review, and the
// failed status is saved over the confirmed record before anything frees.
func TestCreateCommandTaskLandedButUnconfirmedSecondSave(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	// The background loop that saves the failed status waits this long
	// between attempts, so it stays out of the way until the test starts a
	// loop of its own.
	env.manager.createRetry = time.Hour
	env.vault.setRules(func(n int) bool { return n >= 3 }, func(n int) bool { return n == 2 }, true)

	preview := env.previewFor(t, cmdReq("i1", "i3"))
	retry := env.previewFor(t, cmdReq("i1", "i3"))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err == nil {
		t.Fatal("expected the create to fail")
	}

	taskID := expectUnconfirmedCommandCreate(t, env, err, preview.ID, "i1", "i3")

	// The premise: the confirming save landed, so Vault holds exactly one
	// confirmed record of this review although the create failed.
	if confirmed := env.vault.confirmedRecordsFrom(t, preview.ID); confirmed != 1 {
		t.Fatalf("expected exactly one confirmed record in Vault before the heal, got %d", confirmed)
	}

	// The Vault now takes every write, so a retry that wrongly ran the create
	// again could land a second confirmed record. The failed status is not
	// saved yet, because the loop waits.
	env.vault.setRules(nil, nil, false)

	// A retry on the spent preview is refused, so the review still has one.
	again := createCmdReq(preview.ID)
	again.ClientRequestID = "same-preview"

	_, _, err = env.manager.CreateCommandTask(context.Background(), again)
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	if confirmed := env.vault.confirmedRecordsFrom(t, preview.ID); confirmed != 1 {
		t.Fatalf("expected the refused retry to leave one confirmed record, got %d", confirmed)
	}

	// A create from another review of the same instances is refused while
	// the first task holds them.
	req := createCmdReq(retry.ID)
	req.ClientRequestID = "retry"

	_, _, err = env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeInstanceBusy)

	if len(env.manager.ListTasks()) != 1 {
		t.Fatal("the refused retry left a task behind")
	}

	// Start the loop that saves the failed status now.
	env.manager.mu.Lock()
	live := env.manager.tasks[taskID]
	env.manager.wg.Add(1)
	env.manager.mu.Unlock()

	go env.manager.saveUnconfirmedCreate(live, 5*time.Millisecond)

	expectFailedStatusSaved(t, env, taskID)

	// The outcome: the failed status overwrote the confirmed record, so
	// nothing of this review can run, and the record is the one task.
	if confirmed := env.vault.confirmedRecordsFrom(t, preview.ID); confirmed != 0 {
		t.Fatalf("expected no confirmed record after the failed status was saved, got %d", confirmed)
	}

	if stored, ok := env.vault.record(t, taskID); !ok || stored.Status != TaskStatusFailed {
		t.Fatalf("expected the one record to be the failed task, got present %v status %q", ok, stored.Status)
	}
}

// A retry with the same request ID and body returns the first task, even
// after its preview was spent, and a different body is a 409
// request_id_conflict.
func TestCreateCommandTaskIdempotent(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))
	req := createCmdReq(preview.ID)

	first, existing, err := env.manager.CreateCommandTask(context.Background(), req)
	if err != nil || existing {
		t.Fatalf("first create: %v (existing %v)", err, existing)
	}

	again, existing, err := env.manager.CreateCommandTask(context.Background(), req)
	if err != nil || !existing || again.ID != first.ID {
		t.Fatalf("expected the first task back, got %v existing %v", err, existing)
	}

	changed := req
	changed.Reason = "something else"

	_, _, err = env.manager.CreateCommandTask(context.Background(), changed)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)

	if len(env.manager.ListTasks()) != 1 {
		t.Fatal("a retry made another task")
	}
}

// Two creates that carry one request ID at once make one task.
func TestCreateCommandTaskConcurrentDuplicates(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))
	req := createCmdReq(preview.ID)

	ids := make(chan string, 4)

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			task, _, err := env.manager.CreateCommandTask(context.Background(), req)
			if err != nil {
				ids <- "error"

				return
			}

			ids <- task.ID
		})
	}

	wg.Wait()
	close(ids)

	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}

	if len(seen) != 1 || seen["error"] || len(env.manager.ListTasks()) != 1 {
		t.Fatalf("expected one task from four creates, got %d distinct results", len(seen))
	}
}

// A create before the stored tasks are loaded is turned away.
func TestCreateCommandTaskBeforeLoad(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, &cmdDirector{}, newWaitVault())
	t.Cleanup(manager.Stop)

	_, _, err := manager.CreateCommandTask(context.Background(), createCmdReq("pv-1"))
	if !errors.Is(err, ErrTasksLoading) {
		t.Fatalf("expected ErrTasksLoading, got %v", err)
	}
}

// An instance that the preview blocked is left out of the task, and a
// preview with nothing left is a 400 no_selections that spends nothing.
func TestCreateCommandTaskSkipsBlocked(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	preview := env.previewFor(t, cmdReq("i1", "missing"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	if len(task.Instances) != 1 || task.Instances[0].InstanceID != "i1" {
		t.Fatalf("expected only i1, got %+v", task.Instances)
	}

	all := env.previewFor(t, cmdReq("missing"))
	req := createCmdReq(all.ID)
	req.ClientRequestID = "none"

	_, _, err = env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeNoSelections)
	expectPreviewKept(t, env, all.ID)
}

// The command text reaches the saved record and nothing else. No log line
// of a create, of a refused create, or of a failed one holds it or its
// digest.
func TestCreateCommandTaskNeverLogsCommand(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)

	preview := env.previewFor(t, cmdReq("i1"))
	earlier := env.previewFor(t, cmdReq("i1"))

	if _, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID)); err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	busy := earlier
	req := createCmdReq(busy.ID)
	req.ClientRequestID = "busy"

	_, _, err := env.manager.CreateCommandTask(context.Background(), req)
	if err == nil {
		t.Fatal("expected the busy create to fail")
	}

	expectNoCommandText(t, "the busy error", err.Error())
	expectNoCommandText(t, "the log", env.log.text())
}

// A created command task is never run as an upgrade. Once the authorizer
// refuses it, queueing it drops it, resuming it queues nothing, pause and
// cancel turn it away, and the director, which panics on any call, is never
// asked for a deploy.
func TestCreatedCommandTaskIsNeverRunAsUpgrade(t *testing.T) {
	t.Parallel()

	var off atomic.Bool

	env := newCreateCmdEnv(t, func(deps *Deps) {
		deps.Authorizer = &flippingAuthorizer{off: &off}
	})
	preview := env.previewFor(t, cmdReq("i1"))

	task, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	if err != nil {
		t.Fatalf("CreateCommandTask: %v", err)
	}

	off.Store(true)

	live := func() *UpgradeTask {
		env.manager.mu.RLock()
		defer env.manager.mu.RUnlock()

		return env.manager.tasks[task.ID]
	}()

	env.manager.enqueue(live)
	env.manager.resumeIfReady(live)

	if err := env.manager.PauseTask(context.Background(), task.ID); !errors.Is(err, ErrTaskNotRunning) {
		t.Fatalf("pausing a command task should be refused, got %v", err)
	}

	if err := env.manager.CancelTask(context.Background(), task.ID); !errors.Is(err, ErrTaskNotRunning) {
		t.Fatalf("cancelling a pending command task should be refused, got %v", err)
	}

	waitUntil(t, "the queue to drop the command task", func() bool {
		return strings.Contains(env.log.text(), "Skipping command task "+task.ID)
	})

	env.manager.mu.RLock()
	owned := env.manager.owned[task.ID]
	env.manager.mu.RUnlock()

	current := taskCopy(t, env.manager, task.ID)
	if owned || current.Status != TaskStatusPending || current.Instances[0].Status != InstanceStatusPending {
		t.Fatalf("the command task started: owned %v status %q instance %q", owned, current.Status, current.Instances[0].Status)
	}

	if holder, held := env.manager.Guard().Holder("i1", ""); !held || holder.Kind != mutation.KindCommand {
		t.Fatalf("the dropped command task should still hold its instance, got %+v", holder)
	}
}

// The manager keeps the authorizer it was configured with, and a
// ConfigAuthorizer over the commands configuration when it has none.
func TestManagerAuthorizer(t *testing.T) {
	t.Parallel()

	stub := &stubAuthorizer{}
	configured := newCreateCmdEnv(t, func(d *Deps) { d.Authorizer = stub })

	if configured.manager.Authorizer() != CommandAuthorizer(stub) {
		t.Fatal("expected the configured authorizer")
	}

	defaulted := newCreateCmdEnv(t, nil)

	got, ok := defaulted.manager.Authorizer().(*ConfigAuthorizer)
	if !ok || got == nil {
		t.Fatalf("expected a ConfigAuthorizer by default, got %T", defaulted.manager.Authorizer())
	}

	if err := got.AuthorizeStored(context.Background(), CommandTaskSpec{RunAs: "vcap"}); err != nil {
		t.Fatalf("the default authorizer should read the commands configuration, got %v", err)
	}

	unconfigured := NewManager(&logger.NoOpLogger{}, &cmdDirector{}, newWaitVault())
	t.Cleanup(unconfigured.Stop)

	err := unconfigured.Authorizer().AuthorizeStored(context.Background(), CommandTaskSpec{RunAs: "vcap"})
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeCommandsDisabled)
}

// The authorizer is asked before the request is matched, so a refused
// request that also differs from its preview answers the refusal, and the
// preview is not spent.
func TestCreateCommandTaskAuthorizerBeforeMatch(t *testing.T) {
	t.Parallel()

	stub := &stubAuthorizer{stored: apierr.New(http.StatusForbidden, apierr.CodeCommandsDisabled, "running commands is disabled")}
	env := newCreateCmdEnv(t, func(d *Deps) { d.Authorizer = stub })
	preview := env.previewFor(t, cmdReq("i1"))

	req := createCmdReq(preview.ID)
	req.Command = "echo another-marker"

	_, _, err := env.manager.CreateCommandTask(context.Background(), req)
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeCommandsDisabled)
	expectNoSpend(t, env)
	expectPreviewKept(t, env, preview.ID)
}

// A preview that has expired answers 410 preview_expired and creates nothing.
func TestCreateCommandTaskPreviewExpired(t *testing.T) {
	t.Parallel()

	env := newCreateCmdEnv(t, nil)
	preview := env.previewFor(t, cmdReq("i1"))

	env.now.Store(preview.ExpiresAt.Add(time.Second))

	_, _, err := env.manager.CreateCommandTask(context.Background(), createCmdReq(preview.ID))
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
	expectTasksNone(t, env)
}
