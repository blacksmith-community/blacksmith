package upgrade

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/credhub"
)

// baselineLimit is how many of a credential's newest versions the baseline
// and the candidate listing read, which is the most CredHub returns. A
// regeneration adds at most a few versions, so the newest ones always
// cover it.
const baselineLimit = 20

// regenerateVariables regenerates each selected variable of the instance in
// order and validates each new version before it moves on. A variable not
// yet sent saves its baseline and then "sending", both confirmed, before
// the request, and a named answer saves "confirmed" before anything else.
// A variable loaded in "sending" or "outcome_unknown" is never sent again,
// and the instance waits for an operator to reconcile it. A variable in
// "confirmed" or "adopted" skips only the request, so every pass validates
// every new version before any later step. It returns stop when the
// instance must not go on, because the task paused, the instance failed,
// or it waits for a decision. The caller does not hold mu.
func (m *Manager) regenerateVariables(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) (bool, error) {
	api := m.certDeps().CredHub
	if api == nil {
		return m.pauseWith(ctx, task, inst, apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured,
			"CredHub is not configured, so certificates can't be regenerated."))
	}

	m.mu.Lock()

	if inst.Certificates == nil {
		m.mu.Unlock()

		return true, m.failRotation(ctx, task, inst, "The instance has no certificate rotation to regenerate.")
	}

	inst.Certificates.Phase = RotationPhaseRegenerate
	count := len(inst.Certificates.Variables)

	m.mu.Unlock()

	for index := range count {
		stop, err := m.regenerateVariable(ctx, api, task, inst, index)
		if stop || err != nil {
			return true, err
		}
	}

	return false, nil
}

// regenerateVariable takes one variable through its request, when it
// needs one, and its validation.
func (m *Manager) regenerateVariable(ctx context.Context, api CredHubAPI, task *UpgradeTask, inst *InstanceUpgrade, index int) (bool, error) {
	variable, pending := m.variableAt(inst, index)

	switch variable.RegenerateState {
	case RegenerateNotSent:
		stop, err := m.sendRegenerate(ctx, api, task, inst, index)
		if stop || err != nil {
			return true, err
		}
	case RegenerateSending:
		return m.reconcileRegeneration(ctx, api, task, inst, index,
			fmt.Sprintf("Blacksmith stopped while it was asking CredHub to regenerate %s, so it can't tell whether CredHub made a new version.", shortName(variable.Name)))
	case RegenerateOutcomeUnknown:
		if pending != nil && pending.Kind == DecisionReconcileRegeneration && pending.Variable == variable.Name {
			m.mu.Lock()
			inst.Status = StatusAwaitingDecision
			pauseForDecisionLocked(task)
			m.mu.Unlock()

			return true, nil
		}

		return m.reconcileRegeneration(ctx, api, task, inst, index,
			fmt.Sprintf("Blacksmith can't tell whether CredHub regenerated %s.", shortName(variable.Name)))
	case RegenerateAbandoned:
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("%s: regeneration abandoned. Any version CredHub made stays there and is not deployed.", shortName(variable.Name)))
	case RegenerateConfirmed, RegenerateAdopted:
	default:
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("%s has the unknown regeneration state %q.", shortName(variable.Name), variable.RegenerateState))
	}

	return m.validateVariable(ctx, api, task, inst, index)
}

// variableAt copies the variable at index and the instance's pending
// decision.
func (m *Manager) variableAt(inst *InstanceUpgrade, index int) (VariableRotation, *PendingDecision) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Certificates.Variables[index].Clone(), inst.Certificates.Pending.Clone()
}

// changeVariable applies change to the variable at index under mu.
func (m *Manager) changeVariable(inst *InstanceUpgrade, index int, change func(v *VariableRotation)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	change(&inst.Certificates.Variables[index])
}

