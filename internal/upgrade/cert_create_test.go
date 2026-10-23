package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/internal/mutation"
)

const (
	createRequestID = "3b8f"
	createName      = "CHG-1234 Redis leaves"
)

// createVault is a waitVault whose writes of certificate task records fail
// before they store anything while the test's rule says so. The rule gets
// the number of the record write, counted from 1.
//
// Under the land rule a chosen record write stores the record and then
// returns an error anyway, and while blind is set every read of a record
// fails, so the manager can't tell that the write landed.
type createVault struct {
	*waitVault

	script sync.Mutex
	writes int
	fail   func(n int) bool
	land   func(n int) bool
	blind  bool
	// landed holds the body of each record write that stored, by number,
	// so a test can check one write after later ones replace it.
	landed map[int][]byte
}

func (v *createVault) Put(ctx context.Context, path string, data interface{}) error {
	if !strings.HasPrefix(path, certificateTasksVaultPath+"/") {
		return v.waitVault.Put(ctx, path, data)
	}

	v.script.Lock()
	v.writes++
	number := v.writes
	refused := v.fail != nil && v.fail(number)
	unacknowledged := v.land != nil && v.land(number)
	v.script.Unlock()

	if refused {
		return errPutRefused
	}

	err := v.waitVault.Put(ctx, path, data)
	if err == nil {
		v.keep(number, data)
	}

	if err == nil && unacknowledged {
		return errAnswerLost
	}

	return err
}

func (v *createVault) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	v.script.Lock()
	blind := v.blind && strings.HasPrefix(path, certificateTasksVaultPath+"/")
	v.script.Unlock()

	if blind {
		return false, errGetRefused
	}

	return v.waitVault.Get(ctx, path, result)
}

// keep records the body of a record write that stored.
func (v *createVault) keep(number int, data interface{}) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}

	v.script.Lock()
	defer v.script.Unlock()

	if v.landed == nil {
		v.landed = make(map[int][]byte)
	}

	v.landed[number] = body
}

// landedRecord returns the record that the numbered write stored, or false
// when that write stored nothing.
func (v *createVault) landedRecord(t *testing.T, number int) (UpgradeTask, bool) {
	t.Helper()

	v.script.Lock()
	body, ok := v.landed[number]
	v.script.Unlock()

	var task UpgradeTask
	if !ok {
		return task, false
	}

	err := json.Unmarshal(body, &task)
	if err != nil {
		t.Fatalf("decoding record write %d: %v", number, err)
	}

	return task, true
}

// landUnacknowledged sets the rule that picks the record writes that store
// the record but answer an error, and makes every record read fail.
func (v *createVault) landUnacknowledged(land func(n int) bool) {
	v.script.Lock()
	defer v.script.Unlock()

	v.land = land
	v.blind = true
}

// failRecords sets the rule that picks the record writes that fail.
func (v *createVault) failRecords(fail func(n int) bool) {
	v.script.Lock()
	defer v.script.Unlock()

	v.fail = fail
}

// heal lets every later write land and every later read answer.
func (v *createVault) heal() {
	v.script.Lock()
	defer v.script.Unlock()

	v.fail = nil
	v.land = nil
	v.blind = false
}

// createEnv returns a preview environment whose manager saves to a
// createVault, retries an unconfirmed create's save at once, and has
// loaded its stored tasks.
func createEnv(t *testing.T) (*previewEnv, *createVault) {
	t.Helper()

	vault := &createVault{waitVault: newWaitVault()}

	env := newPreviewEnv(t)
	env.manager.vault = vault
	env.manager.createRetry = 5 * time.Millisecond
	env.manager.Start()
	waitForLoad(t, env.manager)

	return env, vault
}

func createRequest(previewID string) CreateCertificateTaskRequest {
	return CreateCertificateTaskRequest{
		PreviewID:       previewID,
		ClientRequestID: createRequestID,
		Name:            createName,
		Reason:          "CHG-1234",
	}
}

// taskCopy returns a copy of the manager's own task.
func taskCopy(t *testing.T, manager *Manager, taskID string) UpgradeTask {
	t.Helper()

	manager.mu.RLock()
	defer manager.mu.RUnlock()

	task, ok := manager.tasks[taskID]
	if !ok {
		t.Fatalf("the manager has no task %s", taskID)
	}

	return *copyTask(task)
}

