package upgrade

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
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

	// patchOffMessage is what a patch task shows when it pauses because
	// patch in place was turned off after it was created.
	patchOffMessage = "Patch in place is off on this broker, so this task paused before changing anything more on the VMs. " +
		"Redeploy this instance, which renders its jobs from BOSH state, or turn certificates.patch_in_place.enabled back on and resume."

	// deployPendingNote tells the operator what became of the new
	// versions when a redeploy did not apply them.
	deployPendingNote = "The new certificate versions remain in CredHub as deploy_pending, " +
		"and a converge or another redeploy applies them."

	// convergeUnappliedNote tells the operator what became of an instance
	// when a converge deploy did not apply the latest versions.
	convergeUnappliedNote = "The divergence record stays, and the instance stays unconverged " +
		"until another converge task or a deploy with the latest variables brings BOSH in line."

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

	// resolveVerifyTimeout bounds the verification a resolve request runs.
	resolveVerifyTimeout = 2 * time.Minute

	// cancelledVerifyAttempts is how many times a cancelled task tries to
	// verify a done deploy whose reads fail, waiting verifyInterval at
	// first and twice as long each time after, up to maxVerifyBackoff.
	cancelledVerifyAttempts = 5
	maxVerifyBackoff        = 2 * time.Minute

	// cancelledResumeWaitDefault is how long a cancelled patch task keeps
	// trying to finish a VM left part way through its swap, as resumes
	// verifyInterval apart at first and twice as long each time after, up
	// to maxVerifyBackoff, before it waits for a resume.
	cancelledResumeWaitDefault = 15 * time.Minute
)

// errVerifyUnreadable marks a verification that could not read what it
// checks, which pauses the task rather than failing the instance.
var errVerifyUnreadable = errors.New("the verification could not read what it checks")

// runsCertificatesLocked reports whether the manager runs this task as a
// certificate rotation. Only a configured manager does, and only a
// redeploy, a patch, or a converge. The caller holds mu.
func (m *Manager) runsCertificatesLocked(task *UpgradeTask) bool {
	mode := certTaskMode(task)

	return task.KindOrDefault() == TaskKindCertificates && m.configured &&
		(mode == RotationModeRedeploy || mode == RotationModePatch || mode == RotationModeConverge)
}

// convergesLocked reports whether the instance's next deploy is a
// converge: the task is a converge task, or the instance reached phase
// converge, as a patch with the immediate policy does once its last VM
// is done. The caller holds mu for reading.
func convergesLocked(task *UpgradeTask, inst *InstanceUpgrade) bool {
	if certTaskMode(task) == RotationModeConverge {
		return true
	}

	return inst.Certificates != nil && inst.Certificates.Phase == RotationPhaseConverge
}

// verifiesDeployLocked reports whether the manager checks the instance's
// done deploy before it settles the instance: a redeploy's deploy, whose
// new versions it verifies, and a converge deploy, which it reconciles.
// The caller holds mu for reading.
func (m *Manager) verifiesDeployLocked(task *UpgradeTask, inst *InstanceUpgrade) bool {
	return m.configured && (certTaskMode(task) == RotationModeRedeploy || convergesLocked(task, inst))
}

// rotateInstance runs the rotation of the task's mode on the instance,
// and reports whether the instance settled.
func (m *Manager) rotateInstance(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	m.mu.RLock()
	mode, converges := certTaskMode(task), convergesLocked(task, inst)
	m.mu.RUnlock()

	switch {
	case converges:
		return m.convergeInstance(ctx, task, inst, cancelCh)
	case mode == RotationModePatch:
		return m.rotateInstancePatch(ctx, task, inst, cancelCh)
	default:
		return m.rotateInstanceRedeploy(ctx, task, inst, cancelCh)
	}
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
			// An operator may have answered the decision since the step
			// was read, and then the task must not pause again.
			m.mu.Lock()

			waits := waitsForDecision(inst)
			if waits {
				pauseForDecisionLocked(task)
			}

			m.mu.Unlock()

			if !waits {
				continue
			}

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
			m.rotateInstance(ctx, task, inst, cancelCh)
		}

		if ctx.Err() != nil {
			m.logger.Info("Manager stopping during certificate task %s, so instance %s stays as it is", task.ID, inst.InstanceID)

			return false
		}

		m.mu.RLock()
		end := stepEndLocked(task, inst)
		failed := inst.Status == InstanceStatusFailed
		m.mu.RUnlock()

		switch end { //nolint:exhaustive // The default branch covers every other value.
		case endSettled:
			// The task goes on to its next instance, whose pause wait
			// holds it when this failure paused the task.
			if failed {
				m.pauseAfterFailure(persistCtx, task, inst)
			}

			return true
		case endAgain:
			continue
		default:
			return false
		}
	}
}