// sendRegenerate saves the variable's baseline and old identity, then
// "sending", and only then asks CredHub for a new version. A named answer
// is saved as "confirmed" before anything else happens.
func (m *Manager) sendRegenerate(ctx context.Context, api CredHubAPI, task *UpgradeTask, inst *InstanceUpgrade, index int) (bool, error) {
	variable, _ := m.variableAt(inst, index)
	short := shortName(variable.Name)
	step := "regenerate " + short

	versions, err := api.CredentialVersions(ctx, variable.Name, baselineLimit)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, step, err)
	}

	baseline := make([]string, 0, len(versions))
	for _, version := range versions {
		baseline = append(baseline, version.ID)
	}

	oldPublic, err := api.CertificateVersion(ctx, variable.OldVersionID)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, step, err)
	}

	old := firstCertificate(oldPublic)
	if old == nil {
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("The deployed version %s of %s could not be parsed, so Blacksmith did not regenerate it.", variable.OldVersionID, short))
	}

	m.changeVariable(inst, index, func(v *VariableRotation) {
		v.Baseline = baseline
		v.OldSerial = old.SerialNumber.Text(16)
		v.OldNotAfter = old.NotAfter
		v.OldCertHash = certpatch.NormalisedHash([]byte(oldPublic.Certificate))
	})

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, step)

		return true, err
	}

	m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateSending })

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateNotSent })
		m.pauseUnconfirmed(task, inst, step)

		return true, err
	}

	// The switches are read once more after the save, so turning rotation
	// off while the save was under way still stops the request.
	err = m.certWritesAllowed(m.rotationMode(task))
	if err != nil {
		m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateNotSent })

		return m.pauseWith(ctx, task, inst, err)
	}

	result, err := api.Regenerate(ctx, variable.CertificateID)

	switch {
	case err == nil && result != nil && result.VersionID != "":
		return m.confirmRegeneration(ctx, task, inst, index, result.VersionID)
	case err == nil || credhub.IsUncertain(err):
		return m.reconcileRegeneration(ctx, api, task, inst, index,
			fmt.Sprintf("CredHub did not say whether it regenerated %s.", short))
	case ctx.Err() != nil:
		// The broker is stopping and the request was never sent, so the
		// variable can be sent again when the task resumes.
		m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateNotSent })

		if saveErr := m.persistCertConfirmed(ctx, task); saveErr != nil {
			m.logger.Error("Certificate task %s: %v", task.ID, saveErr)
		}

		return true, fmt.Errorf("regenerating %s: %w", short, err)
	default:
		m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateNotSent })

		return m.certainRegenerateFailure(ctx, task, inst, short, err)
	}
}

// certainRegenerateFailure handles a regenerate request that certainly
// made no new version, with the variable already back at not_sent. A 4xx
// answer other than 401 is CredHub refusing this regeneration, which
// fails the instance. Any other certain failure means the request never
// reached CredHub, because the connection or the token could not be had,
// or CredHub turned the token away. Spec 8.6 and 8.10 don't classify
// those, and the next attempt may well succeed, so the task pauses and
// the variable is sent again when it resumes.
func (m *Manager) certainRegenerateFailure(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, short string, err error) (bool, error) {
	var answered *credhub.APIError

	switch {
	case errors.As(err, &answered) && answered.Status == http.StatusUnauthorized:
		return m.pauseWith(ctx, task, inst, apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable,
			fmt.Sprintf("CredHub did not accept Blacksmith's token when asked to regenerate %s, so it made no new version. Resume once Blacksmith's CredHub client is authorized: %v", short, err)))
	case errors.As(err, &answered):
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("CredHub refused to regenerate %s, so it made no new version: %v", short, err))
	default:
		return m.pauseWith(ctx, task, inst, apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable,
			fmt.Sprintf("Blacksmith could not reach CredHub to regenerate %s, so CredHub made no new version. Resume once CredHub answers: %v", short, err)))
	}
}

// rotationMode reads the task's rotation mode under mu, as certTaskMode
// requires. The caller does not hold mu.
func (m *Manager) rotationMode(task *UpgradeTask) RotationMode {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return certTaskMode(task)
}

// confirmRegeneration saves the version CredHub named before anything
// else runs.
func (m *Manager) confirmRegeneration(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, index int, versionID string) (bool, error) {
	var short string

	m.changeVariable(inst, index, func(v *VariableRotation) {
		v.RegenerateState = RegenerateConfirmed
		v.NewVersionID = versionID
		short = shortName(v.Name)
	})

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "go on after regenerating "+short)

		return true, err
	}

	return false, nil
}

