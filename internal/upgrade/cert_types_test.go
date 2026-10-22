//nolint:testpackage // the tests load stored tasks and read the manager's unexported state
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/mutation"
)

const (
	// certTaskID names the certificate task a test stores or saves.
	certTaskID = "0f6c2b1e-3d4a-4f5b-8c9d-1e2f3a4b5c6d"
	// certRequestID is the client request ID a certificate task carries.
	certRequestID = "3b8f6c1e-2a4d-4e6f-9b1c-5d7e8f9a0b1c"
	// certRequestDigest is the digest of that create request's body.
	certRequestDigest = "9f2c4e6a8b0d1f3e5a7c9e1b3d5f7a9c1e3b5d7f9a1c3e5b7d9f1a3c5e7b9d1f"
	// certVariable is the variable a stored certificate task rotates.
	certVariable = "redis_cluster_crt"
	// certJob is the job whose files the stored rotation patches.
	certJob = "redis"
	// indexStatus is the key of an index entry's status.
	indexStatus = "status"
	// indexCreatedAt is the key of an index entry's creation time.
	indexCreatedAt = "created_at"
	// certSecondInstance is a second instance a test task names.
	certSecondInstance = "instance-2"
)

// v170UpgradeRecord is an upgrade task as Blacksmith v1.7.0 stored it,
// before tasks had a kind.
const v170UpgradeRecord = `{
  "id": "v170-upgrade",
  "name": "noble 1.2",
  "status": "completed",
  "paused": false,
  "target_stemcell": {"os": "ubuntu-noble", "version": "1.2"},
  "instances": [
    {"instance_id": "instance-1", "deployment_name": "redis-instance-1", "status": "success", "bosh_task_id": 41}
  ],
  "total_count": 1,
  "completed_count": 1,
  "failed_count": 0,
  "cancelled_count": 0,
  "skipped_count": 0,
  "unknown_count": 0,
  "created_at": "2026-01-02T03:04:05Z"
}`

// fullCertificateTask returns a certificate task with every field a
// rotation records filled in with realistic hashes, serials, and version
// IDs, as a patch run part way through its first VM would leave it.
func fullCertificateTask() UpgradeTask {
	created := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	started := created.Add(time.Minute)

	return UpgradeTask{
		ID:     certTaskID,
		Name:   "rotate redis certificates",
		Kind:   TaskKindCertificates,
		Status: TaskStatusRunning,
		Certificates: &CertificateTaskSpec{
			Mode:      RotationModePatch,
			Converge:  ConvergePending,
			Reason:    "expires in 6 days",
			PreviewID: "7d6c5b4a-3928-4170-8f6e-5d4c3b2a1908",
		},
		ClientRequestID:   certRequestID,
		RequestDigest:     certRequestDigest,
		CreatedFrom:       "ui",
		CreationConfirmed: true,
		Instances: []InstanceUpgrade{{
			InstanceID:     waitInstanceID,
			DeploymentName: waitDeployment,
			Status:         InstanceStatusRunning,
			StartedAt:      &started,
			Certificates:   fullRotation(created),
		}},
		TotalCount: 1,
		CreatedAt:  created,
		StartedAt:  &started,
	}
}

