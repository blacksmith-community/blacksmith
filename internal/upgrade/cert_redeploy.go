package upgrade

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

const (
	// rotationOffMessage is what a certificate task shows when it pauses
	// because rotation was turned off after it was created.
	rotationOffMessage = "Rotation is off on this broker, so this task paused before changing anything. " +
		"Turn certificates.rotation.enabled back on and resume, or cancel the task."

	// deployPendingNote tells the operator what became of the new
	// versions when a redeploy did not apply them.
	deployPendingNote = "The new certificate versions remain in CredHub as deploy_pending, " +
		"and a converge or another redeploy applies them."

	// neverStartedMessage is what a loaded certificate task that changed
	// nothing shows, since its creation may have been reported as failed.
	neverStartedMessage = "Blacksmith restarted before this task changed anything, and it can't tell whether " +
		"the task's creation was reported as failed. Check that the rotation is still wanted, then resume it or cancel the task."

	// verifyAttempts is how many times a listener is probed for the new
	// certificate, verifyIntervalDefault apart.
	verifyAttempts        = 3
	verifyIntervalDefault = 10 * time.Second

	// rescanTimeout bounds the inventory scan that follows a settled
	// rotation.
	rescanTimeout = 2 * time.Minute

	// cancelledVerifyAttempts is how many times a cancelled task tries to
	// verify a done deploy whose reads fail, waiting verifyInterval at
	// first and twice as long each time after, up to maxVerifyBackoff.
	cancelledVerifyAttempts = 5
	maxVerifyBackoff        = 2 * time.Minute
)

// errVerifyUnreadable marks a verification that could not read what it
// checks, which pauses the task rather than failing the instance.
var errVerifyUnreadable = errors.New("the verification could not read what it checks")

// runsCertificatesLocked reports whether the manager runs this task as a
// certificate rotation. Only a configured manager does, and only a
// redeploy, since the other modes have no runner yet. The caller holds mu.
func (m *Manager) runsCertificatesLocked(task *UpgradeTask) bool {
	return task.KindOrDefault() == TaskKindCertificates && m.configured &&
		certTaskMode(task) == RotationModeRedeploy
}

// processCertificateInstance takes one instance of a certificate task
// through the pause wait and its rotation, and reports whether the task
// goes on to its next instance. An instance that waits for a decision
// pauses the task again, and one whose reservation still waits for
// another holder runs once that holder frees it.
func (m *Manager) processCertificateInstance(task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	ctx := m.ctx
	persistCtx := context.WithoutCancel(ctx)

	for {
		if ctx.Err() != nil {
			return false
		}

		goOn, _ := m.waitWhilePaused(persistCtx, task, cancelCh)
		if !goOn {
			m.verifyIfCancelled(ctx, task, inst)

			return false
		}

		if m.reservationWaits(task, inst) {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(m.restoreRetry):
			}

			continue
		}

		switch m.nextCertificateStep(inst) {
		case stepSettled:
			return true
		case stepDecision:
			m.mu.Lock()
			pauseForDecisionLocked(task)
			m.mu.Unlock()

			m.logger.Info("Instance %s of certificate task %s waits for a decision, so the task stays paused", inst.InstanceID, task.ID)

			err := m.persistCertConfirmed(persistCtx, task)
			if err != nil {
				m.logger.Error("Certificate task %s: %v", task.ID, err)
			}

			continue
		case stepInFlight:
			m.logger.Info("Instance %s of certificate task %s has a deploy in flight, so the task stops until it settles", inst.InstanceID, task.ID)

			return false
		case stepVerify:
			m.verifyAndSettle(ctx, task, inst)
		case stepMutation:
			m.redeployFromMutation(ctx, task, inst, cancelCh)
		case stepRotate:
			m.rotateInstanceRedeploy(ctx, task, inst, cancelCh)
		}

		if ctx.Err() != nil {
			m.logger.Info("Manager stopping during certificate task %s, so instance %s stays as it is", task.ID, inst.InstanceID)

			return false
		}

		m.mu.RLock()
		settled := inst.Status.settled() && inst.Status != InstanceStatusOutcomeUnknown
		paused := task.Paused
		cancelled := task.Status == TaskStatusCancelled
		m.mu.RUnlock()

		switch {
		case settled:
			return true
		case paused || cancelled:
			continue
		default:
			return false
		}
	}
}

// certificateStep is what the processor does next with an instance.
type certificateStep int

const (
	stepRotate certificateStep = iota
	stepMutation
	stepVerify
	stepInFlight
	stepDecision
	stepSettled
)

