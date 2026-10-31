package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// The actions an operator takes on a pending decision.
const (
	actionAdopt   = "adopt"
	actionAbandon = "abandon"
	actionSkip    = "skip"
	actionVerify  = "verify"
	actionFailed  = "failed"
)

// The endpoints a decision's digest names, so one request ID can't be
// replayed against another kind of decision.
const (
	endpointReconcile = "reconcile"
	endpointApprove   = "approve"
	endpointResolve   = "resolve"
)

// ReconcileRequest is the body of a reconcile decision. Adopt names the
// candidate version to adopt, and abandon gives up the regeneration.
type ReconcileRequest struct {
	Variable        string `json:"variable"`
	Action          string `json:"action"`
	VersionID       string `json:"version_id,omitempty"`
	ClientRequestID string `json:"client_request_id,omitempty"`
}

// ApproveRequest is the body of an approve decision. It names the preview
// that RepreviewForDecision bound to the decision, or skips the instance
// with the action skip.
type ApproveRequest struct {
	PreviewID       string `json:"preview_id,omitempty"`
	Action          string `json:"action,omitempty"`
	ClientRequestID string `json:"client_request_id,omitempty"`
}

// ResolveRequest is the body of a resolve decision, whose action is
// verify, failed, or abandon.
type ResolveRequest struct {
	Action          string `json:"action"`
	ClientRequestID string `json:"client_request_id,omitempty"`
}

// RepreviewForDecision builds the single-instance preview an operator
// approves changed effects with. The instance must await an
// approve_changed_effects decision. The preview uses the task's own mode,
// converge policy, and the instance's selected variables, and runs as the
// task, so the task's own reservation does not block it while any other
// holder does. It is bound to the task, the instance, and the decision's
// Since, so it approves that decision and nothing else.
func (m *Manager) RepreviewForDecision(ctx context.Context, taskID, instanceID string) (*Preview, error) {
	err := m.decisionsReady()
	if err != nil {
		return nil, err
	}

	m.mu.RLock()

	task, inst, err := m.decisionInstanceLocked(taskID, instanceID)
	if err == nil {
		err = pendingDecisionLocked(inst, DecisionApproveChangedEffects)
	}

	if err != nil {
		m.mu.RUnlock()

		return nil, err
	}

	mode := certTaskMode(task)
	converge := taskConverge(task)
	names := selectedNamesLocked(inst)
	since := inst.Certificates.Pending.Since

	m.mu.RUnlock()

	err = m.certWritesAllowed(mode)
	if err != nil {
		return nil, err
	}

	err = m.credhubReady()
	if err != nil {
		return nil, err
	}

	d := m.certDeps()
	if d.Previews == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for previews.")
	}

	req := PreviewRequest{
		Mode:       mode,
		Converge:   converge,
		Selections: []Selection{{InstanceID: instanceID, Variables: names}},
	}

	previewed := m.previewInstance(ctx, req, req.Selections[0], taskID)
	if previewed.failure != nil {
		return nil, previewed.failure
	}

	preview := &Preview{
		ID:            previewIDPrefix + uuid.NewString(),
		Instances:     []PreviewInstance{previewed},
		Warnings:      []string{},
		mode:          mode,
		converge:      converge,
		forTask:       taskID,
		forInstance:   instanceID,
		decisionSince: since,
		selection:     names,
	}

	if mode == RotationModePatch {
		preview.Warnings = append(preview.Warnings, patchWarning)
	}

	d.Previews.Put(preview, m.previewTTL())

	return preview, nil
}

// ApproveChangedEffects answers an approve_changed_effects decision. With
// a preview_id it approves the effects of a preview that
// RepreviewForDecision bound to this decision, copies them into the
// instance's approval, and resumes the task. It checks the switches first,
// because the approval lets the task go on to write. With the action skip
// it marks the instance skipped and frees it, whatever the switches say,
// since a skip settles only the broker's own state.
func (m *Manager) ApproveChangedEffects(ctx context.Context, taskID, instanceID string, req ApproveRequest) error {
	err := m.decisionsReady()
	if err != nil {
		return err
	}

	switch req.Action {
	case "":
		if req.PreviewID == "" {
			return apierr.New(http.StatusBadRequest, apierr.CodeApprovalMismatch,
				"Name the preview_id of a preview of this decision, or skip the instance with the action skip.")
		}

		err = m.certWritesAllowed(m.decisionMode(taskID))
		if err != nil {
			return err
		}
	case actionSkip:
	default:
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			fmt.Sprintf("The action %q is not one an approval takes. Send a preview_id, or the action skip.", req.Action))
	}

	unlock := m.lockRequest(decisionLockKey(taskID, instanceID))
	defer unlock()

	body := req
	body.ClientRequestID = ""

	digest, err := decisionDigest(endpointApprove, body)
	if err != nil {
		return err
	}

	// The preview is read before mu is taken, and its error is answered
	// only after a replay has had its chance to answer.
	var (
		preview  *Preview
		fetchErr error
	)

	if req.Action == "" {
		preview, fetchErr = m.boundPreview(req.PreviewID)
	}

	m.mu.Lock()

	task, inst, replayed, err := m.awaitedLocked(taskID, instanceID, DecisionApproveChangedEffects, req.ClientRequestID, digest)
	if err != nil || replayed {
		m.mu.Unlock()

		return m.answerReplay(ctx, task, replayed, err)
	}

	if req.Action == actionSkip {
		now := m.certNowLocked()
		decision := m.decisionAuditLocked(ctx, DecisionApproveChangedEffects, actionSkip, "")

		err = m.applyConfirmedLocked(ctx, task, inst, func(task *UpgradeTask, inst *InstanceUpgrade) {
			skipInstanceLocked(task, inst, now)
			recordDecisionLocked(inst, req.ClientRequestID, digest, decision)
		})
		if err != nil {
			return err
		}

		m.resumeAfterDecision(ctx, task, false)

		return nil
	}

	err = approvalMismatchLocked(task, inst, preview, fetchErr)
	if err != nil {
		m.mu.Unlock()

		return err
	}

	approved := previewFor(preview, instanceID)

	inst.Certificates.Approved = approved.approved.Clone()
	inst.Certificates.Pending = nil
	inst.Status = InstanceStatusRunning
	inst.Error = ""
	recordDecisionLocked(inst, req.ClientRequestID, digest, m.decisionAuditLocked(ctx, DecisionApproveChangedEffects, "approve", "preview "+preview.ID))

	m.auditDecisionLocked(task, inst)
	m.mu.Unlock()

	return m.saveDecision(ctx, task, false)
}