// fullRotation returns the rotation record of fullCertificateTask's one
// instance, with its first VM backed up.
func fullRotation(created time.Time) *InstanceRotation {
	notAfter := created.AddDate(1, 0, 0)

	return &InstanceRotation{
		Phase: RotationPhasePatch,
		Approved: ApprovedEffects{
			ManifestDigest: "sha256:5c1e9a7b3d2f4e6a8c0b1d3f5e7a9c2b4d6f8e0a1c3e5b7d9f2a4c6e8b0d1f3a",
			Selected:       []VersionPair{{Name: certVariable, DeployedVersionID: "v1", LatestVersionID: "v1"}},
			Additional:     []VersionPair{{Name: "redis_ca", DeployedVersionID: "c1", LatestVersionID: "c2"}},
			VMCIDs:         []string{"vm-1a2b", "vm-3c4d"},
		},
		Variables: []VariableRotation{{
			Name:            certVariable,
			CertificateID:   "cert-7e1d",
			Baseline:        []string{"v1"},
			OldVersionID:    "v1",
			OldSerial:       "4f:1a:22",
			OldNotAfter:     notAfter,
			OldCertHash:     "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
			OldKeyHash:      "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
			RegenerateState: RegenerateConfirmed,
			NewVersionID:    "v2",
			NewSerial:       "6c:2b:33",
			NewNotAfter:     &notAfter,
			NewCertHash:     "b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1",
			NewKeyHash:      "1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f00f",
			Validation:      []Check{{Name: "chains to the signing CA", Passed: true, Detail: "verified"}},
		}},
		VMs: []VMJournal{{
			VM:      "redis/0",
			CID:     "vm-1a2b",
			Group:   certJob,
			Index:   0,
			JobDirs: map[string]string{certJob: "/var/vcap/data/jobs/redis/4e5f6a7b"},
			Phase:   VMPhaseBackedUp,
			Files: []FileEntry{{
				Path:       "/var/vcap/data/jobs/redis/4e5f6a7b/config/tls/redis.crt",
				Job:        certJob,
				Role:       FileRoleCertificate,
				Variable:   certVariable,
				Owner:      "vcap:vcap",
				Mode:       "0600",
				OldHash:    "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90",
				NewHash:    "b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1",
				BackupPath: "/var/vcap/data/certpatch/" + certTaskID + "/b/0",
				State:      FileStateBackedUp,
			}},
			Processes: []string{certJob},
			Targets:   []TargetResult{{Variable: certVariable, Port: 6379, Method: "probe", OK: true, Detail: "serves the new certificate"}},
			GateAfter: "cluster_state:ok",
			Cleanup:   CleanupPending,
		}},
		Gate:     "redis-cluster",
		Converge: &ConvergeDeploy{ContextID: "c0a1b2c3-d4e5-4f60-8a9b-0c1d2e3f4a5b", BOSHTaskID: 77},
		Pending: &PendingDecision{
			Kind:     DecisionReconcileRegeneration,
			Variable: certVariable,
			Candidates: []Candidate{{
				VersionID: "v2",
				CreatedAt: created,
				Serial:    "6c:2b:33",
				NotAfter:  notAfter,
				Checks:    []Check{{Name: "chains to the signing CA", Passed: true}},
				Valid:     true,
			}},
			Changes: []string{"The manifest changed since the preview."},
			Since:   created,
		},
	}
}

// storeCertificateTask saves the certificate task in the vault and names
// it in the certificate tasks index, the way the manager saves it.
func storeCertificateTask(t *testing.T, vault *waitVault, task UpgradeTask) {
	t.Helper()

	storeInIndex(t, vault, certificateTasksVaultPath, task)
}

// storeInIndex saves the task under the index given and names it there.
func storeInIndex(t *testing.T, vault *waitVault, indexPath string, task UpgradeTask) {
	t.Helper()

	ctx := context.Background()

	err := vault.Put(ctx, indexPath+"/"+task.ID, task)
	if err != nil {
		t.Fatalf("storing the task: %v", err)
	}

	index := map[string]interface{}{}

	_, err = vault.Get(ctx, indexPath, &index)
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	index[task.ID] = map[string]interface{}{indexStatus: task.Status, indexCreatedAt: task.CreatedAt.Unix()}

	err = vault.Put(ctx, indexPath, index)
	if err != nil {
		t.Fatalf("storing the index: %v", err)
	}
}

// storedPaths returns every path the vault holds.
func (v *waitVault) storedPaths() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	paths := make([]string, 0, len(v.saved))
	for path := range v.saved {
		paths = append(paths, path)
	}

	return paths
}

// storedRecord decodes what the vault holds at the path.
func (v *waitVault) storedRecord(t *testing.T, path string) (UpgradeTask, bool) {
	t.Helper()

	v.mu.Lock()
	raw, ok := v.saved[path]
	v.mu.Unlock()

	var task UpgradeTask
	if !ok {
		return task, false
	}

	err := json.Unmarshal(raw, &task)
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}

	return task, true
}