// nextCertificateStep reads the instance's state and picks its next step.
func (m *Manager) nextCertificateStep(inst *InstanceUpgrade) certificateStep {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rotation := inst.Certificates

	switch {
	case inst.Status == StatusAwaitingDecision || inst.Status == InstanceStatusOutcomeUnknown:
		return stepDecision
	case inst.Status.settled():
		return stepSettled
	case rotation == nil:
		return stepRotate
	}

	taskID := followedTaskID(inst)
	inFlight := inst.Status == InstanceStatusRunning || inst.Status == InstanceStatusInterrupted

	switch {
	case inFlight && rotation.Deploy != nil && rotation.Deploy.State == boshTaskDone &&
		taskID > 0 && rotation.Deploy.BOSHTaskID == taskID:
		return stepVerify
	case inFlight && (taskID > 0 || followedContextID(inst) != ""):
		return stepInFlight
	case rotation.Phase == RotationPhaseDeploy:
		return stepMutation
	default:
		return stepRotate
	}
}

// reservationWaits reports whether the instance is on the restore waiting
// list for this task, so it may not run yet.
func (m *Manager) reservationWaits(task *UpgradeTask, inst *InstanceUpgrade) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	waiter, waiting := m.waiterLocked(inst.InstanceID)

	return waiting && waiter == task
}

// rotateInstanceRedeploy rotates the instance's selected certificates by
// redeploying it. It runs the preflight and the BeforeRegenerate re-check,
// unless the regeneration already started, regenerates the variables, and
// goes on to the deploy as redeployFromMutation describes. Each step that
// writes checks the switches first, and with rotation off the task pauses
// before it changes anything. It reports whether the instance settled.
func (m *Manager) rotateInstanceRedeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	if m.refuseWrites(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	if m.stopIfCancelled(ctx, task, inst, "The task was cancelled before the rotation changed anything.") {
		return true
	}

	m.markRotating(ctx, task, inst)

	if !m.regenerationStarted(inst) {
		if m.preflightStops(ctx, task, inst) || m.stopForChanges(ctx, task, inst, BeforeRegenerate) {
			return m.instanceSettled(inst)
		}

		if m.stopIfCancelled(ctx, task, inst, "The task was cancelled before the rotation changed anything.") {
			return true
		}
	}

	stop, err := m.regenerateVariables(ctx, task, inst)
	if err != nil {
		m.logger.Error("Certificate task %s stopped regenerating instance %s: %v", task.ID, inst.InstanceID, err)
	}

	if stop || err != nil {
		return m.instanceSettled(inst)
	}

	return m.redeployFromMutation(ctx, task, inst, cancelCh)
}

// redeployFromMutation takes an instance whose new versions are in CredHub
// to its deploy. It runs the BeforeMutation re-check, checks the switches,
// saves phase deploy for certain, and sends the director's own manifest
// with force_latest_variables, through the same send and follow an upgrade
// uses. A deploy whose context ID could not be saved for certain is not
// sent, and the task pauses. It reports whether the instance settled.
func (m *Manager) redeployFromMutation(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	if m.refuseWrites(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	m.markRotating(ctx, task, inst)

	if m.stopForChanges(ctx, task, inst, BeforeMutation) {
		return m.instanceSettled(inst)
	}

	if m.stopIfCancelled(ctx, task, inst, "The task was cancelled before the deploy. "+deployPendingNote) {
		return true
	}

	if m.refuseWrites(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	view := m.viewRotation(task, inst)

	m.setPhase(inst, RotationPhaseDeploy)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "start the deploy of "+view.deployment)

		return false
	}

	detail, err := directorCall(ctx, func(c context.Context) (*bosh.DeploymentDetail, error) {
		return m.director.GetDeployment(c, view.deployment)
	})

	switch {
	case ctx.Err() != nil:
		return false
	case err != nil:
		m.pauseWith(ctx, task, inst, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, //nolint:errcheck,gosec // pauseWith logs and saves it
			fmt.Sprintf("The director did not return deployment %s, so the deploy was not sent. Resume to try again: %v", view.deployment, err)))

		return false
	case detail == nil:
		m.pauseWith(ctx, task, inst, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, //nolint:errcheck,gosec // pauseWith logs and saves it
			fmt.Sprintf("The director returned no manifest for deployment %s, so the deploy was not sent. Resume to try again.", view.deployment)))

		return false
	case manifestDigest(detail.Manifest) != view.approved.ManifestDigest:
		m.awaitNewApproval(ctx, task, inst, []string{"The manifest changed since the preview."})

		return false
	}

	save := func() error { return m.persistCertConfirmed(ctx, task) }

	if !m.deployWithContext(ctx, task, inst, detail.Manifest, bosh.DeployOptions{ForceLatestVariables: true}, save, cancelCh) {
		m.pauseUnconfirmed(task, inst, "send the deploy of "+view.deployment)

		return false
	}

	return m.instanceSettled(inst)
}