// ReconcileRegeneration answers a reconcile_regeneration decision. Adopt
// takes a candidate version that passed every check as the variable's new
// version and resumes the task, whose runner re-checks the approved
// effects before the deploy. It checks the switches first, because an
// adoption lets the task go on to write. Abandon fails the instance with
// the regeneration abandoned and frees it, whatever the switches say,
// since it settles only the broker's own state.
func (m *Manager) ReconcileRegeneration(ctx context.Context, taskID, instanceID string, req ReconcileRequest) error {
	err := m.decisionsReady()
	if err != nil {
		return err
	}

	switch req.Action {
	case actionAdopt:
		if req.VersionID == "" {
			return apierr.New(http.StatusBadRequest, apierr.CodeCandidateInvalid, "Name the version_id of the candidate to adopt.")
		}

		err = m.certWritesAllowed(m.decisionMode(taskID))
		if err != nil {
			return err
		}
	case actionAbandon:
	default:
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			fmt.Sprintf("The action %q is not one a reconciliation takes. Send adopt or abandon.", req.Action))
	}

	unlock := m.lockRequest(decisionLockKey(taskID, instanceID))
	defer unlock()

	body := req
	body.ClientRequestID = ""

	digest, err := decisionDigest(endpointReconcile, body)
	if err != nil {
		return err
	}

	// The settings are read before mu, which their read takes too.
	settingsPause := m.pausesOnFailure()

	m.mu.Lock()

	task, inst, replayed, err := m.awaitedLocked(taskID, instanceID, DecisionReconcileRegeneration, req.ClientRequestID, digest)
	if err == nil && !replayed {
		err = reconcilesVariableLocked(inst, req.Variable)
	}

	if err != nil || replayed {
		m.mu.Unlock()

		return m.answerReplay(ctx, task, replayed, err)
	}

	if req.Action == actionAbandon {
		now := m.certNowLocked()
		decision := m.decisionAuditLocked(ctx, DecisionReconcileRegeneration, actionAbandon, "variable "+req.Variable)

		err = m.applyConfirmedLocked(ctx, task, inst, func(task *UpgradeTask, inst *InstanceUpgrade) {
			abandonRegenerationLocked(task, inst, now)
			holdDecidedFailureLocked(task, inst, settingsPause)
			recordDecisionLocked(inst, req.ClientRequestID, digest, decision)
		})
		if err != nil {
			return err
		}

		m.resumeAfterDecision(ctx, task, true)

		return nil
	}

	if task.Status == TaskStatusCancelled {
		m.mu.Unlock()

		return cancelledDecision("abandon the regeneration")
	}

	pending := inst.Certificates.Pending
	since := pending.Since
	candidates := cloneEach(pending.Candidates, Candidate.Clone)
	variable, _ := variableNamed(inst, req.Variable)

	m.mu.Unlock()

	// A decision loaded after a restart from sending has no candidates on
	// record, so they are listed and checked now, as the runner lists them.
	if len(candidates) == 0 {
		candidates, err = m.listCandidates(ctx, m.certDeps().CredHub, variable)
		if err != nil {
			return credhubUnavailable("the check of the candidate versions", err)
		}
	}

	if !validCandidate(candidates, req.VersionID) {
		return apierr.New(http.StatusBadRequest, apierr.CodeCandidateInvalid,
			fmt.Sprintf("Version %s of %s is not a candidate that passed every check, so it can't be adopted.", req.VersionID, shortName(req.Variable)))
	}

	m.mu.Lock()

	err = sameDecisionLocked(task, inst, DecisionReconcileRegeneration, req.Variable, since)
	if err != nil {
		m.mu.Unlock()

		return err
	}

	_, index := variableNamed(inst, req.Variable)
	inst.Certificates.Variables[index].RegenerateState = RegenerateAdopted
	inst.Certificates.Variables[index].NewVersionID = req.VersionID
	inst.Certificates.Pending = nil
	inst.Status = InstanceStatusRunning
	inst.Error = ""
	recordDecisionLocked(inst, req.ClientRequestID, digest, m.decisionAuditLocked(ctx, DecisionReconcileRegeneration, actionAdopt, "variable "+req.Variable+" version "+req.VersionID))

	m.auditDecisionLocked(task, inst)
	m.mu.Unlock()

	return m.saveDecision(ctx, task, false)
}

