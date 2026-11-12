package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

const secretCommand = "echo top-secret-marker"

// cmdVault holds instance records by instance ID.
type cmdVault struct {
	interfaces.Vault

	records map[string]map[string]any
}

func (v *cmdVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	record, ok := v.records[path]
	if !ok {
		return false, nil
	}

	target, isMap := result.(*map[string]interface{})
	if !isMap {
		return false, errors.New("cmdVault reads maps only") //nolint:err113 // test fake
	}

	*target = record

	return true, nil
}

// cmdDirector lists VMs by deployment.
type cmdDirector struct {
	bosh.Director

	vms map[string][]bosh.VM
}

func (d *cmdDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	vms, ok := d.vms[deployment]
	if !ok {
		return nil, bosh.ErrDeploymentNotFound
	}

	return slices.Clone(vms), nil
}

type cmdEnv struct {
	manager *Manager
	now     *atomic.Value
}

func newCmdEnv(t *testing.T, change func(*Deps)) *cmdEnv {
	t.Helper()

	director := &cmdDirector{vms: map[string][]bosh.VM{
		"redis-i1": {
			{ID: "id-0", CID: "cid-0", Job: "redis", Index: 0, AZ: "z1", IPs: []string{"10.0.0.4"}, State: "running", JobState: "running"},
			{ID: "id-1", CID: "", Job: "redis", Index: 1, AZ: "z2", IPs: []string{"10.0.0.5"}, State: "missing", JobState: ""},
			{ID: "id-2", CID: "cid-2", Job: "redis", Index: 2, AZ: "z3", IPs: []string{"10.0.0.6"}, State: "stopped", JobState: "stopped"},
		},
		"pg-i2":    {{ID: "id-9", CID: "cid-9", Job: "pg", Index: 0, AZ: "z1", State: "running", JobState: "running"}},
		"redis-i3": {{ID: "id-7", CID: "cid-7", Job: "redis", Index: 0, State: "running", JobState: "running"}},
	}}

	vault := &cmdVault{records: map[string]map[string]any{
		"i1": {"deployment_name": "redis-i1", "instance_name": "one", "service_id": "redis", "plan_id": "small"},
		"i2": {"deployment_name": "pg-i2", "service_id": "postgres", "plan_id": "tiny"},
		"i3": {"deployment_name": "redis-i3", "service_id": "redis", "plan_id": "small"},
	}}

	manager := NewManager(&logger.NoOpLogger{}, director, vault)
	t.Cleanup(manager.Stop)

	now := &atomic.Value{}
	now.Store(time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))

	deps := Deps{Now: func() time.Time { return now.Load().(time.Time) }}
	if change != nil {
		change(&deps)
	}

	if err := manager.Configure(deps); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	return &cmdEnv{manager: manager, now: now}
}

func cmdReq(ids ...string) CommandPreviewRequest {
	return CommandPreviewRequest{InstanceIDs: ids, Command: secretCommand, RunAs: "vcap", TimeoutSeconds: 600}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return string(raw)
}

func TestPreviewCommandListsTargets(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	sum := sha256.Sum256([]byte(secretCommand))

	if preview.ID == "" || preview.ExpiresAt.IsZero() {
		t.Fatalf("expected an ID and an expiry, got %+v", preview)
	}

	if preview.CommandDigest != hex.EncodeToString(sum[:]) || preview.CommandLength != len(secretCommand) {
		t.Fatalf("wrong digest or length: %s %d", preview.CommandDigest, preview.CommandLength)
	}

	if preview.RunAs != "vcap" || preview.Shell != "/bin/bash" || preview.TimeoutSeconds != 600 || preview.Parallel {
		t.Fatalf("wrong settings: %+v", preview)
	}

	if len(preview.Instances) != 1 {
		t.Fatalf("expected one instance, got %+v", preview.Instances)
	}

	inst := preview.Instances[0]
	if inst.Deployment != "redis-i1" || inst.Service != "redis" || inst.Blocked != nil || len(inst.VMs) != 3 {
		t.Fatalf("wrong instance: %+v", inst)
	}

	first, noVM, stopped := inst.VMs[0], inst.VMs[1], inst.VMs[2]
	if !first.Targeted || first.Group != "redis" || first.AZ != "z1" || first.CID != "cid-0" || first.JobState != "running" || !slices.Equal(first.IPs, []string{"10.0.0.4"}) {
		t.Fatalf("wrong first VM: %+v", first)
	}

	if noVM.Targeted || noVM.Reason != "no VM" {
		t.Fatalf("a VM without a CID must not be targeted, got %+v", noVM)
	}

	if !stopped.Targeted || stopped.State != "stopped" || stopped.JobState != "stopped" {
		t.Fatalf("a stopped VM is targeted and shows its state, got %+v", stopped)
	}
}

