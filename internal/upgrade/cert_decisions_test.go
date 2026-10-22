//nolint:testpackage // the tests bind previews through the manager's unexported fields
package upgrade

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/mutation"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// decisionSince is when the fixtures' decisions were asked.
var decisionSince = validateNow.Add(-time.Hour)

// pausedForDecision returns the env's task paused, with its one instance
// started and waiting for the decision given.
func (e *redeployEnv) pausedForDecision(status InstanceStatus, pending *PendingDecision) UpgradeTask {
	task := e.task()
	task.Status = TaskStatusPaused
	task.Paused = true

	started := validateNow
	inst := &task.Instances[0]
	inst.Status = status
	inst.StartedAt = &started
	inst.Error = "The instance waits for a decision."
	inst.Certificates.Pending = pending

	return task
}

// awaitingApproval returns a paused task whose instance waits for an
// operator to approve its changed effects, because the manifest changed
// after the preview.
func (e *redeployEnv) awaitingApproval() UpgradeTask {
	task := e.pausedForDecision(StatusAwaitingDecision, &PendingDecision{
		Kind:    DecisionApproveChangedEffects,
		Changes: []string{"The manifest changed since the preview."},
		Since:   decisionSince,
	})
	task.Instances[0].Certificates.Approved.ManifestDigest = "stale-digest"

	return task
}

// awaitingReconcile returns a paused task whose instance waits for an
// operator to reconcile a regeneration whose outcome is unknown, with the
// candidates given.
func (e *redeployEnv) awaitingReconcile(candidates []Candidate) UpgradeTask {
	task := e.pausedForDecision(StatusAwaitingDecision, &PendingDecision{
		Kind:       DecisionReconcileRegeneration,
		Variable:   regenLeaf,
		Candidates: candidates,
		Since:      decisionSince,
	})

	rotation := task.Instances[0].Certificates
	rotation.Phase = RotationPhaseRegenerate
	rotation.Variables[0].RegenerateState = RegenerateOutcomeUnknown
	rotation.Variables[0].Baseline = []string{"v1"}

	return task
}

// awaitingOutcome returns a paused task whose done deploy could not be
// verified, so the instance waits for an operator to resolve its outcome.
// The director and the listener already show v2.
func (e *redeployEnv) awaitingOutcome() UpgradeTask {
	e.madeNewVersion()
	e.deployedNewVersion()

	task := e.deployedTask()
	task.Status = TaskStatusPaused
	task.Paused = true

	inst := &task.Instances[0]
	inst.Status = InstanceStatusOutcomeUnknown
	inst.Error = "Blacksmith could not verify the new certificates."
	inst.Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}
	inst.Certificates.Pending = &PendingDecision{Kind: DecisionResolveDeployOutcome, Since: decisionSince}

	return task
}

// bindPreview stores a preview bound to the test instance's decision, as
// RepreviewForDecision would, with the mode, converge policy, decision
// time, and selection given.
func bindPreview(t *testing.T, manager *Manager, id string, mode RotationMode, converge ConvergePolicy, since time.Time, selection []string) *Preview {
	t.Helper()

	selected := make([]VersionPair, 0, len(selection))
	for _, name := range selection {
		selected = append(selected, VersionPair{Name: name, DeployedVersionID: "v1", LatestVersionID: "v1"})
	}

	preview := &Preview{
		ID: id,
		Instances: []PreviewInstance{{
			InstanceID: waitInstanceID,
			approved:   ApprovedEffects{ManifestDigest: "fresh-digest", Selected: selected, VMCIDs: []string{redeployVMCID}},
		}},
		Warnings:      []string{},
		mode:          mode,
		converge:      converge,
		forTask:       certTaskID,
		forInstance:   waitInstanceID,
		decisionSince: since,
		selection:     selection,
	}

	manager.Dependencies().Previews.Put(preview, time.Hour)

	return preview
}