// reconcileRegeneration records that a regeneration's outcome is unknown,
// lists every version that is not in the baseline with its checks, and
// leaves the instance waiting for an operator to adopt one or abandon the
// regeneration. It never sends the request again.
func (m *Manager) reconcileRegeneration(ctx context.Context, api CredHubAPI, task *UpgradeTask, inst *InstanceUpgrade, index int, why string) (bool, error) {
	m.changeVariable(inst, index, func(v *VariableRotation) { v.RegenerateState = RegenerateOutcomeUnknown })

	variable, _ := m.variableAt(inst, index)
	short := shortName(variable.Name)

	candidates, err := m.listCandidates(ctx, api, variable)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, "list the versions that the regeneration of "+short+" may have made", err)
	}

	m.mu.Lock()
	inst.Status = StatusAwaitingDecision
	inst.CompletedAt = nil
	inst.Error = why + " Blacksmith won't send the CredHub request again. Adopt a candidate version that passed every check, or abandon the regeneration."
	inst.Certificates.Pending = &PendingDecision{
		Kind:       DecisionReconcileRegeneration,
		Variable:   variable.Name,
		Candidates: candidates,
		Since:      m.certNowLocked(),
	}
	pauseForDecisionLocked(task)
	m.mu.Unlock()

	m.logger.Error("Instance %s of %s waits for a decision: %s", inst.InstanceID, inst.DeploymentName, why)

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		// The instance still waits for the decision, but Vault may hold an
		// older record, so a restart may ask the question again.
		message := "Blacksmith could not confirm that it saved this task's progress to Vault, so after a restart the instance may ask for this decision again. Resume once Vault is healthy."

		m.mu.Lock()
		inst.Error += " " + message
		m.mu.Unlock()

		m.logger.Error("Certificate task %s paused: %s", task.ID, message)

		return true, err
	}

	return true, nil
}

// certNowLocked reads the configured clock while the caller holds mu.
func (m *Manager) certNowLocked() time.Time {
	if m.deps.Now == nil {
		return time.Now()
	}

	return m.deps.Now()
}

// listCandidates returns each version of the variable that is not in its
// baseline, oldest first, validated against the old version and the
// current version of the signing CA.
func (m *Manager) listCandidates(ctx context.Context, api CredHubAPI, variable VariableRotation) ([]Candidate, error) {
	versions, err := api.CredentialVersions(ctx, variable.Name, baselineLimit)
	if err != nil {
		return nil, fmt.Errorf("listing the versions of %s: %w", shortName(variable.Name), err)
	}

	known := make(map[string]bool, len(variable.Baseline))
	for _, id := range variable.Baseline {
		known[id] = true
	}

	fresh := make([]credhub.CredentialVersion, 0, len(versions))

	for _, version := range versions {
		if !known[version.ID] {
			fresh = append(fresh, version)
		}
	}

	if len(fresh) == 0 {
		return []Candidate{}, nil
	}

	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].CreatedAt.Before(fresh[j].CreatedAt) })

	oldPublic, err := api.CertificateVersion(ctx, variable.OldVersionID)
	if err != nil {
		return nil, fmt.Errorf("reading the deployed version of %s: %w", shortName(variable.Name), err)
	}

	old := firstCertificate(oldPublic)

	candidates := make([]Candidate, 0, len(fresh))

	for _, version := range fresh {
		public, err := api.CertificateVersion(ctx, version.ID)
		if err != nil {
			return nil, fmt.Errorf("reading version %s of %s: %w", version.ID, shortName(variable.Name), err)
		}

		signingCA, err := m.signingCA(ctx, api, variable.Name, public)
		if err != nil {
			return nil, err
		}

		candidate := Candidate{VersionID: version.ID, CreatedAt: version.CreatedAt}

		var newCert *x509.Certificate
		if cert := firstCertificate(public); cert != nil {
			newCert = cert
			candidate.Serial = cert.SerialNumber.Text(16)
			candidate.NotAfter = cert.NotAfter
		}

		candidate.Checks = ValidateNew(old, newCert, signingCA, nil, m.certNow())
		candidate.Valid = AllPassed(candidate.Checks)
		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

// validateVariable checks the variable's new version against its old one
// and the current version of its signing CA, and saves the checks. A
// failed check fails the instance before any later step runs.
func (m *Manager) validateVariable(ctx context.Context, api CredHubAPI, task *UpgradeTask, inst *InstanceUpgrade, index int) (bool, error) {
	variable, _ := m.variableAt(inst, index)
	short := shortName(variable.Name)
	step := "validate the new version of " + short

	if variable.NewVersionID == "" {
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("%s has no new version, so its new certificate failed validation.", short))
	}

	newPublic, err := api.CertificateVersion(ctx, variable.NewVersionID)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, step, err)
	}

	oldPublic, err := api.CertificateVersion(ctx, variable.OldVersionID)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, step, err)
	}

	signingCA, err := m.signingCA(ctx, api, variable.Name, newPublic)
	if err != nil {
		return m.pauseForCredHub(ctx, task, inst, step, err)
	}

	newCert := firstCertificate(newPublic)
	checks := ValidateNew(firstCertificate(oldPublic), newCert, signingCA, nil, m.certNow())

	m.changeVariable(inst, index, func(v *VariableRotation) {
		v.Validation = checks
		v.NewCertHash = certpatch.NormalisedHash([]byte(newPublic.Certificate))

		if newCert != nil {
			notAfter := newCert.NotAfter
			v.NewSerial = newCert.SerialNumber.Text(16)
			v.NewNotAfter = &notAfter
		}
	})

	if !AllPassed(checks) {
		return true, m.failRotation(ctx, task, inst,
			fmt.Sprintf("The new certificate failed validation: version %s of %s %s. CredHub keeps the new version, which is not deployed.",
				variable.NewVersionID, short, failedChecks(checks)))
	}

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "go on after validating the new version of "+short)

		return true, err
	}

	return false, nil
}