// stepEnd is what the processor does once a step of an instance returns.
type stepEnd int

const (
	// endStop stops the processor, and the task waits to be queued again.
	endStop stepEnd = iota
	// endAgain reads the instance's next step.
	endAgain
	// endSettled goes on to the task's next instance.
	endSettled
)

// stepEndLocked reads the task and the instance once a step returns. A
// settled instance ends the step. A paused or cancelled task, or a running
// instance with no pending decision, reads the next step again: the last
// case is an instance whose decision an operator answered, and whose task
// resumed, after the step stopped for it, which this processor still
// owns. The caller holds mu for reading.
func stepEndLocked(task *UpgradeTask, inst *InstanceUpgrade) stepEnd {
	switch {
	case inst.Status.settled() && !waitsForDecision(inst):
		return endSettled
	case task.Paused || task.Status == TaskStatusCancelled:
		return endAgain
	case inst.Status == InstanceStatusRunning && !waitsForDecision(inst):
		return endAgain
	default:
		return endStop
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

	return certificateStepLocked(inst)
}

// certificateStepLocked picks the instance's next step, as
// nextCertificateStep does. The caller holds mu for reading.
func certificateStepLocked(inst *InstanceUpgrade) certificateStep {
	rotation := inst.Certificates

	switch {
	case waitsForDecision(inst):
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

	if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
		return true
	}

	m.markRotating(ctx, task, inst)

	if !m.regenerationStarted(inst) {
		if m.preflightStops(ctx, task, inst) {
			return m.instanceSettled(inst)
		}

		// A task cancelled by now ends the instance rather than stopping it
		// for an approval.
		if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
			return true
		}

		if m.stopForChanges(ctx, task, inst, BeforeRegenerate) {
			return m.instanceSettled(inst)
		}

		if m.stopIfCancelled(ctx, task, inst, cancelledUnchanged) {
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

	// A task cancelled by now ends the instance rather than stopping it for
	// an approval.
	if m.stopIfCancelled(ctx, task, inst, cancelledUndeployed) {
		return true
	}

	if m.stopForChanges(ctx, task, inst, BeforeMutation) {
		return m.instanceSettled(inst)
	}

	if m.stopIfCancelled(ctx, task, inst, cancelledUndeployed) {
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
		m.awaitNewApproval(ctx, task, inst, []string{"The manifest changed since the preview."}, cancelledUndeployed)

		return false
	}

	save := func() error { return m.persistCertConfirmed(ctx, task) }

	if !m.deployWithContext(ctx, task, inst, detail.Manifest, bosh.DeployOptions{ForceLatestVariables: true}, save, cancelCh) {
		m.pauseUnconfirmed(task, inst, "send the deploy of "+view.deployment)

		return false
	}

	return m.instanceSettled(inst)
}

// convergeInstance brings BOSH in line with the versions CredHub holds by
// deploying the instance's unchanged manifest with the latest variables.
// It regenerates nothing. It checks the switches as a converge needs them,
// runs the converge subset of the preflight and the BeforeConverge
// re-check, saves phase converge for certain, and sends the director's
// own manifest with force_latest_variables, through the same send and
// follow an upgrade uses. The context ID and the BOSH task ID go on the
// rotation's converge record, each saved for certain, so a restart
// follows the deploy rather than sending it again. The instance stays
// reserved until the deploy's reconciliation settles it, as
// verifyAndSettle describes. It reports whether the instance settled.
func (m *Manager) convergeInstance(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	if m.refuseConverge(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	cancelled := m.cancelledBeforeConverge(task)

	if m.stopIfCancelled(ctx, task, inst, cancelled) {
		return true
	}

	m.markRotating(ctx, task, inst)

	err := m.convergePreflight(ctx, task, inst)
	if err != nil {
		m.convergePreflightFailed(ctx, task, inst, err)

		return m.instanceSettled(inst)
	}

	// A task cancelled by now ends the instance rather than stopping it
	// for an approval.
	if m.stopIfCancelled(ctx, task, inst, cancelled) {
		return true
	}

	if m.stopForChanges(ctx, task, inst, BeforeConverge) {
		return m.instanceSettled(inst)
	}

	if m.stopIfCancelled(ctx, task, inst, cancelled) {
		return true
	}

	if m.refuseConverge(ctx, task, inst) {
		return m.instanceSettled(inst)
	}

	return m.sendConverge(ctx, task, inst, cancelled, cancelCh)
}

// sendConverge saves phase converge for certain and sends the converge
// deploy of the director's own manifest, as convergeInstance describes.
// It reports whether the instance settled.
func (m *Manager) sendConverge(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, cancelled string, cancelCh <-chan struct{}) bool {
	view := m.viewRotation(task, inst)

	m.mu.Lock()

	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Certificates.Phase = RotationPhaseConverge
	if inst.Certificates.Converge == nil {
		inst.Certificates.Converge = &ConvergeDeploy{}
	}

	m.mu.Unlock()

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "start the converge deploy of "+view.deployment)

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
			fmt.Sprintf("The director did not return deployment %s, so the converge deploy was not sent. Resume to try again: %v", view.deployment, err)))

		return false
	case detail == nil:
		m.pauseWith(ctx, task, inst, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, //nolint:errcheck,gosec // pauseWith logs and saves it
			fmt.Sprintf("The director returned no manifest for deployment %s, so the converge deploy was not sent. Resume to try again.", view.deployment)))

		return false
	case manifestDigest(detail.Manifest) != view.approved.ManifestDigest:
		m.awaitNewApproval(ctx, task, inst, []string{"The manifest changed since the preview."}, cancelled)

		return false
	}

	save := func() error { return m.persistCertConfirmed(ctx, task) }

	if !m.deployWithContext(ctx, task, inst, detail.Manifest, bosh.DeployOptions{ForceLatestVariables: true}, save, cancelCh) {
		m.pauseUnconfirmed(task, inst, "send the converge deploy of "+view.deployment)

		return false
	}

	return m.instanceSettled(inst)
}