// lookupDirector counts the director calls that settle a loaded deploy.
// Any call it does not script panics through the nil embedded Director.
type lookupDirector struct {
	bosh.Director

	lookups atomic.Int32
}

func (d *lookupDirector) FindRunningTaskForDeployment(context.Context, string) (*bosh.Task, error) {
	d.lookups.Add(1)

	return nil, nil //nolint:nilnil // no running task is an answer
}

func (d *lookupDirector) WaitForTask(ctx context.Context, _ int) (*bosh.Task, error) {
	d.lookups.Add(1)

	<-ctx.Done()

	return nil, ctx.Err() //nolint:wrapcheck // a test fake
}

// countContextLookups replaces the lookup by context ID for the test and
// returns the count of lookups it receives.
func countContextLookups(t *testing.T) *atomic.Int32 {
	t.Helper()

	var count atomic.Int32

	original := awaitTaskByContextID
	awaitTaskByContextID = func(ctx context.Context, _ bosh.Director, _ string, _ time.Time) (*bosh.Task, error) {
		count.Add(1)

		<-ctx.Done()

		return nil, ctx.Err() //nolint:wrapcheck // a test fake
	}

	t.Cleanup(func() { awaitTaskByContextID = original })

	return &count
}

// An upgrade record stored before tasks had a kind loads as an upgrade.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestUpgradeRecordWithoutKindLoadsAsUpgrade(t *testing.T) {
	vault := newWaitVault()

	vault.saved[upgradeTasksVaultPath+"/v170-upgrade"] = []byte(v170UpgradeRecord)
	vault.saved[upgradeTasksVaultPath] = []byte(`{"v170-upgrade": {"status": "completed", "created_at": 1767323045}}`)

	manager := loadManager(t, newWaitDirector(), vault)

	task, err := manager.GetTask("v170-upgrade")
	if err != nil {
		t.Fatalf("the v1.7.0 upgrade did not load: %v", err)
	}

	if task.Kind != "" {
		t.Fatalf("expected the stored record to keep its empty kind, got %q", task.Kind)
	}

	if kind := task.KindOrDefault(); kind != TaskKindUpgrade {
		t.Fatalf("expected the v1.7.0 upgrade to read as %q, got %q", TaskKindUpgrade, kind)
	}

	if summary := task.ToSummary(); summary.Kind != TaskKindUpgrade || summary.Target != "ubuntu-noble/1.2" {
		t.Fatalf("expected an upgrade summary targeting ubuntu-noble/1.2, got kind %q and target %q", summary.Kind, summary.Target)
	}
}

// A certificate task is saved under its own index and never under the
// upgrade index.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskPersistsUnderOwnIndex(t *testing.T) {
	vault := newWaitVault()
	manager := managerWithTasks(t, vault)

	task := fullCertificateTask()

	manager.mu.Lock()
	manager.tasks[task.ID] = &task
	manager.mu.Unlock()

	err := manager.persistTask(context.Background(), &task)
	if err != nil {
		t.Fatalf("saving the certificate task: %v", err)
	}

	if _, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+certTaskID); !ok {
		t.Fatalf("expected the record under %s/%s, got %v", certificateTasksVaultPath, certTaskID, vault.storedPaths())
	}

	var index map[string]interface{}

	exists, err := vault.Get(context.Background(), certificateTasksVaultPath, &index)
	if err != nil || !exists {
		t.Fatalf("expected the certificate tasks index, got exists %v and error %v", exists, err)
	}

	if _, ok := index[certTaskID]; !ok {
		t.Fatalf("expected the certificate tasks index to name the task, got %v", index)
	}

	for _, path := range vault.storedPaths() {
		if strings.HasPrefix(path, upgradeTasksVaultPath) {
			t.Fatalf("expected nothing under %s, found %s", upgradeTasksVaultPath, path)
		}
	}
}