// refuseWrites checks the switches before a step that writes, and pauses
// the task with the reason when they refuse. It reports whether it paused.
func (m *Manager) refuseWrites(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	err := m.certWritesAllowed(m.rotationMode(task))
	if err == nil {
		return false
	}

	m.pauseWith(ctx, task, inst, writesRefused(err)) //nolint:errcheck,gosec // pauseWith logs and saves it

	return true
}

// writesRefused gives a refusal from certWritesAllowed the message a
// paused task shows.
func writesRefused(err error) error {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) && apiErr.Code == apierr.CodeRotationDisabled {
		return apierr.New(apiErr.Status, apiErr.Code, rotationOffMessage)
	}

	return err
}

// isWritesRefused reports whether a preflight error is one of the
// refusals certWritesAllowed returns, which pause rather than fail.
func isWritesRefused(err error) bool {
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		return false
	}

	switch apiErr.Code {
	case apierr.CodeRotationDisabled, apierr.CodePatchDisabled, apierr.CodeCredHubNotConfigured:
		return true
	default:
		return false
	}
}

// markRotating marks the instance running and saves it, keeping the time
// it first started. An error left by a pause is cleared.
func (m *Manager) markRotating(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	m.mu.Lock()

	if inst.StartedAt == nil {
		now := m.certNowLocked()
		inst.StartedAt = &now
	}

	inst.Status = InstanceStatusRunning
	inst.Error = ""

	m.mu.Unlock()

	err := m.persistTask(context.WithoutCancel(ctx), task)
	if err != nil {
		m.logger.Error("Failed to save instance %s of certificate task %s as running: %v", inst.InstanceID, task.ID, err)
	}
}

// setPhase moves the instance's rotation to the phase given.
func (m *Manager) setPhase(inst *InstanceUpgrade, phase RotationPhase) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Certificates.Phase = phase
}

// regenerationStarted reports whether any variable of the instance has
// left not_sent, so the preflight already ran before a pause or restart.
func (m *Manager) regenerationStarted(inst *InstanceUpgrade) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if inst.Certificates == nil {
		return false
	}

	for _, variable := range inst.Certificates.Variables {
		if variable.RegenerateState != RegenerateNotSent || variable.NewVersionID != "" {
			return true
		}
	}

	return false
}

// instanceSettled reports whether the instance reached a final outcome.
func (m *Manager) instanceSettled(inst *InstanceUpgrade) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Status.settled() && inst.Status != InstanceStatusOutcomeUnknown
}

// preflightStops runs the preflight and reports whether it stopped the
// rotation. A refusal by the switches pauses the task, and any other
// failure fails the instance, which changed nothing yet.
func (m *Manager) preflightStops(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	m.setPhase(inst, RotationPhasePreflight)

	err := m.certPreflight(ctx, task, inst)

	switch {
	case err == nil:
		return false
	case ctx.Err() != nil:
		return true
	case isWritesRefused(err):
		m.pauseWith(ctx, task, inst, writesRefused(err)) //nolint:errcheck,gosec // pauseWith logs and saves it

		return true
	}

	message := err.Error()

	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		message = apiErr.Message
	}

	m.failRotation(ctx, task, inst, "The preflight failed, so nothing changed: "+message) //nolint:errcheck,gosec // failRotation logs and saves it

	return true
}

// stopForChanges re-checks the approved effects at the point given and
// reports whether the rotation stops. A read that fails pauses the task,
// and a change makes the instance wait for a new approval.
func (m *Manager) stopForChanges(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, at RecheckPoint) bool {
	changes, err := m.recheckEffects(ctx, task, inst, at)

	switch {
	case err != nil && ctx.Err() != nil:
		return true
	case err != nil:
		m.pauseWith(ctx, task, inst, err) //nolint:errcheck,gosec // pauseWith logs and saves it

		return true
	case len(changes) == 0:
		return false
	}

	m.awaitNewApproval(ctx, task, inst, changes)

	return true
}

