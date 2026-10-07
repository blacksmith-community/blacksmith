package broker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// unknownOutcomeVerdict is what one pass of the unknown-outcome resolver
// learned from the director.
type unknownOutcomeVerdict int

const (
	// outcomeStillUnknown leaves the record as it is, and the platform
	// polls again.
	outcomeStillUnknown unknownOutcomeVerdict = iota
	// outcomeTaskFound names the task the request started.
	outcomeTaskFound
	// outcomeDeploymentGone is a deprovision whose deployment the director
	// says, with its deployment-not-found code, does not exist.
	outcomeDeploymentGone
	// outcomeNeverStarted is a request the director never took as a task,
	// shown by a lookup that succeeded after the grace period.
	outcomeNeverStarted
)

// unknownOutcomeFinding is the verdict of one resolver pass with what it
// found.
type unknownOutcomeFinding struct {
	verdict unknownOutcomeVerdict
	// taskID is the task found, for outcomeTaskFound.
	taskID int
	// source says how the task was found, for outcomeTaskFound.
	source string
	// err is the error of the director call that failed, when one did.
	err error
	// reason says why the outcome is still unknown when no call failed
	// and the grace period has passed.
	reason string
}

// unknownOutcomeNow returns the time an unknown outcome is recorded and
// judged by.
func (b *Broker) unknownOutcomeNow() time.Time {
	if b.unknownOutcomeClock != nil {
		return b.unknownOutcomeClock()
	}

	return time.Now()
}

// requestNoun names the director request an operation sends.
func requestNoun(action string) string {
	if action == operationTypeDeprovision {
		return "delete"
	}

	return "deploy"
}

// unknownOutcomePassTimeout bounds the director lookups of one resolver
// pass, so a poll answers in about that long however slow the director
// is.
const unknownOutcomePassTimeout = 10 * time.Second

// unknownOutcomePass keeps the resolver passes over one instance's unknown
// outcome to one at a time, and holds the error the last pass ended with.
type unknownOutcomePass struct {
	// running is held by the pass that runs.
	running sync.Mutex

	lastErrMu sync.Mutex
	lastErr   string
}

func (pass *unknownOutcomePass) setLastErr(err error) {
	pass.lastErrMu.Lock()
	defer pass.lastErrMu.Unlock()

	pass.lastErr = ""
	if err != nil {
		pass.lastErr = err.Error()
	}
}

func (pass *unknownOutcomePass) lastError() string {
	pass.lastErrMu.Lock()
	defer pass.lastErrMu.Unlock()

	return pass.lastErr
}

// unknownOutcomePassFor returns the pass state of an instance.
func (b *Broker) unknownOutcomePassFor(instanceID string) *unknownOutcomePass {
	value, _ := b.unknownOutcomePasses.LoadOrStore(instanceID, &unknownOutcomePass{})
	pass, _ := value.(*unknownOutcomePass)

	return pass
}

// resolveUnknownOutcome settles an operation whose director request lost
// its answer, from what the director says now. deploymentName is the
// deployment the instance's index entry or the poll names, and the
// deployment the record names wins over it. It answers in progress until
// the director shows what happened, and it changes nothing while any
// director call fails.
//
// One pass runs per instance at a time, and a poll that finds one running
// answers in progress with the error the last pass ended with. The
// record's second read and the director lookups of a pass run under
// unknownOutcomePassTimeout with the director client's retries turned
// off. Once a lookup has settled the
// outcome, the pass follows it with the poll's own context, as any other
// poll would.
func (b *Broker) resolveUnknownOutcome(ctx context.Context, instanceID, deploymentName string, operation recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	noun := requestNoun(operation.Action)
	pass := b.unknownOutcomePassFor(instanceID)

	if !pass.running.TryLock() {
		logger.Info("A check of the %s of instance %s with context ID %s is already running, so this poll answers in progress", noun, instanceID, operation.ContextID)

		description := fmt.Sprintf("A check of whether the director started the %s with context ID %s is already running", noun, operation.ContextID)
		if lastErr := pass.lastError(); lastErr != "" {
			description += "; the last check ended with " + lastErr
		}

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: description}, nil
	}

	defer pass.running.Unlock()

	passCtx, cancel := context.WithTimeout(boshapi.WithoutRetries(ctx), unknownOutcomePassTimeout)
	defer cancel()

	// A pass that ran while this poll read the record may have settled
	// it, so the record is read again under the pass's lock.
	current, err := b.readRecordedOperation(passCtx, instanceID)
	if err != nil {
		pass.setLastErr(err)
		logger.Error("Instance %s: the record of the %s with context ID %s could not be read again: %s", instanceID, noun, operation.ContextID, err)

		return osbapi.LastOperationResponse{
			State:       osbapi.StateInProgress,
			Description: fmt.Sprintf("Waiting for the director to show whether it started the %s with context ID %s; %s", noun, operation.ContextID, err),
		}, nil
	}

	if current.State != internalVault.TaskStateOutcomeUnknown || current.Action != operation.Action || current.ContextID != operation.ContextID {
		logger.Info("Instance %s: the unknown outcome of the %s with context ID %s was settled while this poll waited", instanceID, noun, operation.ContextID)

		return osbapi.LastOperationResponse{
			State:       osbapi.StateInProgress,
			Description: fmt.Sprintf("The outcome of the %s with context ID %s was just settled", noun, operation.ContextID),
		}, nil
	}

	if current.DeploymentName != "" {
		deploymentName = current.DeploymentName
	}

	finding := b.findUnknownOutcome(passCtx, deploymentName, current, logger)

	cancel()
	pass.setLastErr(finding.err)

	return b.settleUnknownOutcome(ctx, instanceID, deploymentName, current, finding, logger)
}