// expectStillAwaiting fails the test unless the instance still waits for
// the decision of the kind given, and the task still holds it.
func expectStillAwaiting(t *testing.T, manager *Manager, status InstanceStatus, kind string) {
	t.Helper()

	inst := current(t, manager).Instances[0]
	if inst.Status != status || inst.Certificates.Pending == nil || inst.Certificates.Pending.Kind != kind {
		t.Fatalf("expected the instance to still wait for %s, got %s with %+v", kind, inst.Status, inst.Certificates.Pending)
	}

	expectHeld(t, manager)
}

// The task holds the instance, and its own reservation does not block the
// re-preview, which is bound to the decision it answers.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRepreviewWhileReserved(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())
	expectHeld(t, manager)

	preview, err := manager.RepreviewForDecision(context.Background(), certTaskID, waitInstanceID)
	if err != nil {
		t.Fatalf("RepreviewForDecision: %v", err)
	}

	if len(preview.Instances) != 1 || preview.Instances[0].Blocked != nil {
		t.Fatalf("expected one unblocked instance, got %+v", preview.Instances)
	}

	if preview.forTask != certTaskID || preview.forInstance != waitInstanceID || !preview.decisionSince.Equal(decisionSince) {
		t.Fatalf("expected the preview bound to the decision, got task %q instance %q since %s", preview.forTask, preview.forInstance, preview.decisionSince)
	}

	if preview.mode != RotationModeRedeploy || len(preview.selection) != 1 || preview.selection[0] != regenLeaf {
		t.Fatalf("expected the task's mode and selection, got %s and %v", preview.mode, preview.selection)
	}

	if got := preview.Instances[0].approved.ManifestDigest; got != manifestDigest(env.manifest) {
		t.Fatalf("expected the preview to bind the current manifest, got %q", got)
	}

	stored, err := manager.Dependencies().Previews.Get(preview.ID)
	if err != nil || stored != preview {
		t.Fatalf("expected the preview to be stored, got %v", err)
	}
}

// A deployment action that holds the deployment blocks the re-preview, and
// approving the blocked preview answers with its block code.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestRepreviewBlockedByOtherHolder(t *testing.T) {
	env := newRedeployEnv(t)

	guard := mutation.NewGuard(time.Now)

	free, err := guard.Acquire("", waitDeployment, mutation.Holder{Kind: mutation.KindDeploymentAction, Description: "a deployment action on redis-instance-1"})
	if err != nil {
		t.Fatalf("holding the deployment: %v", err)
	}

	t.Cleanup(free)

	manager := env.loadWaiting(guard, env.awaitingApproval())

	preview, err := manager.RepreviewForDecision(context.Background(), certTaskID, waitInstanceID)
	if err != nil {
		t.Fatalf("RepreviewForDecision: %v", err)
	}

	blocked := preview.Instances[0].Blocked
	if blocked == nil || blocked.Code != apierr.CodeInstanceBusy || !strings.Contains(blocked.Message, "deployment action") {
		t.Fatalf("expected the re-preview blocked by the deployment action, got %+v", blocked)
	}

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: preview.ID})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeInstanceBusy)
}

