package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
)

const (
	// createUnconfirmedMessage is what a certificate task whose creation
	// could not be confirmed records and answers.
	createUnconfirmedMessage = "Blacksmith could not confirm that this task was saved, so it never started."
	// orphanCreationMessage is what the loader records on a stored
	// certificate task whose creation was never confirmed.
	orphanCreationMessage = "creation was never confirmed, so this task never ran"
	// createRetryInterval is how often the failed status of an unconfirmed
	// create is saved again until a save of it is confirmed.
	createRetryInterval = 10 * time.Second
)

// CreateCertificateTaskRequest is the body of a request that creates a
// certificate task from a preview. It has no created_from field, because
// where a task was created comes from the request's context, never from
// the client.
type CreateCertificateTaskRequest struct {
	PreviewID       string `json:"preview_id"`
	ClientRequestID string `json:"client_request_id"`
	Name            string `json:"name"`
	Reason          string `json:"reason"`
}

// certNow reads the configured clock, or the system clock on a manager
// that was never configured.
func (m *Manager) certNow() time.Time {
	now := m.certDeps().Now
	if now == nil {
		return time.Now()
	}

	return now()
}

// requestLock serialises the creates that carry one client request ID.
// holders counts the creates that hold or wait for it, under the manager's
// mu.
type requestLock struct {
	mu      sync.Mutex
	holders int
}

// lockRequest waits until no other create with the request ID is under
// way and returns the function that lets the next one go. A create with no
// request ID shares a lock with nothing. The manager's mu is never held
// while it waits.
func (m *Manager) lockRequest(requestID string) func() {
	if requestID == "" {
		return func() {}
	}

	m.mu.Lock()

	lock := m.createLocks[requestID]
	if lock == nil {
		lock = &requestLock{}
		m.createLocks[requestID] = lock
	}

	lock.holders++
	m.mu.Unlock()

	lock.mu.Lock()

	return func() {
		lock.mu.Unlock()

		m.mu.Lock()
		defer m.mu.Unlock()

		lock.holders--
		if lock.holders == 0 {
			delete(m.createLocks, requestID)
		}
	}
}

// CreateCertificateTask creates a certificate task from a stored preview.
// It reports existing when the request ID already made a task with the
// same body, and returns that task instead of making another.
//
// The creates that share a request ID run one at a time, so a duplicate
// sent at once waits for the first and then finds its task. A new task
// copies each clear instance's approved effects from the preview, reserves
// its instances, and is saved twice through persistCertConfirmed, first
// with CreationConfirmed false and then with it true. Only once both saves
// are confirmed is the request ID indexed and the task queued.
//
// A failure before the first save releases every reservation. A failure
// during either save leaves the task failed in memory, still holding its
// instances, because a runnable record may already be in Vault, and a
// background loop saves the failed status until a save of it is confirmed
// and only then releases them. That answers 500 and indexes nothing, so a
// retry after Vault recovers creates a new task.
func (m *Manager) CreateCertificateTask(ctx context.Context, req CreateCertificateTaskRequest) (*UpgradeTask, bool, error) {
	// Until the loader has put every stored task in m.tasks, neither the
	// request index nor the guard knows what they hold.
	select {
	case <-m.loaded:
	default:
		return nil, false, ErrTasksLoading
	}

	unlock := m.lockRequest(req.ClientRequestID)
	defer unlock()

	digest, err := requestDigest(req)
	if err != nil {
		return nil, false, apierr.Wrap(http.StatusInternalServerError, apierr.CodeInternal, err)
	}

	// A retry finds its task before the preview is read, so it gets the
	// original back even after the preview expired.
	original, found, err := m.existingCertificateTask(req.ClientRequestID, digest)
	if found || err != nil {
		return original, found, err
	}

	preview, err := m.createPreview(req.PreviewID)
	if err != nil {
		return nil, false, err
	}

	err = m.certWritesAllowed(preview.mode)
	if err != nil {
		return nil, false, err
	}

	task := m.newCertificateTask(req, digest, preview)
	if len(task.Instances) == 0 {
		return nil, false, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, "Every instance in the preview is blocked, so there is nothing to rotate.")
	}

	m.logger.Info("Creating certificate task %s for %d instances from preview %s", task.ID, len(task.Instances), preview.ID)

	err = m.reserveCertificateTask(task)
	if err != nil {
		return nil, false, err
	}

	err = m.persistCertConfirmed(ctx, task)
	if err == nil {
		m.mu.Lock()
		task.CreationConfirmed = true
		m.mu.Unlock()

		err = m.persistCertConfirmed(ctx, task)
	}

	if err != nil {
		return nil, false, m.failUnconfirmedCreate(task, err)
	}

	m.requests.Put(req.ClientRequestID, task.ID, digest)

	m.mu.Lock()
	m.owned[task.ID] = true
	created := copyTask(task)
	m.mu.Unlock()

	m.enqueue(task) //nolint:contextcheck // the task outlives the request, so it is queued under the manager's context

	return created, false, nil
}