// refuseConverge checks the switches as a converge needs them, which is
// the rotation switch alone whatever the task's mode, and pauses the task
// with the reason when they refuse. It reports whether it paused.
func (m *Manager) refuseConverge(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	err := m.certWritesAllowed(RotationModeConverge)
	if err == nil {
		return false
	}

	m.pauseWith(ctx, task, inst, writesRefused(err)) //nolint:errcheck,gosec // pauseWith logs and saves it

	return true
}

// cancelledBeforeConverge is the message of an instance whose task was
// cancelled before its converge deploy was sent. A converge task changed
// nothing by then, while a patch task already patched the VMs.
func (m *Manager) cancelledBeforeConverge(task *UpgradeTask) string {
	if m.rotationMode(task) == RotationModeConverge {
		return cancelledUnchanged
	}

	return cancelledUnconverged
}

// convergePreflightFailed stops a converge after its preflight failed. A
// refusal by the switches pauses the task, as preflightFailed does, and
// any other failure fails the instance, since no converge deploy was
// sent.
func (m *Manager) convergePreflightFailed(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, err error) {
	if ctx.Err() != nil || isWritesRefused(err) {
		m.preflightFailed(ctx, task, inst, err)

		return
	}

	message := err.Error()

	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		message = apiErr.Message
	}

	if m.rotationMode(task) != RotationModeConverge {
		message += " " + patchedUnconverged
	}

	m.failRotation(ctx, task, inst, "The converge preflight failed, so no converge deploy was sent: "+message) //nolint:errcheck,gosec // failRotation logs and saves it
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
	if !errors.As(err, &apiErr) {
		return err
	}

	switch apiErr.Code {
	case apierr.CodeRotationDisabled:
		return apierr.New(apiErr.Status, apiErr.Code, rotationOffMessage)
	case apierr.CodePatchDisabled:
		return apierr.New(apiErr.Status, apiErr.Code, patchOffMessage)
	default:
		return err
	}
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

	return regenerationStartedLocked(inst)
}

// regenerationStartedLocked is regenerationStarted for a caller that
// holds mu.
func regenerationStartedLocked(inst *InstanceUpgrade) bool {
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

	return inst.Status.settled() && !waitsForDecision(inst)
}