// RefreshCandidates lists again, for a reconcile_regeneration decision,
// every version of its variable that is not in the baseline, checks each
// as the runner does, and stores them on the decision with the time of
// the check. A decision loaded after a crash has none on record, and a
// regeneration can finish after the first listing, so this is how an
// operator finds a version to adopt. It only reads CredHub and writes
// nothing there or to the director, so it needs no switch, and it works
// on a cancelled task, whose operator can still abandon. A decision that
// changed while CredHub was read answers 409 and stores nothing.
func (m *Manager) RefreshCandidates(ctx context.Context, taskID, instanceID string) error {
	err := m.decisionsReady()
	if err != nil {
		return err
	}

	unlock := m.lockRequest(decisionLockKey(taskID, instanceID))
	defer unlock()

	m.mu.RLock()

	task, inst, err := m.decisionInstanceLocked(taskID, instanceID)
	if err == nil {
		err = pendingDecisionLocked(inst, DecisionReconcileRegeneration)
	}

	if err == nil {
		err = reconcilesVariableLocked(inst, inst.Certificates.Pending.Variable)
	}

	if err != nil {
		m.mu.RUnlock()

		return err
	}

	since := inst.Certificates.Pending.Since
	variable, _ := variableNamed(inst, inst.Certificates.Pending.Variable)

	m.mu.RUnlock()

	err = m.credhubReady()
	if err != nil {
		return err
	}

	candidates, err := m.listCandidates(ctx, m.certDeps().CredHub, variable)
	if err != nil {
		return credhubUnavailable("the check of the candidate versions", err)
	}

	m.mu.Lock()

	err = pendingDecisionLocked(inst, DecisionReconcileRegeneration)
	if err == nil && (!inst.Certificates.Pending.Since.Equal(since) || inst.Certificates.Pending.Variable != variable.Name) {
		err = apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision,
			fmt.Sprintf("Instance %s now waits for another decision, so read the task again.", inst.InstanceID))
	}

	if err != nil {
		m.mu.Unlock()

		return err
	}

	now := m.certNowLocked()
	inst.Certificates.Pending.Candidates = candidates
	inst.Certificates.Pending.CandidatesCheckedAt = &now
	task.Revision++

	m.mu.Unlock()

	// The candidates are a record of a read, which the next refresh or
	// adoption makes again, so a save that fails is logged and the
	// refresh still answers with what it found.
	err = m.persistTask(context.WithoutCancel(ctx), task)
	if err != nil {
		m.logger.Error("Failed to save the candidates of instance %s of certificate task %s: %v", instanceID, taskID, err)
	}

	return nil
}

// ResolveDeployOutcome answers a resolve_deploy_outcome decision. Verify
// runs the redeploy's verification now and settles the instance by its
// result. Failed fails the instance and frees it. Abandon leaves the
// outcome unknown for good, closes the instance, and frees it. Verify and
// failed first check that the director has ended the instance's deploy
// task, and answer deployment_locked while it runs. It works on
// a cancelled task too, so a cancelled task never holds an instance with
// nothing following it, and it needs no switch, since it writes nothing to
// CredHub or the director.
func (m *Manager) ResolveDeployOutcome(ctx context.Context, taskID, instanceID string, req ResolveRequest) error {
	err := m.decisionsReady()
	if err != nil {
		return err
	}

	switch req.Action {
	case actionVerify, actionFailed, actionAbandon:
	default:
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			fmt.Sprintf("The action %q is not one a resolution takes. Send verify, failed, or abandon.", req.Action))
	}

	unlock := m.lockRequest(decisionLockKey(taskID, instanceID))
	defer unlock()

	body := req
	body.ClientRequestID = ""

	digest, err := decisionDigest(endpointResolve, body)
	if err != nil {
		return err
	}

	// The settings are read before mu, which their read takes too.
	settingsPause := m.pausesOnFailure()

	m.mu.Lock()

	task, inst, replayed, err := m.awaitedLocked(taskID, instanceID, DecisionResolveDeployOutcome, req.ClientRequestID, digest)
	if err != nil || replayed {
		m.mu.Unlock()

		return m.answerReplay(ctx, task, replayed, err)
	}

	since := inst.Certificates.Pending.Since

	// Verify and failed need the deploy to have ended, and abandon is the
	// operator's override, so it goes ahead while the director still runs
	// the deploy.
	if req.Action != actionAbandon {
		taskID, deployment := recordedDeployTask(inst), inst.DeploymentName

		m.mu.Unlock()

		err = m.deployTaskEnded(taskID, deployment)
		if err != nil {
			return err
		}

		m.mu.Lock()

		err = sameDecisionLocked(task, inst, DecisionResolveDeployOutcome, "", since)
		if err != nil {
			m.mu.Unlock()

			return err
		}
	}

	var (
		ok      bool
		message string
		detail  string
	)

	switch req.Action {
	case actionVerify:
		m.mu.Unlock()

		verifyErr := m.verifyNow(task, inst) //nolint:contextcheck // The verification runs on the manager's lifecycle context, so a finished request never cancels it.
		if errors.Is(verifyErr, errVerifyUnreadable) {
			return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
				"Blacksmith could not verify the deploy now, so the instance still waits for this decision: "+verifyErr.Error())
		}

		m.mu.Lock()

		err = sameDecisionLocked(task, inst, DecisionResolveDeployOutcome, "", since)
		if err != nil {
			m.mu.Unlock()

			return err
		}

		ok = verifyErr == nil
		if !ok {
			message = verifyErr.Error()
			detail = "verification failed"
		}
	case actionFailed:
		message = "The operator marked the deploy failed. " + unappliedNoteLocked(task, inst)
	}

	now := m.certNowLocked()
	decision := m.decisionAuditLocked(ctx, DecisionResolveDeployOutcome, req.Action, detail)

	err = m.applyConfirmedLocked(ctx, task, inst, func(task *UpgradeTask, inst *InstanceUpgrade) {
		if req.Action == actionAbandon {
			abandonOutcomeLocked(task, inst, now)
		} else {
			settleOutcomeLocked(task, inst, ok, message, now)
			holdDecidedFailureLocked(task, inst, settingsPause)
		}

		recordDecisionLocked(inst, req.ClientRequestID, digest, decision)
	})
	if err != nil {
		return err
	}

	m.resumeAfterDecision(ctx, task, req.Action != actionAbandon && !ok)

	return nil
}

