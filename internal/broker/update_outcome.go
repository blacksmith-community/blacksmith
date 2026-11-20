package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
)

// updateOutcomeRetry is how long an update waits between the resolver
// passes it runs over its own lost deploy answer.
const updateOutcomeRetry = 15 * time.Second

// errDeployTaskNotRecorded is why an update records the outcome of its
// deploy as unknown when the record names no task for it: the write of a
// lost answer did not land, or a restart stopped the update before the
// deploy's task was recorded.
var errDeployTaskNotRecorded = errors.New("the record names no task for the deploy")

// updateOutcomeRetryWait returns the wait between an update's resolver
// passes.
func (b *Broker) updateOutcomeRetryWait() time.Duration {
	if b.updateOutcomeWait > 0 {
		return b.updateOutcomeWait
	}

	return updateOutcomeRetry
}

// resolveLostAnswer runs resolver passes over the update's lost deploy
// answer until the record names the task the deploy started, or records
// that the director never started one. The update's goroutine keeps its
// claim the whole time, so nothing else deploys the instance while the
// outcome is unknown, and a poll only reports it. It returns the task to
// follow, and false when the update has ended, which is when the director
// never started the deploy, when another write replaced the record, or
// when ctx ends with the outcome still unknown. The record keeps the
// unknown outcome in the last case, so re-adoption resolves it after a
// restart.
func (p *updatePhase) resolveLostAnswer(ctx context.Context) (*bosh.Task, bool) {
	passed := false

	for {
		recorded, err := p.broker.readRecordedOperation(ctx, p.instanceID)

		switch {
		case err != nil:
			p.logger.Warning("Instance %s: the record of its update with context ID %s could not be read, so the update reads it again: %s", p.instanceID, p.contextID, err)
		case recorded.Action != operationTypeUpdate || recorded.ContextID != p.contextID:
			p.logger.Error("Instance %s: its record now holds the %s with context ID %q, not the update with context ID %s, so the update stops", p.instanceID, recorded.Action, recorded.ContextID, p.contextID)

			return nil, false
		case recorded.hasTask():
			p.logger.Info("Instance %s: the update's deploy with context ID %s started task %d", p.instanceID, p.contextID, recorded.TaskID)

			return &bosh.Task{ID: recorded.TaskID}, true
		case recorded.State == recordedStateFailed:
			p.neverStarted(ctx)

			return nil, false
		case recorded.State != internalVault.TaskStateOutcomeUnknown:
			// The record names no task and no unknown outcome, because the
			// write of the lost answer failed or a restart stopped the
			// update around its deploy. The outcome is recorded as unknown
			// before any pass runs, because a pass settles only that.
			p.broker.recordOutcomeUnknown(context.WithoutCancel(ctx), p.instanceID, operationTypeUpdate, "deploy", p.contextID, errDeployTaskNotRecorded, p.logger)
		case !passed:
			_, err = p.broker.resolveUnknownOutcome(ctx, p.instanceID, p.target.deployment, recorded, p.logger)
			if err != nil {
				p.logger.Warning("Instance %s: a check of the update's deploy with context ID %s failed: %s", p.instanceID, p.contextID, err)
			}

			// The record is read again at once, so a pass that settled the
			// outcome is acted on without a wait.
			passed = true

			continue
		}

		passed = false

		if !p.waitForOutcome(ctx) {
			p.logger.Warning("Instance %s: the update stopped resolving its deploy with context ID %s, which stays unknown on record, so re-adoption resolves it: %s", p.instanceID, p.contextID, ctx.Err())

			return nil, false
		}
	}
}

// neverStarted finishes an update whose deploy the resolver found the
// director never started. For the update's own deploy, the deployment is
// as it was, so the failure stands and nothing rolls back. For the
// rollback's deploy, the update's failed deploy is still in place, so the
// rollback failed, and the write that settles it takes the settle mark
// off. See internalVault.TaskRecordSettlePendingKey.
//
// An update re-adoption can't carry through is listed in Attention
// required, as its settle by a task does, and the write that takes the
// mark off replaces the resolver's description with one that says why
// the update can't be carried through and what comes next.
func (p *updatePhase) neverStarted(ctx context.Context) {
	defer p.noteUnresumableUpdate()

	if p.rollback != nil {
		p.settleRollback(ctx, nil, "")

		return
	}

	description := ""
	if p.unresumable != "" {
		description = p.unresumableDescription(fmt.Sprintf("The director lists no task for the update's deploy with context ID %s, so the update was not deployed", p.contextID))
	}

	// The mark comes off before the change record is written, as every
	// settle write comes before its change record. A broker that shuts
	// down before the mark comes off leaves both to the next start.
	if !p.clearSettlePending(ctx, description) {
		return
	}

	p.broker.auditUpdate(context.WithoutCancel(ctx), p.instanceID, p.target, p.request, updateOutcomeFailed, "", 0)
	p.dropRollbackManifest(ctx)
}

// clearSettlePending takes the settle mark off the update's record, trying
// the write until it lands, and replaces the record's description with
// description in the same write when it isn't empty, which the history
// then gains too. A history append that fails after the record landed
// leaves the record unmarked, so the next try finds no mark and counts it
// as cleared. A record that no longer holds the marked failure of the
// update owes nothing more of its record, so it counts as cleared. It
// reports false only when the broker shuts down before the write lands,
// and the mark then stays for the next start.
func (p *updatePhase) clearSettlePending(ctx context.Context, description string) bool {
	wait := p.broker.updateRetryWait()

	for {
		recordCtx, cancel := recordContext(ctx)
		err := p.broker.Vault.ClearSettlePending(recordCtx, p.instanceID, p.contextID, description)

		cancel()

		switch {
		case err == nil:
			return true
		case errors.Is(err, internalVault.ErrTaskRecordReplaced):
			p.logger.Info("Instance %s: its record no longer holds the marked failure of the update with context ID %s, so no settle mark is left to clear: %s", p.instanceID, p.contextID, err)

			return true
		}

		p.logger.Error("Instance %s: failed to clear the settle mark of its update with context ID %s, and tries again in %v: %s", p.instanceID, p.contextID, wait, err)

		if !p.retryAfter(&wait) {
			p.logger.Warning("Instance %s: the broker is shutting down before the settle mark of its update with context ID %s came off, so re-adoption finishes the update at the next start", p.instanceID, p.contextID)

			return false
		}
	}
}

// waitForOutcome waits out the gap between two resolver passes, and
// reports false when ctx ends first.
func (p *updatePhase) waitForOutcome(ctx context.Context) bool {
	timer := time.NewTimer(p.broker.updateOutcomeRetryWait())
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