// waitsForDecision reports whether a certificate instance waits for an
// operator's decision. An instance awaiting a decision does, and so does
// one of unknown outcome, unless an operator abandoned that outcome, which
// closed the instance with phase done and no pending decision. The caller
// holds mu.
func waitsForDecision(inst *InstanceUpgrade) bool {
	switch inst.Status { //nolint:exhaustive // The default branch covers every other value.
	case StatusAwaitingDecision:
		return true
	case InstanceStatusOutcomeUnknown:
		rotation := inst.Certificates

		return rotation == nil || rotation.Pending != nil || rotation.Phase != RotationPhaseDone
	default:
		return false
	}
}

// preflightStops runs the preflight and reports whether it stopped the
// rotation. A refusal by the switches pauses the task, and any other
// failure fails the instance, which changed nothing yet.
func (m *Manager) preflightStops(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	m.setPhase(inst, RotationPhasePreflight)

	err := m.certPreflight(ctx, task, inst)
	if err == nil {
		return false
	}

	return m.preflightFailed(ctx, task, inst, err)
}

// preflightFailed stops the rotation after a preflight error. A refusal
// by the switches pauses the task, and any other failure fails the
// instance, which changed nothing yet. It reports true, unless the
// manager is stopping, when it records nothing.
func (m *Manager) preflightFailed(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, err error) bool {
	switch {
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

	cancelled := cancelledUndeployed
	if at == BeforeRegenerate {
		cancelled = cancelledUnchanged
	}

	m.awaitNewApproval(ctx, task, inst, changes, cancelled)

	return true
}

// awaitNewApproval makes the instance wait for an operator to approve the
// changed effects, pauses the task, and saves it. A task cancelled while
// the effects were re-checked ends the instance as cancelled, with the
// message given, since it changes nothing more.
func (m *Manager) awaitNewApproval(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, changes []string, cancelled string) {
	m.mu.Lock()

	if task.Status == TaskStatusCancelled {
		m.cancelInstanceLocked(task, inst, cancelled)
		m.mu.Unlock()

		m.logger.Info("Instance %s of certificate task %s: %s", inst.InstanceID, task.ID, cancelled)

		err := m.persistCertConfirmed(ctx, task)
		if err != nil {
			m.logger.Error("Certificate task %s: %v", task.ID, err)
		}

		return
	}

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

	m.cancelInstanceLocked(task, inst, message)

	m.mu.Unlock()

	m.logger.Info("Instance %s of certificate task %s: %s", inst.InstanceID, task.ID, message)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}

	return true
}

// cancelInstanceLocked marks an unsettled instance of a cancelled task
// cancelled with the message, and frees it. The caller holds mu.
func (m *Manager) cancelInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade, message string) {
	if !inst.Status.settled() {
		now := m.certNowLocked()
		inst.Status = InstanceStatusCancelled
		inst.CompletedAt = &now
		inst.Error = message
		task.CancelledCount++
	}

	m.releaseUnheldLocked(task)
}

