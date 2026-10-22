package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
)

// RecheckPoint names the moment a certificate task checks its approved
// effects again.
type RecheckPoint int

// The points at which a certificate task re-checks its effects.
const (
	// BeforeRegenerate comes before the first regenerate request, and
	// compares everything the operator approved.
	BeforeRegenerate RecheckPoint = iota + 1
	// BeforeMutation comes before the deploy or the first VM change, once
	// the task has made its new versions.
	BeforeMutation
	// BeforeConverge comes before the deploy that brings BOSH in line with
	// the patched VMs.
	BeforeConverge
)

// certPreflight checks, in the order spec 8.4 lists them, that the
// instance can be rotated now. It changes nothing, and the caller fails
// the instance on any error, which is an *apierr.Error with the code of the
// first check that failed. The switches come first. A converge selects no
// variables, so it skips the variable and signing-CA checks.
func (m *Manager) certPreflight(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) error {
	view := m.viewRotation(task, inst)
	mode, instanceID, deployment := view.mode, view.instanceID, view.deployment

	names := make([]string, 0, len(view.variables))
	for _, variable := range view.variables {
		names = append(names, variable.Name)
	}

	err := m.certWritesAllowed(mode)
	if err != nil {
		return err
	}

	err = m.preflightGuard(task.ID, instanceID, deployment)
	if err != nil {
		return err
	}

	directorName, err := m.rotationDirector(ctx)
	if err != nil {
		return err
	}

	err = m.preflightNoDirectorTask(ctx, deployment)
	if err != nil {
		return err
	}

	err = m.credhubReady()
	if err != nil {
		return err
	}

	if mode == RotationModeConverge || len(names) == 0 {
		return nil
	}

	return m.preflightVariables(ctx, instanceID, deployment, directorName, names)
}

// rotationView is a copy of what a certificate step reads from its task
// and instance, taken under mu.
type rotationView struct {
	mode       RotationMode
	instanceID string
	deployment string
	approved   ApprovedEffects
	variables  []VariableRotation
}

// viewRotation copies what a certificate step reads from its task and
// instance. The caller does not hold mu.
func (m *Manager) viewRotation(task *UpgradeTask, inst *InstanceUpgrade) rotationView {
	m.mu.RLock()
	defer m.mu.RUnlock()

	view := rotationView{mode: certTaskMode(task), instanceID: inst.InstanceID, deployment: inst.DeploymentName}

	if rotation := inst.Certificates; rotation != nil {
		view.approved = rotation.Approved.Clone()
		view.variables = cloneEach(rotation.Variables, VariableRotation.Clone)
	}

	return view
}

// certTaskMode returns the task's rotation mode. A task with no spec reads
// as a redeploy, which needs only the rotation switch. The caller holds
// mu for reading.
func certTaskMode(task *UpgradeTask) RotationMode {
	if task.Certificates == nil {
		return RotationModeRedeploy
	}

	return task.Certificates.Mode
}

// preflightGuard checks that the guard holds the instance for this task.
func (m *Manager) preflightGuard(taskID, instanceID, deployment string) error {
	holder, held := m.Guard().Holder(instanceID, deployment)

	switch {
	case !held:
		return apierr.New(http.StatusConflict, apierr.CodeInstanceBusy,
			"This task no longer holds the instance, so it changes nothing on it.")
	case holder.TaskID != taskID:
		return apierr.New(http.StatusConflict, apierr.CodeInstanceBusy,
			"The instance is busy with "+holder.Description+", so this task changes nothing on it.")
	}

	return nil
}

// preflightNoDirectorTask checks that the director runs no task on the
// deployment. The director filters its task list by the deployment and by
// the queued, processing, and cancelling states, and a cancelling task
// still holds the deployment's lock, so any task it lists blocks.
func (m *Manager) preflightNoDirectorTask(ctx context.Context, deployment string) error {
	running, err := directorCall(ctx, func(c context.Context) ([]bosh.Task, error) {
		return m.director.FindRunningTasksForDeployment(c, deployment)
	})
	if err != nil {
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the tasks of deployment %s: %v", deployment, err))
	}

	if len(running) > 0 {
		return apierr.New(http.StatusConflict, apierr.CodeDeploymentLocked,
			fmt.Sprintf("The director is running task %d on deployment %s, so wait for it to finish.", running[0].ID, deployment))
	}

	return nil
}