// expectNothingHeld fails the test when the guard holds any instance.
func expectNothingHeld(t *testing.T, manager *Manager) {
	t.Helper()

	if held := manager.Guard().Snapshot(); len(held) != 0 {
		t.Fatalf("expected nothing reserved, got %+v", held)
	}
}

// expectCertificateHolder fails the test unless the task holds the
// instance as a certificate rotation.
func expectCertificateHolder(t *testing.T, manager *Manager, instanceID, taskID string) {
	t.Helper()

	holder, held := manager.Guard().Holder(instanceID, "")
	want := mutation.Holder{Kind: mutation.KindCertificates, TaskID: taskID, Description: `certificate rotation "` + createName + `"`}

	if !held || holder.Kind != want.Kind || holder.TaskID != want.TaskID || holder.Description != want.Description {
		t.Fatalf("expected %+v to hold %s, got %+v (held %v)", want, instanceID, holder, held)
	}
}

// The task copies each instance's bound effects into its record, holds
// the instance, and is saved confirmed before it is answered.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskCopiesApproval(t *testing.T) {
	env, vault := createEnv(t)
	env.credhub.setLatest(previewPassword(waitDeployment), "p2")
	preview := previewOf(t, env, redeploy(waitInstanceID))
	bound := preview.Instances[0].approved

	task, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil || existing {
		t.Fatalf("CreateCertificateTask: existing %v, %v", existing, err)
	}

	if task.Kind != TaskKindCertificates || task.Status != TaskStatusPending || !task.CreationConfirmed || task.CreatedFrom != "" {
		t.Fatalf("unexpected task: %+v", task)
	}

	spec := task.Certificates
	if spec == nil || spec.PreviewID != preview.ID || spec.Mode != RotationModeRedeploy || spec.Reason != "CHG-1234" {
		t.Fatalf("unexpected certificate spec: %+v", spec)
	}

	if task.ClientRequestID != createRequestID || task.RequestDigest == "" || task.TotalCount != 1 || len(task.Instances) != 1 {
		t.Fatalf("unexpected request fields or instances: %+v", task)
	}

	inst := task.Instances[0]
	if inst.InstanceID != waitInstanceID || inst.DeploymentName != waitDeployment || inst.InstanceName != "orders-cache" ||
		inst.ServiceID != "redis-id" || inst.PlanID != "cluster-7-id" || inst.Status != InstanceStatusPending {
		t.Fatalf("unexpected instance: %+v", inst)
	}

	rotation := inst.Certificates
	if rotation == nil || rotation.Phase != RotationPhasePreflight || !reflect.DeepEqual(rotation.Approved, bound) {
		t.Fatalf("expected the bound effects %+v copied, got %+v", bound, rotation)
	}

	want := VariableRotation{Name: previewLeaf(waitDeployment), CertificateID: "cert-" + waitDeployment, OldVersionID: "v1", RegenerateState: RegenerateNotSent}
	if len(rotation.Variables) != 1 || !reflect.DeepEqual(rotation.Variables[0], want) {
		t.Fatalf("expected variables [%+v], got %+v", want, rotation.Variables)
	}

	// The record shares no slice with the stored preview, so nothing that
	// touches one ever changes the other.
	live := taskCopy(t, env.manager, task.ID).Instances[0].Certificates.Approved

	env.manager.mu.RLock()
	approved := env.manager.tasks[task.ID].Instances[0].Certificates.Approved
	env.manager.mu.RUnlock()

	if &approved.Selected[0] == &bound.Selected[0] || &approved.Additional[0] == &bound.Additional[0] || &approved.VMCIDs[0] == &bound.VMCIDs[0] {
		t.Fatal("the task's approval shares a slice with the preview")
	}

	if !reflect.DeepEqual(live, bound) {
		t.Fatalf("the live approval %+v differs from the preview's %+v", live, bound)
	}

	expectCertificateHolder(t, env.manager, waitInstanceID, task.ID)

	// The worker may already have started the task and saved it running,
	// so either status shows the create's record, as long as it is
	// confirmed.
	stored, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+task.ID)
	if !ok || !stored.CreationConfirmed || (stored.Status != TaskStatusPending && stored.Status != TaskStatusRunning) {
		t.Fatalf("expected a confirmed pending or running record, got %+v (stored %v)", stored, ok)
	}
}