// A bound preview built for another mode or converge policy is refused,
// and the message names which one differs.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveRejectsWrongMode(t *testing.T) {
	env := newRedeployEnv(t)
	env.mu.Lock()
	env.switches = switches(true, true)
	env.mu.Unlock()

	stored := env.awaitingApproval()
	stored.Certificates.Mode = RotationModePatch
	stored.Certificates.Converge = ConvergePending

	manager := env.load(stored)

	redeploy := bindPreview(t, manager, "pv-redeploy-mode", RotationModeRedeploy, "", decisionSince, []string{regenLeaf})

	err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: redeploy.ID})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeApprovalMismatch)

	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("expected the message to name the mode, got %q", err.Error())
	}

	immediate := bindPreview(t, manager, "pv-immediate", RotationModePatch, ConvergeImmediate, decisionSince, []string{regenLeaf})

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: immediate.ID})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeApprovalMismatch)

	if !strings.Contains(err.Error(), "converge policy") {
		t.Fatalf("expected the message to name the converge policy, got %q", err.Error())
	}

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// A bound preview whose selected variables differ from the instance's is
// refused, naming the selections.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveRejectsChangedSelections(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	other := bindPreview(t, manager, "pv-other-selection", RotationModeRedeploy, "", decisionSince, []string{"/ws-bosh/redis-instance-1/other_crt"})

	err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: other.ID})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeApprovalMismatch)

	if !strings.Contains(err.Error(), "selected variables") {
		t.Fatalf("expected the message to name the selected variables, got %q", err.Error())
	}

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// A preview bound to an earlier decision of the instance approves nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveRejectsStaleDecision(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	stale := bindPreview(t, manager, "pv-stale", RotationModeRedeploy, "", decisionSince.Add(-time.Minute), []string{regenLeaf})

	err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: stale.ID})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeNotAwaitingDecision)

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// An ordinary preview of the same instance is bound to no decision, so it
// approves nothing.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveRejectsUnboundPreview(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	preview, err := manager.PreviewRotation(context.Background(), PreviewRequest{
		Mode:       RotationModeRedeploy,
		Selections: []Selection{{InstanceID: waitInstanceID, Variables: []string{regenLeaf}}},
	})
	if err != nil {
		t.Fatalf("PreviewRotation: %v", err)
	}

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: preview.ID})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeNotAwaitingDecision)

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// Approving a bound preview replaces the approved effects, resumes the
// task, and the rotation goes on to completion against them.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveWithBoundPreview(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	preview, err := manager.RepreviewForDecision(context.Background(), certTaskID, waitInstanceID)
	if err != nil {
		t.Fatalf("RepreviewForDecision: %v", err)
	}

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: preview.ID, ClientRequestID: "approve-1"})
	if err != nil {
		t.Fatalf("ApproveChangedEffects: %v", err)
	}

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	rotation := task.Instances[0].Certificates

	if rotation.Approved.ManifestDigest != manifestDigest(env.manifest) || rotation.Pending != nil {
		t.Fatalf("expected the new approval and no pending decision, got digest %q and %+v", rotation.Approved.ManifestDigest, rotation.Pending)
	}

	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })

	if len(env.director.deploys()) != 1 {
		t.Fatalf("expected one deploy, got %d", len(env.director.deploys()))
	}

	expectReleased(t, manager)
}

// Adopting a candidate that passes every check adopts it without a new
// CredHub request, and the rotation deploys it. The candidates are listed
// at the decision, since none were recorded.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestReconcileAdoptValidCandidate(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()

	manager := env.load(env.awaitingReconcile(nil))

	err := manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{
		Variable: regenLeaf, Action: "adopt", VersionID: "v2", ClientRequestID: "adopt-1",
	})
	if err != nil {
		t.Fatalf("ReconcileRegeneration: %v", err)
	}

	task := eventuallyInstance(t, manager, InstanceStatusSuccess)
	variable := task.Instances[0].Certificates.Variables[0]

	if variable.RegenerateState != RegenerateAdopted || variable.NewVersionID != "v2" {
		t.Fatalf("expected v2 adopted, got %s with %q", variable.RegenerateState, variable.NewVersionID)
	}

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected no new regenerate request, got %d", count)
	}

	if len(env.director.deploys()) != 1 {
		t.Fatalf("expected one deploy, got %d", len(env.director.deploys()))
	}

	expectReleased(t, manager)
}

// A version that failed validation, or that is no candidate at all, can't
// be adopted, and the instance keeps waiting.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestReconcileAdoptInvalidCandidate(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()

	manager := env.load(env.awaitingReconcile([]Candidate{{VersionID: "v2", CreatedAt: regenLater, Valid: false}}))

	for _, version := range []string{"v2", "v9"} {
		err := manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{
			Variable: regenLeaf, Action: "adopt", VersionID: version,
		})
		expectAPIError(t, err, http.StatusBadRequest, apierr.CodeCandidateInvalid)
	}

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionReconcileRegeneration)

	if state := current(t, manager).Instances[0].Certificates.Variables[0].RegenerateState; state != RegenerateOutcomeUnknown {
		t.Fatalf("expected the variable to stay outcome_unknown, got %s", state)
	}
}

