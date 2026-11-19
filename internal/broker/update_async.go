package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/internal/upgrade"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/graft/pkg/yaml"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	"github.com/google/uuid"
)

// updateTaskRetry is how long the update waits before it reads its BOSH
// task again after a read failed.
const updateTaskRetry = 5 * time.Second

// errUpdateTaskStopped reports that the update stopped following its BOSH
// task before the task ended, because the broker is shutting down or the
// update ran past its time limit. The task keeps running, and the record
// keeps naming it, so re-adoption follows it after a restart.
var errUpdateTaskStopped = errors.New("stopped following the update's BOSH task")

// updatePhase is an accepted update that needs a deploy. Its goroutine
// holds the instance's claim from the moment the handler accepts the
// update until the update settles.
type updatePhase struct {
	broker     *Broker
	instanceID string
	target     updateTarget
	request    updateRequest
	context    map[string]interface{}
	plan       services.Plan
	rendered   string
	contextID  string
	logger     logger.Logger
	// rollback is the rollback the update runs after its deploy failed,
	// and nil until then. While it is set, contextID is the rollback's.
	rollback *internalVault.UpdateRollback
}

// launchUpdate records the accepted update and starts the goroutine that
// deploys and settles it. It reports whether the goroutine took the claim
// over, and the answer the platform gets.
func (b *Broker) launchUpdate(ctx context.Context, phase *updatePhase, claim *operationClaim) (osbapi.UpdateResponse, bool, bool, error) { //nolint:contextcheck // the update outlives the request and runs under the service context
	phase.contextID = uuid.NewString()

	staged := internalVault.StagedUpdate{
		Fingerprint:    phase.request.fingerprint,
		PreviousPlanID: phase.target.instance.PlanID,
		PlanID:         phase.request.planID,
		Parameters:     phase.request.parameters,
		Context:        phase.context,
		ChangedKeys:    phase.request.changedKeys,
		Actor:          phase.request.actor,
	}

	recordCtx, cancelRecord := recordContext(ctx)
	err := b.Vault.TrackUpdateAccepted(recordCtx, phase.instanceID, "Update request accepted", phase.contextID, phase.target.deployment, staged)

	cancelRecord()

	if err != nil {
		if errors.Is(err, internalVault.ErrUpdateNotAllowed) || errors.Is(err, internalVault.ErrCleanupOwed) || errors.Is(err, internalVault.ErrOutcomeUnknownRecorded) {
			return osbapi.UpdateResponse{}, false, false, updateRecordAnswer(phase.instanceID, err, phase.logger)
		}

		phase.logger.Error("Failed to record the accepted update of instance %s: %s", phase.instanceID, err)

		return osbapi.UpdateResponse{}, false, false, fmt.Errorf("failed to record the update of instance %s: %w", phase.instanceID, err)
	}

	// The update outlives the request, so it runs under the service
	// context with the task monitor's bound. A shutdown ends it, and the
	// record keeps naming the BOSH task, so re-adoption follows the task
	// after the restart.
	asyncCtx, cancelAsync := context.WithTimeout(b.serviceContext(), taskMonitorTimeout)

	// Nothing after this point can fail the request, so a repeated update
	// may now be answered as accepted, and the goroutine takes the claim
	// over and gives it up once the update has settled.
	claim.launched.Store(true)

	b.track("update of instance "+phase.instanceID, func() {
		defer b.releaseOperation(phase.instanceID, claim)
		defer cancelAsync()

		phase.run(asyncCtx)
	})

	phase.logger.Info("Accepted the update of service instance %s, which deploys %s with context ID %s", phase.instanceID, phase.target.deployment, phase.contextID)

	return osbapi.UpdateResponse{Operation: operationTypeUpdate}, true, true, nil
}