// A retried create with the same body returns the task the first made.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskRetryReturnsOriginal(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	first, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil || existing {
		t.Fatalf("first create: existing %v, %v", existing, err)
	}

	second, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil || !existing || second.ID != first.ID {
		t.Fatalf("expected the original task %s back, got %+v existing %v, %v", first.ID, second, existing, err)
	}

	if tasks := env.manager.ListTasks(); len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
}

// Concurrent creates with one request ID make one task, and every caller
// gets it back.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskConcurrentDuplicates(t *testing.T) {
	const callers = 16

	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		ids   = make(map[string]int)
		found int
		errs  []error
	)

	start.Add(1)

	for range callers {
		done.Add(1)

		go func() {
			defer done.Done()

			start.Wait()

			task, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)

				return
			}

			ids[task.ID]++

			if existing {
				found++
			}
		}()
	}

	start.Done()
	done.Wait()

	if len(errs) != 0 || len(ids) != 1 || found != callers-1 {
		t.Fatalf("expected one task and %d existing answers, got %v, %d existing, errors %v", callers-1, ids, found, errs)
	}

	if tasks := env.manager.ListTasks(); len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
}

// A known request ID with another body is a conflict.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskRequestIDConflict(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	changed := createRequest(preview.ID)
	changed.Name = "CHG-9999 another change"

	_, _, err = env.manager.CreateCertificateTask(context.Background(), changed)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)
}

// A blocked instance is left out of the task, and a preview with nothing
// but blocked instances creates nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskSkipsBlocked(t *testing.T) {
	env, _ := createEnv(t)

	release, err := env.manager.Guard().Acquire(previewInstance2, previewDeployment2, mutation.Holder{
		Kind: mutation.KindUpgrade, TaskID: "upgrade-9", Description: `upgrade "nightly stemcells"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)

	preview := previewOf(t, env, redeploy(waitInstanceID, previewInstance2))

	task, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil {
		t.Fatalf("CreateCertificateTask: %v", err)
	}

	if len(task.Instances) != 1 || task.Instances[0].InstanceID != waitInstanceID || task.TotalCount != 1 {
		t.Fatalf("expected only the clear instance, got %+v", task.Instances)
	}

	blocked := previewOf(t, env, redeploy(previewInstance2))
	req := createRequest(blocked.ID)
	req.ClientRequestID = "7c1d"

	_, _, err = env.manager.CreateCertificateTask(context.Background(), req)
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeNoSelections)
}

// An instance taken after the preview refuses the create, which then
// holds nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskBusyReleasesEverything(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID, previewInstance2))

	release, err := env.manager.Guard().Acquire(previewInstance2, previewDeployment2, mutation.Holder{
		Kind: mutation.KindUpgrade, TaskID: "upgrade-9", Description: `upgrade "nightly stemcells"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	_, _, err = env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	expectAPIError(t, err, http.StatusConflict, apierr.CodeInstanceBusy)

	if !strings.Contains(err.Error(), "nightly stemcells") || !strings.Contains(err.Error(), "can't be rotated") {
		t.Fatalf("the busy error should name the holder and the rotation: %v", err)
	}

	release()
	expectNothingHeld(t, env.manager)

	if tasks := env.manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("expected no task, got %d", len(tasks))
	}
}

// A create before the stored tasks are loaded is turned away.
func TestCreateCertificateTaskBeforeLoad(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if !errors.Is(err, ErrTasksLoading) {
		t.Fatalf("expected ErrTasksLoading, got %v", err)
	}

	expectNothingHeld(t, env.manager)
}

// Turning rotation off after the preview stops the create.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskRotationOff(t *testing.T) {
	env, vault := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	env.setSwitches(false, true)

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	expectNothingHeld(t, env.manager)

	if paths := vault.storedPaths(); len(paths) != 0 {
		t.Fatalf("expected nothing saved, got %v", paths)
	}
}

// A retry finds the task its request ID made even after the preview has
// left the store and rotation was turned off, because answering it writes
// nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskRetryAfterPreviewExpired(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	created, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil {
		t.Fatalf("CreateCertificateTask: %v", err)
	}

	store := env.manager.certDeps().Previews
	store.mu.Lock()
	delete(store.previews, preview.ID)
	store.mu.Unlock()

	env.setSwitches(false, true)

	retried, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil || !existing || retried.ID != created.ID {
		t.Fatalf("expected the original task %s back, got %+v existing %v, %v", created.ID, retried, existing, err)
	}
}