// Abandoning the regeneration fails the instance and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestReconcileAbandon(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()

	manager := env.load(env.awaitingReconcile(nil))

	err := manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{
		Variable: regenLeaf, Action: "abandon",
	})
	if err != nil {
		t.Fatalf("ReconcileRegeneration: %v", err)
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "regeneration abandoned") {
		t.Fatalf("expected the instance failed with the regeneration abandoned, got %s: %q", inst.Status, inst.Error)
	}

	if inst.Certificates.Pending != nil || inst.Certificates.Variables[0].RegenerateState != RegenerateAbandoned {
		t.Fatalf("expected no pending decision and an abandoned variable, got %+v and %s", inst.Certificates.Pending, inst.Certificates.Variables[0].RegenerateState)
	}

	expectReleased(t, manager)

	waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status == TaskStatusFailed })

	if len(env.director.deploys()) != 0 || env.credhub.regenerateCount() != 0 {
		t.Fatalf("expected no deploy and no regenerate, got %d and %d", len(env.director.deploys()), env.credhub.regenerateCount())
	}
}

// Skipping the approval marks the instance skipped and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestApproveSkipReleasesGuard(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{Action: "skip"})
	if err != nil {
		t.Fatalf("ApproveChangedEffects: %v", err)
	}

	task := current(t, manager)
	if inst := task.Instances[0]; inst.Status != InstanceStatusSkipped || inst.Certificates.Pending != nil {
		t.Fatalf("expected a skipped instance with no pending decision, got %s with %+v", inst.Status, inst.Certificates.Pending)
	}

	if task.SkippedCount != 1 {
		t.Fatalf("expected one skipped instance, got %d", task.SkippedCount)
	}

	expectReleased(t, manager)

	waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status == TaskStatusCompleted })

	if len(env.director.deploys()) != 0 {
		t.Fatalf("expected no deploy, got %d", len(env.director.deploys()))
	}
}

// The same request ID and body after the decision applied answers as the
// first did, and changes nothing more.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionRequestIDReplay(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	req := ApproveRequest{Action: "skip", ClientRequestID: "skip-1"}

	for attempt := 1; attempt <= 2; attempt++ {
		err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, req)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}

	task := current(t, manager)
	if task.SkippedCount != 1 || task.Instances[0].Status != InstanceStatusSkipped {
		t.Fatalf("expected one skip, got %d skipped and %s", task.SkippedCount, task.Instances[0].Status)
	}

	if got := task.Instances[0].Certificates.DecisionRequestID; got != "skip-1" {
		t.Fatalf("expected the request ID recorded, got %q", got)
	}

	waitUntil(t, "the saved request ID", func() bool {
		return env.stored().Instances[0].Certificates.DecisionRequestID == "skip-1"
	})
}

// A settling decision sent twice with the same request ID applies once,
// logs one audit line, and answers the second as it did the first.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestSettlingDecisionReplay(t *testing.T) {
	cases := []struct {
		name   string
		stored func(env *redeployEnv) UpgradeTask
		decide func(manager *Manager) error
		status InstanceStatus
		choice string
	}{
		{
			name:   "resolve verify",
			stored: (*redeployEnv).awaitingOutcome,
			decide: func(manager *Manager) error {
				return manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify", ClientRequestID: "replay-1"})
			},
			status: InstanceStatusSuccess,
			choice: "verify",
		},
		{
			name:   "resolve failed",
			stored: (*redeployEnv).awaitingOutcome,
			decide: func(manager *Manager) error {
				return manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "failed", ClientRequestID: "replay-1"})
			},
			status: InstanceStatusFailed,
			choice: "failed",
		},
		{
			name:   "resolve abandon",
			stored: (*redeployEnv).awaitingOutcome,
			decide: func(manager *Manager) error {
				return manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "replay-1"})
			},
			status: InstanceStatusOutcomeUnknown,
			choice: "abandon",
		},
		{
			name: "abandon regeneration",
			stored: func(env *redeployEnv) UpgradeTask {
				env.madeNewVersion()

				return env.awaitingReconcile(nil)
			},
			decide: func(manager *Manager) error {
				return manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{
					Variable: regenLeaf, Action: "abandon", ClientRequestID: "replay-1",
				})
			},
			status: InstanceStatusFailed,
			choice: "abandon",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.load(tc.stored(env))

			for attempt := 1; attempt <= 2; attempt++ {
				err := tc.decide(manager)
				if err != nil {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
			}

			inst := current(t, manager).Instances[0]
			if inst.Status != tc.status || inst.Certificates.Pending != nil {
				t.Fatalf("expected a settled %s instance, got %s with %+v", tc.status, inst.Status, inst.Certificates.Pending)
			}

			if got := inst.Certificates.DecisionRequestID; got != "replay-1" {
				t.Fatalf("expected the request ID recorded, got %q", got)
			}

			audits := env.logs.auditLines()
			if len(audits) != 1 || !strings.Contains(audits[0], "audit=certificate-rotation event=decision") || !strings.Contains(audits[0], "choice="+tc.choice) {
				t.Fatalf("expected one audit line for the %s decision, got %q", tc.choice, audits)
			}

			expectReleased(t, manager)

			waitUntil(t, "the saved request ID", func() bool {
				return env.stored().Instances[0].Certificates.DecisionRequestID == "replay-1"
			})
		})
	}
}