// recordedDeployTask returns the BOSH task ID of the instance's deploy,
// from the task it follows or else from its deploy's recorded end, or 0
// when it has neither. The caller holds mu.
func recordedDeployTask(inst *InstanceUpgrade) int {
	if taskID := followedTaskID(inst); taskID > 0 {
		return taskID
	}

	if inst.Certificates != nil && inst.Certificates.Deploy != nil {
		return inst.Certificates.Deploy.BOSHTaskID
	}

	return 0
}

// deployTaskEnded asks the director whether the instance's deploy task
// has ended, under the manager's context, before a resolve settles the
// instance by its outcome. A task that still runs answers 409
// deployment_locked, and a task the director can't report on answers 500,
// and either way the instance keeps waiting for the decision. A task the
// director no longer knows, as after it purges old tasks, has ended. An
// instance with no recorded task has nothing to wait for.
func (m *Manager) deployTaskEnded(taskID int, deployment string) error {
	if taskID <= 0 {
		return nil
	}

	boshTask, err := directorCall(m.ctx, func(c context.Context) (*bosh.Task, error) {
		return m.director.GetTask(c, taskID)
	})
	if directorTaskGone(err) {
		m.logger.Info("Director task %d of deployment %s is no longer known to the director, so it has ended", taskID, deployment)

		return nil
	}

	if err != nil || boshTask == nil {
		if err == nil {
			err = errors.New("the director reported no task")
		}

		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("Blacksmith could not read director task %d of deployment %s, so the instance still waits for this decision: %v", taskID, deployment, err))
	}

	if directorTaskRunning(boshTask.State) {
		return apierr.New(http.StatusConflict, apierr.CodeDeploymentLocked,
			fmt.Sprintf("The director is still running task %d on deployment %s, so its outcome is not known yet. Wait for it to end, or abandon the instance.", taskID, deployment))
	}

	return nil
}

// directorTaskGone reports whether the director answered 404 task not
// found, which it does for a task it has purged.
func directorTaskGone(err error) bool {
	var apiErr *boshapi.APIError

	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Code == boshapi.ErrorCodeTaskNotFound
}

// directorTaskRunning reports whether a director task in the state given
// has not ended.
func directorTaskRunning(state string) bool {
	switch state {
	case "queued", "processing", "cancelling":
		return true
	default:
		return false
	}
}

// verifyNow runs a resolve's verification of the instance's deploy under
// the manager's context and its own deadline, so a client that goes away
// does not fail the instance. A converge deploy is reconciled, as
// verifyDeploy describes. A verification that ctx's end cut short could
// not read what it checks, so the instance keeps waiting.
func (m *Manager) verifyNow(task *UpgradeTask, inst *InstanceUpgrade) error {
	ctx, cancel := context.WithTimeout(m.ctx, resolveVerifyTimeout)
	defer cancel()

	err := m.verifyDeploy(ctx, task, inst)
	if err != nil && ctx.Err() != nil && !errors.Is(err, errVerifyUnreadable) {
		return fmt.Errorf("%w: the verification did not finish within %v: %w", errVerifyUnreadable, resolveVerifyTimeout, err)
	}

	return err
}

// decisionsReady turns a decision away until the loader has put every
// stored task in m.tasks, since until then the task may be missing.
func (m *Manager) decisionsReady() error {
	return m.tasksReady()
}

// DecisionTaskKind is the kind of the task a decision names, for a gate
// that runs before the decision. While the stored tasks load, or when the
// task is missing, it turns the decision away with the error the decision
// itself would give.
func (m *Manager) DecisionTaskKind(taskID string) (TaskKind, error) {
	err := m.decisionsReady()
	if err != nil {
		return "", err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return "", noCertificateTask(taskID)
	}

	return task.KindOrDefault(), nil
}