// certAfterDeploy is the certificate finisher. It records how the deploy's
// BOSH task ended, as settleCertificateDeploy does. A redeploy or converge
// whose task is done is then verified or reconciled and settled, as
// verifyAndSettle describes, while the instance stays running and
// reserved.
func (m *Manager) certAfterDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, taskID int, finished *bosh.Task) {
	m.settleCertificateDeploy(task, inst, taskID, finished)

	m.mu.Lock()

	verifies := finished != nil && m.verifiesDeployLocked(task, inst)
	verify := verifies && finished.State == boshTaskDone && inst.Status == InstanceStatusRunning

	// A redeploy or converge whose BOSH task failed closes as one the
	// director rejected does, at phase done and scanned again.
	failed := verifies && finished.State != boshTaskDone && inst.Status == InstanceStatusFailed

	rescan := func() {}
	if failed {
		rescan = m.endCertificateInstanceLocked(task, inst) //nolint:contextcheck // The follow-up scan runs on the manager's lifecycle context, so a finished request never cancels it.
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

// verifyAttempt makes one attempt to verify a done redeploy, or to
// reconcile a done converge, and settle the instance. It returns nil once the instance settles or ctx ends, and a
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

	err = m.verifyDeploy(ctx, task, inst)

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

	succeeded, message := err == nil, ""
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

	m.settleCertificateInstance(task, inst, succeeded, message) //nolint:contextcheck // The follow-up scan runs on the manager's lifecycle context, so a finished request never cancels it.

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

// pauseAfterFailure pauses a running certificate task once one of its
// instances failed, when pause_on_failure is on and an instance is still
// pending, so the operator decides whether the remaining instances go on.
// A resume goes on with them and never retries the failed one, which is
// settled. It saves the pause.
func (m *Manager) pauseAfterFailure(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	if !m.pausesOnFailure() {
		return
	}

	m.mu.Lock()

	pause := task.Status == TaskStatusRunning && !task.Paused && anyPendingLocked(task)
	if pause {
		task.Paused = true
		task.Status = TaskStatusPaused
	}

	m.mu.Unlock()

	if !pause {
		return
	}

	m.logger.Info("Instance %s of certificate task %s failed, so the task pauses before its remaining instances", inst.InstanceID, task.ID)

	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to save certificate task %s paused after a failure: %v", task.ID, err)
	}
}

// certResumeAllowed refuses the resume of a paused certificate task while
// the switches refuse its mode, with the reason a paused task shows. A
// patch task with a VM part way through its swap always resumes, since
// only the resume finishes or rolls back that VM, and the switches then
// stop the task before its next VM. Any other task, and a task that is
// not paused, is left to ResumeTask.
func (m *Manager) certResumeAllowed(taskID string) error {
	m.mu.RLock()
	task, exists := m.tasks[taskID]
	check := exists && task.KindOrDefault() == TaskKindCertificates && (task.Status == TaskStatusPaused || waitsCancelledLocked(task))

	var mode RotationMode
	if check {
		mode = certTaskMode(task)
		check = mode != RotationModePatch || !taskSwapOpenLocked(task)
	}

	m.mu.RUnlock()

	if !check {
		return nil
	}

	err := m.certWritesAllowed(mode)
	if err != nil {
		m.logger.Info("Certificate task %s stays paused, because its writes are refused: %v", taskID, err)

		return writesRefused(err)
	}

	return nil
}

// instanceFailed reports whether the instance failed.
func (m *Manager) instanceFailed(inst *InstanceUpgrade) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Status == InstanceStatusFailed
}

// Messages of an instance that a cancel ends before its deploy was sent.
const (
	cancelledUnchanged  = "The task was cancelled before the rotation changed anything."
	cancelledUndeployed = "The task was cancelled before the deploy. " + deployPendingNote
	// cancelledUnconverged ends a patched instance whose task was
	// cancelled before its converge deploy.
	cancelledUnconverged = "The task was cancelled before the converge deploy. " + patchedUnconverged
	// patchedUnconverged says what a patched instance holds when no
	// converge deploy followed its patch.
	patchedUnconverged = "The patched VMs keep the new certificates, and BOSH still records the old versions, " +
		"so the instance stays unconverged until a converge task or a deploy with the latest variables brings BOSH in line."
)

// cancelIdleLocked ends each running instance of a cancelled redeploy or
// patch task that nothing processes or settles and that has nothing in
// flight: no deploy task or context ID on record, and no regeneration
// whose outcome is unknown. Such an instance would otherwise stay
// reserved with nothing to follow it. It is marked cancelled, saying that
// its new versions are deploy_pending when its regeneration started, and
// released. A patch instance ends as patchCancelEnd says, and one whose
// VM may be part way through a swap is left for the resume that checks
// its files. An instance with a deploy or a regeneration in flight is
// left to whatever follows it, or to the next load, which makes it wait
// for a decision. A manager that is stopping ends nothing, since the next
// load does it. It returns how many instances it ended. The caller holds
// mu.
func (m *Manager) cancelIdleLocked(task *UpgradeTask) int {
	mode := certTaskMode(task)
	if task.Status != TaskStatusCancelled || task.KindOrDefault() != TaskKindCertificates ||
		(mode != RotationModeRedeploy && mode != RotationModePatch && mode != RotationModeConverge) ||
		m.owned[task.ID] || m.settling[task.ID] > 0 || m.ctx.Err() != nil {
		return 0
	}

	ended := 0

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status != InstanceStatusRunning || followedTaskID(inst) > 0 || followedContextID(inst) != "" || regenerationUncertainLocked(inst) {
			continue
		}

		status, message, end := idleCancelEndLocked(task, inst, mode)
		if !end {
			continue
		}

		now := m.certNowLocked()
		inst.Status = status
		inst.CompletedAt = &now
		inst.Error = message

		if status == InstanceStatusFailed {
			task.FailedCount++
		} else {
			task.CancelledCount++
		}

		ended++
	}

	if ended > 0 {
		m.releaseUnheldLocked(task)
	}

	return ended
}