// awaitNewApproval makes the instance wait for an operator to approve the
// changed effects, pauses the task, and saves it.
func (m *Manager) awaitNewApproval(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, changes []string) {
	m.mu.Lock()

	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Status = StatusAwaitingDecision
	inst.Error = "The approved effects changed, so the rotation waits for a new approval: " + strings.Join(changes, " ")
	inst.Certificates.Pending = &PendingDecision{
		Kind:    DecisionApproveChangedEffects,
		Changes: changes,
		Since:   m.certNowLocked(),
	}

	pauseForDecisionLocked(task)

	m.mu.Unlock()

	m.logger.Info("Instance %s of certificate task %s waits for a new approval: %s", inst.InstanceID, task.ID, strings.Join(changes, " "))

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}
}

// stopIfCancelled ends a cancelled task's instance before its next write,
// marks it cancelled with the message, and saves it. It reports whether
// the task was cancelled.
func (m *Manager) stopIfCancelled(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, message string) bool {
	m.mu.Lock()

	if task.Status != TaskStatusCancelled {
		m.mu.Unlock()

		return false
	}

	if !inst.Status.settled() {
		now := m.certNowLocked()
		inst.Status = InstanceStatusCancelled
		inst.CompletedAt = &now
		inst.Error = message
		task.CancelledCount++
	}

	m.releaseUnheldLocked(task)

	m.mu.Unlock()

	m.logger.Info("Instance %s of certificate task %s: %s", inst.InstanceID, task.ID, message)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}

	return true
}

// certAfterDeploy is the certificate finisher. It records how the deploy's
// BOSH task ended, as settleCertificateDeploy does. A redeploy whose task
// is done is then verified and settled, as verifyAndSettle describes,
// while the instance stays running and reserved.
func (m *Manager) certAfterDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task) {
	m.settleCertificateDeploy(task, inst, taskID, finished)

	m.mu.Lock()

	redeploy := finished != nil && m.configured && certTaskMode(task) == RotationModeRedeploy
	verify := redeploy && finished.State == boshTaskDone && inst.Status == InstanceStatusRunning

	// A redeploy whose BOSH task failed closes as one the director
	// rejected does, at phase done and scanned again.
	failed := redeploy && finished.State != boshTaskDone && inst.Status == InstanceStatusFailed

	rescan := func() {}
	if failed {
		rescan = m.endCertificateInstanceLocked(task, inst)
	}

	m.mu.Unlock()

	if failed {
		rescan()

		err := m.persistCertConfirmed(context.WithoutCancel(ctx), task)
		if err != nil {
			m.logger.Error("Certificate task %s: %v", task.ID, err)
		}

		return
	}

	if verify {
		m.verifyAndSettle(ctx, task, inst)
	}
}

// verifyAndSettle verifies a done redeploy and settles the instance. It
// waits while the instance's reservation waits for another holder, saves
// phase deploy for certain, verifies, saves the result for certain, and
// only then settles the instance, which frees it. A verification that
// could not read what it checks, or a save that was not confirmed, pauses
// a live task, and a resume verifies again. A cancelled task can't be
// resumed, so it retries with backoff instead, and when the attempts run
// out the instance waits for an operator to resolve the deploy's outcome.
// Stop leaves the instance running, and the next load verifies it.
func (m *Manager) verifyAndSettle(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	for m.reservationWaits(task, inst) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.restoreRetry):
		}
	}

	backoff := m.verifyInterval

	for attempt := 1; ; attempt++ {
		failure := m.verifyAttempt(ctx, task, inst)

		switch {
		case failure == nil || ctx.Err() != nil:
			return
		case !m.taskCancelled(task):
			failure.pause(ctx)

			return
		case attempt >= cancelledVerifyAttempts:
			m.awaitVerifyDecision(ctx, task, inst, attempt, failure.err)

			return
		}

		m.logger.Error("Certificate task %s is cancelled, so instance %s retries its verification in %s: %v", task.ID, inst.InstanceID, backoff, failure.err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(2*backoff, maxVerifyBackoff)
	}
}

// verifyFailure is an attempt to verify that could not finish, with the
// way a live task pauses for it.
type verifyFailure struct {
	err   error
	pause func(ctx context.Context)
}