// noCertificateTask is the error of a decision on a task that is missing
// or is not a certificate task.
func noCertificateTask(taskID string) error {
	return apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound,
		fmt.Sprintf("Blacksmith has no certificate task %s.", taskID))
}

// decisionMode is the mode of the task, whose switches a decision checks
// before it looks the instance up. An unknown task reads as a redeploy,
// which needs only the rotation switch.
func (m *Manager) decisionMode(taskID string) RotationMode {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return RotationModeRedeploy
	}

	return certTaskMode(task)
}

// decisionLockKey is the request lock that serialises the decisions on one
// instance of one task. Its prefix keeps it apart from any client request
// ID.
func decisionLockKey(taskID, instanceID string) string {
	return "decision\x00" + taskID + "\x00" + instanceID
}

// decisionDigest is the digest of a decision's body, without its request
// ID, under the name of its endpoint.
func decisionDigest(endpoint string, body any) (string, error) {
	digest, err := requestDigest(struct {
		Endpoint string `json:"endpoint"`
		Body     any    `json:"body"`
	}{Endpoint: endpoint, Body: body})
	if err != nil {
		return "", apierr.Wrap(http.StatusInternalServerError, apierr.CodeInternal, err)
	}

	return digest, nil
}

// decisionInstanceLocked finds the instance of the certificate task. The
// caller holds mu.
func (m *Manager) decisionInstanceLocked(taskID, instanceID string) (*UpgradeTask, *InstanceUpgrade, error) {
	task, ok := m.tasks[taskID]
	if !ok || task.KindOrDefault() != TaskKindCertificates {
		return nil, nil, noCertificateTask(taskID)
	}

	for i := range task.Instances {
		if task.Instances[i].InstanceID == instanceID {
			return task, &task.Instances[i], nil
		}
	}

	return nil, nil, apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound,
		fmt.Sprintf("Certificate task %s has no instance %s.", taskID, instanceID))
}

// awaitedLocked finds the instance a decision of the kind given answers,
// and reports whether the request repeats the decision applied last. A
// repeat with the same body answers as the first did, one with another
// body is a conflict, and any other request needs the decision still
// pending. The caller holds mu.
func (m *Manager) awaitedLocked(taskID, instanceID, kind, requestID, digest string) (*UpgradeTask, *InstanceUpgrade, bool, error) {
	task, inst, err := m.decisionInstanceLocked(taskID, instanceID)
	if err != nil {
		return nil, nil, false, err
	}

	rotation := inst.Certificates
	if requestID != "" && rotation != nil && rotation.DecisionRequestID == requestID {
		if rotation.DecisionDigest != digest {
			return nil, nil, false, apierr.New(http.StatusConflict, apierr.CodeRequestIDConflict,
				"This client_request_id already answered a decision with another body.")
		}

		return task, inst, true, nil
	}

	err = pendingDecisionLocked(inst, kind)
	if err != nil {
		return nil, nil, false, err
	}

	return task, inst, false, nil
}

// pendingDecisionLocked checks that the instance waits for a decision of
// the kind given. A reconcile or approve decision waits in
// awaiting_decision, and a resolve decision in outcome_unknown. The caller
// holds mu.
func pendingDecisionLocked(inst *InstanceUpgrade, kind string) error {
	status := StatusAwaitingDecision
	if kind == DecisionResolveDeployOutcome {
		status = InstanceStatusOutcomeUnknown
	}

	rotation := inst.Certificates
	if inst.Status == status && rotation != nil && rotation.Pending != nil && rotation.Pending.Kind == kind {
		return nil
	}

	return apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision,
		fmt.Sprintf("Instance %s is not waiting for a %s decision.", inst.InstanceID, kind))
}

// sameDecisionLocked checks, after a step that ran without mu, that the
// instance still waits for the decision it waited for before. The caller
// holds mu.
func sameDecisionLocked(task *UpgradeTask, inst *InstanceUpgrade, kind, variable string, since time.Time) error {
	err := pendingDecisionLocked(inst, kind)
	if err != nil {
		return err
	}

	pending := inst.Certificates.Pending
	if !pending.Since.Equal(since) || pending.Variable != variable {
		return apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision,
			fmt.Sprintf("Instance %s now waits for another decision, so read the task again.", inst.InstanceID))
	}

	if kind != DecisionResolveDeployOutcome && task.Status == TaskStatusCancelled {
		return cancelledDecision("answer the decision another way")
	}

	return nil
}

// reconcilesVariableLocked checks that the pending reconcile decision is
// about the variable the request names. The caller holds mu.
func reconcilesVariableLocked(inst *InstanceUpgrade, name string) error {
	pending := inst.Certificates.Pending
	if pending.Variable == name {
		if _, index := variableNamed(inst, name); index >= 0 {
			return nil
		}
	}

	return apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision,
		fmt.Sprintf("Instance %s waits for a decision about %s, not %s.", inst.InstanceID, shortName(pending.Variable), shortName(name)))
}

// variableNamed returns the instance's record of the variable and its
// index, or -1 when the instance has none.
func variableNamed(inst *InstanceUpgrade, name string) (VariableRotation, int) {
	for i, variable := range inst.Certificates.Variables {
		if variable.Name == name {
			return variable.Clone(), i
		}
	}

	return VariableRotation{}, -1
}