// findUnknownOutcome asks the director what became of the request that
// carried the operation's context ID. It looks for the task by its context
// ID first, and then at the deployment. A deprovision whose deployment the
// director says does not exist is settled as deleted. A provision adopts a
// task running on its deployment, or the task a create event on it names.
// Only after a lookup by the context ID that succeeded, made at or after the
// grace period's end, found no task, and the deployment check found nothing
// either, is the request taken as never started. For a provision, that also
// takes the director's word that the deployment does not exist.
func (b *Broker) findUnknownOutcome(ctx context.Context, deploymentName string, operation recordedOperation, logger logger.Logger) unknownOutcomeFinding {
	// The time is read before the lookup, so the lookup is made at or
	// after it.
	now := b.unknownOutcomeNow()

	task, err := b.BOSH.FindTaskByContextID(ctx, operation.ContextID)
	if err != nil {
		return unknownOutcomeFinding{err: fmt.Errorf("the lookup of the task with context ID %s failed: %w", operation.ContextID, err)}
	}

	if task != nil {
		return unknownOutcomeFinding{verdict: outcomeTaskFound, taskID: task.ID, source: "its context ID " + operation.ContextID}
	}

	switch operation.Action {
	case operationTypeDeprovision:
		_, err = b.BOSH.GetDeployment(ctx, deploymentName)
		if errors.Is(err, bosh.ErrDeploymentNotFound) {
			return unknownOutcomeFinding{verdict: outcomeDeploymentGone}
		}

		if err != nil {
			return unknownOutcomeFinding{err: fmt.Errorf("the check of deployment %s failed: %w", deploymentName, err)}
		}
	case operationTypeProvision:
		finding, found := b.findCreateTask(ctx, deploymentName, logger)
		if found {
			return finding
		}
	default:
		return unknownOutcomeFinding{reason: fmt.Sprintf("the record names the action %q, which no deployment check covers", operation.Action)}
	}

	if operation.UnknownSince.IsZero() {
		return unknownOutcomeFinding{reason: "the record does not say when the outcome became unknown"}
	}

	if now.Before(operation.UnknownSince.Add(bosh.UnknownOutcomeGrace)) {
		return unknownOutcomeFinding{}
	}

	if operation.Action == operationTypeProvision {
		_, err = b.BOSH.GetDeployment(ctx, deploymentName)
		if err == nil {
			return unknownOutcomeFinding{reason: fmt.Sprintf("deployment %s exists, and no task the director lists names it", deploymentName)}
		}

		if !errors.Is(err, bosh.ErrDeploymentNotFound) {
			return unknownOutcomeFinding{err: fmt.Errorf("the check of deployment %s failed: %w", deploymentName, err)}
		}
	}

	return unknownOutcomeFinding{verdict: outcomeNeverStarted}
}

// findCreateTask looks for the deploy task of a provision on its
// deployment, the way recoverProvision does. It reports true with the
// finding when a task runs on the deployment, when a create event names
// one, or when either check fails.
func (b *Broker) findCreateTask(ctx context.Context, deploymentName string, logger logger.Logger) (unknownOutcomeFinding, bool) {
	running, err := b.BOSH.FindRunningTaskForDeployment(ctx, deploymentName)
	if err != nil {
		return unknownOutcomeFinding{err: fmt.Errorf("the check for a task running on deployment %s failed: %w", deploymentName, err)}, true
	}

	if running != nil {
		return unknownOutcomeFinding{verdict: outcomeTaskFound, taskID: running.ID, source: "a task running on deployment " + deploymentName}, true
	}

	taskID, err := b.findDeploymentEventTask(ctx, deploymentName, boshEventActionCreate, logger)
	if err != nil {
		return unknownOutcomeFinding{err: fmt.Errorf("the read of the events of deployment %s failed: %w", deploymentName, err)}, true
	}

	if taskID > 0 {
		return unknownOutcomeFinding{verdict: outcomeTaskFound, taskID: taskID, source: "a create event on deployment " + deploymentName}, true
	}

	return unknownOutcomeFinding{}, false
}