// The loader reads the upgrade index and the certificate index.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadReadsBothIndexes(t *testing.T) {
	vault := newWaitVault()

	upgrade := storedUpgrade(loadedTaskID, InstanceUpgrade{InstanceID: certSecondInstance, DeploymentName: "redis-instance-2", Status: InstanceStatusSuccess})
	upgrade.Status = TaskStatusCompleted
	upgrade.CompletedCount = 1
	storeUpgrade(t, vault, upgrade)

	certificate := fullCertificateTask()
	certificate.Status = TaskStatusCompleted
	certificate.Instances[0].Status = InstanceStatusSuccess
	storeCertificateTask(t, vault, certificate)

	manager := loadManager(t, newWaitDirector(), vault)

	kinds := make(map[string]TaskKind)
	for _, task := range manager.ListTasks() {
		kinds[task.ID] = task.KindOrDefault()
	}

	if len(kinds) != 2 || kinds[loadedTaskID] != TaskKindUpgrade || kinds[certTaskID] != TaskKindCertificates {
		t.Fatalf("expected one upgrade and one certificate task, got %v", kinds)
	}
}

// A certificate record found under the upgrade index is not trusted, so it
// is skipped rather than run as an upgrade.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadSkipsARecordUnderTheWrongIndex(t *testing.T) {
	vault := newWaitVault()

	misplaced := fullCertificateTask()
	misplaced.Status = TaskStatusPending
	storeInIndex(t, vault, upgradeTasksVaultPath, misplaced)

	manager := loadManager(t, newWaitDirector(), vault)

	_, err := manager.GetTask(certTaskID)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected the misplaced record to stay unloaded, got %v", err)
	}
}

// A certificate instance whose regeneration was being sent when the broker
// stopped waits for a decision. No lookup on the director can say whether
// CredHub made a new version, so none is made.
//
//nolint:paralleltest // it replaces the lookup by context ID
func TestLoadTurnsSendingIntoAwaitingDecision(t *testing.T) {
	contextLookups := countContextLookups(t)
	director := &lookupDirector{}
	vault := newWaitVault()

	stored := fullCertificateTask()
	inst := &stored.Instances[0]
	inst.ContextID = loadedContextID
	inst.Certificates.Phase = RotationPhaseRegenerate
	inst.Certificates.Pending = nil
	inst.Certificates.Variables[0].RegenerateState = RegenerateSending
	inst.Certificates.Variables[0].NewVersionID = ""

	storeCertificateTask(t, vault, stored)

	manager := loadManager(t, director, vault)

	task, err := manager.GetTask(certTaskID)
	if err != nil {
		t.Fatalf("the certificate task did not load: %v", err)
	}

	loaded := task.Instances[0]
	if loaded.Status != StatusAwaitingDecision {
		t.Fatalf("expected the instance to await a decision, got %s", loaded.Status)
	}

	if loaded.Certificates.Pending == nil || loaded.Certificates.Pending.Kind != DecisionReconcileRegeneration {
		t.Fatalf("expected a reconcile_regeneration decision, got %+v", loaded.Certificates.Pending)
	}

	if loaded.Certificates.Pending.Variable != certVariable {
		t.Fatalf("expected the decision to name %s, got %q", certVariable, loaded.Certificates.Pending.Variable)
	}

	if state := loaded.Certificates.Variables[0].RegenerateState; state != RegenerateOutcomeUnknown {
		t.Fatalf("expected the variable to be outcome_unknown, got %q", state)
	}

	if loaded.Certificates.Phase != RotationPhaseRegenerate {
		t.Fatalf("expected the phase to stay regenerate, got %q", loaded.Certificates.Phase)
	}

	if !task.Paused || task.Status != TaskStatusPaused {
		t.Fatalf("expected the task to be paused, got paused %v and status %s", task.Paused, task.Status)
	}

	saved, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+certTaskID)
	if !ok || saved.Instances[0].Status != StatusAwaitingDecision || saved.Status != TaskStatusPaused {
		t.Fatalf("expected the decision to be saved, got %+v", saved)
	}

	if _, held := manager.Guard().Holder(waitInstanceID, waitDeployment); !held {
		t.Fatalf("expected the instance to stay reserved while it awaits a decision")
	}

	// Give a lookup that was wrongly started time to reach the director.
	time.Sleep(50 * time.Millisecond)

	if count := contextLookups.Load() + director.lookups.Load(); count != 0 {
		t.Fatalf("expected no task lookup on the director, got %d", count)
	}
}