// The same request ID with another body is a conflict, on any decision
// endpoint, and a new ID against an instance that no longer waits is
// refused.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionRequestIDConflict(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())
	ctx := context.Background()

	err := manager.ApproveChangedEffects(ctx, certTaskID, waitInstanceID, ApproveRequest{Action: "skip", ClientRequestID: "decide-1"})
	if err != nil {
		t.Fatalf("ApproveChangedEffects: %v", err)
	}

	err = manager.ApproveChangedEffects(ctx, certTaskID, waitInstanceID, ApproveRequest{PreviewID: "pv-other", ClientRequestID: "decide-1"})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)

	err = manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "verify", ClientRequestID: "decide-1"})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)

	err = manager.ApproveChangedEffects(ctx, certTaskID, waitInstanceID, ApproveRequest{Action: "skip", ClientRequestID: "decide-2"})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeNotAwaitingDecision)

	if task := current(t, manager); task.SkippedCount != 1 {
		t.Fatalf("expected one skip, got %d", task.SkippedCount)
	}
}

// With rotation off, neither decision that lets the task write is taken.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionWithRotationOff(t *testing.T) {
	env := newRedeployEnv(t)
	env.madeNewVersion()

	manager := env.load(env.awaitingReconcile([]Candidate{{VersionID: "v2", CreatedAt: regenLater, Valid: true}}))
	env.setSwitches(false)

	err := manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{
		Variable: regenLeaf, Action: "adopt", VersionID: "v2",
	})
	expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionReconcileRegeneration)

	task := current(t, manager)
	if task.Status != TaskStatusPaused || task.Instances[0].Certificates.Variables[0].RegenerateState != RegenerateOutcomeUnknown {
		t.Fatalf("expected the paused task unchanged, got %s with %s", task.Status, task.Instances[0].Certificates.Variables[0].RegenerateState)
	}
}

// Resolving with verify checks the deploy now, and a deploy that shows
// the new certificates completes the instance and the task.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveVerify(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingOutcome())
	expectHeld(t, manager)

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify", ClientRequestID: "verify-1"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	task := current(t, manager)
	if inst := task.Instances[0]; inst.Status != InstanceStatusSuccess || inst.Certificates.Pending != nil {
		t.Fatalf("expected a completed instance with no pending decision, got %s with %+v", inst.Status, inst.Certificates.Pending)
	}

	expectReleased(t, manager)

	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })
}

// Resolving with failed fails the instance and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveFailed(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingOutcome())

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "failed"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	task := current(t, manager)
	if inst := task.Instances[0]; inst.Status != InstanceStatusFailed || inst.Certificates.Pending != nil || task.FailedCount != 1 {
		t.Fatalf("expected one failed instance with no pending decision, got %s with %+v and %d failed", inst.Status, inst.Certificates.Pending, task.FailedCount)
	}

	expectReleased(t, manager)
}