func TestPreviewCommandMixedServices(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i2"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if !slices.Equal(preview.Services, []string{"postgres", "redis"}) {
		t.Fatalf("expected both services, got %v", preview.Services)
	}

	if !slices.Contains(preview.Warnings, "mixed_services") {
		t.Fatalf("expected mixed_services, got %v", preview.Warnings)
	}

	same, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i3"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if len(same.Warnings) != 0 || len(same.Services) != 1 {
		t.Fatalf("one service must not warn, got %v %v", same.Services, same.Warnings)
	}
}

func TestPreviewCommandBlocksBusyInstance(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	release, err := env.manager.Guard().Acquire("i1", "redis-i1", mutation.Holder{
		Kind: mutation.KindUpgrade, TaskID: "upgrade-9", Description: `upgrade "nightly"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i3"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	busy := preview.Instances[0].Blocked
	if busy == nil || busy.Code != apierr.CodeInstanceBusy || !strings.Contains(busy.Message, `busy with upgrade "nightly"`) {
		t.Fatalf("expected a busy block that names the holder, got %+v", busy)
	}

	if preview.Instances[1].Blocked != nil {
		t.Fatalf("the other instance must be clear, got %+v", preview.Instances[1])
	}
}

func TestPreviewCommandUnknownInstanceAndDeploymentAreBlocked(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	delete(env.manager.director.(*cmdDirector).vms, "redis-i3")

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("nope", "i3"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	for _, inst := range preview.Instances {
		if inst.Blocked == nil || inst.Blocked.Code != apierr.CodeInstanceNotFound {
			t.Fatalf("expected instance_not_found for %s, got %+v", inst.InstanceID, inst.Blocked)
		}
	}
}

func TestPreviewCommandValidation(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	tests := []struct {
		name   string
		change func(*CommandPreviewRequest)
		code   string
		ok     bool
	}{
		{"64 KiB passes", func(r *CommandPreviewRequest) { r.Command = strings.Repeat("a", 64*1024) }, "", true},
		{"over 64 KiB", func(r *CommandPreviewRequest) { r.Command = strings.Repeat("a", 64*1024+1) }, apierr.CodeCommandTooLong, false},
		{"run as nobody", func(r *CommandPreviewRequest) { r.RunAs = "nobody" }, apierr.CodeInvalidRunUser, false},
		{"run as empty", func(r *CommandPreviewRequest) { r.RunAs = "" }, apierr.CodeInvalidRunUser, false},
		{"run as root", func(r *CommandPreviewRequest) { r.RunAs = "root" }, "", true},
		{"timeout 9", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 9 }, apierr.CodeTimeoutOutOfRange, false},
		{"timeout 3601", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 3601 }, apierr.CodeTimeoutOutOfRange, false},
		{"timeout 10", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 10 }, "", true},
		{"timeout 600", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 600 }, "", true},
		{"timeout 3600", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 3600 }, "", true},
		{"timeout unset means default", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 0 }, "", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := cmdReq("i1")
			test.change(&req)

			preview, err := env.manager.PreviewCommand(context.Background(), req)
			if test.ok {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}

				if req.TimeoutSeconds == 0 && preview.TimeoutSeconds != CommandTimeoutDefault {
					t.Fatalf("expected the default timeout, got %d", preview.TimeoutSeconds)
				}

				return
			}

			wantAPIError(t, err, http.StatusBadRequest, test.code)
		})
	}
}

func TestPreviewCommandMaxInFlight(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	tests := []struct {
		name string
		max  int
		ok   bool
	}{
		{"equal to workers", 4, false},
		{"one below workers", 3, true},
		{"zero", 0, false},
		{"negative", -1, false},
		{"one", 1, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := cmdReq("i1")
			req.Parallel = true
			req.MaxInFlight = test.max

			preview, err := env.manager.PreviewCommand(context.Background(), req)
			if !test.ok {
				wantAPIError(t, err, http.StatusBadRequest, apierr.CodeMaxInFlightTooHigh)

				return
			}

			if err != nil {
				t.Fatalf("PreviewCommand: %v", err)
			}

			if preview.WorkerCount != 4 || preview.MaxInFlightLimit != 3 || !preview.ParallelAvailable || preview.MaxInFlight != test.max {
				t.Fatalf("wrong parallel report: %+v", preview)
			}
		})
	}
}

func TestPreviewCommandSerialIgnoresMaxInFlight(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	req := cmdReq("i1")
	req.MaxInFlight = 99

	preview, err := env.manager.PreviewCommand(context.Background(), req)
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if preview.MaxInFlight != 0 || preview.Parallel {
		t.Fatalf("a serial preview stores max_in_flight 0, got %+v", preview)
	}

	if !preview.MatchesCommand(preview.CommandDigest, preview.CommandLength, "vcap", 600, false, 99) {
		t.Fatal("a serial preview must match whatever max_in_flight the create sends")
	}
}

func TestPreviewCommandOneWorkerMeansNoParallel(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, func(d *Deps) {
		d.WorkerCount = func(context.Context) (int, bool) { return 1, true }
		d.BOSH = func() config.BOSHConfig { return config.BOSHConfig{DirectorWorkers: 1} }
	})

	serial, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if serial.WorkerCount != 1 || serial.MaxInFlightLimit != 0 || serial.ParallelAvailable {
		t.Fatalf("expected no parallel, got %+v", serial)
	}

	for _, value := range []int{0, 1, 2, 3} {
		req := cmdReq("i1")
		req.Parallel = true
		req.MaxInFlight = value

		_, err := env.manager.PreviewCommand(context.Background(), req)
		wantAPIError(t, err, http.StatusBadRequest, apierr.CodeMaxInFlightTooHigh)
	}
}

func TestPreviewCommandWorkerCountSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		deps func(*Deps)
		want int
	}{
		{"default seam and default config", nil, 4},
		{"default seam answers false", func(d *Deps) { d.WorkerCount = DirectorWorkerCount }, 4},
		{"config when the seam answers false", func(d *Deps) {
			d.WorkerCount = func(context.Context) (int, bool) { return 99, false }
			d.BOSH = func() config.BOSHConfig { return config.BOSHConfig{DirectorWorkers: 6} }
		}, 6},
		{"seam first when it answers", func(d *Deps) {
			d.WorkerCount = func(context.Context) (int, bool) { return 8, true }
			d.BOSH = func() config.BOSHConfig { return config.BOSHConfig{DirectorWorkers: 6} }
		}, 8},
		{"nil BOSH means the default", func(d *Deps) { d.WorkerCount = nil; d.BOSH = nil }, 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			env := newCmdEnv(t, test.deps)

			preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
			if err != nil {
				t.Fatalf("PreviewCommand: %v", err)
			}

			if preview.WorkerCount != test.want || preview.MaxInFlightLimit != test.want-1 {
				t.Fatalf("expected %d workers, got %+v", test.want, preview)
			}
		})
	}
}

// The limit the console reads before a preview is the limit a preview
// reports, from the same worker count, so the two never disagree.
func TestCommandMaxInFlightLimitMatchesPreview(t *testing.T) {
	t.Parallel()

	for _, workers := range []int{1, 2, 5, 13} {
		env := newCmdEnv(t, func(d *Deps) {
			d.WorkerCount = func(context.Context) (int, bool) { return workers, true }
		})

		preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
		if err != nil {
			t.Fatalf("PreviewCommand: %v", err)
		}

		got := env.manager.CommandMaxInFlightLimit(context.Background())
		if got != preview.MaxInFlightLimit {
			t.Fatalf("with %d workers the limit is %d, the preview says %d", workers, got, preview.MaxInFlightLimit)
		}
	}
}

func TestDirectorWorkerCountSeamIsUnknown(t *testing.T) {
	t.Parallel()

	if n, ok := DirectorWorkerCount(context.Background()); ok || n != 0 {
		t.Fatalf("the director exposes no count, got %d %v", n, ok)
	}
}

func TestPreviewCommandNeverHoldsTheText(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	raw := mustJSON(t, preview)
	if strings.Contains(raw, "top-secret-marker") {
		t.Fatalf("the preview holds the command text: %s", raw)
	}

	long := cmdReq("i1")
	long.Command = secretCommand + strings.Repeat("a", 64*1024)

	_, err = env.manager.PreviewCommand(context.Background(), long)
	if err == nil || strings.Contains(err.Error(), "top-secret-marker") {
		t.Fatalf("the error must exist and not hold the text, got %v", err)
	}
}

func TestPreviewCommandStoredAndSpentOnce(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	store := env.manager.Dependencies().Previews

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	got, err := store.GetCommand(preview.ID)
	if err != nil || got.ID != preview.ID {
		t.Fatalf("GetCommand: %v %+v", err, got)
	}

	// The certificate side refuses it and leaves it alone.
	_, err = store.Spend(preview.ID, PreviewKindCertificate)
	wantAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)

	_, err = store.Get(preview.ID)
	if err == nil {
		t.Fatal("a certificate Get must not return a command preview")
	}

	spent, err := store.Spend(preview.ID, PreviewKindCommand)
	if err != nil || spent.Command == nil || spent.Command.ID != preview.ID {
		t.Fatalf("first spend must win: %v %+v", err, spent)
	}

	_, err = store.Spend(preview.ID, PreviewKindCommand)
	wantAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
}

func TestMatchesCommand(t *testing.T) {
	t.Parallel()

	p := &CommandPreview{CommandDigest: "d", CommandLength: 5, RunAs: "vcap", TimeoutSeconds: 600, Parallel: true, MaxInFlight: 2}

	if !p.MatchesCommand("d", 5, "vcap", 600, true, 2) {
		t.Fatal("identical settings must match")
	}

	for name, got := range map[string]bool{
		"digest":   p.MatchesCommand("x", 5, "vcap", 600, true, 2),
		"length":   p.MatchesCommand("d", 6, "vcap", 600, true, 2),
		"run as":   p.MatchesCommand("d", 5, "root", 600, true, 2),
		"timeout":  p.MatchesCommand("d", 5, "vcap", 601, true, 2),
		"parallel": p.MatchesCommand("d", 5, "vcap", 600, false, 2),
		"max":      p.MatchesCommand("d", 5, "vcap", 600, true, 3),
	} {
		if got {
			t.Errorf("a different %s must not match", name)
		}
	}
}

func TestMatchesCommandParallelFlagAlone(t *testing.T) {
	t.Parallel()

	serial := &CommandPreview{CommandDigest: "d", CommandLength: 5, RunAs: "vcap", TimeoutSeconds: 600}
	if serial.MatchesCommand("d", 5, "vcap", 600, true, 0) {
		t.Error("a serial preview must not match a parallel create with max 0")
	}

	parallel := &CommandPreview{CommandDigest: "d", CommandLength: 5, RunAs: "vcap", TimeoutSeconds: 600, Parallel: true}
	if parallel.MatchesCommand("d", 5, "vcap", 600, false, 0) {
		t.Error("a parallel preview must not match a serial create")
	}
}

func TestMatchesCommandTimeoutDefault(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	req := cmdReq("i1")
	req.TimeoutSeconds = 0

	preview, err := env.manager.PreviewCommand(context.Background(), req)
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if !preview.MatchesCommand(preview.CommandDigest, preview.CommandLength, "vcap", 0, false, 0) {
		t.Error("a create that sends 0 must match a preview made with 0")
	}

	if !preview.MatchesCommand(preview.CommandDigest, preview.CommandLength, "vcap", 600, false, 0) {
		t.Error("an explicit 600 must match the default")
	}
}

func TestPreviewCommandEmptyInputs(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	for _, tc := range []struct {
		name   string
		change func(*CommandPreviewRequest)
		code   string
	}{
		{"empty command", func(r *CommandPreviewRequest) { r.Command = "" }, apierr.CodeInvalidRequest},
		{"blank command", func(r *CommandPreviewRequest) { r.Command = " \t\n" }, apierr.CodeInvalidRequest},
		{"only empty ID", func(r *CommandPreviewRequest) { r.InstanceIDs = []string{""} }, apierr.CodeNoSelections},
		{"empty ID among IDs", func(r *CommandPreviewRequest) { r.InstanceIDs = []string{"i1", ""} }, apierr.CodeNoSelections},
		{"no IDs", func(r *CommandPreviewRequest) { r.InstanceIDs = nil }, apierr.CodeNoSelections},
	} {
		req := cmdReq("i1")
		tc.change(&req)

		_, err := env.manager.PreviewCommand(context.Background(), req)
		wantAPIError(t, err, http.StatusBadRequest, tc.code)
	}
}

func TestPreviewCommandCountsDuplicateOnce(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i1", "i3"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	if len(preview.Instances) != 2 {
		t.Fatalf("expected two instances, got %+v", preview.Instances)
	}
}

type failVault struct{ cmdVault }

func (failVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errors.New("vault is down") //nolint:err113 // test fake
}

type failDirector struct{ cmdDirector }

func (failDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	return nil, errors.New("director is down") //nolint:err113 // test fake
}

func TestPreviewCommandReadFailuresFailThePreview(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	env.manager.vault = &failVault{}

	_, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i3"))
	wantAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

	env = newCmdEnv(t, nil)
	env.manager.director = &failDirector{}

	_, err = env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i3"))
	wantAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)
}

func TestSpendKinds(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store := NewPreviewStore(func() time.Time { return now })

	store.Put(&Preview{ID: "pv-cert"}, time.Minute)
	store.PutCommand(&CommandPreview{ID: "pv-cmd"}, time.Minute)

	_, err := store.Spend("pv-cert", PreviewKindCommand)
	wantAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)

	// The command spend of the certificate preview consumed nothing.
	spent, err := store.Spend("pv-cert", PreviewKindCertificate)
	if err != nil || spent.Certificate == nil {
		t.Fatalf("certificate spend: %v %+v", err, spent)
	}

	_, err = store.Spend("pv-cert", PreviewKindCertificate)
	wantAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	_, err = store.Spend("pv-missing", PreviewKindCommand)
	wantAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	_, err = store.Spend("pv-cmd", PreviewKindCertificate)
	wantAPIError(t, err, http.StatusConflict, apierr.CodePreviewMismatch)

	now = now.Add(2 * time.Minute)

	_, err = store.GetCommand("pv-cmd")
	wantAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	store.PutCommand(&CommandPreview{ID: "pv-cmd3"}, time.Minute)
	now = now.Add(2 * time.Minute)

	_, err = store.Spend("pv-cmd3", PreviewKindCommand)
	wantAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)
}

func TestSpendRaceHasOneWinner(t *testing.T) {
	t.Parallel()

	store := NewPreviewStore(time.Now)
	store.PutCommand(&CommandPreview{ID: "pv-race"}, time.Minute)

	var (
		wins atomic.Int32
		wg   sync.WaitGroup
	)

	start := make(chan struct{})

	for range 2 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			if _, err := store.Spend("pv-race", PreviewKindCommand); err == nil {
				wins.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("exactly one spend must win, got %d", wins.Load())
	}
}

func TestPreviewCommandRefusalMessagesSayWhatToDo(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)

	tests := []struct {
		name   string
		change func(*CommandPreviewRequest)
		want   string
	}{
		{"too long", func(r *CommandPreviewRequest) { r.Command = strings.Repeat("a", MaxCommandBytes+1) },
			"The command is 65537 bytes, which is over the limit of 65536 bytes, so shorten it."},
		{"unknown user", func(r *CommandPreviewRequest) { r.RunAs = "nobody" },
			"A command runs as root or as vcap, so choose one of those."},
		{"timeout too short", func(r *CommandPreviewRequest) { r.TimeoutSeconds = 9 },
			"The timeout must be between 10 and 3600 seconds, so choose a value in that range."},
		{"empty command", func(r *CommandPreviewRequest) { r.Command = "  " },
			"The command is empty, so enter the command to run."},
		{"too many nodes at once", func(r *CommandPreviewRequest) { r.Parallel = true; r.MaxInFlight = 4 },
			"With 4 director workers, at most 3 nodes can run at once, because the SSH setup and cleanup for each node hold a director worker. Choose a number from 1 to 3."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			req := cmdReq("i1")
			test.change(&req)

			_, err := env.manager.PreviewCommand(context.Background(), req)
			wantMessage(t, err, test.want)
		})
	}
}

func TestPreviewCommandWithOneWorkerSaysToTurnParallelOff(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, func(d *Deps) {
		d.WorkerCount = func(context.Context) (int, bool) { return 1, true }
		d.BOSH = func() config.BOSHConfig { return config.BOSHConfig{DirectorWorkers: 1} }
	})

	req := cmdReq("i1")
	req.Parallel = true
	req.MaxInFlight = 1

	_, err := env.manager.PreviewCommand(context.Background(), req)
	wantMessage(t, err, "Parallel runs need at least 2 director workers, and this director has 1, so turn parallel off.")
}

func TestPreviewCommandBlockedMessagesSayWhy(t *testing.T) {
	t.Parallel()

	env := newCmdEnv(t, nil)
	env.manager.vault.(*cmdVault).records["i4"] = map[string]any{"service_id": "redis"}

	release, err := env.manager.Guard().Acquire("i1", "redis-i1", mutation.Holder{
		Kind: mutation.KindUpgrade, TaskID: "upgrade-9", Description: `upgrade "nightly"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)

	preview, err := env.manager.PreviewCommand(context.Background(), cmdReq("i1", "i4"))
	if err != nil {
		t.Fatalf("PreviewCommand: %v", err)
	}

	want := []string{
		`This instance is busy with upgrade "nightly", so it can't take part until that finishes.`,
		"The instance record names no deployment, so Blacksmith can't find its VMs.",
	}

	for i, inst := range preview.Instances {
		if inst.Blocked == nil || inst.Blocked.Message != want[i] {
			t.Errorf("instance %s is blocked with %+v, want %q", inst.InstanceID, inst.Blocked, want[i])
		}
	}
}