// verifyAttempt makes one attempt to verify a done redeploy and settle the
// instance. It returns nil once the instance settles or ctx ends, and a
// failure when a read or a confirmed save did not succeed.
func (m *Manager) verifyAttempt(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) *verifyFailure {
	unconfirmed := func(step string, err error) *verifyFailure {
		return &verifyFailure{
			err:   fmt.Errorf("the save before Blacksmith could %s was not confirmed: %w", step, err),
			pause: func(context.Context) { m.pauseUnconfirmed(task, inst, step) },
		}
	}

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		return unconfirmed("verify the deploy of "+inst.DeploymentName, err)
	}

	err = m.verifyRedeploy(ctx, inst)

	switch {
	case ctx.Err() != nil:
		return nil
	case errors.Is(err, errVerifyUnreadable):
		return &verifyFailure{
			err: err,
			pause: func(ctx context.Context) {
				m.pauseWith(ctx, task, inst, fmt.Errorf("%w. The deploy is done, so resume to verify it again", err)) //nolint:errcheck,gosec // pauseWith logs and saves it
			},
		}
	}

	ok, message := err == nil, ""
	if err != nil {
		message = err.Error()
	}

	m.mu.Lock()
	inst.Error = message
	m.mu.Unlock()

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		return unconfirmed("finish instance "+inst.InstanceID+" after its verification", err)
	}

	m.settleCertificateInstance(task, inst, ok, message)

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}

	return nil
}

// taskCancelled reports whether the task was cancelled.
func (m *Manager) taskCancelled(task *UpgradeTask) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return task.Status == TaskStatusCancelled
}

// awaitVerifyDecision records that a cancelled task's done deploy could not
// be verified. The instance's outcome is unknown, it stays reserved, and
// it waits for an operator to resolve the deploy's outcome, as a deploy
// whose end Blacksmith can't learn does. No count changes.
func (m *Manager) awaitVerifyDecision(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, attempts int, cause error) {
	m.mu.Lock()

	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Status = InstanceStatusOutcomeUnknown
	inst.CompletedAt = nil
	inst.Error = fmt.Sprintf("BOSH task %d finished the deploy, but Blacksmith could not verify the new certificates in %d attempts: %v. "+
		"Check which certificates the deployment serves, then resolve the deploy's outcome.", followedTaskID(inst), attempts, cause)
	inst.Certificates.Pending = &PendingDecision{
		Kind:  DecisionResolveDeployOutcome,
		Since: m.certNowLocked(),
	}

	pauseForDecisionLocked(task)

	m.mu.Unlock()

	m.logger.Error("Instance %s of certificate task %s waits for a decision: %s", inst.InstanceID, task.ID, inst.Error)

	err := m.persistTask(context.WithoutCancel(ctx), task)
	if err != nil {
		m.logger.Error("Failed to save instance %s while it waits for a decision: %v", inst.InstanceID, err)
	}
}

// verifyIfCancelled verifies an instance of a cancelled task whose done
// deploy is on record, since the cancel ends the processor's wait and a
// cancelled task is never resumed.
func (m *Manager) verifyIfCancelled(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	if ctx.Err() != nil || !m.taskCancelled(task) || m.nextCertificateStep(inst) != stepVerify {
		return
	}

	m.verifyAndSettle(ctx, task, inst)
}

// settleCertificateInstance records the final outcome of a certificate
// instance. It marks it succeeded or failed with the message, counts it as
// settleInstance counts an upgrade instance, sets phase done, releases its
// reservation, and has the inventory scan it again in the background, so
// the inventory shows the new certificates. An instance that already
// settled keeps its outcome and its count.
func (m *Manager) settleCertificateInstance(task *UpgradeTask, inst *InstanceUpgrade, ok bool, message string) {
	m.mu.Lock()

	if !inst.Status.settled() || inst.Status == InstanceStatusOutcomeUnknown {
		now := m.certNowLocked()
		inst.CompletedAt = &now

		if ok {
			inst.Status = InstanceStatusSuccess
			inst.Error = ""
			task.CompletedCount++
		} else {
			inst.Status = InstanceStatusFailed
			inst.Error = message
			task.FailedCount++
		}
	}

	rescan := m.endCertificateInstanceLocked(task, inst)
	status := inst.Status

	m.mu.Unlock()

	if ok {
		m.logger.Info("Instance %s of certificate task %s rotated its certificates", inst.InstanceID, task.ID)
	} else {
		m.logger.Error("Instance %s of certificate task %s ended %s: %s", inst.InstanceID, task.ID, status, message)
	}

	rescan()
}

