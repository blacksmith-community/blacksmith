package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/internal/settings"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	vaultPkg "blacksmith/pkg/vault"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/yaml"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The outcomes an update's audit record names. A refusal also names its
// reason code.
const (
	updateOutcomeRefused   = "refused"
	updateOutcomeNoOp      = "no-op"
	updateOutcomeSucceeded = "succeeded"
	updateOutcomeFailed    = "failed"
)

// The reason codes a refused update's audit record names.
const (
	updateReasonMaintenanceInfo   = "maintenance_info"
	updateReasonPlanChange        = "plan_change"
	updateReasonPlanMissing       = "plan_missing"
	updateReasonInvalidParameters = "invalid_parameters"
	updateReasonLiteralRefused    = "literal_refused"
	updateReasonAsyncRequired     = "async_required"
	updateReasonDeploymentName    = "deployment_name"
)

// ErrUpdateNoVault is returned when the broker has no Vault to read the
// instance from, so an update can't be checked.
var ErrUpdateNoVault = errors.New("the broker has no Vault, so it can't check the update")

// updateTarget is what an update reads about the instance before it
// claims it, and again once it holds the claim.
type updateTarget struct {
	instance   *vaultPkg.Instance
	deployment string
	// parameters and context are the tenant values the instance's root
	// record holds now.
	parameters map[string]interface{}
	context    map[string]interface{}
}

// updateRequest is an update request merged over the stored values.
type updateRequest struct {
	planID      string
	parameters  map[string]interface{}
	changedKeys []string
	fingerprint string
	// actor names the platform user who sent the request. The update's
	// goroutine outlives the request, so its change records name the
	// user from here.
	actor string
}

// Update answers a PATCH of a service instance. It checks the request in
// a fixed order and writes nothing until every check has passed. A
// request that changes nothing in the rendered manifest, such as one that
// changes only the context, is committed at once and answered 200.
func (b *Broker) Update(
	ctx context.Context,
	instanceID string,
	details osbapi.UpdateRequest,
	asyncAllowed bool,
) (
	osbapi.UpdateResponse,
	bool,
	error,
) {
	logger := logger.Get().Named("broker")

	prevPlanID := ""
	if details.PreviousValues != nil {
		prevPlanID = details.PreviousValues.PlanID
	}

	logger.Info("Starting update of service instance %s (service: %s)", instanceID, details.ServiceID)
	logger.Debug("Update request - InstanceID: %s, ServiceID: %s, NewPlanID: %s, PreviousPlanID: %s",
		instanceID, details.ServiceID, details.PlanID, prevPlanID)

	if b.Vault == nil {
		return osbapi.UpdateResponse{}, false, ErrUpdateNoVault
	}

	err := b.tasksLoadingAnswer(instanceID, operationTypeUpdate, logger)
	if err != nil {
		return osbapi.UpdateResponse{}, false, err
	}

	target, err := b.readUpdateTarget(ctx, instanceID, details, false, logger)
	if err != nil {
		return osbapi.UpdateResponse{}, false, err
	}

	claim := &operationClaim{operation: operationTypeUpdate, claimed: time.Now()}

	held, claimed, err := b.claimOperation(instanceID, target.deployment, claim)
	if err != nil {
		return osbapi.UpdateResponse{}, false, guardBusyAnswer(instanceID, operationTypeUpdate, err, logger)
	}

	if !claimed {
		return b.answerUpdateWhileClaimed(ctx, instanceID, target, details, asyncAllowed, held, logger)
	}

	// Until the goroutine takes the claim over, Update owns it and gives
	// it up on every way out, a panic included.
	handedOff := false

	defer func() {
		if !handedOff {
			b.releaseOperation(instanceID, claim)
		}
	}()

	// The checks run again under the claim, so nothing that settled
	// between the first read and the claim, such as a deprovision, is
	// missed.
	target, err = b.readUpdateTarget(ctx, instanceID, details, true, logger)
	if err != nil {
		return osbapi.UpdateResponse{}, false, err
	}

	err = b.refuseUpdateWhileTaskRuns(ctx, instanceID, target.deployment, logger)
	if err != nil {
		return osbapi.UpdateResponse{}, false, err
	}

	response, async, handedOff, err := b.acceptUpdate(ctx, instanceID, target, details, asyncAllowed, claim, logger)

	return response, async, err
}