// run carries the live release and stemcell versions forward into the
// requested manifest, uploads the releases that manifest still needs,
// deploys it, follows the deploy task to its end, and settles the update.
// The carry comes first, so a release the live deployment runs at a newer
// version is never uploaded at the template's version.
func (p *updatePhase) run(ctx context.Context) {
	p.progress(ctx, "Reading the deployment's live release and stemcell versions", 0)

	deployed, live, err := p.carryLiveVersions(ctx)
	if err != nil {
		p.logger.Error("Instance %s: %s", p.instanceID, err)
		p.fail(ctx, "The update was not deployed, because the deployment's live release and stemcell versions could not be read", 0)

		return
	}

	// The live manifest is what the director last ran, and the claim
	// keeps anything else from changing it, so it is saved before the
	// deploy for the rollback the setting may call for if the deploy
	// fails.
	err = p.saveRollbackManifest(ctx, live)
	if err != nil {
		p.logger.Error("Instance %s: %s", p.instanceID, err)
		p.fail(ctx, "The update was not deployed, because the deployment's last good manifest could not be saved for a rollback", 0)

		return
	}

	p.progress(ctx, "Uploading BOSH releases", 0)

	err = manifest.UploadReleasesFromManifest(ctx, deployed, p.broker.BOSH, p.logger, func(message string) {
		p.progress(ctx, message, 0)
	})
	if err != nil {
		p.logger.Error("Instance %s: failed to upload the releases of its update: %s", p.instanceID, err)
		p.fail(ctx, "Release upload failed: "+err.Error(), 0)

		return
	}

	task, sent := p.deploy(ctx, deployed)
	if !sent {
		return
	}

	finished, err := p.broker.followUpdateTask(ctx, task.ID, p.logger)
	if err != nil {
		p.logger.Warning("Instance %s: %s, and the record keeps naming task %d, so re-adoption follows it", p.instanceID, err, task.ID)

		return
	}

	p.settle(ctx, finished, deployed)
}

// progress records the update's step, which the platform's poll shows.
func (p *updatePhase) progress(ctx context.Context, message string, taskID int) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := p.broker.Vault.TrackProgress(recordCtx, p.instanceID, operationTypeUpdate, message, taskID, nil)
	if err != nil {
		p.logger.Error("failed to track the update's progress (%s): %s", message, err)
	}
}

// fail records the update as failed with description and leaves a change
// record. The deployment, the stored plan, parameters, and manifest stay
// as they were, and nothing is deleted.
func (p *updatePhase) fail(ctx context.Context, description string, taskID int) {
	recordCtx, cancel := recordContext(context.WithoutCancel(ctx))
	defer cancel()

	err := p.broker.Vault.TrackFailed(recordCtx, p.instanceID, operationTypeUpdate, description, p.contextID, taskID)
	if err != nil {
		p.logger.Error("CRITICAL: failed to record the failed update of instance %s: %s", p.instanceID, err)
	}

	p.broker.auditUpdate(recordCtx, p.instanceID, p.target, p.request, updateOutcomeFailed, "", taskID)
	p.dropRollbackManifest(ctx)
}

// carryLiveVersions returns the requested manifest with the live version
// of each release and of the stemcell wherever the live one is newer than
// the version the manifest names, so an update never moves an instance an
// upgrade moved forward back to the template's versions. It also returns
// the live manifest as the director answered it.
func (p *updatePhase) carryLiveVersions(ctx context.Context) (string, string, error) {
	live, err := p.broker.BOSH.GetDeployment(ctx, p.target.deployment)
	if err != nil {
		return "", "", fmt.Errorf("failed to read the live manifest of deployment %s: %w", p.target.deployment, err)
	}

	if live == nil {
		return "", "", fmt.Errorf("%w: deployment %s", errNoLiveManifest, p.target.deployment)
	}

	deployed, err := carryLiveVersions(p.rendered, live.Manifest)
	if err != nil {
		return "", "", fmt.Errorf("failed to carry the live versions of deployment %s forward: %w", p.target.deployment, err)
	}

	return deployed, live.Manifest, nil
}

// errNoLiveManifest reports a director that answered a deployment read
// with no deployment.
var errNoLiveManifest = errors.New("the director answered with no deployment")

