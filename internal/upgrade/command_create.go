package upgrade

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"blacksmith/internal/apierr"
)

// CommandCreateRequest is the body of a request that creates a command task
// from a preview.
type CommandCreateRequest struct {
	PreviewID       string `json:"preview_id"`
	ClientRequestID string `json:"client_request_id"`
	Command         string `json:"command"`
	RunAs           string `json:"run_as"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	Parallel        bool   `json:"parallel"`
	MaxInFlight     int    `json:"max_in_flight"`
	Name            string `json:"name"`
	Reason          string `json:"reason"`
	// CredentialPresented is set by the handler when the request carried the
	// second credential, and is never read from the body.
	CredentialPresented bool `json:"-"`
}

// Authorizer returns the authorizer that decides whether a command request
// may go ahead. It is the one Configure was given, or a ConfigAuthorizer over
// the commands configuration when it was given none.
func (m *Manager) Authorizer() CommandAuthorizer {
	if authorizer := m.certDeps().Authorizer; authorizer != nil {
		return authorizer
	}

	return NewConfigAuthorizer(nil)
}

// CreateCommandTask creates a command task from a stored command preview. It
// reports existing when the request ID already made a task with the same
// body, and returns that task instead of making another.
//
// The preview is read first and checked, and only a request that passes both
// checks spends it. The authorizer sees the settings the preview approved,
// and the request must match them. The task is then built from the preview's
// settings, so what runs is what was reviewed, and only the command text
// comes from the request, which the match has proved equal in digest and
// length to the reviewed one. The text goes into the saved task and nowhere
// else, so no log line, error, or activity record holds it.
//
// The task reserves its instances and is saved twice, first with
// CreationConfirmed false and then true, as a certificate task is. A failure
// after the preview was spent frees every reservation, leaves no task in
// memory, and puts the preview back so the create can be tried again.
func (m *Manager) CreateCommandTask(ctx context.Context, req CommandCreateRequest) (*UpgradeTask, bool, error) {
	select {
	case <-m.loaded:
	default:
		return nil, false, ErrTasksLoading
	}

	unlock := m.lockRequest(req.ClientRequestID)
	defer unlock()

	digest, err := requestDigest(req)
	if err != nil {
		return nil, false, apierr.Wrap(http.StatusInternalServerError, apierr.CodeInternal, err)
	}

	original, found, err := m.existingCertificateTask(req.ClientRequestID, digest)
	if found || err != nil {
		return original, found, err
	}

	store := m.certDeps().Previews
	if store == nil {
		return nil, false, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "Blacksmith has no store for previews.")
	}

	preview, err := store.GetCommand(req.PreviewID)
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the store answers with the API error
	}

	err = m.Authorizer().AuthorizeStored(ctx, commandSpec(preview, req, ""))
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the authorizer answers with the API error
	}

	if !preview.MatchesCommand(commandDigest(req.Command), len(req.Command), req.RunAs, req.TimeoutSeconds, req.Parallel, req.MaxInFlight) {
		return nil, false, apierr.New(http.StatusConflict, apierr.CodePreviewMismatch,
			"The command or its settings differ from the preview, so preview the command again.")
	}

	task := m.newCommandTask(req, digest, preview)
	if len(task.Instances) == 0 {
		return nil, false, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, "Every instance in the preview is blocked, so there is nothing to run on.")
	}

	spent, err := store.Spend(req.PreviewID, PreviewKindCommand)
	if err != nil {
		return nil, false, err //nolint:wrapcheck // the store answers with the API error
	}

	stampActor(ctx, task)

	m.logger.Info("Creating command task %s for %d instances from preview %s", task.ID, len(task.Instances), preview.ID)

	err = m.reserveCertificateTask(task)
	if err != nil {
		store.restoreCommand(spent.Command)

		return nil, false, err
	}

	err = m.persistCertConfirmed(ctx, task)
	if err == nil {
		m.mu.Lock()
		task.CreationConfirmed = true
		m.mu.Unlock()

		err = m.persistCertConfirmed(ctx, task)
	}

	if err != nil {
		m.abandonCommandTask(task)
		store.restoreCommand(spent.Command)

		m.logger.Error("Command task %s was not created, because its creation could not be confirmed: %v", task.ID, err)

		return nil, false, &apierr.Error{
			Status:  http.StatusInternalServerError,
			Code:    apierr.CodeInternal,
			Message: createUnconfirmedMessage,
			Err:     err,
		}
	}

	m.requests.Put(req.ClientRequestID, task.ID, digest)

	m.mu.RLock()
	created := copyTask(task)
	m.mu.RUnlock()

	return created, false, nil
}

// commandSpec returns the spec of a command task whose settings are the
// preview's. Only the command text and the reason come from the request.
func commandSpec(preview *CommandPreview, req CommandCreateRequest, command string) CommandTaskSpec {
	return CommandTaskSpec{
		Command:             command,
		CommandDigest:       preview.CommandDigest,
		CommandLength:       preview.CommandLength,
		RunAs:               preview.RunAs,
		Shell:               preview.Shell,
		PreviewID:           preview.ID,
		TimeoutSeconds:      preview.TimeoutSeconds,
		Parallel:            preview.Parallel,
		MaxInFlight:         preview.MaxInFlight,
		Reason:              req.Reason,
		CredentialPresented: req.CredentialPresented,
	}
}

// newCommandTask builds a pending task with one instance for each instance of
// the preview that is not blocked. Each instance lists the nodes the
// operator reviewed. A node with no VM is skipped from the start, and every
// other node is pending.
func (m *Manager) newCommandTask(req CommandCreateRequest, digest string, preview *CommandPreview) *UpgradeTask {
	spec := commandSpec(preview, req, req.Command)
	instances := make([]InstanceUpgrade, 0, len(preview.Instances))

	for i := range preview.Instances {
		pi := &preview.Instances[i]
		if pi.Blocked != nil {
			continue
		}

		nodes := make([]CommandVM, 0, len(pi.VMs))

		for _, vm := range pi.VMs {
			node := CommandVM{
				Reviewed: vm.CommandVMIdentity.clone(),
				AZ:       vm.AZ,
				JobState: vm.JobState,
				Phase:    CommandPhasePending,
			}

			if !vm.Targeted {
				node.Phase = CommandPhaseSkipped
				node.Error = vm.Reason
			}

			nodes = append(nodes, node)
		}

		instances = append(instances, InstanceUpgrade{
			InstanceID:     pi.InstanceID,
			InstanceName:   pi.InstanceName,
			DeploymentName: pi.Deployment,
			ServiceID:      pi.Service,
			PlanID:         pi.Plan,
			Status:         InstanceStatusPending,
			Command:        &InstanceCommand{Deployment: pi.Deployment, VMs: nodes},
		})
	}

	return &UpgradeTask{
		ID:              uuid.New().String(),
		Name:            req.Name,
		Kind:            TaskKindCommand,
		Status:          TaskStatusPending,
		Command:         &spec,
		ClientRequestID: req.ClientRequestID,
		RequestDigest:   digest,
		Instances:       instances,
		TotalCount:      len(instances),
		CreatedAt:       m.certNow(),
	}
}

// abandonCommandTask frees every reservation of a command task whose creation
// failed and forgets it, so it holds nothing and is listed nowhere.
func (m *Manager) abandonCommandTask(task *UpgradeTask) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.releaseAllLocked(task.ID)
	delete(m.tasks, task.ID)
}