// Resolving with abandon leaves the outcome unknown for good, frees the
// instance, and lets the task finish rather than wait on it again.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveAbandon(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingOutcome())

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "abandon"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	task := current(t, manager)
	inst := task.Instances[0]

	if inst.Status != InstanceStatusOutcomeUnknown || inst.Certificates.Pending != nil || inst.Certificates.Phase != RotationPhaseDone || inst.CompletedAt == nil {
		t.Fatalf("expected a closed instance of unknown outcome, got %s at %s with %+v", inst.Status, inst.Certificates.Phase, inst.Certificates.Pending)
	}

	if task.UnknownCount != 1 {
		t.Fatalf("expected one unknown outcome, got %d", task.UnknownCount)
	}

	expectReleased(t, manager)

	waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status == TaskStatusCompleted })
}

// A cancelled task's instance can be resolved too, so the cancelled task
// never holds it forever, and the task stays cancelled.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveOnCancelledTask(t *testing.T) {
	env := newRedeployEnv(t)

	stored := env.awaitingOutcome()
	stored.Status = TaskStatusCancelled
	stored.Paused = false
	completed := validateNow
	stored.CompletedAt = &completed

	manager := env.load(stored)
	expectHeld(t, manager)

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	task := current(t, manager)
	if task.Instances[0].Status != InstanceStatusSuccess || task.Status != TaskStatusCancelled {
		t.Fatalf("expected a completed instance of a cancelled task, got %s on a %s task", task.Instances[0].Status, task.Status)
	}

	expectReleased(t, manager)
}

// Resolve answers only a pending resolve_deploy_outcome decision.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveNotAwaiting(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	expectAPIError(t, err, http.StatusConflict, apierr.CodeNotAwaitingDecision)

	err = manager.ResolveDeployOutcome(context.Background(), certTaskID, "instance-9", ResolveRequest{Action: "verify"})
	expectAPIError(t, err, http.StatusNotFound, apierr.CodeInstanceNotFound)

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// refuseSaves has the vault refuse every write of the certificate task's
// record until the function it returns is called.
func (e *redeployEnv) refuseSaves() func() {
	e.vault.script.Lock()
	e.vault.refuse = func(UpgradeTask) bool { return true }
	e.vault.script.Unlock()

	return func() {
		e.vault.script.Lock()
		e.vault.refuse = nil
		e.vault.script.Unlock()
	}
}

// A decision that settles the instance is applied only once its save is
// confirmed. When the save fails, the instance still waits and is still
// held, and the same request sent again applies and saves it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionUnsavedThenRetried(t *testing.T) {
	cases := []struct {
		name     string
		task     func(e *redeployEnv) UpgradeTask
		status   InstanceStatus
		kind     string
		decide   func(m *Manager) error
		settled  InstanceStatus
		finished TaskStatus
	}{
		{
			name:   "skip",
			task:   (*redeployEnv).awaitingApproval,
			status: StatusAwaitingDecision,
			kind:   DecisionApproveChangedEffects,
			decide: func(m *Manager) error {
				return m.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{Action: "skip", ClientRequestID: "retry-1"})
			},
			settled:  InstanceStatusSkipped,
			finished: TaskStatusCompleted,
		},
		{
			name: "abandon regeneration",
			task: func(e *redeployEnv) UpgradeTask {
				e.madeNewVersion()

				return e.awaitingReconcile(nil)
			},
			status: StatusAwaitingDecision,
			kind:   DecisionReconcileRegeneration,
			decide: func(m *Manager) error {
				return m.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{Variable: regenLeaf, Action: "abandon", ClientRequestID: "retry-1"})
			},
			settled:  InstanceStatusFailed,
			finished: TaskStatusFailed,
		},
		{
			name:   "resolve failed",
			task:   (*redeployEnv).awaitingOutcome,
			status: InstanceStatusOutcomeUnknown,
			kind:   DecisionResolveDeployOutcome,
			decide: func(m *Manager) error {
				return m.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "failed", ClientRequestID: "retry-1"})
			},
			settled:  InstanceStatusFailed,
			finished: TaskStatusFailed,
		},
		{
			name:   "resolve abandon",
			task:   (*redeployEnv).awaitingOutcome,
			status: InstanceStatusOutcomeUnknown,
			kind:   DecisionResolveDeployOutcome,
			decide: func(m *Manager) error {
				return m.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "abandon", ClientRequestID: "retry-1"})
			},
			settled:  InstanceStatusOutcomeUnknown,
			finished: TaskStatusCompleted,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.load(tc.task(env))

			allow := env.refuseSaves()

			err := tc.decide(manager)
			expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)
			expectStillAwaiting(t, manager, tc.status, tc.kind)

			if got := current(t, manager).Instances[0].Certificates.DecisionRequestID; got != "" {
				t.Fatalf("expected no request ID recorded before the save, got %q", got)
			}

			allow()

			err = tc.decide(manager)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}

			inst := current(t, manager).Instances[0]
			if inst.Status != tc.settled || inst.Certificates.Pending != nil {
				t.Fatalf("expected %s with no pending decision, got %s with %+v", tc.settled, inst.Status, inst.Certificates.Pending)
			}

			saved := env.stored().Instances[0]
			if saved.Status != tc.settled || saved.Certificates.DecisionRequestID != "retry-1" {
				t.Fatalf("expected Vault to hold %s with the request ID, got %s with %q", tc.settled, saved.Status, saved.Certificates.DecisionRequestID)
			}

			expectReleased(t, manager)
			waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status == tc.finished })
		})
	}
}