// deploy records the update's context ID with the deploy step, sends the
// deploy that carries it, and records the task the director's answer
// names. A lost answer is resolved by the deploy's context ID before it
// returns. It reports whether the deploy was sent and its task is known.
func (p *updatePhase) deploy(ctx context.Context, deployed string) (*bosh.Task, bool) {
	p.progress(ctx, "Deploying the update", 0)

	task, err := p.broker.BOSH.UpdateDeployment(boshapi.WithContextID(ctx, p.contextID), p.target.deployment, deployed)
	if err == nil && task == nil {
		err = p.broker.noTaskInAnswer("UpdateDeployment", p.target.deployment, p.contextID)
	}

	if errors.Is(err, bosh.ErrOutcomeUnknown) {
		p.broker.recordOutcomeUnknown(context.WithoutCancel(ctx), p.instanceID, operationTypeUpdate, "deploy", p.contextID, err, p.logger)

		return p.resolveLostAnswer(ctx)
	}

	if err != nil {
		// Any other error means the director started no task, because it
		// refused the deploy or never received it.
		p.logger.Error("Instance %s: the director did not take the update's deploy of %s: %s", p.instanceID, p.target.deployment, err)
		p.fail(ctx, fmt.Sprintf("The director did not take the deploy of %s: %s", p.target.deployment, err), 0)

		return nil, false
	}

	p.logger.Info("The director accepted the update's deploy of %s as task %d", p.target.deployment, task.ID)
	p.progress(ctx, fmt.Sprintf("BOSH deployment in progress (task %d)", task.ID), task.ID)

	return task, true
}

// followUpdateTask waits for the update's BOSH task to end, reading it
// again after a failed read, until ctx ends.
func (b *Broker) followUpdateTask(ctx context.Context, taskID int, logger logger.Logger) (*bosh.Task, error) {
	for {
		task, err := b.BOSH.WaitForTask(ctx, taskID)

		switch {
		case err == nil && task != nil && (task.State == taskStateDone || isFailedTaskState(task.State)):
			return task, nil
		case ctx.Err() != nil:
			return nil, fmt.Errorf("%w %d: %w", errUpdateTaskStopped, taskID, ctx.Err())
		case err != nil:
			logger.Warning("Waiting for the update's BOSH task %d failed, and the update waits for it again: %s", taskID, err)
		}

		timer := time.NewTimer(updateTaskRetry)

		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()

			return nil, fmt.Errorf("%w %d: %w", errUpdateTaskStopped, taskID, ctx.Err())
		}
	}
}

// settle records the update's end under the instance's settle lock. A
// succeeded deploy commits the staged values, the deployed manifest, the
// credentials, and the plan references. A failed one leaves the last good
// values in place, and records the failure or rolls back once, as the
// setting at the failure says. A task the rollback ran settles the
// rollback and never commits anything.
func (p *updatePhase) settle(ctx context.Context, task *bosh.Task, deployed string) {
	if p.rollback != nil {
		p.settleRollback(ctx, task, "")

		return
	}

	if task.State != taskStateDone {
		p.deployFailed(ctx, task)

		return
	}

	settleCtx := context.WithoutCancel(ctx)

	unlock, err := p.broker.lockSettle(settleCtx, p.instanceID)
	if err != nil {
		p.logger.Error("Instance %s: the update's task %d ended, and its settle lock could not be taken, so re-adoption settles it: %s", p.instanceID, task.ID, err)

		return
	}
	defer unlock()

	err = p.broker.commitUpdateValues(settleCtx, p.instanceID, p.target, p.request, p.context, p.logger)
	if err != nil {
		p.logger.Error("Instance %s: the update's task %d succeeded, and its values could not be recorded: %s", p.instanceID, task.ID, err)
		p.fail(settleCtx, fmt.Sprintf("BOSH deployment task %d succeeded, and the broker could not record the new values, so send the update again", task.ID), task.ID)

		return
	}

	p.storeDeployedManifest(settleCtx, deployed)

	err = p.broker.processCredentials(settleCtx, p.instanceID, osbapi.ProvisionRequest{ServiceID: p.target.instance.ServiceID, PlanID: p.plan.ID}, p.logger)
	if err != nil {
		p.logger.Error("Instance %s: the credentials were not refreshed after its update, so the stored ones stay: %s", p.instanceID, err)
	}

	p.broker.storePlanReferences(settleCtx, p.instanceID, &p.plan, p.logger)

	recordCtx, cancelRecord := recordContext(settleCtx)
	defer cancelRecord()

	err = p.broker.Vault.TrackSucceeded(recordCtx, p.instanceID, operationTypeUpdate, fmt.Sprintf("Updated by BOSH deployment task %d", task.ID), task.ID)
	if err != nil {
		p.logger.Error("CRITICAL: failed to record the succeeded update of instance %s: %s", p.instanceID, err)
	}

	p.broker.auditUpdate(recordCtx, p.instanceID, p.target, p.request, updateOutcomeSucceeded, "", task.ID)
	p.dropRollbackManifest(settleCtx)
	p.logger.Info("Instance %s: its update settled as succeeded by BOSH task %d", p.instanceID, task.ID)
}