// endCertificateInstanceLocked closes a settled certificate instance. It
// sets phase done, drops any pending decision, releases the instance's
// reservation, and returns the function that starts the inventory's scan
// of it, which the caller runs once it lets go of mu. A manager that is
// stopping, or has no scan, gets a function that does nothing. The caller
// holds mu.
func (m *Manager) endCertificateInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade) func() {
	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Certificates.Phase = RotationPhaseDone
	inst.Certificates.Pending = nil

	m.releaseUnheldLocked(task)

	scan := m.deps.ScanInstance
	if scan == nil || m.ctx.Err() != nil {
		return func() {}
	}

	m.wg.Add(1)

	instanceID := inst.InstanceID

	return func() { go m.rescan(scan, instanceID) }
}

// rescan has the inventory scan one instance again, under its own bound.
func (m *Manager) rescan(scan func(ctx context.Context, instanceID string) (certinventory.InstanceScan, error), instanceID string) {
	defer m.wg.Done()

	ctx, cancel := context.WithTimeout(m.ctx, rescanTimeout)
	defer cancel()

	_, err := scan(ctx, instanceID)
	if err != nil {
		m.logger.Error("The inventory could not scan instance %s again after its rotation: %v", instanceID, err)
	}
}

// failCertificateDeploy fails an instance whose deploy the director did
// not take, with the note that the new versions wait in CredHub.
func (m *Manager) failCertificateDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, why string) {
	m.settleCertificateInstance(task, inst, false, why+" "+deployPendingNote)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}
}

// verifyRedeploy checks that the deploy applied the new certificates. The
// director must name each selected variable's new version, and each
// listener the manifest's rules give for a selected variable must serve
// the new certificate within verifyAttempts probes. A failure names the
// variable, or the VM and port. A read that fails wraps
// errVerifyUnreadable.
func (m *Manager) verifyRedeploy(ctx context.Context, inst *InstanceUpgrade) error {
	m.mu.RLock()

	deployment := inst.DeploymentName

	var selected []VariableRotation
	if inst.Certificates != nil {
		selected = cloneEach(inst.Certificates.Variables, VariableRotation.Clone)
	}

	m.mu.RUnlock()

	variables, err := directorCall(ctx, func(c context.Context) ([]bosh.Variable, error) {
		return m.director.ListVariables(c, deployment)
	})
	if err != nil {
		return fmt.Errorf("%w: the director did not list the variables of deployment %s: %w", errVerifyUnreadable, deployment, err)
	}

	deployed := make(map[string]string, len(variables))
	for _, variable := range variables {
		deployed[variable.Name] = variable.ID
	}

	for _, variable := range selected {
		short := shortName(variable.Name)

		switch {
		case variable.NewVersionID == "":
			return fmt.Errorf("%s has no new version to verify", short)
		case deployed[variable.Name] != variable.NewVersionID:
			return fmt.Errorf("%s is deployed at version %s, not the new version %s", short, deployed[variable.Name], variable.NewVersionID)
		}
	}

	return m.verifyListeners(ctx, deployment, selected)
}

// verifiedListener is one listener whose certificate the verification
// checks, with the fingerprint it must serve.
type verifiedListener struct {
	listener    certinventory.Listener
	variable    string
	fingerprint [sha256.Size]byte
}

