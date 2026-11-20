package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/settings"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// The outcomes the change record of an update gains from its rollback.
const (
	updateOutcomeRolledBack     = "rolled_back"
	updateOutcomeRollbackFailed = "rollback_failed"
)

// manifestField is the field of a stored manifest record that holds the
// manifest.
const manifestField = "manifest"

// rollbackManifestSuffix names the record, under the instance, that holds
// the manifest the director ran before the update, saved for a rollback.
const rollbackManifestSuffix = "/update_rollback_manifest"

// errRollbackManifestDigest reports a saved rollback manifest whose digest
// does not match its content, which the broker never deploys.
var errRollbackManifestDigest = errors.New("the saved manifest does not match its digest")

// errRollbackManifestMissing reports that no manifest was saved for the
// update's rollback.
var errRollbackManifestMissing = errors.New("no manifest was saved for the rollback")

// manifestDigest returns the SHA-256 digest of a manifest in hex.
func manifestDigest(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))

	return hex.EncodeToString(sum[:])
}

// saveRollbackManifest saves the manifest the director ran before the
// update, so a rollback after a restart deploys exactly that manifest.
func (p *updatePhase) saveRollbackManifest(ctx context.Context, live string) error {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := p.broker.Vault.Put(recordCtx, p.instanceID+rollbackManifestSuffix, map[string]interface{}{
		manifestField: live,
		"digest":      manifestDigest(live),
		"saved_at":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("failed to save the last good manifest of instance %s: %w", p.instanceID, err)
	}

	return nil
}

// savedRollbackManifest reads the manifest saved for the rollback and
// checks it against its digest.
func (p *updatePhase) savedRollbackManifest(ctx context.Context) (string, string, error) {
	return p.broker.savedRollbackManifest(ctx, p.instanceID)
}

// savedRollbackManifest reads the manifest saved for the rollback of the
// update of instanceID and checks it against its digest. It returns an
// error that wraps errRollbackManifestMissing when none was saved, and one
// that wraps errRollbackManifestDigest when it doesn't match its digest.
func (b *Broker) savedRollbackManifest(ctx context.Context, instanceID string) (string, string, error) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	var saved struct {
		Manifest string `json:"manifest"`
		Digest   string `json:"digest"`
	}

	found, err := b.Vault.Get(recordCtx, instanceID+rollbackManifestSuffix, &saved)

	switch {
	case err != nil:
		return "", "", fmt.Errorf("failed to read the saved manifest of instance %s: %w", instanceID, err)
	case !found || saved.Manifest == "":
		return "", "", fmt.Errorf("%w: instance %s", errRollbackManifestMissing, instanceID)
	case manifestDigest(saved.Manifest) != saved.Digest:
		return "", "", fmt.Errorf("%w: instance %s", errRollbackManifestDigest, instanceID)
	}

	return saved.Manifest, saved.Digest, nil
}

// dropRollbackManifest deletes the manifest saved for the rollback once
// the update has settled.
func (p *updatePhase) dropRollbackManifest(ctx context.Context) {
	recordCtx, cancel := recordContext(context.WithoutCancel(ctx))
	defer cancel()

	err := p.broker.Vault.Delete(recordCtx, p.instanceID+rollbackManifestSuffix)
	if err != nil {
		p.logger.Warning("Instance %s: the manifest saved for its update's rollback could not be deleted: %s", p.instanceID, err)
	}
}

// rollbackAtFailure reads the update_rollback setting when the update's
// deploy task with failedContextID failed, and returns the rollback record
// that names it. A layer whose value was refused is logged.
func (b *Broker) rollbackAtFailure(ctx context.Context, failedTaskID int, failedContextID string, logger logger.Logger) internalVault.UpdateRollback {
	var configured *bool
	if b.Config != nil {
		configured = b.Config.Broker.UpdateRollback
	}

	var store settings.Store
	if b.Vault != nil {
		store = b.Vault
	}

	readCtx, cancel := recordContext(ctx)
	defer cancel()

	choice := settings.UpdateRollback(readCtx, store, configured)

	for _, refusal := range choice.Refused {
		logger.Warning("Ignoring the update_rollback setting from %s: %v; the rollback is %t from %s", refusal.Source, refusal.Err, choice.On, choice.Source)
	}

	return internalVault.UpdateRollback{
		Setting: choice.On, SettingSource: choice.Source,
		FailedTaskID: failedTaskID, FailedContextID: failedContextID,
	}
}

