package broker

import (
	"context"
	"errors"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
)

// updateOutcomeRetry is how long an update waits between the resolver
// passes it runs over its own lost deploy answer.
const updateOutcomeRetry = 15 * time.Second

// errOutcomeNotRecorded is why an update records its lost deploy answer
// again: the first record of it did not land.
var errOutcomeNotRecorded = errors.New("the first record of the lost answer did not land")

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
			// The resolver recorded that the director never started the
			// deploy, so the deployment is as it was and nothing rolls back.
			p.broker.auditUpdate(context.WithoutCancel(ctx), p.instanceID, p.target, p.request, updateOutcomeFailed, "", 0)

			return nil, false
		case recorded.State != internalVault.TaskStateOutcomeUnknown:
			// The write of the unknown outcome failed, so the record still
			// says the deploy is being sent. It is written again before any
			// pass runs, because a pass settles only an unknown outcome.
			p.broker.recordOutcomeUnknown(context.WithoutCancel(ctx), p.instanceID, operationTypeUpdate, "deploy", p.contextID, errOutcomeNotRecorded, p.logger)
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