// verifyListeners probes each listener of a selected variable on every VM
// of its instance group.
func (m *Manager) verifyListeners(ctx context.Context, deployment string, selected []VariableRotation) error {
	listeners, facts, err := m.listenersToVerify(ctx, deployment, selected)
	if err != nil || len(listeners) == 0 {
		return err
	}

	vms, err := directorCall(ctx, func(c context.Context) ([]bosh.VM, error) {
		return m.director.GetDeploymentVMs(c, deployment)
	})
	if err != nil {
		return fmt.Errorf("%w: the director did not list the VMs of deployment %s: %w", errVerifyUnreadable, deployment, err)
	}

	prober := m.certDeps().Prober

	for _, l := range listeners {
		group := groupVMs(vms, l.listener.Group)
		if len(group) < facts.Groups[l.listener.Group] {
			return fmt.Errorf("instance group %s has %d of its %d VMs, so not every listener of %s can be checked",
				l.listener.Group, len(group), facts.Groups[l.listener.Group], shortName(l.variable))
		}

		for _, vm := range group {
			err := m.verifyListener(ctx, prober, vm, l)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// listenersToVerify reads the manifest and the new certificates, and
// returns the listeners the rules give for the selected variables.
func (m *Manager) listenersToVerify(ctx context.Context, deployment string, selected []VariableRotation) ([]verifiedListener, *certinventory.ManifestFacts, error) {
	info, err := directorCall(ctx, m.director.GetInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the director did not report its name: %w", errVerifyUnreadable, err)
	}

	if info == nil || info.Name == "" {
		return nil, nil, fmt.Errorf("%w: the director reported no name", errVerifyUnreadable)
	}

	detail, err := directorCall(ctx, func(c context.Context) (*bosh.DeploymentDetail, error) {
		return m.director.GetDeployment(c, deployment)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the director did not return deployment %s: %w", errVerifyUnreadable, deployment, err)
	}

	if detail == nil {
		return nil, nil, fmt.Errorf("%w: the director returned no manifest for deployment %s", errVerifyUnreadable, deployment)
	}

	facts, err := certinventory.WalkManifest(detail.Manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: the manifest of deployment %s could not be read: %w", errVerifyUnreadable, deployment, err)
	}

	newVersions := make(map[string]string, len(selected))
	for _, variable := range selected {
		newVersions[variable.Name] = variable.NewVersionID
	}

	fingerprints := make(map[string][sha256.Size]byte, len(selected))

	var listeners []verifiedListener

	for _, l := range certinventory.ListenerRules(facts) {
		if l.Variable == "" {
			continue
		}

		name := certinventory.ResolveCredHubName(info.Name, deployment, l.Variable)

		versionID, ok := newVersions[name]
		if !ok {
			continue
		}

		fingerprint, ok := fingerprints[name]
		if !ok {
			fingerprint, err = m.newFingerprint(ctx, name, versionID)
			if err != nil {
				return nil, nil, err
			}

			fingerprints[name] = fingerprint
		}

		listeners = append(listeners, verifiedListener{listener: l, variable: name, fingerprint: fingerprint})
	}

	return listeners, facts, nil
}

// newFingerprint returns the SHA-256 of the new version's certificate.
func (m *Manager) newFingerprint(ctx context.Context, name, versionID string) ([sha256.Size]byte, error) {
	api := m.certDeps().CredHub
	if api == nil {
		return [sha256.Size]byte{}, fmt.Errorf("%w: CredHub is not configured", errVerifyUnreadable)
	}

	readCtx, cancel := context.WithTimeout(ctx, certinventory.CredHubCallTimeout)
	defer cancel()

	public, err := api.CertificateVersion(readCtx, versionID)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("%w: CredHub did not return version %s of %s: %w", errVerifyUnreadable, versionID, shortName(name), err)
	}

	cert := firstCertificate(public)
	if cert == nil {
		return [sha256.Size]byte{}, fmt.Errorf("CredHub holds no certificate in version %s of %s", versionID, shortName(name))
	}

	return sha256.Sum256(cert.Raw), nil
}

// groupVMs returns the VMs of the instance group, in index order.
func groupVMs(vms []bosh.VM, group string) []bosh.VM {
	var out []bosh.VM

	for _, vm := range vms {
		if vm.Job == group {
			out = append(out, vm)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })

	return out
}

// verifyListener probes one listener on one VM until it serves the new
// certificate, up to verifyAttempts times, verifyInterval apart.
func (m *Manager) verifyListener(ctx context.Context, prober certinventory.Prober, vm bosh.VM, l verifiedListener) error {
	where := vm.Job + "/" + strconv.Itoa(vm.Index) + " port " + strconv.Itoa(l.listener.Port)

	if len(vm.IPs) == 0 || vm.IPs[0] == "" {
		return fmt.Errorf("%s has no address, so the new certificate of %s can't be checked there", where, shortName(l.variable))
	}

	addr := net.JoinHostPort(vm.IPs[0], strconv.Itoa(l.listener.Port))

	var last error

	for attempt := 1; attempt <= verifyAttempts; attempt++ {
		leaf, _, err := prober.Leaf(ctx, addr, "")

		switch {
		case err != nil:
			last = err
		case sha256.Sum256(leaf.Raw) == l.fingerprint:
			return nil
		default:
			last = errors.New("it serves a different certificate")
		}

		if attempt == verifyAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", errVerifyUnreadable, ctx.Err())
		case <-time.After(m.verifyInterval):
		}
	}

	return fmt.Errorf("%s does not serve the new certificate of %s after %d probes: %w", where, shortName(l.variable), verifyAttempts, last)
}

// deployWithContext sends a deploy under a fresh context ID and follows
// its task. It records the context ID and calls save before the send. An
// upgrade's save logs a failure and lets the deploy go, while a
// certificate task's save must be confirmed, so when save fails the ID is
// cleared and nothing is sent. Zero options send a plain deploy. It
// reports whether the deploy was sent.
func (m *Manager) deployWithContext(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, manifest string, opts bosh.DeployOptions, save func() error, cancelCh <-chan struct{}) bool {
	persistCtx := context.WithoutCancel(ctx)
	certificates := task.KindOrDefault() == TaskKindCertificates

	contextID := uuid.NewString()

	m.mu.Lock()
	recordContextID(inst, contextID)
	m.mu.Unlock()

	err := save()
	if err != nil {
		m.mu.Lock()
		recordContextID(inst, "")
		m.mu.Unlock()

		return false
	}

	deployCtx := boshapi.WithContextID(ctx, contextID)

	var deployTask *bosh.Task
	if opts == (bosh.DeployOptions{}) {
		deployTask, err = m.director.UpdateDeployment(deployCtx, inst.DeploymentName, manifest)
	} else {
		deployTask, err = m.director.UpdateDeploymentWithOptions(deployCtx, inst.DeploymentName, manifest, opts)
	}

	if errors.Is(err, bosh.ErrOutcomeUnknown) {
		m.awaitLostAnswer(ctx, task, inst, contextID, err, cancelCh)

		return true
	}

	// Any error but a lost answer shows the director never received the
	// deploy, including a wait for a batch slot that Stop ended.
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, inst, err)

		return true
	}

	if err != nil {
		if certificates {
			m.failCertificateDeploy(persistCtx, task, inst, fmt.Sprintf("The director did not take the deploy: %v.", err))

			return true
		}

		m.mu.Lock()
		inst.Status = InstanceStatusFailed
		inst.Error = fmt.Sprintf("BOSH task failed: %v", err)
		task.FailedCount++
		m.releaseUnheldLocked(task)
		m.logger.Error("Update failed for %s: %v", inst.DeploymentName, err)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck

		return true
	}

	if deployTask == nil || deployTask.ID <= 0 {
		if certificates {
			m.failCertificateDeploy(persistCtx, task, inst, "The director named no task for the deploy.")

			return true
		}

		m.mu.Lock()
		inst.Status = InstanceStatusFailed
		inst.Error = "the director named no task for the deploy"
		task.FailedCount++
		m.releaseUnheldLocked(task)
		m.logger.Error("The director named no task for the deploy of %s", inst.DeploymentName)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck

		return true
	}

	m.followDeployTask(ctx, task, inst, deployTask.ID, cancelCh)

	return true
}