// rollingBackDescription is what the poll shows while the rollback runs.
func rollingBackDescription(rollback internalVault.UpdateRollback) string {
	description := fmt.Sprintf("The update failed in BOSH task %d, and the broker is redeploying the previous manifest", rollback.FailedTaskID)
	if rollback.TaskID > 0 {
		description += fmt.Sprintf(" (task %d)", rollback.TaskID)
	}

	return description
}

// deployFailed handles the update's deploy task that ended in failure. It
// reads the rollback setting now, so the value in effect at the failure
// decides, and either records the failure or rolls the deployment back
// once.
func (p *updatePhase) deployFailed(ctx context.Context, task *bosh.Task) {
	p.logger.Error("Instance %s: the update's BOSH task %d ended in state %s", p.instanceID, task.ID, task.State)

	failure := fmt.Sprintf("BOSH deployment task %d is %s", task.ID, task.State)
	rollback := p.broker.rollbackAtFailure(context.WithoutCancel(ctx), task.ID, p.contextID, p.logger)

	if !rollback.Setting {
		p.settleFailure(ctx, recordedStateFailed, failure, updateOutcomeFailed, rollback)

		return
	}

	saved, digest, err := p.savedRollbackManifest(context.WithoutCancel(ctx))
	if err != nil {
		p.logger.Error("Instance %s: its update failed, and the previous manifest cannot be redeployed: %s", p.instanceID, err)
		p.settleFailure(ctx, recordedStateFailed, failure+", and the previous manifest could not be read, so it was not redeployed", updateOutcomeFailed, rollback)

		return
	}

	rollback.ContextID = uuid.NewString()
	rollback.ManifestDigest = digest
	rollback.Phase = internalVault.UpdatePhaseRollingBack

	if !p.recordRollingBack(ctx, failure, rollback) {
		return
	}

	p.rollBack(ctx, saved)
}

// recordRollingBack records the rollback before its deploy is sent, so a
// restart re-attaches to it by its context ID and never starts another.
// The write is tried until it lands, under the settle lock. When the
// record refuses it, the record is read again. A record that already
// names the rollback's context ID took an earlier attempt, and the
// rollback goes ahead. A record that holds another action or context ID,
// or has settled the update, has moved on, so nothing rolls back, and the
// failed deploy still gets its change record and the saved manifest is
// deleted. A record that still holds the failed deploy unsettled is tried
// again, after the failed task is recorded when it names another. It
// reports whether the rollback may go ahead, and false when the broker
// shuts down first, and re-adoption then runs the one rollback at the
// next start.
func (p *updatePhase) recordRollingBack(ctx context.Context, failure string, rollback internalVault.UpdateRollback) bool {
	settleCtx := context.WithoutCancel(ctx)

	unlock, err := p.broker.lockSettle(settleCtx, p.instanceID)
	if err != nil {
		p.logger.Error("Instance %s: the update's settle lock could not be taken, so re-adoption settles it: %s", p.instanceID, err)

		return false
	}
	defer unlock()

	wait := p.broker.updateRetryWait()

	for {
		recordCtx, cancel := recordContext(settleCtx)
		err = p.broker.Vault.TrackRollingBack(recordCtx, p.instanceID, rollingBackDescription(rollback), rollback)

		cancel()

		landed := err == nil || errors.Is(err, internalVault.ErrHistoryNotAppended)
		movedOn := false

		if errors.Is(err, internalVault.ErrUpdateNotInFlight) {
			landed, movedOn = p.rollingBackRefused(settleCtx, rollback, err)
		} else if !landed {
			p.logger.Error("Instance %s: failed to record the rollback of its update, and tries again in %v: %s", p.instanceID, wait, err)
		}

		switch {
		case landed:
			p.rollback = &rollback
			p.contextID = rollback.ContextID
			p.logger.Info("Instance %s: its update failed in task %d, so it redeploys the previous manifest with context ID %s (%s)", p.instanceID, rollback.FailedTaskID, rollback.ContextID, failure)

			return true
		case movedOn:
			rollback.Phase, rollback.ContextID, rollback.ManifestDigest = "", "", ""

			auditCtx, cancelAudit := recordContext(settleCtx)
			p.broker.auditUpdateFailure(auditCtx, p.instanceID, p.target, p.request, updateOutcomeFailed, rollback)
			cancelAudit()
			p.dropRollbackManifest(settleCtx)

			return false
		}

		if !p.retryAfter(&wait) {
			p.logger.Warning("Instance %s: the broker is shutting down before the rollback of its update was recorded, so re-adoption runs it at the next start", p.instanceID)

			return false
		}
	}
}