// A certificate instance in the deploy phase settles by its context ID, as
// an upgrade instance does.
//
//nolint:paralleltest // it replaces the lookup by context ID
func TestLoadSettlesADeployPhaseByContextID(t *testing.T) {
	contextLookups := countContextLookups(t)
	vault := newWaitVault()

	stored := fullCertificateTask()
	inst := &stored.Instances[0]
	inst.ContextID = loadedContextID
	inst.Certificates.Phase = RotationPhaseDeploy
	inst.Certificates.Pending = nil

	storeCertificateTask(t, vault, stored)

	loadManager(t, &lookupDirector{}, vault)

	deadline := time.Now().Add(waitTimeout)
	for contextLookups.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("expected the deploy phase to be looked up by its context ID")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// A request index entry is rebuilt from each loaded task.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRequestIndexRebuiltOnLoad(t *testing.T) {
	vault := newWaitVault()

	stored := fullCertificateTask()
	stored.Status = TaskStatusCompleted
	stored.Instances[0].Status = InstanceStatusSuccess
	storeCertificateTask(t, vault, stored)

	manager := loadManager(t, newWaitDirector(), vault)

	taskID, digest, ok := manager.requests.Lookup(certRequestID)
	if !ok || taskID != certTaskID || digest != certRequestDigest {
		t.Fatalf("expected the request ID to find task %s with its digest, got %q, %q, %v", certTaskID, taskID, digest, ok)
	}
}

// A certificate task record holds hashes, serials, and version IDs, and
// never certificate or key content.
func TestTaskRecordHoldsNoPEM(t *testing.T) {
	t.Parallel()

	task := fullCertificateTask()

	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("encoding the task: %v", err)
	}

	if strings.Contains(string(raw), "BEGIN") {
		t.Fatalf("expected no PEM in the task record, got %s", raw)
	}
}