// validCandidate reports whether the version is a candidate that passed
// every check.
func validCandidate(candidates []Candidate, versionID string) bool {
	for _, candidate := range candidates {
		if candidate.VersionID == versionID {
			return candidate.Valid
		}
	}

	return false
}

// cancelledDecision refuses a decision that would let a cancelled task go
// on to write, naming what the operator can do instead.
func cancelledDecision(instead string) error {
	return apierr.New(http.StatusConflict, apierr.CodeInvalidTaskState,
		"The task was cancelled, so it changes nothing more. "+capitalFirst(instead)+" instead.")
}

// capitalFirst returns the text with its first letter in upper case.
func capitalFirst(text string) string {
	if text == "" || text[0] < 'a' || text[0] > 'z' {
		return text
	}

	return string(text[0]-'a'+'A') + text[1:]
}

// recordDecisionLocked records the request ID and digest of the decision
// applied, so a repeat of it applies once, and the decision's audit
// details, so the record is rebuilt after a crash. The caller holds mu.
func recordDecisionLocked(inst *InstanceUpgrade, requestID, digest string, decision DecisionAudit) {
	decision.Outcome = string(inst.Status)
	inst.Certificates.Decision = &decision

	if requestID == "" {
		return
	}

	inst.Certificates.DecisionRequestID = requestID
	inst.Certificates.DecisionDigest = digest
}

// boundPreview reads a stored preview for an approval.
func (m *Manager) boundPreview(previewID string) (*Preview, error) {
	d := m.certDeps()
	if d.Previews == nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for previews.")
	}

	preview, err := d.Previews.Get(previewID)
	if err != nil {
		return nil, err //nolint:wrapcheck // the store answers with the API error
	}

	return preview, nil
}

// approvalMismatchLocked checks that the preview approves the decision the
// instance waits for. It must be bound to this task, this instance, and
// the decision's Since, and match the task's mode, converge policy, and
// the instance's selected variables, and its instance must not be
// blocked. The caller holds mu.
func approvalMismatchLocked(task *UpgradeTask, inst *InstanceUpgrade, preview *Preview, fetchErr error) error {
	if fetchErr != nil {
		return fetchErr
	}

	if task.Status == TaskStatusCancelled {
		return cancelledDecision("skip the instance")
	}

	since := inst.Certificates.Pending.Since
	if preview.forTask != task.ID || preview.forInstance != inst.InstanceID || !preview.decisionSince.Equal(since) ||
		previewFor(preview, inst.InstanceID) == nil {
		return apierr.New(http.StatusConflict, apierr.CodeNotAwaitingDecision,
			fmt.Sprintf("Preview %s was not built for the decision instance %s waits for now, so preview the instance again from its decision.",
				preview.ID, inst.InstanceID))
	}

	mode := certTaskMode(task)
	if preview.mode != mode {
		return apierr.New(http.StatusBadRequest, apierr.CodeApprovalMismatch,
			fmt.Sprintf("The preview's mode %s differs from the task's mode %s.", preview.mode, mode))
	}

	converge := taskConverge(task)
	if preview.converge != converge {
		return apierr.New(http.StatusBadRequest, apierr.CodeApprovalMismatch,
			fmt.Sprintf("The preview's converge policy %q differs from the task's converge policy %q.", preview.converge, converge))
	}

	if !slices.Equal(sortedNames(preview.selection), selectedNamesLocked(inst)) {
		return apierr.New(http.StatusBadRequest, apierr.CodeApprovalMismatch,
			"The preview's selected variables differ from the instance's selected variables.")
	}

	blocked := previewFor(preview, inst.InstanceID).Blocked
	if blocked != nil {
		return apierr.New(http.StatusConflict, blocked.Code, blocked.Message)
	}

	return nil
}

// previewFor returns the preview's entry for the instance, or nil.
func previewFor(preview *Preview, instanceID string) *PreviewInstance {
	for i := range preview.Instances {
		if preview.Instances[i].InstanceID == instanceID {
			return &preview.Instances[i]
		}
	}

	return nil
}

// taskConverge returns the task's converge policy. The caller holds mu
// for reading.
func taskConverge(task *UpgradeTask) ConvergePolicy {
	if task.Certificates == nil {
		return ""
	}

	return task.Certificates.Converge
}

// selectedNamesLocked returns the full names the instance selected,
// sorted. They come from the approved effects, or from the variables'
// records when the approval names none. The caller holds mu.
func selectedNamesLocked(inst *InstanceUpgrade) []string {
	rotation := inst.Certificates
	names := make([]string, 0, len(rotation.Approved.Selected))

	for _, pair := range rotation.Approved.Selected {
		names = append(names, pair.Name)
	}

	if len(names) == 0 {
		for _, variable := range rotation.Variables {
			names = append(names, variable.Name)
		}
	}

	return sortedNames(names)
}

// sortedNames returns a sorted copy of the names.
func sortedNames(names []string) []string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)

	return sorted
}

// skipInstanceLocked marks an instance that waits for a new approval
// skipped at the time given and closes its rotation. The caller holds mu.
func skipInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade, now time.Time) {
	message := "The operator skipped this instance instead of approving its changed effects."

	if regenerationStartedLocked(inst) {
		message += " " + deployPendingNote
	}

	inst.Status = InstanceStatusSkipped
	inst.CompletedAt = &now
	inst.Error = message
	task.SkippedCount++

	closeRotationLocked(inst)
}