// preflightVariables checks each selected variable with the same rules a
// preview applies: it still exists in the deployment, it still lies under
// the deployment's namespace, it is still a leaf, and the CA that would
// sign a regenerated one verifies the deployed one.
func (m *Manager) preflightVariables(ctx context.Context, instanceID, deployment, directorName string, names []string) error {
	d := m.certDeps()
	if d.ScanInstance == nil {
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			"Blacksmith is not set up to read certificates, so it can't check the selected ones.")
	}

	variables, err := directorCall(ctx, func(c context.Context) ([]bosh.Variable, error) {
		return m.director.ListVariables(c, deployment)
	})
	if err != nil {
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the variables of deployment %s: %v", deployment, err))
	}

	scan, err := d.ScanInstance(ctx, instanceID)

	switch {
	case errors.Is(err, certinventory.ErrUnknownInstance):
		return apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound,
			"Blacksmith does not know this instance any more, so it may have been deleted.")
	case err != nil:
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The instance %s could not be scanned: %v", instanceID, err))
	case scan.Err != "":
		return apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The instance %s could not be scanned: %s", instanceID, scan.Err))
	}

	deployed := make(map[string]string, len(variables))
	for _, variable := range variables {
		deployed[variable.Name] = variable.ID
	}

	for _, name := range names {
		check := m.checkSelected(ctx, d.CredHub, purposePreflight, directorName, scan, deployed, name)
		if check.failure != nil {
			return check.failure
		}

		if check.blocked != nil {
			return apierr.New(blockedStatus(check.blocked.Code), check.blocked.Code, check.blocked.Message)
		}
	}

	return nil
}

// blockedStatus is the HTTP status spec 9.4 gives a code a variable check
// can block with.
func blockedStatus(code string) int {
	switch code {
	case apierr.CodeUnknownVariable, apierr.CodeNotRotatable:
		return http.StatusBadRequest
	case apierr.CodeInstanceNotFound:
		return http.StatusNotFound
	default:
		return http.StatusConflict
	}
}

// effectsNow is what the director and CredHub say now about the effects a
// task approved.
type effectsNow struct {
	digest   string
	deployed map[string]string
	latest   map[string]string
	// variables lists the deployment's variables in the director's order.
	variables []bosh.Variable
}

// readEffects reads the manifest, the deployed variables, and the latest
// CredHub version of each, as a preview does. A CredHub failure is a
// credhub_unavailable error, never an empty map.
func (m *Manager) readEffects(ctx context.Context, deployment string) (effectsNow, error) {
	api := m.certDeps().CredHub
	if api == nil {
		return effectsNow{}, apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured,
			"CredHub is not configured, so the task can't check its effects.")
	}

	detail, err := directorCall(ctx, func(c context.Context) (*bosh.DeploymentDetail, error) {
		return m.director.GetDeployment(c, deployment)
	})

	switch {
	case err != nil:
		return effectsNow{}, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not return deployment %s: %v", deployment, err))
	case detail == nil:
		return effectsNow{}, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director returned no manifest for deployment %s.", deployment))
	}

	variables, err := directorCall(ctx, func(c context.Context) ([]bosh.Variable, error) {
		return m.director.ListVariables(c, deployment)
	})
	if err != nil {
		return effectsNow{}, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the variables of deployment %s: %v", deployment, err))
	}

	latest, err := latestVersions(ctx, api, purposeRecheck, variables)
	if err != nil {
		return effectsNow{}, err
	}

	deployed := make(map[string]string, len(variables))
	for _, variable := range variables {
		deployed[variable.Name] = variable.ID
	}

	return effectsNow{
		digest:    manifestDigest(detail.Manifest),
		deployed:  deployed,
		latest:    latest,
		variables: variables,
	}, nil
}

// differing returns each variable whose latest version differs from the
// deployed one, leaving out the names in skip, sorted by name.
func (e effectsNow) differing(skip map[string]bool) []VersionPair {
	var pairs []VersionPair

	for _, variable := range e.variables {
		id, ok := e.latest[variable.Name]
		if !ok || skip[variable.Name] || id == variable.ID {
			continue
		}

		pairs = append(pairs, VersionPair{Name: variable.Name, DeployedVersionID: variable.ID, LatestVersionID: id})
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Name < pairs[j].Name })

	return pairs
}