// heldPutVault is a waitVault whose first save of one path waits inside
// Put until the test releases it, and only then encodes what it was given,
// so the encode reads the value the manager handed over after the test has
// changed the live task.
type heldPutVault struct {
	*waitVault

	path    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (v *heldPutVault) Put(ctx context.Context, path string, data interface{}) error {
	if path == v.path {
		first := false

		v.once.Do(func() { first = true })

		if first {
			close(v.entered)
			<-v.release
		}
	}

	return v.waitVault.Put(ctx, path, data)
}

// A save copies the task first, so a change to the live journal while the
// save is in flight never reaches what is written.
//
//nolint:paralleltest // it holds a save
func TestSnapshotIsolatedFromLiveJournal(t *testing.T) {
	recordPath := certificateTasksVaultPath + "/" + certTaskID
	vault := &heldPutVault{
		waitVault: newWaitVault(),
		path:      recordPath,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	manager := managerWithTasks(t, vault)

	live := fullCertificateTask()

	manager.mu.Lock()
	manager.tasks[live.ID] = &live
	manager.mu.Unlock()

	saved := make(chan error, 1)

	go func() { saved <- manager.persistTask(context.Background(), &live) }()

	select {
	case <-vault.entered:
	case <-time.After(waitTimeout):
		t.Fatal("the save never reached the vault")
	}

	manager.mu.Lock()
	live.Instances[0].Certificates.VMs[0].Files[0].State = FileStateReplaced
	live.Instances[0].Certificates.VMs[0].JobDirs[certJob] = "/var/vcap/data/jobs/redis/changed"
	live.Instances[0].Certificates.Pending.Candidates = append(live.Instances[0].Certificates.Pending.Candidates, Candidate{VersionID: "v3"})
	live.Instances[0].Certificates.Pending.Candidates[0].Checks[0].Passed = false
	manager.mu.Unlock()

	close(vault.release)

	err := <-saved
	if err != nil {
		t.Fatalf("the save failed: %v", err)
	}

	written, ok := vault.storedRecord(t, recordPath)
	if !ok {
		t.Fatalf("the certificate task was not saved")
	}

	rotation := written.Instances[0].Certificates
	if state := rotation.VMs[0].Files[0].State; state != FileStateBackedUp {
		t.Fatalf("expected the saved file entry to stay %s, got %s", FileStateBackedUp, state)
	}

	if dir := rotation.VMs[0].JobDirs[certJob]; dir != "/var/vcap/data/jobs/redis/4e5f6a7b" {
		t.Fatalf("expected the saved job directory to stay as it was, got %s", dir)
	}

	if count := len(rotation.Pending.Candidates); count != 1 || !rotation.Pending.Candidates[0].Checks[0].Passed {
		t.Fatalf("expected the one saved candidate as it was, got %+v", rotation.Pending.Candidates)
	}
}

// A certificate task loaded as pending is never run through the upgrade
// path, even when it reaches the queue.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDispatchSkipsCertificateTasks(t *testing.T) {
	vault := newWaitVault()

	stored := fullCertificateTask()
	stored.Status = TaskStatusPending
	stored.StartedAt = nil
	stored.Instances[0].Status = InstanceStatusPending
	stored.Instances[0].StartedAt = nil
	storeCertificateTask(t, vault, stored)

	// Any director call panics, so a run through the upgrade path fails
	// the task.
	manager := loadManager(t, newWaitDirector(), vault)

	manager.mu.Lock()
	live := manager.tasks[certTaskID]
	owned := manager.owned[certTaskID]
	manager.owned[certTaskID] = true
	manager.mu.Unlock()

	if owned {
		t.Fatalf("expected the loader not to queue the certificate task")
	}

	manager.taskQueue <- live

	deadline := time.Now().Add(waitTimeout)

	for {
		manager.mu.RLock()
		owned = manager.owned[certTaskID]
		manager.mu.RUnlock()

		if !owned {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("the dispatcher never let go of the certificate task")
		}

		time.Sleep(5 * time.Millisecond)
	}

	// Give a wrongly started run time to mark the task.
	time.Sleep(50 * time.Millisecond)

	task, err := manager.GetTask(certTaskID)
	if err != nil {
		t.Fatalf("the certificate task is gone: %v", err)
	}

	if task.Status != TaskStatusPending || task.StartedAt != nil {
		t.Fatalf("expected the certificate task to stay pending and unstarted, got %s started at %v", task.Status, task.StartedAt)
	}

	if _, held := manager.Guard().Holder(waitInstanceID, waitDeployment); !held {
		t.Fatalf("expected the pending certificate task to keep its reservation")
	}

	holder, _ := manager.Guard().Holder(waitInstanceID, waitDeployment)
	if holder.Kind != mutation.KindCertificates || holder.TaskID != certTaskID {
		t.Fatalf("expected a certificates holder for the task, got %+v", holder)
	}
}

// An upgrade with an empty kind still needs a stemcell, releases, or both.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestUpgradeWithEmptyKindStillNeedsTarget(t *testing.T) {
	manager := loadManager(t, newWaitDirector(), newWaitVault())

	_, err := manager.CreateTask(context.Background(), CreateTaskRequest{Kind: "", InstanceIDs: []string{waitInstanceID}})
	if !errors.Is(err, ErrNoUpgradeTarget) {
		t.Fatalf("expected %v, got %v", ErrNoUpgradeTarget, err)
	}
}