// abandonRegenerationLocked fails an instance whose regeneration an
// operator abandoned, at the time given, and closes its rotation. Any
// version CredHub made stays there, undeployed, and the inventory shows it
// as deploy_pending. The caller holds mu.
func abandonRegenerationLocked(task *UpgradeTask, inst *InstanceUpgrade, now time.Time) {
	pending := inst.Certificates.Pending
	_, index := variableNamed(inst, pending.Variable)
	inst.Certificates.Variables[index].RegenerateState = RegenerateAbandoned

	inst.Status = InstanceStatusFailed
	inst.CompletedAt = &now
	inst.Error = shortName(pending.Variable) + ": regeneration abandoned. Any version CredHub made stays there and is not deployed."
	task.FailedCount++

	closeRotationLocked(inst)
}

// abandonOutcomeLocked closes an instance whose deploy outcome an operator
// abandoned, at the time given. It stays outcome_unknown, which is now
// final. The caller holds mu.
func abandonOutcomeLocked(task *UpgradeTask, inst *InstanceUpgrade, now time.Time) {
	inst.CompletedAt = &now
	inst.Error = "The operator abandoned this deploy, so its outcome stays unknown. Check which certificates the deployment serves. " + unappliedNoteLocked(task, inst)
	task.UnknownCount++

	closeRotationLocked(inst)
}

// applyConfirmedLocked applies a decision that settles the instance only
// once Vault holds it. It applies the decision to a copy of the task first
// and saves the copy with confirmation, so a save that is not confirmed
// changes nothing in memory: the instance still waits, the task still
// holds it, and the same request sent again applies and saves it again.
// Once the save is confirmed, it applies the decision to the live task,
// frees the instance, and has the inventory scan it again. apply must
// change only the task and the instance it is given, and give the same
// result each time. The caller holds mu and the decision's request lock,
// and applyConfirmedLocked lets go of mu.
func (m *Manager) applyConfirmedLocked(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, apply func(*UpgradeTask, *InstanceUpgrade)) error {
	index := slices.IndexFunc(task.Instances, func(other InstanceUpgrade) bool { return other.InstanceID == inst.InstanceID })

	draft := copyTask(task)
	apply(draft, &draft.Instances[index])
	draft.Revision = task.Revision + 1
	snapshot := taskSnapshot{task: draft, seq: m.snapshotSeq.Add(1)}

	m.mu.Unlock()

	err := m.confirmSnapshot(ctx, snapshot)
	if err != nil {
		m.logger.Error("Certificate task %s did not apply a decision on instance %s: %v", task.ID, inst.InstanceID, err)

		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			"Blacksmith could not confirm that it saved the decision to Vault, so it did not apply it, and the instance still waits for it. Send the decision again once Vault is healthy.")
	}

	m.mu.Lock()

	// The request lock keeps every other decision off the instance, and
	// nothing else answers a decision, so the instance still waits for it.
	apply(task, inst)
	task.Revision = max(task.Revision, draft.Revision)

	// The decision is recorded before the settle it causes.
	m.auditDecisionLocked(task, inst)

	rescan := m.endCertificateInstanceLocked(task, inst) //nolint:contextcheck // The follow-up scan runs on the manager's lifecycle context, so a finished request never cancels it.

	m.mu.Unlock()

	rescan()

	// A save of the task taken after the copy, without the decision, may
	// have landed after it, so the live task is saved once more.
	if m.snapshotSeq.Load() != snapshot.seq {
		err = m.persistTask(context.WithoutCancel(ctx), task)
		if err != nil {
			m.logger.Error("Failed to save certificate task %s after a decision: %v", task.ID, err)
		}
	}

	return nil
}

// answerReplay answers a decision request. A request that repeats the
// decision applied last saves the task again with confirmation, since the
// first request's save may not have been confirmed, and answers as the
// first did once it is. Any other request answers err. The caller does
// not hold mu.
func (m *Manager) answerReplay(ctx context.Context, task *UpgradeTask, replayed bool, err error) error {
	if !replayed {
		return err
	}

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			"The decision is recorded, but Blacksmith could not confirm that it saved it to Vault. Send it again once Vault is healthy.")
	}

	return nil
}

// saveDecision saves the task once a decision applied, and resumes it.
// failed says whether the decision failed the instance. A save that is not
// confirmed answers 500 and leaves the task paused, since Vault may still
// hold the decision as pending.
func (m *Manager) saveDecision(ctx context.Context, task *UpgradeTask, failed bool) error {
	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Certificate task %s stays paused after a decision: %v", task.ID, err)

		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			"The decision is recorded, but Blacksmith could not confirm that it saved it to Vault, so the task stays paused. Resume it once Vault is healthy.")
	}

	m.resumeAfterDecision(ctx, task, failed)

	return nil
}