// recheckEffects compares what the operator approved with the director
// and CredHub now, and returns one sentence for each difference, or none
// when nothing changed. Every point compares the manifest digest.
//
// BeforeRegenerate compares everything in Approved: the VM CIDs, each
// selected variable's deployed and latest versions, and the additional
// variables. A selected variable the task already regenerated is compared
// with its own new version, and one still being reconciled is left to the
// regeneration step. BeforeMutation compares the additional variables and
// requires each selected variable's latest version to be the one the task
// made. BeforeConverge compares the full set of variables whose latest
// version differs from the deployed one with the set the task expects,
// which is the additional variables and the selected ones at their new
// versions. A read that fails returns an error and no changes, so a
// failure never reads as an unchanged state.
func (m *Manager) recheckEffects(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, at RecheckPoint) ([]string, error) {
	view := m.viewRotation(task, inst)
	deployment, approved, variables := view.deployment, view.approved, view.variables

	if at != BeforeRegenerate && at != BeforeMutation && at != BeforeConverge {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("Task %s asked for an unknown re-check point %d.", task.ID, at))
	}

	now, err := m.readEffects(ctx, deployment)
	if err != nil {
		return nil, err
	}

	changes := []string{}

	if now.digest != approved.ManifestDigest {
		changes = append(changes, "The manifest changed since the preview.")
	}

	selected := make(map[string]bool, len(approved.Selected))
	for _, pair := range approved.Selected {
		selected[pair.Name] = true
	}

	switch at {
	case BeforeRegenerate:
		cidChanges, err := m.recheckVMs(ctx, deployment, approved.VMCIDs)
		if err != nil {
			return nil, err
		}

		changes = append(changes, cidChanges...)
		changes = append(changes, selectedChanges(approved.Selected, variables, now)...)
		changes = append(changes, additionalChanges(approved.Additional, now.differing(selected))...)
	case BeforeMutation:
		changes = append(changes, additionalChanges(approved.Additional, now.differing(selected))...)
		changes = append(changes, createdChanges(approved.Selected, variables, now)...)
	case BeforeConverge:
		changes = append(changes, convergeChanges(expectedAtConverge(approved, variables), selected, now.differing(nil))...)
	}

	if len(changes) == 0 {
		return nil, nil
	}

	return changes, nil
}

// recheckVMs compares the deployment's VM CIDs with the approved ones.
func (m *Manager) recheckVMs(ctx context.Context, deployment string, approved []string) ([]string, error) {
	vms, err := directorCall(ctx, func(c context.Context) ([]bosh.VM, error) {
		return m.director.GetDeploymentVMs(c, deployment)
	})
	if err != nil {
		return nil, apierr.New(http.StatusInternalServerError, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the VMs of deployment %s: %v", deployment, err))
	}

	cids := make([]string, 0, len(vms))

	for _, vm := range vms {
		if vm.CID != "" {
			cids = append(cids, vm.CID)
		}
	}

	sort.Strings(cids)

	want := slices.Clone(approved)
	sort.Strings(want)

	if slices.Equal(cids, want) {
		return nil, nil
	}

	return []string{"The instance's VMs changed since the preview."}, nil
}

// shortName is the last part of a CredHub name, which is the variable's
// name in the manifest.
func shortName(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

// selectedChanges compares each selected variable's deployed and latest
// versions with the approved ones. A variable the task has regenerated
// must have its own new version as the latest one, so a task that resumes
// after a regeneration does not report its own version as an outside
// change. A variable whose request was sent but whose outcome is not known
// yet is left to the regeneration step, which reconciles it, so only its
// deployed version is compared.
func selectedChanges(approved []VersionPair, variables []VariableRotation, now effectsNow) []string {
	rotations := make(map[string]VariableRotation, len(variables))
	for _, variable := range variables {
		rotations[variable.Name] = variable
	}

	var changes []string

	for _, pair := range approved {
		name := shortName(pair.Name)

		deployed, ok := now.deployed[pair.Name]
		if !ok {
			changes = append(changes, name+" is no longer a variable of the deployment.")

			continue
		}

		if deployed != pair.DeployedVersionID {
			changes = append(changes, fmt.Sprintf("%s is deployed at version %s, not %s as in the preview.", name, deployed, pair.DeployedVersionID))
		}

		rotation := rotations[pair.Name]
		latest, ok := now.latest[pair.Name]

		switch {
		case !ok:
			changes = append(changes, name+" has no version in CredHub any more.")
		case rotation.NewVersionID != "":
			if latest != rotation.NewVersionID {
				changes = append(changes, fmt.Sprintf("%s has a newer version (%s) that this task did not create.", name, latest))
			}
		case rotation.RegenerateState != "" && rotation.RegenerateState != RegenerateNotSent:
			// The regeneration step reconciles this variable's versions.
		case latest != pair.LatestVersionID:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) than the preview listed (%s).", name, latest, pair.LatestVersionID))
		}
	}

	return changes
}

// additionalChanges compares the variables that differ now, besides the
// selected ones, with those the preview listed.
func additionalChanges(approved, current []VersionPair) []string {
	listed := make(map[string]VersionPair, len(approved))
	for _, pair := range approved {
		listed[pair.Name] = pair
	}

	var changes []string

	for _, pair := range current {
		name := shortName(pair.Name)

		before, ok := listed[pair.Name]
		delete(listed, pair.Name)

		switch {
		case !ok:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) than the deployed one, which the preview did not list.", name, pair.LatestVersionID))
		case before.LatestVersionID != pair.LatestVersionID:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) than the preview listed (%s).", name, pair.LatestVersionID, before.LatestVersionID))
		case before.DeployedVersionID != pair.DeployedVersionID:
			changes = append(changes, fmt.Sprintf("%s is deployed at version %s, not %s as in the preview.", name, pair.DeployedVersionID, before.DeployedVersionID))
		}
	}

	changes = append(changes, droppedChanges(listed, "%s no longer has a newer version than the deployed one.")...)

	return changes
}