// A new request with rotation off answers rotation_disabled before it
// reads the preview, so an unknown preview doesn't hide the real reason.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskUnknownPreviewRotationOff(t *testing.T) {
	env, vault := createEnv(t)
	env.setSwitches(false, true)

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest("pv-unknown"))
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	expectNothingHeld(t, env.manager)

	if paths := vault.storedPaths(); len(paths) != 0 {
		t.Fatalf("expected nothing saved, got %v", paths)
	}
}

// expectUnconfirmedCreate checks the answer and the state an unconfirmed
// create leaves: a failed task in memory that still holds its instance,
// owns no processor, and has no request index entry.
func expectUnconfirmedCreate(t *testing.T, env *previewEnv, err error) string {
	t.Helper()

	expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

	if !errors.Is(err, ErrSaveUnconfirmed) || err.Error() != createUnconfirmedMessage {
		t.Fatalf("expected the unconfirmed-save message, got %v", err)
	}

	tasks := env.manager.ListTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected the failed task in memory, got %d tasks", len(tasks))
	}

	task := tasks[0]
	if task.Status != TaskStatusFailed || task.CreationConfirmed || task.Instances[0].Status != InstanceStatusFailed || task.Instances[0].Error != createUnconfirmedMessage {
		t.Fatalf("expected a failed, unconfirmed task, got %+v", task)
	}

	expectCertificateHolder(t, env.manager, waitInstanceID, task.ID)

	env.manager.mu.RLock()
	owned := env.manager.owned[task.ID]
	env.manager.mu.RUnlock()

	if owned {
		t.Fatal("an unconfirmed task must never be queued")
	}

	if _, _, indexed := env.manager.requests.Lookup(createRequestID); indexed {
		t.Fatal("an unconfirmed task's request ID must not be indexed")
	}

	return task.ID
}

// expectFailedSaved waits until the failed status of the task is saved and
// the guard frees its instance.
func expectFailedSaved(t *testing.T, env *previewEnv, vault *createVault, taskID string) {
	t.Helper()

	eventuallyFree(t, env.manager, waitInstanceID)

	stored, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+taskID)
	if !ok || stored.Status != TaskStatusFailed || stored.CreationConfirmed {
		t.Fatalf("expected the failed status saved before the release, got %+v (stored %v)", stored, ok)
	}
}

// When the first save can't be confirmed, the instance stays held until
// the failed status is saved.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateFirstSaveUnconfirmedHoldsReservations(t *testing.T) {
	env, vault := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	vault.failRecords(func(int) bool { return true })

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	taskID := expectUnconfirmedCreate(t, env, err)

	// The instance stays held across several retries while Vault refuses.
	time.Sleep(50 * time.Millisecond)
	expectCertificateHolder(t, env.manager, waitInstanceID, taskID)

	vault.heal()
	expectFailedSaved(t, env, vault, taskID)
}

// When the record without confirmation lands but the second save can't be
// confirmed, the task fails held, and a retry after Vault recovers makes a
// new task.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateSecondSaveUnconfirmedHoldsReservations(t *testing.T) {
	env, vault := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	vault.failRecords(func(n int) bool { return n >= 2 })

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	taskID := expectUnconfirmedCreate(t, env, err)

	stored, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+taskID)
	if !ok || stored.CreationConfirmed || stored.Status != TaskStatusPending {
		t.Fatalf("expected the unconfirmed first record, got %+v (stored %v)", stored, ok)
	}

	vault.heal()
	expectFailedSaved(t, env, vault, taskID)

	retried, existing, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil || existing || retried.ID == taskID || !retried.CreationConfirmed {
		t.Fatalf("expected a new confirmed task, got %+v existing %v, %v", retried, existing, err)
	}

	expectCertificateHolder(t, env.manager, waitInstanceID, retried.ID)
}

// When the save that carries creation_confirmed lands in Vault but its
// answer is lost and the read-back fails, the task fails held, and the
// failed status, without creation_confirmed, replaces the confirmed record
// before the guard frees the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateConfirmedSaveLandsUnacknowledged(t *testing.T) {
	env, vault := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	vault.landUnacknowledged(func(n int) bool { return n >= 2 })

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	taskID := expectUnconfirmedCreate(t, env, err)

	// The failed-record loop starts writing at once, so the stored value
	// may already be its record. The second write is the confirmed one.
	landed, ok := vault.landedRecord(t, 2)
	if !ok || landed.ID != taskID || !landed.CreationConfirmed || landed.Status != TaskStatusPending {
		t.Fatalf("expected the confirmed record to have landed, got %+v (stored %v)", landed, ok)
	}

	time.Sleep(50 * time.Millisecond)
	expectCertificateHolder(t, env.manager, waitInstanceID, taskID)

	vault.heal()
	expectFailedSaved(t, env, vault, taskID)
}