// idleCancelEndLocked is the status and message an idle instance of a
// cancelled task ends with, and whether it ends at all. A patch instance
// that was only waiting for its converge deploy ends as the live path
// ends it when the cancel comes before that deploy. The caller holds mu.
func idleCancelEndLocked(task *UpgradeTask, inst *InstanceUpgrade, mode RotationMode) (InstanceStatus, string, bool) {
	message := cancelledUnchanged
	if regenerationStartedLocked(inst) {
		message = cancelledUndeployed
	}

	if mode != RotationModePatch {
		return InstanceStatusCancelled, message, true
	}

	status, message, end := patchCancelEnd(inst, message)
	if end && status == InstanceStatusCancelled && awaitsConvergeLocked(task, inst) {
		message = cancelledUnconverged
	}

	return status, message, end
}

// awaitsConvergeLocked reports whether a patch instance waits only for
// its converge deploy: every VM is done, and the instance reached phase
// converge or its task converges as soon as the VMs are patched. The
// caller holds mu for reading.
func awaitsConvergeLocked(task *UpgradeTask, inst *InstanceUpgrade) bool {
	rotation := inst.Certificates
	if rotation == nil || len(rotation.VMs) == 0 {
		return false
	}

	for _, j := range rotation.VMs {
		if j.Phase != VMPhaseDone {
			return false
		}
	}

	return rotation.Phase == RotationPhaseConverge || taskConverge(task) == ConvergeImmediate
}

// skipUnownedLocked skips the pending instances of a cancelled
// certificate task that nothing processes or settles, as its processor or
// the end of its settles would, since neither will come to it. It returns
// how many it skipped. The caller holds mu.
func (m *Manager) skipUnownedLocked(task *UpgradeTask) int {
	if task.Status != TaskStatusCancelled || task.KindOrDefault() != TaskKindCertificates ||
		m.owned[task.ID] || m.settling[task.ID] > 0 {
		return 0
	}

	return m.skipPendingLocked(task)
}

// regenerationUncertainLocked reports whether a regeneration of the
// instance may have reached CredHub without its answer being recorded.
// The caller holds mu.
func regenerationUncertainLocked(inst *InstanceUpgrade) bool {
	if inst.Certificates == nil {
		return false
	}

	return slices.ContainsFunc(inst.Certificates.Variables, func(v VariableRotation) bool {
		return v.RegenerateState == RegenerateSending || v.RegenerateState == RegenerateOutcomeUnknown
	})
}

// verifyCancelledLocked makes the settle of each instance of a cancelled
// certificate task whose done deploy is on record, when nothing processes
// or settles the task, as when the loader's verification paused it. A
// cancelled task is never resumed, so without it such an instance would
// stay reserved, with nothing to verify it. Each instance counts as a
// settle of the task, so nothing else ends it meanwhile, and the function
// it returns starts the verifications, which the caller runs once it lets
// go of mu. Each one verifies as a cancelled task's processor does: it
// retries reads that fail and, when they keep failing, makes the instance
// wait for an operator to resolve the deploy's outcome. A manager that is
// stopping starts none, since the next load verifies them. The caller
// holds mu.
func (m *Manager) verifyCancelledLocked(task *UpgradeTask) func() {
	if task.Status != TaskStatusCancelled || task.KindOrDefault() != TaskKindCertificates ||
		m.owned[task.ID] || m.settling[task.ID] > 0 || m.ctx.Err() != nil {
		return func() {}
	}

	var verify []*InstanceUpgrade

	for i := range task.Instances {
		if inst := &task.Instances[i]; certificateStepLocked(inst) == stepVerify {
			verify = append(verify, inst)
		}
	}

	if len(verify) == 0 {
		return func() {}
	}

	m.settling[task.ID] += len(verify)
	m.wg.Add(len(verify))

	return func() {
		for _, inst := range verify {
			go m.verifyCancelled(task, inst)
		}
	}
}

// settleCancelledLocked starts what follows the instances of a cancelled
// certificate task that nothing processes or settles: the verification of
// each done deploy, as verifyCancelledLocked does, and the resume of each
// patch left part way through a VM, as resumeCancelledLocked does. The
// function it returns starts them, and the caller runs it once it lets go
// of mu. The caller holds mu.
func (m *Manager) settleCancelledLocked(task *UpgradeTask) func() {
	verify := m.verifyCancelledLocked(task)
	finish := m.resumeCancelledLocked(task)

	return func() {
		verify()
		finish()
	}
}