// refuseUpdateWhileTaskRuns answers 422 ConcurrencyError while a BOSH
// task runs on the deployment, such as one an operator started, and
// fails the update when the director can't say.
func (b *Broker) refuseUpdateWhileTaskRuns(ctx context.Context, instanceID, deployment string, logger logger.Logger) error {
	task, err := b.BOSH.FindRunningTaskForDeployment(ctx, deployment)
	if err != nil {
		logger.Error("Instance %s: could not tell whether deployment %s has a running BOSH task: %s", instanceID, deployment, err)

		return fmt.Errorf("failed to check for a running BOSH task on deployment %s: %w", deployment, err)
	}

	if task != nil {
		logger.Warning("Rejecting update of instance %s: BOSH task %d (%s) is still %s", instanceID, task.ID, task.Description, task.State)

		return fmt.Errorf("%w: a BOSH task is running on the service instance's deployment", osbapi.ErrConcurrencyError)
	}

	return nil
}

// readUpdateTarget reads the instance an update names from the index and
// its records, and refuses an instance the update can't act on. An
// instance the index doesn't have, a deleted one, one whose provision
// hasn't succeeded, and one that still owes a failed provision's cleanup
// get a 422 that says so. A request for another service gets a 400, and
// an operation still running gets a 422 ConcurrencyError.
func (b *Broker) readUpdateTarget(ctx context.Context, instanceID string, details osbapi.UpdateRequest, underClaim bool, logger logger.Logger) (updateTarget, error) {
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("Instance %s: its index entry could not be read for its update: %s", instanceID, err)

		return updateTarget{}, fmt.Errorf("failed to read the index entry of instance %s: %w", instanceID, err)
	}

	switch {
	case !exists:
		logger.Warning("Refusing the update of instance %s: the index has no such instance", instanceID)

		return updateTarget{}, unprocessable("The broker has no service instance with this ID")
	case instance.IsTombstone() || instance.Status == "deprovision_requested":
		logger.Warning("Refusing the update of instance %s: it is deleted or being deleted", instanceID)

		return updateTarget{}, unprocessable("The service instance is deleted or being deleted")
	}

	var record map[string]interface{}

	_, err = b.Vault.Get(ctx, instanceID+"/task", &record)
	if err != nil {
		logger.Error("Instance %s: its task record could not be read for its update: %s", instanceID, err)

		return updateTarget{}, fmt.Errorf("failed to read the task record of instance %s: %w", instanceID, err)
	}

	// Before the claim, an operation still in flight is left to the claim
	// step, which answers a repeat of the update in progress with 202 and
	// anything else with 422 ConcurrencyError. Under the claim, it is
	// refused, because no operation this update could answer for holds it.
	refusal := internalVault.UpdateRefusal(instanceID, record)
	if underClaim || !updateRefusalIsBusy(refusal) {
		err = updateRecordAnswer(instanceID, refusal, logger)
		if err != nil {
			return updateTarget{}, err
		}
	}

	if details.ServiceID != instance.ServiceID {
		logger.Warning("Refusing the update of instance %s: the request names service %s, and the instance belongs to service %s", instanceID, details.ServiceID, instance.ServiceID)

		return updateTarget{}, fmt.Errorf("%w: the service instance does not belong to the service the request names", osbapi.ErrBadRequest)
	}

	var root map[string]interface{}

	_, err = b.Vault.Get(ctx, instanceID, &root)
	if err != nil {
		logger.Error("Instance %s: its stored parameters could not be read for its update: %s", instanceID, err)

		return updateTarget{}, fmt.Errorf("failed to read the record of instance %s: %w", instanceID, err)
	}

	parameters, _ := root["parameters"].(map[string]interface{})
	contextData, _ := root["context"].(map[string]interface{})

	return updateTarget{
		instance:   instance,
		deployment: instance.DeploymentNameFor(instanceID),
		parameters: parameters,
		context:    contextData,
	}, nil
}

// updateRecordAnswer turns the reason an update can't replace the task
// record into the answer the platform gets, or returns nil when there is
// no reason.
func updateRecordAnswer(instanceID string, refusal error, logger logger.Logger) error {
	if refusal == nil {
		return nil
	}

	logger.Warning("Refusing the update of instance %s: %s", instanceID, refusal)

	switch {
	case errors.Is(refusal, internalVault.ErrCleanupOwed):
		return unprocessable("The service instance is still being cleaned up after a failed provision, so it can't be updated until that finishes")
	case errors.Is(refusal, internalVault.ErrInstanceDeleted):
		return unprocessable("The service instance is deleted or being deleted")
	case errors.Is(refusal, internalVault.ErrInstanceNotReady):
		return unprocessable("The service instance is not ready, because its provision has not succeeded; delete it and create it again")
	}

	return fmt.Errorf("%w: another operation on the service instance is still in progress", osbapi.ErrConcurrencyError)
}