// existingCertificateTask returns a copy of the task an earlier create with
// the request ID made, and true. A request ID that made no task returns
// false, and one whose earlier body differs is a 409 request_id_conflict.
func (m *Manager) existingCertificateTask(requestID, digest string) (*UpgradeTask, bool, error) {
	if requestID == "" {
		return nil, false, nil
	}

	taskID, stored, ok := m.requests.Lookup(requestID)
	if !ok {
		return nil, false, nil
	}

	if stored != digest {
		return nil, false, apierr.New(http.StatusConflict, apierr.CodeRequestIDConflict, "This client_request_id was already used for a different request, so no task was created.")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, false, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "The task this client_request_id created is missing.")
	}

	return copyTask(task), true, nil
}

// createPreview returns the stored preview a create names. A preview bound
// to one decision of a task approves that decision only, so it can't
// create a task and counts as unknown here.
func (m *Manager) createPreview(previewID string) (*Preview, error) {
	d := m.certDeps()
	if d.Previews == nil {
		// A manager that was never configured has no store, and refuses
		// every certificate write for that reason first.
		err := m.certWritesAllowed(RotationModeRedeploy)
		if err != nil {
			return nil, err
		}

		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for previews.")
	}

	preview, err := d.Previews.Get(previewID)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store answers with the API error
	}

	if preview.forTask != "" {
		return nil, apierr.New(http.StatusGone, apierr.CodePreviewExpired, "This preview approves a decision of another task, so preview the rotation again.")
	}

	return preview, nil
}

// newCertificateTask builds a pending task with one instance for each
// instance of the preview that is not blocked. Each instance copies the
// effects the preview bound for it, so the approval outlives the preview.
func (m *Manager) newCertificateTask(req CreateCertificateTaskRequest, digest string, preview *Preview) *UpgradeTask {
	instances := make([]InstanceUpgrade, 0, len(preview.Instances))

	for i := range preview.Instances {
		pi := &preview.Instances[i]
		if pi.Blocked != nil {
			continue
		}

		variables := make([]VariableRotation, 0, len(pi.approved.Selected))
		for _, pair := range pi.approved.Selected {
			variables = append(variables, VariableRotation{
				Name:            pair.Name,
				CertificateID:   pi.certificateIDs[pair.Name],
				OldVersionID:    pair.DeployedVersionID,
				RegenerateState: RegenerateNotSent,
			})
		}

		instances = append(instances, InstanceUpgrade{
			InstanceID:     pi.InstanceID,
			InstanceName:   pi.InstanceName,
			DeploymentName: pi.deployment,
			ServiceID:      pi.serviceID,
			PlanID:         pi.planID,
			Status:         InstanceStatusPending,
			Certificates: &InstanceRotation{
				Phase:     RotationPhasePreflight,
				Approved:  pi.approved.Clone(),
				Variables: variables,
				Gate:      pi.gate,
			},
		})
	}

	return &UpgradeTask{
		ID:     uuid.New().String(),
		Name:   req.Name,
		Kind:   TaskKindCertificates,
		Status: TaskStatusPending,
		Certificates: &CertificateTaskSpec{
			Mode:      preview.mode,
			Converge:  preview.converge,
			Reason:    req.Reason,
			PreviewID: preview.ID,
		},
		ClientRequestID: req.ClientRequestID,
		RequestDigest:   digest,
		Instances:       instances,
		TotalCount:      len(instances),
		CreatedAt:       m.certNow(),
	}
}

