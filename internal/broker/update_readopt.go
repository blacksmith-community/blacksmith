package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
)

// errNoStagedUpdate reports an update record that holds no staged values,
// so re-adoption can't commit what the update deployed.
var errNoStagedUpdate = errors.New("the update's record holds no staged values")

// awaitReadoptedUpdateClaim takes the claim of a re-adopted update that
// another operation held at start, trying again after each wait until it
// holds the claim or the broker stops, and then resumes the update. Only
// re-adoption settles an update whose goroutine a restart ended, so giving
// up would leave the update on record, and the instance refusing every
// update, until the next start.
func (b *Broker) awaitReadoptedUpdateClaim(operation readoptOperation, loaded time.Time, checks chan struct{}, logger logger.Logger) {
	ctx := b.serviceContext()

	for {
		timer := time.NewTimer(b.updateOutcomeRetryWait())

		select {
		case <-ctx.Done():
			timer.Stop()
			logger.Info("The broker is shutting down, so the update of instance %s is left for the next start", operation.instanceID)

			return
		case <-timer.C:
		}

		claim := &operationClaim{operation: operationTypeUpdate, claimed: time.Now(), readopted: true}
		claim.launched.Store(true)

		_, taken, err := b.claimOperation(operation.instanceID, operation.deploymentName, claim)
		if err == nil && taken {
			logger.Info("Instance %s is free, so re-adoption resumes its update", operation.instanceID)

			operation.claim = claim
			b.readoptOperation(operation, loaded, checks, logger)

			return
		}
	}
}

// readoptUpdate resumes an update a restart interrupted, under the claim
// re-adoption holds. It rebuilds the update from its task record, follows
// the deploy's task, resolving a lost answer by its context ID first, and
// settles the update as its own goroutine would have. It reports false
// when ctx ends first, and the record then keeps the update for the next
// start.
func (b *Broker) readoptUpdate(ctx context.Context, operation readoptOperation, logger logger.Logger) bool {
	for {
		phase, err := b.readoptedUpdatePhase(ctx, operation, logger)
		if err == nil {
			return phase.resume(ctx)
		}

		if errors.Is(err, errNoStagedUpdate) {
			logger.Error("Instance %s: re-adoption can't resume its update: %s", operation.instanceID, err)

			return true
		}

		logger.Warning("Instance %s: re-adoption could not read its update yet: %s", operation.instanceID, err)

		if !readoptWait(ctx) {
			return false
		}
	}
}

// readoptedUpdatePhase rebuilds an interrupted update from the staged
// values its task record holds, the instance's index entry, and the plan
// the update deploys.
func (b *Broker) readoptedUpdatePhase(ctx context.Context, operation readoptOperation, logger logger.Logger) (*updatePhase, error) {
	readCtx, cancel := recordContext(ctx)
	defer cancel()

	var record map[string]interface{}

	_, err := b.Vault.Get(readCtx, operation.instanceID+"/task", &record)
	if err != nil {
		return nil, fmt.Errorf("failed to read the task record: %w", err)
	}

	staged, err := stagedUpdate(record)
	if err != nil {
		return nil, err
	}

	instance, exists, err := b.Vault.FindInstance(readCtx, operation.instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the index entry: %w", err)
	}

	if !exists {
		return nil, fmt.Errorf("%w: the index has no entry for the instance", errNoStagedUpdate)
	}

	plan, err := b.FindPlan(instance.ServiceID, staged.PlanID)
	if err != nil {
		return nil, fmt.Errorf("%w: the catalog has no plan %s: %w", errNoStagedUpdate, staged.PlanID, err)
	}

	deployment := operation.recorded.DeploymentName
	if deployment == "" {
		deployment = operation.deploymentName
	}

	contextID, _ := record["context_id"].(string)

	// A rollback the restart interrupted is resumed as the rollback, by
	// its own context ID, and never sent again.
	var rollback *internalVault.UpdateRollback
	if staged.Rollback != nil && staged.Rollback.Phase == internalVault.UpdatePhaseRollingBack {
		rollback = staged.Rollback
	}

	return &updatePhase{
		broker:     b,
		instanceID: operation.instanceID,
		target:     updateTarget{instance: instance, deployment: deployment},
		request: updateRequest{
			planID: staged.PlanID, parameters: staged.Parameters,
			changedKeys: staged.ChangedKeys, fingerprint: staged.Fingerprint, actor: staged.Actor,
		},
		context:   staged.Context,
		plan:      plan,
		contextID: contextID,
		logger:    logger,
		rollback:  rollback,
	}, nil
}