// updateRefusalIsBusy reports whether refusal says only that another
// operation is still in flight, rather than that the instance can't be
// updated whatever happens next.
func updateRefusalIsBusy(refusal error) bool {
	if refusal == nil {
		return false
	}

	for _, final := range []error{internalVault.ErrCleanupOwed, internalVault.ErrInstanceDeleted, internalVault.ErrInstanceNotReady} {
		if errors.Is(refusal, final) {
			return false
		}
	}

	return true
}

// unprocessable is a plain 422 whose description the platform shows.
func unprocessable(description string) error {
	return &osbapi.OSBError{StatusCode: http.StatusUnprocessableEntity, Description: description}
}

// answerUpdateWhileClaimed answers an update of an instance another
// operation holds. While an update holds it, a request with the same
// fingerprint as the update on record gets 202 with that update's
// operation, so a repeated PATCH sees the same update in progress, unless
// the platform won't accept an asynchronous answer, which gets 422
// AsyncRequired as the first request would. Any other request gets 422
// ConcurrencyError.
func (b *Broker) answerUpdateWhileClaimed(ctx context.Context, instanceID string, target updateTarget, details osbapi.UpdateRequest, asyncAllowed bool, held *operationClaim, logger logger.Logger) (osbapi.UpdateResponse, bool, error) {
	switch {
	case held == nil:
		logger.Error("Rejecting update of instance %s: its operation claim holds a value of an unexpected type", instanceID)

		return osbapi.UpdateResponse{}, false, osbapi.ErrConcurrencyError
	case held.operation != operationTypeUpdate:
		logger.Warning("Rejecting update of instance %s: its %s has been in progress since %s", instanceID, held.operation, held.claimed.Format(time.RFC3339))

		return osbapi.UpdateResponse{}, false, fmt.Errorf("%w: instance %s has a %s in progress", osbapi.ErrConcurrencyError, instanceID, held.operation)
	case !held.readopted && !held.launched.Load():
		logger.Warning("Rejecting update of instance %s: an earlier update has been in its accept chain since %s and its outcome is not known yet", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.UpdateResponse{}, false, fmt.Errorf("%w: the update of instance %s is still being accepted", osbapi.ErrConcurrencyError, instanceID)
	}

	request := mergeUpdateRequest(target, details)
	request.actor = originatingActor(ctx)

	recorded, err := b.recordedUpdateFingerprint(ctx, instanceID)
	if err != nil {
		logger.Error("Rejecting update of instance %s: the update on record could not be read: %s", instanceID, err)

		return osbapi.UpdateResponse{}, false, fmt.Errorf("%w: the update in progress on instance %s could not be read", osbapi.ErrConcurrencyError, instanceID)
	}

	if recorded == "" || recorded != request.fingerprint {
		logger.Warning("Rejecting update of instance %s: a different update has been in progress since %s", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.UpdateResponse{}, false, fmt.Errorf("%w: a different update of instance %s is in progress", osbapi.ErrConcurrencyError, instanceID)
	}

	if !asyncAllowed {
		logger.Warning("Refusing the repeated update of instance %s: the update in progress since %s runs asynchronously, and the platform does not accept an asynchronous answer", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.UpdateResponse{}, false, osbapi.ErrAsyncRequired
	}

	logger.Info("Update of instance %s is already in progress since %s; accepting the repeated request without starting another", instanceID, held.claimed.Format(time.RFC3339))

	return osbapi.UpdateResponse{Operation: operationTypeUpdate}, true, nil
}

// recordedUpdateFingerprint returns the fingerprint of the update the
// instance's task record holds, or "" when it holds none.
func (b *Broker) recordedUpdateFingerprint(ctx context.Context, instanceID string) (string, error) {
	var record map[string]interface{}

	_, err := b.Vault.Get(ctx, instanceID+"/task", &record)
	if err != nil {
		return "", fmt.Errorf("failed to read the task record of instance %s: %w", instanceID, err)
	}

	if action, _ := record["action"].(string); action != operationTypeUpdate {
		return "", nil
	}

	staged, _ := record[internalVault.TaskRecordUpdateKey].(map[string]interface{})
	fingerprint, _ := staged["fingerprint"].(string)

	return fingerprint, nil
}

// acceptUpdate runs the checks an update makes under its claim, and
// answers it. It reports whether a goroutine took the claim over.
func (b *Broker) acceptUpdate(ctx context.Context, instanceID string, target updateTarget, details osbapi.UpdateRequest, asyncAllowed bool, claim *operationClaim, logger logger.Logger) (osbapi.UpdateResponse, bool, bool, error) {
	request := mergeUpdateRequest(target, details)
	request.actor = originatingActor(ctx)
	refuse := func(reason string, err error) (osbapi.UpdateResponse, bool, bool, error) {
		b.auditUpdate(ctx, instanceID, target, request, updateOutcomeRefused, reason, 0)

		return osbapi.UpdateResponse{}, false, false, err
	}

	if details.MaintenanceInfo != nil {
		logger.Warning("Refusing the update of instance %s: it carries maintenance_info, which the catalog does not publish", instanceID)

		return refuse(updateReasonMaintenanceInfo, fmt.Errorf("%w: the catalog publishes no maintenance_info for this plan", osbapi.ErrMaintenanceInfoConflict))
	}

	if request.planID != target.instance.PlanID {
		logger.Warning("Refusing the update of instance %s: it asks to move from plan %s to plan %s, and plan changes are not supported yet", instanceID, target.instance.PlanID, request.planID)

		return refuse(updateReasonPlanChange, unprocessable("Plan changes are not supported yet, so the service instance stays on its current plan"))
	}

	plan, err := b.FindPlan(target.instance.ServiceID, target.instance.PlanID)
	if err != nil {
		logger.Error("Refusing the update of instance %s: its plan %s is not in the catalog: %s", instanceID, target.instance.PlanID, err)

		return refuse(updateReasonPlanMissing, unprocessable("The service instance's plan is no longer in the catalog, so it can't be updated"))
	}

	err = b.refuseInvalidParameters(instanceID, target.instance.ServiceID, plan.ID, request.parameters, logger)
	if err != nil {
		return refuse(updateReasonInvalidParameters, err)
	}

	renders, err := b.renderUpdate(ctx, instanceID, plan, target, request, logger)
	if errors.Is(err, graft.ErrLiteralRefused) {
		return refuse(updateReasonLiteralRefused, fmt.Errorf("%w: %s", osbapi.ErrInvalidParameters, refusedParamsDescription))
	}

	if errors.Is(err, errUpdateRenamesDeployment) {
		return refuse(updateReasonDeploymentName, unprocessable("The update would rename the service instance's deployment, so it was refused"))
	}

	if err != nil {
		return osbapi.UpdateResponse{}, false, false, err
	}

	if renders.equal {
		err = b.commitUpdateValues(ctx, instanceID, target, request, details.Context, logger)
		if err != nil {
			return osbapi.UpdateResponse{}, false, false, err
		}

		b.auditUpdate(ctx, instanceID, target, request, updateOutcomeNoOp, "", 0)
		logger.Info("Update of instance %s changes nothing in its deployment, so its values were committed without a deploy", instanceID)

		return osbapi.UpdateResponse{}, false, false, nil
	}

	if !asyncAllowed {
		logger.Warning("Refusing the update of instance %s: it needs a deploy, and the platform does not accept an asynchronous answer", instanceID)

		return refuse(updateReasonAsyncRequired, osbapi.ErrAsyncRequired)
	}

	phase := &updatePhase{broker: b, instanceID: instanceID, target: target, request: request, context: details.Context, plan: plan, rendered: renders.requested, logger: logger}

	return b.launchUpdate(ctx, phase, claim)
}

// mergeUpdateRequest merges the request's parameters over the stored
// ones. A key sets or replaces the stored value, a key whose value is null
// removes it, and a key the request leaves out keeps its value, so an
// absent or empty parameters field changes nothing. An absent plan_id
// keeps the instance's plan.
func mergeUpdateRequest(target updateTarget, details osbapi.UpdateRequest) updateRequest {
	merged := maps.Clone(target.parameters)
	if merged == nil {
		merged = map[string]interface{}{}
	}

	for key, value := range details.Parameters {
		if value == nil {
			delete(merged, key)

			continue
		}

		merged[key] = value
	}

	planID := details.PlanID
	if planID == "" {
		planID = target.instance.PlanID
	}

	return updateRequest{
		planID:      planID,
		parameters:  merged,
		changedKeys: changedParameterKeys(target.parameters, merged),
		fingerprint: updateFingerprint(planID, merged),
	}
}

// changedParameterKeys names the parameter keys, by their dotted paths,
// that were added, changed, or removed. It never returns a value.
func changedParameterKeys(before, after map[string]interface{}) []string {
	changes := audit.Diff(audit.Flatten(before), audit.Flatten(after))

	keys := make([]string, 0, len(changes))
	for _, change := range changes {
		keys = append(keys, change.Key)
	}

	sort.Strings(keys)

	return keys
}

// updateFingerprint is the digest of the target plan and the canonical
// JSON of the merged parameters, which encoding/json writes with its keys
// sorted.
func updateFingerprint(planID string, parameters map[string]interface{}) string {
	encoded, err := json.Marshal(parameters)
	if err != nil {
		encoded = []byte(fmt.Sprintf("unencodable %d", len(parameters)))
	}

	sum := sha256.Sum256(append([]byte(planID+"\n"), encoded...))

	return hex.EncodeToString(sum[:])
}

// errUpdateRenamesDeployment reports a requested render whose name is not
// the deployment's recorded name.
var errUpdateRenamesDeployment = errors.New("the requested manifest names another deployment")

// updateRenders holds an update's requested manifest, and whether it has
// the same effect as the current one.
type updateRenders struct {
	requested string
	equal     bool
}

// renderUpdate renders the instance's current manifest from its stored
// parameters and the requested one from the merged parameters, with the
// same call and defaults a provision uses and the recorded deployment
// name, and compares them. A current render that fails counts as
// different, because the requested one is then the only one known to
// deploy.
func (b *Broker) renderUpdate(ctx context.Context, instanceID string, plan services.Plan, target updateTarget, request updateRequest, logger logger.Logger) (updateRenders, error) {
	info, err := b.BOSH.GetInfo(ctx)
	if err != nil {
		logger.Error("Instance %s: the BOSH director could not be reached to render its update: %s", instanceID, err)

		return updateRenders{}, fmt.Errorf("failed to get the BOSH director's information: %w", err)
	}

	defaults := map[string]interface{}{"name": target.deployment, "director_uuid": info.UUID}

	// GenManifest takes no context, so each render runs to its end under
	// manifestMu, as a provision's render does.
	requested, err := renderInstanceManifest(plan, instanceID, defaults, request.parameters) //nolint:contextcheck // GenManifest takes no context
	if err != nil {
		return updateRenders{}, err
	}

	requestedDoc, err := comparableManifest(requested)
	if err != nil {
		return updateRenders{}, err
	}

	if name, _ := requestedDoc["name"].(string); name != target.deployment {
		logger.Error("Instance %s: the requested manifest names deployment %q, not its recorded deployment %s", instanceID, name, target.deployment)

		return updateRenders{}, errUpdateRenamesDeployment
	}

	current, err := renderInstanceManifest(plan, instanceID, defaults, target.parameters) //nolint:contextcheck // GenManifest takes no context
	if err != nil {
		logger.Warning("Instance %s: its current manifest could not be rendered from its stored parameters, so the update is treated as a change: %s", instanceID, err)

		return updateRenders{requested: requested}, nil
	}

	currentDoc, err := comparableManifest(current)
	if err != nil {
		return updateRenders{requested: requested}, nil //nolint:nilerr // an unreadable current render counts as different
	}

	return updateRenders{requested: requested, equal: reflect.DeepEqual(currentDoc, requestedDoc)}, nil
}

// renderInstanceManifest renders plan for instanceID with parameters, as
// a provision's render does.
func renderInstanceManifest(plan services.Plan, instanceID string, defaults, parameters map[string]interface{}) (string, error) {
	params := maps.Clone(parameters)
	if params == nil {
		params = map[string]interface{}{}
	}

	params["instance_id"] = instanceID

	rendered, err := manifest.GenManifest(plan, instanceID, maps.Clone(defaults), utils.Wrap("meta.params", params))
	if err != nil {
		return "", fmt.Errorf("failed to render the manifest of instance %s: %w", instanceID, err)
	}

	return rendered, nil
}

// comparableManifest parses a rendered manifest and drops its top-level
// meta, which holds the render's inputs, the parameters among them, and
// which BOSH never acts on. A parameter the plan's templates don't read
// then leaves the manifest as it was.
func comparableManifest(rendered string) (map[string]interface{}, error) {
	var parsed map[string]interface{}

	err := yaml.Unmarshal([]byte(rendered), &parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to parse a rendered manifest: %w", err)
	}

	delete(parsed, "meta")

	return parsed, nil
}

// commitUpdateValues writes an update's merged parameters, and its
// context when it carries one, to the instance's root record and to the
// provision request stored under its deployment, and the context's names
// to its index entry. Each write keeps every other field. The writes run
// detached from the request, so one that ends part way leaves no record
// half written.
func (b *Broker) commitUpdateValues(ctx context.Context, instanceID string, target updateTarget, request updateRequest, contextData map[string]interface{}, logger logger.Logger) error {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	setValues := func(record map[string]interface{}) {
		if len(request.parameters) == 0 {
			delete(record, "parameters")
		} else {
			record["parameters"] = request.parameters
		}

		if len(contextData) > 0 {
			record["context"] = contextData
		}
	}

	var root map[string]interface{}

	_, err := b.Vault.Get(recordCtx, instanceID, &root)
	if err != nil {
		return fmt.Errorf("failed to read the record of instance %s to commit its update: %w", instanceID, err)
	}

	if root == nil {
		root = map[string]interface{}{}
	}

	setValues(root)

	err = b.Vault.Put(recordCtx, instanceID, root)
	if err != nil {
		logger.Error("Instance %s: its updated values could not be written to its record: %s", instanceID, err)

		return fmt.Errorf("failed to write the updated record of instance %s: %w", instanceID, err)
	}

	detailsPath := instanceID + "/" + target.deployment

	var stored map[string]interface{}

	exists, err := b.Vault.Get(recordCtx, detailsPath, &stored)
	if err != nil {
		return fmt.Errorf("failed to read the provision request of instance %s to commit its update: %w", instanceID, err)
	}

	if requestDetails, isMap := stored["details"].(map[string]interface{}); exists && isMap {
		setValues(requestDetails)

		err = b.Vault.Put(recordCtx, detailsPath, stored)
		if err != nil {
			logger.Error("Instance %s: its updated values could not be written to its provision request: %s", instanceID, err)

			return fmt.Errorf("failed to write the updated provision request of instance %s: %w", instanceID, err)
		}
	}

	if len(contextData) == 0 {
		return nil
	}

	err = b.mergeIndexEntry(recordCtx, instanceID, map[string]interface{}{}, contextData)
	if err != nil {
		logger.Error("Instance %s: its updated context could not be written to its index entry: %s", instanceID, err)

		return err
	}

	return nil
}

// auditUpdate writes the change record of an update that reached its
// checks under the claim. It names the parameter keys and never their
// values, and the actor is the user the request named.
func (b *Broker) auditUpdate(ctx context.Context, instanceID string, target updateTarget, request updateRequest, outcome, reason string, taskID int) {
	b.changeStore().Record(ctx, updateChangeRecord(instanceID, target, request, outcome, reason, taskID))
}

// auditUpdateFailure writes the change record of an update whose deploy
// failed. It names the failed deploy's task, the rollback's task when one
// ran, and the rollback setting in effect at the failure, and never any
// manifest content.
func (b *Broker) auditUpdateFailure(ctx context.Context, instanceID string, target updateTarget, request updateRequest, outcome string, rollback internalVault.UpdateRollback) {
	record := updateChangeRecord(instanceID, target, request, outcome, "", rollback.FailedTaskID)
	record.RollbackTaskID = rollback.TaskID
	record.Setting = fmt.Sprintf("%s=%t (%s)", settings.UpdateRollbackKey, rollback.Setting, rollback.SettingSource)

	b.changeStore().Record(ctx, record)
}

// updateChangeRecord builds the change record of an update.
func updateChangeRecord(instanceID string, target updateTarget, request updateRequest, outcome, reason string, taskID int) audit.ChangeRecord {
	return audit.ChangeRecord{
		Scope:      audit.ScopeInstances,
		Target:     instanceID,
		Instance:   instanceID,
		Deployment: target.deployment,
		Action:     operationTypeUpdate,
		Outcome:    outcome,
		Reason:     reason,
		TaskID:     taskID,
		Old:        target.instance.PlanID,
		New:        request.planID,
		Keys:       strings.Join(request.changedKeys, ", "),
		Actor:      request.actor,
	}
}

// originatingActor names the platform user the request's originating
// identity carries, or "" when it carries none.
func originatingActor(ctx context.Context) string {
	identity, found := osbapi.OriginatingIdentityFromContext(ctx)
	if !found {
		return ""
	}

	user, _ := identity.Value["user_id"].(string)
	if user == "" {
		return identity.Platform
	}

	return identity.Platform + ":" + user
}