// storeDeployedManifest stores the manifest the update deployed as the
// instance's manifest.
func (p *updatePhase) storeDeployedManifest(ctx context.Context, deployed string) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := p.broker.Vault.Put(recordCtx, p.instanceID+"/manifest", map[string]interface{}{
		manifestField: deployed,
		"stored_at":   time.Now().Format(time.RFC3339),
	})
	if err != nil {
		p.logger.Error("Instance %s: failed to store the manifest its update deployed: %s", p.instanceID, err)
	}
}

// carryLiveVersions returns rendered with the live version of each release
// whose live version is newer than the one rendered names, and with the
// live stemcell version when the live stemcell has the same OS at a newer
// version. A release or stemcell that either manifest names by a version
// with no number, such as latest, keeps the rendered version, and a
// release the live deployment doesn't have keeps the template's version.
func carryLiveVersions(rendered, live string) (string, error) {
	var renderedDoc, liveDoc map[string]interface{}

	err := yaml.Unmarshal([]byte(rendered), &renderedDoc)
	if err != nil {
		return "", fmt.Errorf("failed to parse the requested manifest: %w", err)
	}

	err = yaml.Unmarshal([]byte(live), &liveDoc)
	if err != nil {
		return "", fmt.Errorf("failed to parse the live manifest: %w", err)
	}

	liveReleases := releaseVersions(liveDoc)

	var targets []upgrade.ReleaseTarget

	for name, version := range releaseVersions(renderedDoc) {
		if liveVersion, ok := liveReleases[name]; ok && newerVersion(liveVersion, version) {
			targets = append(targets, upgrade.ReleaseTarget{Name: name, Version: liveVersion})
		}
	}

	deployed, _, err := upgrade.MergeReleaseOverlay(rendered, targets)
	if err != nil {
		return "", fmt.Errorf("failed to carry the live release versions forward: %w", err)
	}

	renderedOS, renderedVersion := firstStemcell(renderedDoc)
	liveOS, liveVersion := firstStemcell(liveDoc)

	if renderedOS != "" && renderedOS == liveOS && newerVersion(liveVersion, renderedVersion) {
		deployed, err = upgrade.MergeStemcellOverlay(deployed, liveOS, liveVersion)
		if err != nil {
			return "", fmt.Errorf("failed to carry the live stemcell version forward: %w", err)
		}
	}

	return deployed, nil
}

// newerVersion reports whether candidate is a numbered version newer than
// current, which is also numbered.
func newerVersion(candidate, current string) bool {
	numbered := func(version string) bool { return version != "" && version[0] >= '0' && version[0] <= '9' }

	return numbered(candidate) && numbered(current) && bosh.CompareVersions(candidate, current) > 0
}

// releaseVersions returns the version of each release a manifest names.
func releaseVersions(doc map[string]interface{}) map[string]string {
	versions := map[string]string{}

	releases, _ := doc["releases"].([]interface{})
	for _, entry := range releases {
		release, _ := entry.(map[string]interface{})
		name, _ := release["name"].(string)

		if name != "" {
			versions[name] = fmt.Sprint(release["version"])
		}
	}

	return versions
}

// firstStemcell returns the OS and version of the first stemcell a
// manifest names, which is the one the stemcell overlay sets.
func firstStemcell(doc map[string]interface{}) (string, string) {
	stemcells, _ := doc["stemcells"].([]interface{})
	if len(stemcells) == 0 {
		return "", ""
	}

	stemcell, _ := stemcells[0].(map[string]interface{})
	stemcellOS, _ := stemcell["os"].(string)

	if stemcell["version"] == nil {
		return stemcellOS, ""
	}

	return stemcellOS, fmt.Sprint(stemcell["version"])
}