// stagedUpdate decodes the staged values an update's task record holds.
func stagedUpdate(record map[string]interface{}) (internalVault.StagedUpdate, error) {
	var staged internalVault.StagedUpdate

	raw, held := record[internalVault.TaskRecordUpdateKey]
	if !held || raw == nil {
		return staged, errNoStagedUpdate
	}

	encoded, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(encoded, &staged)
	}

	if err != nil {
		return staged, fmt.Errorf("%w: %w", errNoStagedUpdate, err)
	}

	if staged.PlanID == "" {
		return staged, fmt.Errorf("%w: it names no plan", errNoStagedUpdate)
	}

	return staged, nil
}

// resume follows the interrupted update's deploy and settles it. A record
// that names no task yet may belong to an update the restart stopped
// before or while it sent its deploy, so the resolver asks the director
// by the update's context ID. A rollback the restart interrupted is
// followed and settled the same way by the rollback's context ID, and is
// never sent again. It reports false when ctx ends first.
func (p *updatePhase) resume(ctx context.Context) bool {
	current, err := p.broker.readoptCurrent(ctx, p.instanceID)
	if err != nil {
		p.logger.Warning("Instance %s: re-adoption could not read its update's record again: %s", p.instanceID, err)

		return false
	}

	if current.Action != operationTypeUpdate || current.ContextID != p.contextID || readoptTerminal(current.State) || current.State == internalVault.TaskStateRollbackFailed {
		p.logger.Info("Instance %s: its record now holds the %s %s, so re-adoption leaves it", p.instanceID, current.Action, current.State)

		return true
	}

	taskID := current.TaskID

	if !current.hasTask() {
		var found bool

		taskID, found = p.resolveReadoptedTask(ctx)
		if !found {
			return ctx.Err() == nil
		}
	}

	finished, err := p.broker.followReadoptedTask(ctx, taskID, p.logger)

	switch {
	case ctx.Err() != nil:
		return false
	case bosh.IsTaskNotFound(err) && p.rollback != nil:
		// The director pruned the rollback's task, so whether it restored
		// the deployment can't be known, and the broker can't vouch for
		// the deployment.
		p.settleRollback(ctx, nil, fmt.Sprintf("BOSH task %d is no longer known to the director, so its result is unknown", taskID))

		return true
	case bosh.IsTaskNotFound(err):
		// The director pruned the task, so whether the deploy succeeded
		// can't be known. The stored values stay at their last good state,
		// and a repeat of the update deploys and records them.
		p.fail(ctx, fmt.Sprintf("BOSH deployment task %d is no longer known to the director, so the update's result is unknown. Send the update again", taskID), taskID)

		return true
	case err != nil:
		p.logger.Warning("Instance %s: re-adoption stopped following its update's task %d: %s", p.instanceID, taskID, err)

		return false
	}

	deployed := ""

	// A rollback's task commits nothing, so only the update's own
	// succeeded deploy needs the deployed manifest.
	if finished.State == taskStateDone && p.rollback == nil {
		deployed, err = p.liveManifest(ctx)
		if err != nil {
			p.logger.Warning("Instance %s: its update's task %d succeeded, and the deployed manifest could not be read, so the next start settles it: %s", p.instanceID, taskID, err)

			return false
		}
	}

	p.settle(ctx, finished, deployed)

	return true
}

// resolveReadoptedTask resolves the lost answer of the interrupted
// update's deploy, or of its rollback's, by its context ID, and returns
// the task the deploy started. A rollback's task is recorded as the
// rollback's, so the poll says the previous manifest is being redeployed.
func (p *updatePhase) resolveReadoptedTask(ctx context.Context) (int, bool) {
	task, found := p.resolveLostAnswer(ctx)
	if !found {
		return 0, false
	}

	if p.rollback != nil {
		p.rollback.TaskID = task.ID
		p.progress(ctx, rollingBackDescription(*p.rollback), task.ID)
	}

	return task.ID, true
}

// liveManifest reads the manifest the director holds for the update's
// deployment, which is the one the update deployed once its task
// succeeded. A failed read is tried again after each wait until ctx ends.
func (p *updatePhase) liveManifest(ctx context.Context) (string, error) {
	for {
		live, err := p.broker.BOSH.GetDeployment(ctx, p.target.deployment)
		if err == nil && live == nil {
			err = fmt.Errorf("%w: deployment %s", errNoLiveManifest, p.target.deployment)
		}

		if err == nil {
			return live.Manifest, nil
		}

		p.logger.Warning("Instance %s: the manifest of deployment %s could not be read: %s", p.instanceID, p.target.deployment, err)

		if !readoptWait(ctx) {
			return "", fmt.Errorf("failed to read the manifest of deployment %s: %w", p.target.deployment, err)
		}
	}
}