// failedChecks describes each check that failed.
func failedChecks(checks []Check) string {
	var failed []string

	for _, check := range checks {
		if !check.Passed {
			failed = append(failed, fmt.Sprintf("failed %s (%s)", check.Name, check.Detail))
		}
	}

	return strings.Join(failed, ", ")
}

// signingCA returns the current version of the CA that signs the named
// certificate. A certificate that names no signer, or signs itself, is
// checked against the CA that CredHub stored with the version.
func (m *Manager) signingCA(ctx context.Context, api CredHubAPI, name string, version *credhub.CertificatePublic) (*x509.Certificate, error) {
	meta, err := api.CertificateMetadata(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("reading the details of %s: %w", shortName(name), err)
	}

	if meta.SignedBy == "" || meta.SignedBy == name {
		return parseFirst(version.CA), nil
	}

	versions, err := api.CredentialVersions(ctx, meta.SignedBy, 1)
	if err != nil {
		return nil, fmt.Errorf("listing the versions of the CA %s: %w", shortName(meta.SignedBy), err)
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("the CA %s has no version: %w", shortName(meta.SignedBy), credhub.ErrNotFound)
	}

	public, err := api.CertificateVersion(ctx, versions[0].ID)
	if err != nil {
		return nil, fmt.Errorf("reading the current version of the CA %s: %w", shortName(meta.SignedBy), err)
	}

	return firstCertificate(public), nil
}

// firstCertificate parses the certificate of a CredHub version, or returns
// nil when it holds none that parses.
func firstCertificate(public *credhub.CertificatePublic) *x509.Certificate {
	if public == nil {
		return nil
	}

	return parseFirst(public.Certificate)
}

func parseFirst(text string) *x509.Certificate {
	certs, err := certinventory.ParsePEMCertificates([]byte(text))
	if err != nil || len(certs) == 0 {
		return nil
	}

	return certs[0]
}

// pauseForCredHub pauses the task after a CredHub read failed, so nothing
// later runs on what it could not read. A broker that is stopping returns
// without pausing, and the task resumes where it was.
func (m *Manager) pauseForCredHub(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, step string, cause error) (bool, error) {
	if ctx.Err() != nil {
		return true, fmt.Errorf("%s: %w", step, ctx.Err())
	}

	return m.pauseWith(ctx, task, inst, apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable,
		fmt.Sprintf("CredHub did not answer a read Blacksmith needs, so it did not %s. Resume once CredHub answers: %v", step, cause)))
}

// pauseWith pauses the task with the error's message on the instance,
// saves the record as well as it can, and returns the error.
func (m *Manager) pauseWith(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, err error) (bool, error) {
	message := err.Error()

	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		message = apiErr.Message
	}

	m.mu.Lock()

	if task.Status == TaskStatusPending || task.Status == TaskStatusRunning || task.Status == TaskStatusPaused {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	inst.Error = message

	m.mu.Unlock()

	m.logger.Error("Certificate task %s paused: %s", task.ID, message)

	if saveErr := m.persistCertConfirmed(ctx, task); saveErr != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, saveErr)
	}

	return true, err
}

// failRotation fails the instance with the message, releases what the
// task no longer holds, saves the record as well as it can, and returns the
// message as an error.
func (m *Manager) failRotation(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, message string) error {
	now := m.certNow()

	m.mu.Lock()

	if !inst.Status.settled() {
		inst.Status = InstanceStatusFailed
		inst.CompletedAt = &now
		task.FailedCount++
	}

	inst.Error = message
	m.releaseUnheldLocked(task)

	m.mu.Unlock()

	m.logger.Error("Instance %s of %s: %s", inst.InstanceID, inst.DeploymentName, message)

	if err := m.persistCertConfirmed(ctx, task); err != nil {
		m.logger.Error("Certificate task %s: %v", task.ID, err)
	}

	return errors.New(message)
}