// A certificate task never carries upgrade targets, and CreateTask turns a
// certificates request away before the upgrade target rule sees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskHasNoUpgradeTargets(t *testing.T) {
	manager := loadManager(t, newWaitDirector(), newWaitVault())

	_, err := manager.CreateTask(context.Background(), CreateTaskRequest{Kind: TaskKindCertificates, InstanceIDs: []string{waitInstanceID}})
	if !errors.Is(err, ErrCertificateTaskKind) {
		t.Fatalf("expected %v, got %v", ErrCertificateTaskKind, err)
	}

	if tasks := manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no task to be created, got %d", len(tasks))
	}

	_, err = manager.CreateTask(context.Background(), CreateTaskRequest{Kind: "rollback", InstanceIDs: []string{waitInstanceID}})
	if !errors.Is(err, ErrInvalidTaskKind) {
		t.Fatalf("expected %v for an unknown kind, got %v", ErrInvalidTaskKind, err)
	}

	task := fullCertificateTask()
	task.Certificates.Mode = RotationModeRedeploy
	task.Instances = append(task.Instances, InstanceUpgrade{
		InstanceID:   certSecondInstance,
		Status:       InstanceStatusPending,
		Certificates: &InstanceRotation{Approved: ApprovedEffects{Selected: []VersionPair{{Name: "redis_ca"}}}},
	})

	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("encoding the task: %v", err)
	}

	var decoded map[string]json.RawMessage

	err = json.Unmarshal(raw, &decoded)
	if err != nil {
		t.Fatalf("decoding the task: %v", err)
	}

	if stemcell := string(decoded["target_stemcell"]); stemcell != `{"os":"","version":""}` {
		t.Fatalf("expected an empty stemcell target, got %s", stemcell)
	}

	if releases, ok := decoded["target_releases"]; ok && string(releases) != "[]" && string(releases) != "null" {
		t.Fatalf("expected no release targets, got %s", releases)
	}

	summary := task.ToSummary()
	if summary.Target != "Redeploy, 2 certificates" || strings.Contains(summary.Target, "/") {
		t.Fatalf("expected the target \"Redeploy, 2 certificates\", got %q", summary.Target)
	}

	if summary.Kind != TaskKindCertificates || summary.TargetStemcell != "" || len(summary.TargetReleases) != 0 {
		t.Fatalf("expected a certificates summary with no upgrade targets, got %+v", summary)
	}

	task.Certificates.Mode = RotationModePatch
	task.Instances = task.Instances[:1]

	if target := task.ToSummary().Target; target != "Patch, 1 certificate" {
		t.Fatalf("expected the target \"Patch, 1 certificate\", got %q", target)
	}
}

// An upgrade summary names its stemcell and releases in its target.
func TestUpgradeSummaryTarget(t *testing.T) {
	t.Parallel()

	task := UpgradeTask{
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		TargetReleases: []ReleaseTarget{{Name: "redis", Version: "4.1"}, {Name: "bpm", Version: "1.4.26"}},
	}

	if target := task.ToSummary().Target; target != "ubuntu-noble/1.2, redis/4.1, bpm/1.4.26" {
		t.Fatalf("expected the stemcell and the releases, got %q", target)
	}

	releasesOnly := UpgradeTask{TargetReleases: []ReleaseTarget{{Name: "redis", Version: "4.1"}}}
	if target := releasesOnly.ToSummary().Target; target != "redis/4.1" {
		t.Fatalf("expected the release alone, got %q", target)
	}
}

// The summary counts the instances that wait on a decision, and a
// cancelled task keeps the count so its decisions stay visible.
func TestSummaryCountsAwaitingDecisions(t *testing.T) {
	t.Parallel()

	task := UpgradeTask{
		Kind:   TaskKindCertificates,
		Status: TaskStatusCancelled,
		Instances: []InstanceUpgrade{
			{InstanceID: "a", Certificates: &InstanceRotation{Pending: &PendingDecision{}}},
			{InstanceID: "b", Certificates: &InstanceRotation{}},
			{InstanceID: "c", Certificates: &InstanceRotation{Pending: &PendingDecision{}}},
			{InstanceID: "d"},
		},
	}

	if got := task.ToSummary().AwaitingDecisionCount; got != 2 {
		t.Fatalf("expected 2 awaiting instances on a cancelled task, got %d", got)
	}

	raw, err := json.Marshal(task.ToSummary())
	if err != nil {
		t.Fatalf("encoding the summary: %v", err)
	}

	if !strings.Contains(string(raw), `"awaiting_decision_count":2`) {
		t.Fatalf("expected the count in the JSON, got %s", raw)
	}

	if none := (&UpgradeTask{}).ToSummary().AwaitingDecisionCount; none != 0 {
		t.Fatalf("expected 0 for a task with no instances, got %d", none)
	}
}