// recordWrites counts the certificate record writes the vault was asked
// for.
func (v *createVault) recordWrites() int {
	v.script.Lock()
	defer v.script.Unlock()

	return v.writes
}

// Stop takes mu once after it cancels the manager's context, so a create
// that is deciding under mu whether to start its retry loop either adds
// the loop to the WaitGroup before Stop waits or sees the context
// cancelled and starts none.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestStopWaitsForCreateCriticalSection(t *testing.T) {
	env, _ := createEnv(t)

	env.manager.mu.Lock()

	stopped := make(chan struct{})

	go func() {
		env.manager.Stop()
		close(stopped)
	}()

	<-env.manager.ctx.Done()

	select {
	case <-stopped:
		env.manager.mu.Unlock()
		t.Fatal("Stop returned while a create could still be deciding under mu")
	case <-time.After(50 * time.Millisecond):
	}

	env.manager.mu.Unlock()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop never returned after mu was released")
	}
}

// An unconfirmed create after Stop starts no retry loop, so nothing writes
// to Vault once the create has answered.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateAfterStopStartsNoRetryLoop(t *testing.T) {
	env, vault := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	vault.failRecords(func(int) bool { return true })
	env.manager.Stop()

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

	writes := vault.recordWrites()

	time.Sleep(50 * time.Millisecond)

	if later := vault.recordWrites(); later != writes {
		t.Fatalf("expected no record write after the create answered, got %d more", later-writes)
	}
}

// A stored certificate task whose creation was never confirmed loads
// failed, holds nothing, and never runs.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestLoadOrphanCreationNeverRuns(t *testing.T) {
	vault := newWaitVault()

	stored := fullCertificateTask()
	stored.CreationConfirmed = false
	stored.Status = TaskStatusPending
	stored.StartedAt = nil
	stored.Instances[0].Status = InstanceStatusPending
	stored.Instances[0].StartedAt = nil
	stored.Instances[0].Certificates.Pending = nil
	storeCertificateTask(t, vault, stored)

	manager := loadManager(t, newWaitDirector(), vault)

	task := taskCopy(t, manager, certTaskID)
	if task.Status != TaskStatusFailed || task.Instances[0].Status != InstanceStatusFailed || task.Instances[0].Error != orphanCreationMessage {
		t.Fatalf("expected the orphan to load failed, got %+v", task)
	}

	expectNothingHeld(t, manager)

	manager.mu.RLock()
	owned := manager.owned[certTaskID]
	manager.mu.RUnlock()

	if owned {
		t.Fatal("an orphan must never be queued")
	}

	if _, _, indexed := manager.requests.Lookup(certRequestID); indexed {
		t.Fatal("an orphan's request ID must not be indexed")
	}

	saved, ok := vault.storedRecord(t, certificateTasksVaultPath+"/"+certTaskID)
	if !ok || saved.Status != TaskStatusFailed || saved.Instances[0].Status != InstanceStatusFailed {
		t.Fatalf("expected the failed status saved, got %+v", saved)
	}
}

// A task created under a context with an actor records who made it and
// where from, and one created without an actor records neither.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskRecordsActor(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	ctx := audit.WithActor(context.Background(), audit.Actor{Username: "admin", RemoteAddr: "10.0.0.5:4000", ForwardedFor: "203.0.113.9"})

	task, _, err := env.manager.CreateCertificateTask(ctx, createRequest(preview.ID))
	if err != nil {
		t.Fatalf("CreateCertificateTask: %v", err)
	}

	if task.CreatedBy != "admin" || task.CreatedFrom != "10.0.0.5:4000 via 203.0.113.9" {
		t.Fatalf("expected the actor recorded, got by %q from %q", task.CreatedBy, task.CreatedFrom)
	}

	live := taskCopy(t, env.manager, task.ID)
	if live.CreatedBy != "admin" || live.CreatedFrom != task.CreatedFrom {
		t.Fatalf("the stored task lost the actor: %+v", live)
	}
}