// reserveCertificateTask reserves every instance of the new task and puts
// the task in m.tasks, in one critical section. A busy instance is a 409
// instance_busy naming its holder, and the task then holds nothing.
func (m *Manager) reserveCertificateTask(task *UpgradeTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	err := m.reserveNewLocked(task)
	if err != nil {
		if errors.Is(err, ErrInstanceBusy) {
			return apierr.Wrap(http.StatusConflict, apierr.CodeInstanceBusy, err)
		}

		return apierr.Wrap(http.StatusInternalServerError, apierr.CodeInternal, err)
	}

	m.tasks[task.ID] = task

	return nil
}

// failUnconfirmedCreate fails a new task whose saves could not be
// confirmed, keeps its reservations, and starts the loop that saves the
// failed status and then releases them. It returns the 500 the create
// answers.
func (m *Manager) failUnconfirmedCreate(task *UpgradeTask, cause error) error {
	now := m.certNow()

	m.mu.Lock()

	task.CreationConfirmed = false
	task.Status = TaskStatusFailed
	task.CompletedAt = &now

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status.settled() {
			continue
		}

		inst.Status = InstanceStatusFailed
		inst.Error = createUnconfirmedMessage
		inst.CompletedAt = copyTime(&now)
		task.FailedCount++
	}

	interval := m.createRetry
	m.mu.Unlock()

	m.logger.Error("Certificate task %s failed, because its creation could not be confirmed: %v", task.ID, cause)

	if m.ctx.Err() == nil {
		m.wg.Add(1)

		go m.saveUnconfirmedCreate(task, interval)
	}

	return &apierr.Error{
		Status:  http.StatusInternalServerError,
		Code:    apierr.CodeInternal,
		Message: createUnconfirmedMessage,
		Err:     cause,
	}
}

// saveUnconfirmedCreate saves the failed status of a task whose creation
// could not be confirmed, every interval until a save is confirmed or Stop
// ends it, and then releases the task's reservations. Until then a record
// that would run may be in Vault, so the instances stay held.
func (m *Manager) saveUnconfirmedCreate(task *UpgradeTask, interval time.Duration) {
	defer m.wg.Done()

	for {
		err := m.persistCertConfirmed(m.ctx, task)
		if err == nil {
			m.mu.Lock()
			m.releaseAllLocked(task.ID)
			m.mu.Unlock()

			m.logger.Info("Saved the failed status of certificate task %s, so its instances are free", task.ID)

			return
		}

		select {
		case <-time.After(interval):
		case <-m.stopCh:
			return
		}
	}
}

// failOrphanLocked fails a loaded certificate task whose creation was
// never confirmed, which an interrupted create left, so it never runs. It
// reports whether it changed the task. The caller holds mu.
func failOrphanLocked(task *UpgradeTask, now time.Time) bool {
	if task.Status == TaskStatusFailed {
		return false
	}

	task.Status = TaskStatusFailed
	task.CompletedAt = &now

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status.settled() {
			continue
		}

		inst.Status = InstanceStatusFailed
		inst.Error = orphanCreationMessage
		inst.CompletedAt = copyTime(&now)
		task.FailedCount++
	}

	return true
}

// isOrphanCreation reports whether a loaded task is a certificate task
// whose creation was never confirmed.
func isOrphanCreation(task *UpgradeTask) bool {
	return task.KindOrDefault() == TaskKindCertificates && !task.CreationConfirmed
}

// failLoadedOrphans fails each loaded orphan and saves it, as
// failOrphanLocked describes. It returns the tasks that are not orphans,
// which alone index their request IDs and restore their reservations.
func (m *Manager) failLoadedOrphans(loaded []*UpgradeTask) []*UpgradeTask {
	kept := make([]*UpgradeTask, 0, len(loaded))

	var changed []*UpgradeTask

	now := m.certNow()

	m.mu.Lock()

	for _, task := range loaded {
		if !isOrphanCreation(task) {
			kept = append(kept, task)

			continue
		}

		if failOrphanLocked(task, now) {
			changed = append(changed, task)
		}
	}

	m.mu.Unlock()

	for _, task := range changed {
		m.logger.Info("Certificate task %s failed at load, because its creation was never confirmed", task.ID)

		err := m.persistTask(context.WithoutCancel(m.ctx), task)
		if err != nil {
			m.logger.Error("Failed to persist the failed status of certificate task %s: %v", task.ID, fmt.Errorf("saving the orphan: %w", err))
		}
	}

	return kept
}