// resumeAfterDecision resumes a task that paused for a decision once no
// instance of it waits for one. A task that still has an instance to run
// stays paused while the switches refuse its writes, and so does one whose
// decision failed an instance while pause_on_failure is on and an instance
// is still pending, since a failed instance pauses the task. A task paused
// after a failure stays paused while an instance is pending, whatever the
// decision, so only Continue moves it on. A failure's reason with nothing
// pending left to hold no longer says why the task waits, so a resume
// clears it.
func (m *Manager) resumeAfterDecision(ctx context.Context, task *UpgradeTask, failed bool) {
	m.mu.RLock()

	mode := certTaskMode(task)
	work := remainingWorkLocked(task)

	m.mu.RUnlock()

	if work {
		err := m.certWritesAllowed(mode)
		if err != nil {
			m.logger.Info("Certificate task %s stays paused after a decision, because its writes are refused: %v", task.ID, err)

			return
		}
	}

	holdForFailure := failed && m.taskPausesOnFailure(task)

	m.mu.Lock()

	if task.Status != TaskStatusPaused || !task.Paused || anyWaitsLocked(task) ||
		((holdForFailure || task.PauseReason != "") && anyPendingLocked(task)) {
		m.mu.Unlock()

		return
	}

	task.Paused = false
	task.PauseReason = ""
	task.Status = TaskStatusRunning
	clearNeverStartedLocked(task)
	m.noteResumeLocked(task)

	m.mu.Unlock()

	m.logger.Info("Certificate task %s resumes, since no instance waits for a decision", task.ID)

	err := m.persistTask(context.WithoutCancel(ctx), task)
	if err != nil {
		m.logger.Error("Failed to save certificate task %s as resumed: %v", task.ID, err)
	}

	m.signalTask(task.ID)
	m.resumeIfReady(task) //nolint:contextcheck // the resume outlives the request, so it runs under the manager's context
}

// holdDecidedFailureLocked gives a task the reason of its pause when the
// decision just failed the instance, the task pauses on a failure, and an
// instance is still pending, so the save of the decision carries the
// reason. settingsPause is the certificate settings' pause_on_failure,
// which a task saved before tasks carried the choice follows. The caller
// holds mu.
func holdDecidedFailureLocked(task *UpgradeTask, inst *InstanceUpgrade, settingsPause bool) {
	pauses := task.KindOrDefault() == TaskKindCertificates && settingsPause
	if task.PauseOnFailure != nil {
		pauses = *task.PauseOnFailure
	}

	if pauses && inst.Status == InstanceStatusFailed {
		holdForFailureLocked(task, inst)
	}
}

// taskPausesOnFailure reports whether a failed instance pauses the task.
// A task that carries the choice follows it. One saved before tasks
// carried it pauses when it is a certificate task and the certificate
// settings say so, and an upgrade of that age never does. PauseOnFailure
// is set before the task is shared and never changes, so it is read
// without the lock.
func (m *Manager) taskPausesOnFailure(task *UpgradeTask) bool {
	if task.PauseOnFailure != nil {
		return *task.PauseOnFailure
	}

	return task.KindOrDefault() == TaskKindCertificates && m.pausesOnFailure()
}

// pausesOnFailure reads pause_on_failure, which is on unless the
// certificate settings turn it off.
func (m *Manager) pausesOnFailure() bool {
	d := m.certDeps()
	if d.Settings == nil {
		return certinventory.DefaultSettings().PauseOnFailure
	}

	return d.Settings().PauseOnFailure
}

// remainingWorkLocked reports whether any instance of the task still has a
// step to run. The caller holds mu for reading.
func remainingWorkLocked(task *UpgradeTask) bool {
	for i := range task.Instances {
		inst := &task.Instances[i]
		if !inst.Status.settled() && inst.Status != StatusAwaitingDecision {
			return true
		}
	}

	return false
}

// anyWaitsLocked reports whether any instance of the task waits for a
// decision. The caller holds mu for reading.
func anyWaitsLocked(task *UpgradeTask) bool {
	return slices.ContainsFunc(task.Instances, func(inst InstanceUpgrade) bool { return waitsForDecision(&inst) })
}

// anyPendingLocked reports whether any instance of the task has not
// started. The caller holds mu for reading.
func anyPendingLocked(task *UpgradeTask) bool {
	return slices.ContainsFunc(task.Instances, func(inst InstanceUpgrade) bool { return inst.Status == InstanceStatusPending })
}

// decisionAuditLocked builds the audit details of a decision from the
// request's actor and the time now. The choice names the event, which is
// adopted, abandoned, approved, skipped, or, for a resolution that settled
// the instance, verified or marked_failed. The caller holds mu.
func (m *Manager) decisionAuditLocked(ctx context.Context, kind, choice, detail string) DecisionAudit {
	var event string

	switch choice {
	case actionAdopt:
		event = AuditAdopted
	case actionAbandon:
		event = AuditAbandoned
	case "approve":
		event = AuditApproved
	case actionSkip:
		event = AuditSkipped
	case actionVerify:
		event = AuditVerified
	case actionFailed:
		event = AuditMarkedFailed
	default:
		m.logger.Error("audit=certificate-rotation decision=%s has no event for the choice %q", kind, choice)
	}

	decision := DecisionAudit{ID: uuid.New().String(), Event: event, Detail: kind + ": " + strings.TrimSpace(choice+" "+detail), At: m.certNowLocked()}

	if actor, found := audit.ActorFrom(ctx); found {
		decision.Actor = actor.Username
		decision.From = actor.From()
	}

	return decision
}