// rollingBackRefused reads the record again after it refused the rollback
// write with refusal, and reports whether an earlier attempt landed, and
// whether the record has moved on. When neither holds, the write is
// tried again, after the failed task is recorded when the record names
// another.
func (p *updatePhase) rollingBackRefused(ctx context.Context, rollback internalVault.UpdateRollback, refusal error) (bool, bool) {
	readCtx, cancel := recordContext(ctx)
	current, err := p.broker.readRecordedOperation(readCtx, p.instanceID)

	cancel()

	switch {
	case err != nil:
		p.logger.Error("Instance %s: the record refused the rollback of its update, and could not be read again, so the write is tried again: %v", p.instanceID, errors.Join(refusal, err))

		return false, false
	case current.Action == operationTypeUpdate && current.ContextID == rollback.ContextID:
		return true, false
	case current.Action != operationTypeUpdate || current.ContextID != rollback.FailedContextID || updateSettledState(current.State):
		p.logger.Error("Instance %s: its record now holds the %s %s with context ID %q, not its failed update, so nothing rolls back: %s", p.instanceID, current.Action, current.State, current.ContextID, refusal)

		return false, true
	}

	p.logger.Error("Instance %s: its record holds the failed update with task %d, and refused the rollback, so the write is tried again: %s", p.instanceID, current.TaskID, refusal)

	if current.TaskID != rollback.FailedTaskID {
		recordCtx, cancelRecord := recordContext(ctx)
		err = p.broker.Vault.TrackUpdateTask(recordCtx, p.instanceID, fmt.Sprintf("BOSH deployment in progress (task %d)", rollback.FailedTaskID), rollback.FailedContextID, rollback.FailedTaskID)

		cancelRecord()

		if err != nil && !errors.Is(err, internalVault.ErrHistoryNotAppended) {
			p.logger.Error("Instance %s: failed to record the failed task %d before the rollback is tried again: %s", p.instanceID, rollback.FailedTaskID, err)
		}
	}

	return false, false
}

// rollBack deploys the saved manifest exactly as it was, once, and
// follows its task to its end. A lost answer is resolved by the
// rollback's own context ID and never by sending the rollback again.
func (p *updatePhase) rollBack(ctx context.Context, saved string) {
	task, err := p.broker.BOSH.UpdateDeployment(boshapi.WithContextID(ctx, p.contextID), p.target.deployment, saved)
	if err == nil && task == nil {
		err = p.broker.noTaskInAnswer("UpdateDeployment", p.target.deployment, p.contextID)
	}

	switch {
	case errors.Is(err, bosh.ErrOutcomeUnknown):
		p.broker.recordOutcomeUnknown(context.WithoutCancel(ctx), p.instanceID, operationTypeUpdate, "rollback deploy", p.contextID, err, p.logger)

		var found bool

		task, found = p.resolveLostAnswer(ctx)
		if !found {
			return
		}
	case err != nil:
		p.logger.Error("Instance %s: the director did not take the rollback's deploy of %s: %s", p.instanceID, p.target.deployment, err)
		p.settleRollback(ctx, nil, "the director did not take its deploy")

		return
	}

	p.followRollback(ctx, task.ID)
}