// settleUnknownOutcome acts on a resolver pass's finding. A task found is
// recorded and then followed like any other recorded task. A deployment
// the director says is gone settles a deprovision as deleted. A request
// the director never took as a task fails the operation, and a provision
// failed this way gets the cleanup of any failed provision. Anything else
// answers in progress with the context ID, and the record is left as it
// is.
func (b *Broker) settleUnknownOutcome(ctx context.Context, instanceID, deploymentName string, operation recordedOperation, finding unknownOutcomeFinding, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	noun := requestNoun(operation.Action)

	switch finding.verdict {
	case outcomeTaskFound:
		logger.Info("The %s of instance %s with context ID %s started task %d, found by %s", noun, instanceID, operation.ContextID, finding.taskID, finding.source)

		if operation.Action == operationTypeDeprovision {
			b.recordTask(ctx, instanceID, operation.Action, fmt.Sprintf("BOSH deletion in progress (task %d)", finding.taskID), finding.taskID, logger)

			return b.handleDeprovisionTask(ctx, instanceID, deploymentName, finding.taskID, logger)
		}

		b.recordTask(ctx, instanceID, operation.Action, fmt.Sprintf("BOSH deployment in progress (task %d)", finding.taskID), finding.taskID, logger)

		return b.handleProvisionTask(ctx, instanceID, deploymentName, finding.taskID, logger)
	case outcomeDeploymentGone:
		return b.settleUnknownDeleteAsDeleted(ctx, instanceID, deploymentName, operation, logger)
	case outcomeNeverStarted:
		return b.failUnknownOutcome(ctx, instanceID, deploymentName, operation, logger), nil
	case outcomeStillUnknown:
	}

	description := fmt.Sprintf("Waiting for the director to show whether it started the %s with context ID %s", noun, operation.ContextID)

	switch {
	case finding.err != nil:
		logger.Warning("The outcome of the %s of instance %s with context ID %s is still unknown, because %s", noun, instanceID, operation.ContextID, finding.err)

		description += fmt.Sprintf("; %s", finding.err)
	case finding.reason != "":
		logger.Warning("The outcome of the %s of instance %s with context ID %s is still unknown: %s", noun, instanceID, operation.ContextID, finding.reason)

		description += "; " + finding.reason
	default:
		logger.Debug("The outcome of the %s of instance %s with context ID %s is still unknown, and its grace period runs until %s", noun, instanceID, operation.ContextID, operation.UnknownSince.Add(bosh.UnknownOutcomeGrace).Format(time.RFC3339))
	}

	return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: description}, nil
}

// settleUnknownDeleteAsDeleted finishes a deprovision whose deployment the
// director says does not exist, as a finished delete task would, and then
// records the deprovision as finished.
func (b *Broker) settleUnknownDeleteAsDeleted(ctx context.Context, instanceID, deploymentName string, operation recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	logger.Info("The director says deployment %s of instance %s does not exist, so the delete with context ID %s is settled as deleted", deploymentName, instanceID, operation.ContextID)

	response, err := b.handleSuccessfulDeprovision(ctx, instanceID, deploymentName, logger)
	if err != nil || response.State != osbapi.StateSucceeded {
		return response, err
	}

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	trackErr := b.Vault.Track(recordCtx, instanceID, operationTypeDeprovision, 0, nil)
	if trackErr != nil {
		logger.Error("failed to record the settled delete with context ID %s of instance %s: %s", operation.ContextID, instanceID, trackErr)
	}

	return response, nil
}

// failUnknownOutcome records an operation whose request the director never
// took as a task as failed. The index entry stays, so a deprovision can be
// retried, and a failed provision is cleaned up as any other is.
func (b *Broker) failUnknownOutcome(ctx context.Context, instanceID, deploymentName string, operation recordedOperation, logger logger.Logger) osbapi.LastOperationResponse {
	description := fmt.Sprintf("The deploy with context ID %s never reached the director as a task, so nothing was deployed", operation.ContextID)
	if operation.Action == operationTypeDeprovision {
		description = fmt.Sprintf("The delete with context ID %s never reached the director as a task, so nothing was deleted. Retry the deprovision", operation.ContextID)
	}

	logger.Error("Instance %s: %s (the outcome was unknown since %s)", instanceID, description, operation.UnknownSince.Format(time.RFC3339))

	b.recordTask(ctx, instanceID, operation.Action, description, -1, logger)

	if operation.Action == operationTypeProvision {
		// The cleanup outlives this poll, as it does for a deploy task
		// that failed.
		go b.cleanupFailedDeployment(ctx, deploymentName, logger)
	}

	return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: description}
}