// A repeat of an approval whose save was not confirmed saves the task
// again, and answers as the first did only once the save is confirmed.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionReplaySavesAgain(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())

	preview, err := manager.RepreviewForDecision(context.Background(), certTaskID, waitInstanceID)
	if err != nil {
		t.Fatalf("RepreviewForDecision: %v", err)
	}

	req := ApproveRequest{PreviewID: preview.ID, ClientRequestID: "approve-1"}
	allow := env.refuseSaves()

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, req)
	expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, req)
	expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

	allow()

	err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if got := env.stored().Instances[0].Certificates.DecisionRequestID; got != "approve-1" {
		t.Fatalf("expected Vault to hold the approval's request ID, got %q", got)
	}
}

// With rotation off, an operator can still free a held instance, since a
// skip or an abandon settles only the broker's own state.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestSettlingDecisionsWithRotationOff(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		env := newRedeployEnv(t)
		manager := env.load(env.awaitingApproval())
		env.setSwitches(false)

		err := manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{PreviewID: "pv-any"})
		expectAPIError(t, err, http.StatusForbidden, apierr.CodeRotationDisabled)

		err = manager.ApproveChangedEffects(context.Background(), certTaskID, waitInstanceID, ApproveRequest{Action: "skip"})
		if err != nil {
			t.Fatalf("ApproveChangedEffects: %v", err)
		}

		if status := current(t, manager).Instances[0].Status; status != InstanceStatusSkipped {
			t.Fatalf("expected the instance skipped, got %s", status)
		}

		expectReleased(t, manager)
	})

	t.Run("abandon", func(t *testing.T) {
		env := newRedeployEnv(t)
		env.madeNewVersion()

		manager := env.load(env.awaitingReconcile(nil))
		env.setSwitches(false)

		err := manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{Variable: regenLeaf, Action: "abandon"})
		if err != nil {
			t.Fatalf("ReconcileRegeneration: %v", err)
		}

		if status := current(t, manager).Instances[0].Status; status != InstanceStatusFailed {
			t.Fatalf("expected the instance failed, got %s", status)
		}

		expectReleased(t, manager)
	})
}

// A decision request that names an action its endpoint does not take is
// an invalid request, and the instance keeps waiting.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionUnknownAction(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingApproval())
	ctx := context.Background()

	err := manager.ApproveChangedEffects(ctx, certTaskID, waitInstanceID, ApproveRequest{Action: "shuffle"})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)

	err = manager.ReconcileRegeneration(ctx, certTaskID, waitInstanceID, ReconcileRequest{Variable: regenLeaf, Action: "shuffle"})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)

	err = manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "shuffle"})
	expectAPIError(t, err, http.StatusBadRequest, apierr.CodeInvalidRequest)

	expectStillAwaiting(t, manager, StatusAwaitingDecision, DecisionApproveChangedEffects)
}