// followRollback records the rollback's task and follows it to its end.
func (p *updatePhase) followRollback(ctx context.Context, taskID int) {
	p.rollback.TaskID = taskID
	p.progress(ctx, rollingBackDescription(*p.rollback), taskID)

	finished, ended := p.awaitTask(ctx, taskID)
	if !ended {
		return
	}

	p.settleRollback(ctx, finished, "")
}

// settleRollback records the end of the rollback. A task that succeeded
// restored the previous deployment, so the update failed and the instance
// is usable. Anything else means both deploys failed, and the deployment
// may be inconsistent. reason says why no task ran when task is nil.
func (p *updatePhase) settleRollback(ctx context.Context, task *bosh.Task, reason string) {
	rollback := *p.rollback

	if task != nil {
		rollback.TaskID = task.ID
	}

	if task != nil && task.State == taskStateDone && !p.rollbackUnverified {
		p.restored = true
		rollback.Phase = internalVault.UpdatePhaseRolledBack
		description := fmt.Sprintf("The update failed in BOSH task %d, and BOSH task %d restored the previous deployment", rollback.FailedTaskID, task.ID)
		p.settleFailure(ctx, recordedStateFailed, description, updateOutcomeRolledBack, rollback)

		return
	}

	switch {
	case task != nil:
		reason = fmt.Sprintf("BOSH task %d is %s", task.ID, task.State)
	case reason == "":
		reason = "the director started no task for it"
	}

	rollback.Phase = internalVault.UpdatePhaseRollbackFailed
	description := fmt.Sprintf("The update failed in BOSH task %d, and its rollback failed too (%s). The deployment may be in an inconsistent state, so an operator should check it", rollback.FailedTaskID, reason)

	// The record that names the rollback is damaged, and the manifest
	// saved for it doesn't back it up, so whatever the task did, the
	// broker can't vouch for what it restored.
	if task != nil && p.rollbackUnverified {
		description = fmt.Sprintf("The update failed in BOSH task %d, and the broker can't vouch for its rollback in BOSH task %d, which is %s, because the update's record is damaged and doesn't match the manifest saved for the rollback. The deployment may be in an inconsistent state, so an operator should check it", rollback.FailedTaskID, task.ID, task.State)
	}

	p.settleFailure(ctx, internalVault.TaskStateRollbackFailed, description, updateOutcomeRollbackFailed, rollback)
}

// settleFailure records the end of an update whose deploy failed, under
// the instance's settle lock, trying the write until it lands, and then
// writes its change record and deletes the manifest saved for the
// rollback. The stored plan, parameters, and manifest stay at their last
// good values, and nothing is deleted from the director. While the plan
// the update stages is out of the catalog, the description also says
// that an operator must restore it.
func (p *updatePhase) settleFailure(ctx context.Context, state, description, outcome string, rollback internalVault.UpdateRollback) {
	settleCtx := context.WithoutCancel(ctx)

	// A repeat is refused while the plan is out of the catalog, so the
	// answer says what has to happen first.
	if p.planMissing {
		description = fmt.Sprintf("%s. Plan %s is no longer in the catalog. %s", description, p.request.planID, updatePlanMissingNext)
	}

	unlock, err := p.broker.lockSettle(settleCtx, p.instanceID)
	if err != nil {
		p.logger.Error("Instance %s: the update's settle lock could not be taken, so re-adoption settles it: %s", p.instanceID, err)

		return
	}
	defer unlock()

	if !p.settleWrite(settleCtx, outcome, settleExpect{contextID: p.contextID, state: state}, func(recordCtx context.Context) error {
		return p.broker.Vault.SettleUpdateFailure(recordCtx, p.instanceID, state, description, p.contextID, rollback) //nolint:wrapcheck // settleWrite matches the vault's sentinels
	}) {
		return
	}

	recordCtx, cancel := recordContext(settleCtx)
	defer cancel()

	p.broker.auditUpdateFailure(recordCtx, p.instanceID, p.target, p.request, outcome, rollback)
	p.dropRollbackManifest(settleCtx)
	p.logger.Info("Instance %s: its update settled as %s (%s)", p.instanceID, outcome, description)
}