// neverStartedLocked reports whether a loaded certificate task changed
// nothing yet: it is confirmed and unfinished, every instance is pending,
// and no variable's regeneration was recorded. The caller holds mu.
func neverStartedLocked(task *UpgradeTask) bool {
	if task.KindOrDefault() != TaskKindCertificates || !task.CreationConfirmed || task.Paused {
		return false
	}

	if task.Status != TaskStatusPending && task.Status != TaskStatusRunning {
		return false
	}

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status != InstanceStatusPending {
			return false
		}

		if inst.Certificates == nil {
			continue
		}

		for _, variable := range inst.Certificates.Variables {
			if variable.RegenerateState != RegenerateNotSent || variable.NewVersionID != "" {
				return false
			}
		}
	}

	return true
}

// pauseNeverStartedLocked pauses a loaded certificate task that changed
// nothing yet, since its creation may have been reported as failed, so it
// never runs until an operator resumes it. The caller holds mu.
func (m *Manager) pauseNeverStartedLocked(task *UpgradeTask) {
	task.Status = TaskStatusPaused
	task.Paused = true

	for i := range task.Instances {
		task.Instances[i].Error = neverStartedMessage
	}

	m.logger.Info("Certificate task %s changed nothing before Blacksmith restarted, so it loads paused until an operator resumes it", task.ID)
}

// finishRecordedDeploy ends the following of a loaded certificate deploy
// whose end is already on record. It hands the recorded end to the
// finisher without asking the director again, so a director that has
// pruned the task, or a stop during the follow, can't turn a recorded done
// into an unknown outcome.
func (m *Manager) finishRecordedDeploy(deploy loadedDeploy) {
	m.mu.RLock()
	outcome := *deploy.instance.Certificates.Deploy
	m.mu.RUnlock()

	m.logger.Info("BOSH task %d of instance %s ended %s before Blacksmith stopped, so its outcome comes from the record",
		outcome.BOSHTaskID, deploy.instance.InstanceID, outcome.State)

	finished := &bosh.Task{ID: outcome.BOSHTaskID, State: outcome.State, Result: outcome.Result}

	m.finishFollowedDeploy(m.ctx, deploy.task, deploy.instance, deploy.taskID, finished)
	m.persistSettled(context.WithoutCancel(m.ctx), deploy.task)
}
