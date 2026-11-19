package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
)

// errNoStagedUpdate reports an update record that holds no staged values
// re-adoption can read, so it can't commit what the update deployed.
var errNoStagedUpdate = errors.New("the update's record holds no staged values")

// errUpdateNotIndexed reports an update of an instance the index no
// longer names, which re-adoption leaves alone.
var errUpdateNotIndexed = errors.New("the index has no entry for the instance")

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
// settles the update as its own goroutine would have. Only the end of ctx
// stops it, and it reports false then, and the record keeps the update
// for the next start.
func (b *Broker) readoptUpdate(ctx context.Context, operation readoptOperation, logger logger.Logger) bool {
	for {
		phase, err := b.readoptedUpdatePhase(ctx, operation, logger)
		if err == nil {
			return phase.resume(ctx)
		}

		if errors.Is(err, errUpdateNotIndexed) {
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
// the update deploys. When the staged values don't decode, or the plan
// has left the catalog, the phase it returns can still follow the task
// the record names, and it settles the update as failed with the reason
// once that task ends.
func (b *Broker) readoptedUpdatePhase(ctx context.Context, operation readoptOperation, logger logger.Logger) (*updatePhase, error) {
	readCtx, cancel := recordContext(ctx)
	defer cancel()

	var record map[string]interface{}

	_, err := b.Vault.Get(readCtx, operation.instanceID+"/task", &record)
	if err != nil {
		return nil, fmt.Errorf("failed to read the task record: %w", err)
	}

	instance, exists, err := b.Vault.FindInstance(readCtx, operation.instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the index entry: %w", err)
	}

	if !exists {
		return nil, errUpdateNotIndexed
	}

	var (
		plan        services.Plan
		unresumable string
	)

	staged, stagedErr := stagedUpdate(record)
	if stagedErr == nil {
		plan, err = b.FindPlan(instance.ServiceID, staged.PlanID)
		if err != nil {
			unresumable = fmt.Sprintf("the broker can't record the update's values, because plan %s is no longer in the catalog", staged.PlanID)
		}
	} else {
		unresumable = "the broker can't read the update's values back from its record"
	}

	if unresumable != "" {
		logger.Error("Instance %s: re-adoption follows its update's task to its end and then settles the update as failed, because %s: %v", operation.instanceID, unresumable, errors.Join(stagedErr, err))
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
		context:     staged.Context,
		plan:        plan,
		contextID:   contextID,
		logger:      logger,
		rollback:    rollback,
		unresumable: unresumable,
		stagedLost:  stagedErr != nil,
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
	for err != nil {
		p.logger.Warning("Instance %s: re-adoption could not read its update's record again, and reads it again: %s", p.instanceID, err)

		if !readoptWait(ctx) {
			return false
		}

		current, err = p.broker.readoptCurrent(ctx, p.instanceID)
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

	// The task started before the restart, so the director counts it
	// against its limit, as for any re-adopted task.
	p.broker.adoptReadoptedTask(taskID)

	finished, ended := p.awaitTask(ctx, taskID)
	if !ended {
		return ctx.Err() == nil
	}

	deployed := ""

	// A rollback's task commits nothing, so only the update's own
	// succeeded deploy needs the deployed manifest.
	if finished.State == taskStateDone && p.rollback == nil {
		deployed, err = p.liveManifest(ctx)

		switch {
		case errors.Is(err, errDeployedManifestUnread):
			// The deployment runs the update's values, so they are
			// committed, and the stored manifest stays as it was.
			p.logger.Error("Instance %s: its update's task %d succeeded, and the deployed manifest could not be read, so the update's values are committed and the stored manifest is kept: %s", p.instanceID, taskID, err)
		case err != nil:
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

// updateManifestTimeout bounds re-adoption's read of the manifest an
// update's succeeded task deployed. The task has ended, so the claim no
// longer guards a running deploy, and a deployment the director no longer
// answers for would otherwise keep the claim for good.
const updateManifestTimeout = 10 * time.Minute

// errDeployedManifestUnread reports that the manifest an update's
// succeeded task deployed could not be read within its bound.
var errDeployedManifestUnread = errors.New("the deployed manifest could not be read within its bound")

// updateManifestWait is the bound on re-adoption's read of the manifest
// an update's succeeded task deployed.
func (b *Broker) updateManifestWait() time.Duration {
	if b.updateManifestBound > 0 {
		return b.updateManifestBound
	}

	return updateManifestTimeout
}

// liveManifest reads the manifest the director holds for the update's
// deployment, which is the one the update deployed once its task
// succeeded. A failed read is tried again after each wait, for up to
// updateManifestTimeout, and then it returns errDeployedManifestUnread.
// It returns another error when ctx ends first.
func (p *updatePhase) liveManifest(ctx context.Context) (string, error) {
	readCtx, cancel := context.WithTimeout(ctx, p.broker.updateManifestWait())
	defer cancel()

	for {
		live, err := p.broker.BOSH.GetDeployment(readCtx, p.target.deployment)
		if err == nil && live == nil {
			err = fmt.Errorf("%w: deployment %s", errNoLiveManifest, p.target.deployment)
		}

		if err == nil {
			return live.Manifest, nil
		}

		p.logger.Warning("Instance %s: the manifest of deployment %s could not be read: %s", p.instanceID, p.target.deployment, err)

		if !readoptWait(readCtx) {
			if ctx.Err() != nil {
				return "", fmt.Errorf("failed to read the manifest of deployment %s: %w", p.target.deployment, ctx.Err())
			}

			return "", fmt.Errorf("%w: deployment %s: %w", errDeployedManifestUnread, p.target.deployment, err)
		}
	}
}