// droppedChanges returns the sentence for each expected variable that no
// longer differs, sorted by name.
func droppedChanges(left map[string]VersionPair, format string) []string {
	names := make([]string, 0, len(left))
	for name := range left {
		names = append(names, name)
	}

	sort.Strings(names)

	changes := make([]string, 0, len(names))
	for _, name := range names {
		changes = append(changes, fmt.Sprintf(format, shortName(name)))
	}

	return changes
}

// createdChanges requires each selected variable's latest version to be
// the one the task made. A selected variable with no new version is a
// change too, because nothing may be deployed or patched for it.
func createdChanges(approved []VersionPair, variables []VariableRotation, now effectsNow) []string {
	made := make(map[string]string, len(variables))
	for _, variable := range variables {
		made[variable.Name] = variable.NewVersionID
	}

	var changes []string

	for _, pair := range approved {
		name := shortName(pair.Name)

		want := made[pair.Name]
		latest, ok := now.latest[pair.Name]

		switch {
		case want == "":
			changes = append(changes, name+" has no version that this task made.")
		case !ok:
			changes = append(changes, name+" has no version in CredHub any more.")
		case latest != want:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) that this task did not create.", name, latest))
		}
	}

	return changes
}

// expectedAtConverge is the set of variables a converge expects to differ
// from their deployed versions: the additional ones at their approved
// latest versions, and the selected ones at the versions the task made.
func expectedAtConverge(approved ApprovedEffects, variables []VariableRotation) map[string]VersionPair {
	expected := make(map[string]VersionPair, len(approved.Additional)+len(approved.Selected))
	for _, pair := range approved.Additional {
		expected[pair.Name] = pair
	}

	made := make(map[string]string, len(variables))
	for _, variable := range variables {
		made[variable.Name] = variable.NewVersionID
	}

	for _, pair := range approved.Selected {
		if id := made[pair.Name]; id != "" {
			pair.LatestVersionID = id
		}

		expected[pair.Name] = pair
	}

	return expected
}

// convergeChanges compares the variables that differ now with the set a
// converge expects. selected names the variables the task regenerated.
func convergeChanges(expected map[string]VersionPair, selected map[string]bool, current []VersionPair) []string {
	left := make(map[string]VersionPair, len(expected))
	for name, pair := range expected {
		left[name] = pair
	}

	var changes []string

	for _, pair := range current {
		name := shortName(pair.Name)

		want, ok := left[pair.Name]
		delete(left, pair.Name)

		switch {
		case !ok:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) than the deployed one, which this task did not expect.", name, pair.LatestVersionID))
		case want.LatestVersionID != pair.LatestVersionID && selected[pair.Name]:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) that this task did not create.", name, pair.LatestVersionID))
		case want.LatestVersionID != pair.LatestVersionID:
			changes = append(changes, fmt.Sprintf("%s has a newer version (%s) than the preview listed (%s).", name, pair.LatestVersionID, want.LatestVersionID))
		}
	}

	changes = append(changes, droppedChanges(left, "%s no longer differs from its deployed version.")...)

	return changes
}