// A resolve's verification runs under the manager's context, so a client
// that has gone away does not cut it short or fail the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveVerifyOutlivesRequest(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingOutcome())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := manager.ResolveDeployOutcome(ctx, certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	if status := current(t, manager).Instances[0].Status; status != InstanceStatusSuccess {
		t.Fatalf("expected the instance verified, got %s", status)
	}

	expectReleased(t, manager)
}

// A resolve's verification that finds the old version deployed fails the
// instance with what it found, and frees it.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveVerifyFails(t *testing.T) {
	env := newRedeployEnv(t)
	stored := env.awaitingOutcome()

	env.director.mu.Lock()
	env.director.deployed = "v1"
	env.director.mu.Unlock()

	manager := env.load(stored)

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	task := current(t, manager)
	inst := task.Instances[0]

	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "not the new version v2") || task.FailedCount != 1 {
		t.Fatalf("expected the instance failed for the old version, got %s: %q with %d failed", inst.Status, inst.Error, task.FailedCount)
	}

	expectReleased(t, manager)
	waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status == TaskStatusFailed })
}

// While the director still runs the instance's deploy task, verify and
// failed answer deployment_locked and the instance keeps waiting, and
// abandon, the operator's override, still goes ahead.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveWhileDeployRuns(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.awaitingOutcome())
	env.director.runTask(true)

	for _, action := range []string{"verify", "failed"} {
		err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: action})
		expectAPIError(t, err, http.StatusConflict, apierr.CodeDeploymentLocked)
	}

	expectStillAwaiting(t, manager, InstanceStatusOutcomeUnknown, DecisionResolveDeployOutcome)

	if lists := env.director.listCalls(); lists != 0 {
		t.Fatalf("expected no verification while the deploy runs, got %d variable lists", lists)
	}

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "abandon"})
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}

	expectReleased(t, manager)
}

// A deploy task the director has purged answers task not found, and it
// has ended, so verify and failed settle the instance. Any other director
// error still leaves the instance waiting.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveAfterDeployTaskPurged(t *testing.T) {
	purged := &boshapi.APIError{StatusCode: http.StatusNotFound, Code: boshapi.ErrorCodeTaskNotFound, Description: "Task 4711 not found"}

	for _, tc := range []struct {
		action string
		status InstanceStatus
	}{
		{action: "verify", status: InstanceStatusSuccess},
		{action: "failed", status: InstanceStatusFailed},
	} {
		t.Run(tc.action, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.load(env.awaitingOutcome())

			env.director.mu.Lock()
			env.director.taskErr = fmt.Errorf("get BOSH task: %w", purged)
			env.director.mu.Unlock()

			err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: tc.action})
			if err != nil {
				t.Fatalf("ResolveDeployOutcome: %v", err)
			}

			if inst := current(t, manager).Instances[0]; inst.Status != tc.status || inst.Certificates.Pending != nil {
				t.Fatalf("expected a settled %s instance, got %s with %+v", tc.status, inst.Status, inst.Certificates.Pending)
			}

			expectReleased(t, manager)
		})
	}

	for name, failure := range map[string]error{
		"route not found": &boshapi.APIError{StatusCode: http.StatusNotFound, Description: "Endpoint '/tasks/4711' not found."},
		"server error":    &boshapi.APIError{StatusCode: http.StatusInternalServerError, Code: 100},
	} {
		t.Run(name, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.load(env.awaitingOutcome())

			env.director.mu.Lock()
			env.director.taskErr = failure
			env.director.mu.Unlock()

			err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
			expectAPIError(t, err, http.StatusInternalServerError, apierr.CodeInternal)

			expectStillAwaiting(t, manager, InstanceStatusOutcomeUnknown, DecisionResolveDeployOutcome)
		})
	}
}