// verifyCancelled verifies one instance that verifyCancelledLocked counted
// as a settle of its cancelled task, and ends that settle.
func (m *Manager) verifyCancelled(task *UpgradeTask, inst *InstanceUpgrade) {
	defer m.wg.Done()
	defer m.endSettling(loadedDeploy{task: task, instance: inst})

	m.logger.Info("Certificate task %s is cancelled, so instance %s verifies its done deploy now", task.ID, inst.InstanceID)

	m.verifyAndSettle(m.ctx, task, inst)
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
	rescan, status := m.settleCertificateInstanceLocked(task, inst, ok, message)
	m.mu.Unlock()

	if ok {
		m.logger.Info("Instance %s of certificate task %s rotated its certificates", inst.InstanceID, task.ID)
	} else {
		m.logger.Error("Instance %s of certificate task %s ended %s: %s", inst.InstanceID, task.ID, status, message)
	}

	rescan()
}

// settleCertificateInstanceLocked records the outcome as
// settleCertificateInstance describes, and returns the function that
// starts the inventory's scan, which the caller runs once it lets go of
// mu, and the instance's status. The caller holds mu.
func (m *Manager) settleCertificateInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade, ok bool, message string) (func(), InstanceStatus) {
	settleOutcomeLocked(task, inst, ok, message, m.certNowLocked())

	return m.endCertificateInstanceLocked(task, inst), inst.Status
}

// settleOutcomeLocked records the outcome on the instance and counts it, as
// settleCertificateInstance describes, with the time given, and closes the
// instance's rotation. It changes only the task and the instance, so it
// can be applied to a copy of the task before the live task. The caller
// holds mu.
func settleOutcomeLocked(task *UpgradeTask, inst *InstanceUpgrade, ok bool, message string, now time.Time) {
	if !inst.Status.settled() || waitsForDecision(inst) {
		inst.CompletedAt = &now

		if ok {
			inst.Status = InstanceStatusSuccess
			inst.Error = message
			task.CompletedCount++
		} else {
			inst.Status = InstanceStatusFailed
			inst.Error = message
			task.FailedCount++
		}
	}

	closeRotationLocked(inst)
}

// endCertificateInstanceLocked closes a settled certificate instance. It
// sets phase done, drops any pending decision, releases the instance's
// reservation, and returns the function that starts the inventory's scan
// of it, which the caller runs once it lets go of mu. A manager that is
// stopping, or has no scan, gets a function that does nothing. The caller
// holds mu.
func (m *Manager) endCertificateInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade) func() {
	closeRotationLocked(inst)
	m.releaseUnheldLocked(task)

	scan := m.deps.ScanInstance
	if scan == nil || m.ctx.Err() != nil {
		return func() {}
	}

	m.wg.Add(1)

	instanceID := inst.InstanceID

	return func() { go m.rescan(scan, instanceID) }
}

// closeRotationLocked sets a settled instance's rotation to phase done and
// drops any pending decision. The caller holds mu.
func closeRotationLocked(inst *InstanceUpgrade) {
	if inst.Certificates == nil {
		inst.Certificates = &InstanceRotation{}
	}

	inst.Certificates.Phase = RotationPhaseDone
	inst.Certificates.Pending = nil
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

// unappliedNoteLocked is the note an instance ends with when its deploy
// did not apply the new versions: a redeploy's versions wait in CredHub
// as deploy_pending, while a converge leaves the instance unconverged.
// The caller holds mu for reading.
func unappliedNoteLocked(task *UpgradeTask, inst *InstanceUpgrade) string {
	if convergesLocked(task, inst) {
		return convergeUnappliedNote
	}

	return deployPendingNote
}

// failCertificateDeploy fails an instance whose deploy the director did
// not take, with the note unappliedNoteLocked gives.
func (m *Manager) failCertificateDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, why string) {
	m.mu.RLock()
	note := unappliedNoteLocked(task, inst)
	m.mu.RUnlock()

	m.settleCertificateInstance(task, inst, false, why+" "+note) //nolint:contextcheck // The follow-up scan runs on the manager's lifecycle context, so a finished request never cancels it.

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}
}

// verifyDeploy checks the instance's done deploy before the instance
// settles. A converge deploy is reconciled, as reconcileConverge
// describes, and any other deploy is a redeploy, whose new versions
// verifyRedeploy checks.
func (m *Manager) verifyDeploy(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) error {
	m.mu.RLock()
	converges := convergesLocked(task, inst)
	m.mu.RUnlock()

	if converges {
		return m.reconcileConverge(ctx, inst)
	}

	return m.verifyRedeploy(ctx, inst)
}

// reconcileConverge confirms that the instance's divergence record
// cleared after its converge deploy. Up to verifyAttempts times,
// verifyInterval apart, it scans the instance, reconciles the record with
// the scan, and reads it. A record that a confirmed reconcile removed is
// cleared. One that stays, because not every listener was seen serving the
// deployed version, and a reconcile that was skipped or whose write
// failed, are not, and when the attempts run out the converge fails with
// a plain error: the deploy is done, the record stays, and the next scan
// reconciles it again. A broker with no divergence store has no record. A
// scan or read that fails could not read what it checks, so the instance
// stays reserved and is reconciled again.
func (m *Manager) reconcileConverge(ctx context.Context, inst *InstanceUpgrade) error {
	deps := m.certDeps()
	if deps.Divergence == nil {
		return nil
	}

	m.mu.RLock()
	instanceID := inst.InstanceID

	var taskID int
	if inst.Certificates != nil && inst.Certificates.Converge != nil {
		taskID = inst.Certificates.Converge.BOSHTaskID
	}

	m.mu.RUnlock()

	if deps.ScanInstance == nil {
		return fmt.Errorf("%w: Blacksmith has no scanner to reconcile instance %s", errVerifyUnreadable, instanceID)
	}

	sources := certinventory.ReconcileSources{Versions: deps.CredHub, Log: m.logger}
	if lister, ok := m.director.(bosh.DeploymentTaskLister); ok {
		sources.Tasks = lister
	}

	var reason string

	for attempt := 1; attempt <= verifyAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("the reconcile of instance %s stopped: %w", instanceID, ctx.Err())
			case <-time.After(m.verifyInterval):
			}
		}

		cleared, why, err := m.convergeCleared(ctx, deps, instanceID, sources)
		if err != nil || cleared {
			return err
		}

		reason = why
	}

	return fmt.Errorf("BOSH task %d finished the converge deploy, but Blacksmith could not confirm that the divergence record of instance %s cleared: %s. "+
		"The record stays, and the next scan reconciles it again", taskID, instanceID, reason)
}

// convergeCleared makes one attempt to confirm that the instance's
// divergence record cleared. It returns why the record is not known to
// have cleared, or an error wrapping errVerifyUnreadable when the scan or
// the read failed.
func (m *Manager) convergeCleared(ctx context.Context, deps Deps, instanceID string, sources certinventory.ReconcileSources) (bool, string, error) {
	scan, err := deps.ScanInstance(ctx, instanceID)

	switch {
	case err != nil:
		return false, "", fmt.Errorf("%w: instance %s could not be scanned after its converge deploy: %w", errVerifyUnreadable, instanceID, err)
	case scan.Err != "":
		return false, "", fmt.Errorf("%w: instance %s could not be scanned after its converge deploy: %s", errVerifyUnreadable, instanceID, scan.Err)
	}

	err = deps.Divergence.ReconcileScanConfirmed(ctx, scan, sources)
	if err != nil {
		//nolint:nilerr // A reconcile that did not land leaves the record unconfirmed, which is a reason, not a failed read.
		return false, "its reconcile did not land (" + err.Error() + ")", nil
	}

	_, found, err := deps.Divergence.Get(ctx, instanceID)

	switch {
	case err != nil:
		return false, "", fmt.Errorf("%w: the divergence record of instance %s could not be read: %w", errVerifyUnreadable, instanceID, err)
	case found:
		return false, "not every listener has been seen serving the deployed version", nil
	}

	return true, "", nil
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

		if perr := m.persistTask(persistCtx, task); perr != nil {
			m.logger.Error("Failed to save the failed deploy of instance %s in task %s: %v", inst.InstanceID, task.ID, perr)
		}

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

		if perr := m.persistTask(persistCtx, task); perr != nil {
			m.logger.Error("Failed to save the failed deploy of instance %s in task %s: %v", inst.InstanceID, task.ID, perr)
		}

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

// clearNeverStartedLocked clears the never-started message from the
// instances still pending once an operator resumes the task, since the
// resume answers it. The caller holds mu.
func clearNeverStartedLocked(task *UpgradeTask) {
	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status == InstanceStatusPending && inst.Error == neverStartedMessage {
			inst.Error = ""
		}
	}
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
