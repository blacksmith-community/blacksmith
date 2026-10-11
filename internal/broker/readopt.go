package broker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

const (
	// readoptBackoffStart is the first wait before the first phase of
	// re-adoption reads Vault again after a failed read. The wait doubles
	// up to readoptBackoffMax.
	readoptBackoffStart = 5 * time.Second

	// readoptBackoffMax is the longest wait between two reads of Vault in
	// the first phase of re-adoption.
	readoptBackoffMax = time.Minute

	// readoptBackoffFactor is how much each failed read of Vault in the
	// first phase of re-adoption lengthens the wait before the next.
	readoptBackoffFactor = 2

	// readoptRetryInterval is how often a re-adopted operation is checked
	// again while it has not settled.
	readoptRetryInterval = 30 * time.Second
)

// readoptReadWorkers is how many record reads the first phase of
// re-adoption makes at once.
//
//nolint:gochecknoglobals // a limit on the reads, kept beside the others
var readoptReadWorkers = 8

// readoptDeploymentChecks is how many re-adopted operations whose index
// entry is gone make their first deployment check at once, so a first
// boot with many old records does not flood the director.
//
//nolint:gochecknoglobals // a limit on the director calls, kept beside the others
var readoptDeploymentChecks = 4

// readoptIntervalOverride, when positive, replaces readoptBackoffStart,
// readoptBackoffMax, and readoptRetryInterval in nanoseconds.
//
//nolint:gochecknoglobals // a test seam, set only through export_test.go
var readoptIntervalOverride atomic.Int64

// readoptBoundOverride, when positive, replaces the bound of every
// re-adoption goroutine in nanoseconds.
//
//nolint:gochecknoglobals // a test seam, set only through export_test.go
var readoptBoundOverride atomic.Int64

// readoptStore is the part of Vault that the first phase of re-adoption
// reads.
type readoptStore interface {
	GetIndex(ctx context.Context, path string) (*vaultPkg.Index, error)
	ListOperationRecordIDs(ctx context.Context) ([]string, bool, error)
	State(ctx context.Context, instanceID string) (string, int, map[string]interface{}, error)
	Get(ctx context.Context, path string, out interface{}) (bool, error)
}

// taskAdopter is a director that can count a task started before a
// restart against its limit and follow it, such as bosh.PooledDirector.
type taskAdopter interface {
	Adopt(taskID int)
}

// readoptCandidate is an instance whose operation record the first phase
// of re-adoption reads.
type readoptCandidate struct {
	instanceID string

	// indexed reports whether the index names the instance, and entry is
	// its index entry then.
	indexed        bool
	entry          map[string]interface{}
	deploymentName string

	// exists reports whether the instance has an operation record, and
	// recorded holds it when it does and it could be parsed.
	exists   bool
	parsed   bool
	recorded recordedOperation

	// metadata is <id>/metadata, read only for a deprovision without an
	// index entry that carries a context ID.
	metadata map[string]interface{}

	// credentialsStored reports whether <id>/credentials exists. It is
	// read only for an indexed provision in progress whose index entry
	// has created_at.
	credentialsStored bool
}

// readoptOperation is an operation the first phase of re-adoption claimed.
type readoptOperation struct {
	instanceID     string
	indexed        bool
	deploymentName string
	recorded       recordedOperation
	claim          *operationClaim
}

// readoptMigration is a record that the first phase found finished, and
// that one write marks as finished.
type readoptMigration struct {
	instanceID string
	recorded   recordedOperation
	state      string
}

// ReadoptInFlightOperations finds the provisions and deprovisions that a
// previous run of the broker left in flight, and claims each of them, so
// that when it returns every such operation holds its claim. Then one
// goroutine for each settles it as LastOperation would, and releases its
// claim when it ends. It reads the instance index, lists the mount to find
// the records whose index entry is gone, and reads each instance's record.
// A failed read is retried with a backoff until Vault answers or ctx ends,
// and no claim is taken until every read has succeeded. It returns an
// error only when ctx ends first.
//
// A record that is finished but not marked so, a provision whose index
// entry has created_at and whose credentials are stored, or a delete whose
// deleted_at is on record, is not claimed. One goroutine marks each of
// them, with no director call. With no director, nothing is re-adopted.
func (b *Broker) ReadoptInFlightOperations(ctx context.Context) error {
	logger := logger.Get().Named("readopt")

	if b.BOSH == nil {
		logger.Warning("No BOSH director is configured, so the service operations left in flight are not re-adopted")

		return nil
	}

	started := time.Now()
	store := b.readoptVaultStore()

	candidates, listed, err := b.readoptListing(ctx, store, logger)
	if err != nil {
		return err
	}

	err = readoptReadRecords(ctx, store, candidates, logger)
	if err != nil {
		return err
	}

	err = readoptReadMetadata(ctx, store, candidates, logger)
	if err != nil {
		return err
	}

	err = readoptReadCredentials(ctx, store, candidates, logger)
	if err != nil {
		return err
	}

	operations, migrations := sortReadoptCandidates(candidates, logger)

	loaded := time.Now()
	claimed := make([]readoptOperation, 0, len(operations))

	for _, operation := range operations {
		claim := &operationClaim{operation: operation.recorded.Action, claimed: loaded, readopted: true}
		claim.launched.Store(true)

		held, ok := b.claimOperation(operation.instanceID, claim)
		if !ok {
			heldOperation := "an operation"
			if held != nil {
				heldOperation = "a " + held.operation
			}

			logger.Info("Instance %s already has %s in progress, so its recorded %s is not re-adopted", operation.instanceID, heldOperation, operation.recorded.Action)

			continue
		}

		operation.claim = claim
		claimed = append(claimed, operation)
	}

	logger.Info("Re-adoption listed %d folders, read %d records, claimed %d operations, and found %d finished records to mark, in %v",
		listed, len(candidates), len(claimed), len(migrations), time.Since(started).Round(time.Millisecond))

	if len(migrations) > 0 {
		b.track("marking of finished service operations", func() { //nolint:contextcheck // the marking outlives the boot call and runs under the service context
			b.markFinishedRecords(migrations, logger)
		})
	}

	checks := make(chan struct{}, readoptDeploymentChecks)

	for _, operation := range claimed {
		b.track(fmt.Sprintf("re-adoption of the %s of instance %s", operation.recorded.Action, operation.instanceID), func() { //nolint:contextcheck // the operation outlives the boot call and runs under the service context
			b.readoptOperation(operation, loaded, checks, logger)
		})
	}

	return nil
}

// readoptVaultStore returns the Vault the first phase of re-adoption reads.
func (b *Broker) readoptVaultStore() readoptStore {
	if b.readoptVault != nil {
		return b.readoptVault
	}

	return b.Vault
}

// readoptListing reads the index and lists the mount, and retries both
// until both succeed. It returns a candidate for every indexed instance
// and for every listed folder the index does not name, and how many
// folders the listing held. A listing that does not hold the index while
// the index has entries is taken as failed, because a mount that holds
// the index must list it.
func (b *Broker) readoptListing(ctx context.Context, store readoptStore, logger logger.Logger) ([]*readoptCandidate, int, error) {
	backoff := newReadoptBackoff()

	for {
		candidates, listed, err := readoptListOnce(ctx, store)
		if err == nil {
			return candidates, listed, nil
		}

		if errors.Is(err, internalVault.ErrMountListRefused) {
			logger.Error("Re-adoption could not list the Vault mount, because the token cannot list it, and lists it again in %v: %s", backoff.next(), err)
		} else {
			logger.Error("Re-adoption could not read the instance index and the list of instances from Vault, and reads them again in %v: %s", backoff.next(), err)
		}

		waitErr := backoff.wait(ctx)
		if waitErr != nil {
			return nil, 0, fmt.Errorf("re-adoption of service operations stopped before Vault answered: %w", waitErr)
		}
	}
}

func readoptListOnce(ctx context.Context, store readoptStore) ([]*readoptCandidate, int, error) {
	idx, err := store.GetIndex(ctx, "db")
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read the instance index: %w", err)
	}

	ids, indexListed, err := store.ListOperationRecordIDs(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list the instances in Vault: %w", err)
	}

	if len(idx.Data) > 0 && !indexListed {
		return nil, 0, fmt.Errorf("%w: the index has %d entries, but the listing of the mount does not hold it", errReadoptListingIncomplete, len(idx.Data))
	}

	candidates := make([]*readoptCandidate, 0, len(idx.Data)+len(ids))
	named := make(map[string]bool, len(idx.Data))

	indexedIDs := make([]string, 0, len(idx.Data))
	for instanceID := range idx.Data {
		indexedIDs = append(indexedIDs, instanceID)
	}

	sort.Strings(indexedIDs)

	for _, instanceID := range indexedIDs {
		raw := idx.Data[instanceID]
		if raw == nil {
			continue
		}

		entry := copyIndexEntry(raw)
		named[instanceID] = true

		candidates = append(candidates, &readoptCandidate{
			instanceID:     instanceID,
			indexed:        true,
			entry:          entry,
			deploymentName: indexEntryDeploymentName(instanceID, entry),
		})
	}

	sort.Strings(ids)

	for _, instanceID := range ids {
		if named[instanceID] {
			continue
		}

		candidates = append(candidates, &readoptCandidate{instanceID: instanceID})
	}

	return candidates, len(ids), nil
}

// errReadoptListingIncomplete is the error for a listing of the mount that
// does not hold the index the mount holds.
var errReadoptListingIncomplete = errors.New("the listing of the Vault mount is incomplete")

// readoptReadRecords reads the record of every candidate, eight at a time.
// A failed read is retried by itself until it succeeds, and the reads
// that succeeded are kept.
func readoptReadRecords(ctx context.Context, store readoptStore, candidates []*readoptCandidate, logger logger.Logger) error {
	return readoptInParallel(ctx, len(candidates), func(index int) error {
		candidate := candidates[index]

		return readoptRetryRead(ctx, candidate.instanceID+"/task", logger, func() error {
			state, taskID, task, err := store.State(ctx, candidate.instanceID)
			if err != nil {
				return fmt.Errorf("failed to read the record of instance %s: %w", candidate.instanceID, err)
			}

			candidate.exists = state != "not_found"
			if !candidate.exists {
				return nil
			}

			recorded, parseErr := parseRecordedOperation(candidate.instanceID, state, taskID, task)
			if parseErr != nil {
				logger.Error("Re-adoption skips the record of instance %s, which it cannot read: %s", candidate.instanceID, parseErr)

				return nil
			}

			candidate.parsed = true
			candidate.recorded = recorded

			return nil
		})
	})
}

// readoptReadMetadata reads <id>/metadata for each deprovision without an
// index entry that carries a context ID and is still in flight, as
// readoptReadRecords reads the records.
func readoptReadMetadata(ctx context.Context, store readoptStore, candidates []*readoptCandidate, logger logger.Logger) error {
	var needed []*readoptCandidate

	for _, candidate := range candidates {
		if !candidate.indexed && candidate.parsed && candidate.recorded.Action == operationTypeDeprovision &&
			candidate.recorded.ContextID != "" && readoptClaimable(candidate.recorded.State) {
			needed = append(needed, candidate)
		}
	}

	return readoptInParallel(ctx, len(needed), func(index int) error {
		candidate := needed[index]

		return readoptRetryRead(ctx, candidate.instanceID+"/metadata", logger, func() error {
			var metadata map[string]interface{}

			_, err := store.Get(ctx, candidate.instanceID+"/metadata", &metadata)
			if err != nil {
				return fmt.Errorf("failed to read %s/metadata: %w", candidate.instanceID, err)
			}

			candidate.metadata = metadata

			return nil
		})
	})
}

// readoptReadCredentials checks whether <id>/credentials exists for each
// indexed provision in progress whose index entry has created_at, as
// readoptReadRecords reads the records.
func readoptReadCredentials(ctx context.Context, store readoptStore, candidates []*readoptCandidate, logger logger.Logger) error {
	var needed []*readoptCandidate

	for _, candidate := range candidates {
		if candidate.indexed && candidate.parsed && candidate.recorded.Action == operationTypeProvision &&
			candidate.recorded.State == recordedStateInProgress && indexEntryCreated(candidate.entry) {
			needed = append(needed, candidate)
		}
	}

	return readoptInParallel(ctx, len(needed), func(index int) error {
		candidate := needed[index]

		return readoptRetryRead(ctx, candidate.instanceID+"/credentials", logger, func() error {
			var credentials map[string]interface{}

			exists, err := store.Get(ctx, candidate.instanceID+"/credentials", &credentials)
			if err != nil {
				return fmt.Errorf("failed to read %s/credentials: %w", candidate.instanceID, err)
			}

			candidate.credentialsStored = exists

			return nil
		})
	})
}

// readoptInParallel runs read for each index below count, readoptReadWorkers
// at a time, and returns once every read has returned. It returns an error
// when ctx ends first.
func readoptInParallel(ctx context.Context, count int, read func(index int) error) error {
	if count == 0 {
		return nil
	}

	work := make(chan int)

	var (
		group    sync.WaitGroup
		firstErr error
		errOnce  sync.Once
	)

	for range min(readoptReadWorkers, count) {
		group.Go(func() {
			for index := range work {
				err := read(index)
				if err != nil {
					errOnce.Do(func() { firstErr = err })
				}
			}
		})
	}

feed:
	for index := range count {
		select {
		case work <- index:
		case <-ctx.Done():
			break feed
		}
	}

	close(work)
	group.Wait()

	if firstErr != nil {
		return firstErr
	}

	if ctx.Err() != nil {
		return fmt.Errorf("re-adoption of service operations stopped before Vault answered: %w", ctx.Err())
	}

	return nil
}

// readoptRetryRead runs read until it succeeds, with a backoff between
// tries, and returns an error only when ctx ends first.
func readoptRetryRead(ctx context.Context, what string, logger logger.Logger, read func() error) error {
	backoff := newReadoptBackoff()

	for {
		err := read()
		if err == nil {
			return nil
		}

		logger.Error("Re-adoption could not read %s from Vault, and reads it again in %v: %s", what, backoff.next(), err)

		waitErr := backoff.wait(ctx)
		if waitErr != nil {
			return fmt.Errorf("re-adoption of service operations stopped before Vault answered: %w", waitErr)
		}
	}
}

// sortReadoptCandidates sorts the records read into the operations to
// claim and the finished records to mark. Every other record is left
// alone.
func sortReadoptCandidates(candidates []*readoptCandidate, logger logger.Logger) ([]readoptOperation, []readoptMigration) {
	operations := make([]readoptOperation, 0, len(candidates))

	var migrations []readoptMigration

	for _, candidate := range candidates {
		recorded := candidate.recorded

		switch {
		case !candidate.exists || !candidate.parsed:
			logDebugUnindexed(candidate, logger, "it has no operation record")

			continue
		case recorded.Action != operationTypeProvision && recorded.Action != operationTypeDeprovision:
			logDebugUnindexed(candidate, logger, fmt.Sprintf("its record names the action %q", recorded.Action))

			continue
		case !readoptClaimable(recorded.State):
			logger.Debug("Instance %s: its %s is %s, so it is not re-adopted", candidate.instanceID, recorded.Action, recorded.State)

			continue
		}

		if candidate.indexed {
			switch {
			case finishedDeprovision(recorded):
				logger.Debug("Instance %s: its deprovision is finished", candidate.instanceID)

				continue
			case recorded.Action == operationTypeProvision && recorded.State == recordedStateInProgress &&
				indexEntryCreated(candidate.entry) && candidate.credentialsStored:
				migrations = append(migrations, readoptMigration{instanceID: candidate.instanceID, recorded: recorded, state: internalVault.TaskStateSucceeded})

				continue
			}
		} else if recorded.Action == operationTypeDeprovision {
			switch {
			case recorded.ContextID == "":
				logger.Debug("Instance %s has no index entry, and its deprovision without a context ID is finished", candidate.instanceID)

				continue
			case deletionAlreadyRecorded(candidate.metadata, nil):
				migrations = append(migrations, readoptMigration{instanceID: candidate.instanceID, recorded: recorded, state: internalVault.TaskStateDeleted})

				continue
			}
		}

		deploymentName := candidate.deploymentName
		if !candidate.indexed {
			deploymentName = recorded.DeploymentName
		}

		operations = append(operations, readoptOperation{
			instanceID:     candidate.instanceID,
			indexed:        candidate.indexed,
			deploymentName: deploymentName,
			recorded:       recorded,
		})
	}

	return operations, migrations
}

// logDebugUnindexed logs, at Debug, why a folder the index does not name
// holds no operation to re-adopt.
func logDebugUnindexed(candidate *readoptCandidate, logger logger.Logger, reason string) {
	if candidate.indexed {
		return
	}

	logger.Debug("Folder %s holds no operation to re-adopt: %s", candidate.instanceID, reason)
}

// readoptClaimable reports whether a record in state is still in flight.
func readoptClaimable(state string) bool {
	switch state {
	case recordedStateInitializing, recordedStateInProgress, internalVault.TaskStateOutcomeUnknown:
		return true
	}

	return false
}

// readoptTerminal reports whether a record in state is settled.
func readoptTerminal(state string) bool {
	switch state {
	case recordedStateFailed, internalVault.TaskStateSucceeded, internalVault.TaskStateDeleted:
		return true
	}

	return false
}

// finishedDeprovision reports whether recorded is the record a deprovision
// leaves when it finishes without a delete task of its own, which is
// in_progress with no task.
func finishedDeprovision(recorded recordedOperation) bool {
	return recorded.Action == operationTypeDeprovision && recorded.State == recordedStateInProgress && recorded.TaskID == 0
}

// indexEntryCreated reports whether an index entry has created_at, which
// a provision's completion steps write once every other step has
// succeeded. Code older than that wrote it before the steps, so the
// first phase of re-adoption also asks for stored credentials before it
// takes created_at as a finished provision.
func indexEntryCreated(entry map[string]interface{}) bool {
	created, _ := entry["created_at"].(string)

	return created != ""
}

// markFinishedRecords marks each finished record as succeeded or deleted,
// under the settle lock and with no director call.
func (b *Broker) markFinishedRecords(migrations []readoptMigration, logger logger.Logger) {
	ctx := b.serviceContext()

	for _, migration := range migrations {
		if ctx.Err() != nil {
			logger.Info("The broker is shutting down, so the remaining finished records are marked at the next start")

			return
		}

		if migration.state == internalVault.TaskStateSucceeded {
			b.markProvisionSucceeded(ctx, migration, logger)

			continue
		}

		b.recordDeleted(ctx, migration.instanceID, migration.recorded.ContextID,
			fmt.Sprintf("Deployment %s was deleted before Blacksmith restarted", migration.recorded.DeploymentName), logger)
	}
}

// markProvisionSucceeded marks a provision whose index entry has
// created_at and whose credentials are stored as succeeded.
func (b *Broker) markProvisionSucceeded(ctx context.Context, migration readoptMigration, logger logger.Logger) {
	unlock, err := b.lockSettle(ctx, migration.instanceID)
	if err != nil {
		logger.Error("The finished provision of instance %s was not marked succeeded: %s", migration.instanceID, err)

		return
	}
	defer unlock()

	description := "The provision finished before Blacksmith restarted"
	if migration.recorded.hasTask() {
		description = fmt.Sprintf("BOSH deployment task %d finished before Blacksmith restarted", migration.recorded.TaskID)
	}

	b.recordSucceeded(ctx, migration.instanceID, description, migration.recorded.TaskID, logger)
}

// recordDeleted marks the deprovision whose delete carried contextID as
// deleted, under the settle lock, while the record still holds it.
func (b *Broker) recordDeleted(ctx context.Context, instanceID, contextID, description string, logger logger.Logger) {
	unlock, err := b.lockSettle(ctx, instanceID)
	if err != nil {
		logger.Error("The delete of instance %s was not marked deleted: %s", instanceID, err)

		return
	}
	defer unlock()

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err = b.Vault.TrackDeleted(recordCtx, instanceID, contextID, description)

	switch {
	case errors.Is(err, internalVault.ErrTaskRecordReplaced):
		logger.Info("The delete of instance %s with context ID %s finished, and its record now holds a newer operation, which is left as it is: %s", instanceID, contextID, err)
	case err != nil:
		logger.Error("Failed to mark the delete of instance %s with context ID %s as deleted: %s", instanceID, contextID, err)
	default:
		logger.Info("Marked the delete of instance %s with context ID %s as deleted: %s", instanceID, contextID, description)
	}
}

// readoptOperation settles one re-adopted operation, and releases its claim
// when it ends. It gives up after provisionTimeout for a provision or
// taskMonitorTimeout for a deprovision, and the operation is left to
// LastOperation and the next start then. It also ends with the service
// context.
func (b *Broker) readoptOperation(operation readoptOperation, loaded time.Time, checks chan struct{}, logger logger.Logger) {
	defer b.releaseOperation(operation.instanceID, operation.claim)

	bound := readoptBound(operation.recorded.Action)

	ctx, cancel := context.WithTimeout(b.serviceContext(), bound)
	defer cancel()

	var settled bool

	if operation.indexed {
		settled = b.readoptIndexed(ctx, operation, logger)
	} else {
		settled = b.readoptUnindexed(ctx, operation, loaded, checks, logger)
	}

	switch {
	case settled:
	case b.serviceContext().Err() != nil:
		logger.Info("The broker is shutting down, so the re-adopted %s of instance %s is left for the next start", operation.recorded.Action, operation.instanceID)
	default:
		logger.Error("Re-adoption gave up on the %s of instance %s after %v, and leaves it to LastOperation and the next start", operation.recorded.Action, operation.instanceID, bound)
	}
}

// readoptIndexed settles a re-adopted operation of an indexed instance,
// through the handlers LastOperation uses, and checks it again every
// readoptRetryInterval until it settles. It reports false when ctx ends
// first.
func (b *Broker) readoptIndexed(ctx context.Context, operation readoptOperation, logger logger.Logger) bool {
	for {
		current, err := b.readoptCurrent(ctx, operation.instanceID)

		switch {
		case err != nil:
			logger.Error("Re-adoption could not read the record of instance %s again: %s", operation.instanceID, err)
		case current.Action != operation.recorded.Action:
			logger.Info("The record of instance %s now holds a %s, so the re-adopted %s is left to it", operation.instanceID, current.Action, operation.recorded.Action)

			return true
		case readoptTerminal(current.State) || finishedDeprovision(current):
			return true
		default:
			response, stepErr := b.readoptIndexedStep(ctx, operation, current, logger)
			if stepErr == nil && response.State != osbapi.StateInProgress {
				b.settleReadoptedAnswer(ctx, operation, current, response, logger)

				return true
			}

			if stepErr != nil && ctx.Err() == nil {
				logger.Warning("Re-adoption could not settle the %s of instance %s yet: %s", operation.recorded.Action, operation.instanceID, stepErr)
			}
		}

		if !readoptWait(ctx) {
			return false
		}
	}
}

// readoptIndexedStep makes one check of a re-adopted operation of an
// indexed instance and returns the answer LastOperation would give.
func (b *Broker) readoptIndexedStep(ctx context.Context, operation readoptOperation, current recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	switch {
	case current.State == internalVault.TaskStateOutcomeUnknown:
		return b.resolveUnknownOutcome(ctx, operation.instanceID, operation.deploymentName, current, logger)
	case current.hasTask():
		return b.settleReadoptedTask(ctx, operation, current, logger)
	case current.Action == operationTypeProvision:
		return b.recoverProvision(ctx, operation.instanceID, operation.deploymentName, logger)
	default:
		return b.recoverDeprovision(ctx, operation.instanceID, operation.deploymentName, logger)
	}
}

// settleReadoptedTask follows the recorded task of a re-adopted operation
// of an indexed instance until it ends, and settles the operation through
// the task handler. A provision whose task the director no longer has is
// settled by completePrunedProvision.
func (b *Broker) settleReadoptedTask(ctx context.Context, operation readoptOperation, current recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	_, err := b.followReadoptedTask(ctx, current.TaskID, logger)
	if ctx.Err() != nil {
		return osbapi.LastOperationResponse{}, fmt.Errorf("stopped following BOSH task %d: %w", current.TaskID, ctx.Err())
	}

	if current.Action == operationTypeDeprovision {
		return b.handleDeprovisionTask(ctx, operation.instanceID, operation.deploymentName, current.TaskID, logger)
	}

	if !bosh.IsTaskNotFound(err) {
		_, err = b.BOSH.GetTask(ctx, current.TaskID)
	}

	if bosh.IsTaskNotFound(err) {
		return b.completePrunedProvision(ctx, operation, current, logger)
	}

	return b.handleProvisionTaskCleaningUpOn(ctx, b.serviceContext(), operation.instanceID, operation.deploymentName, current.TaskID, logger)
}

// completePrunedProvision settles a provision whose create task the
// director no longer has. A task still running on the deployment is
// followed first. When the deployment exists and nothing runs on it, the
// provision's completion steps run once and it is recorded as succeeded.
// A deployment that is missing leaves the provision in progress.
func (b *Broker) completePrunedProvision(ctx context.Context, operation readoptOperation, current recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	deploymentName := operation.deploymentName
	inProgress := osbapi.LastOperationResponse{
		State:       osbapi.StateInProgress,
		Description: fmt.Sprintf("BOSH deployment task %d is no longer known to the director", current.TaskID),
	}

	_, err := b.BOSH.GetDeployment(ctx, deploymentName)
	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		logger.Warning("Instance %s: the director no longer has BOSH task %d of its provision, and deployment %s does not exist", operation.instanceID, current.TaskID, deploymentName)

		return inProgress, nil
	}

	if err != nil {
		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to check whether deployment %s exists: %w", deploymentName, err)
	}

	running, err := b.BOSH.FindRunningTaskForDeployment(ctx, deploymentName)
	if err != nil {
		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to check for a running BOSH task on deployment %s: %w", deploymentName, err)
	}

	if running != nil {
		logger.Info("Instance %s: BOSH task %d runs on deployment %s, so re-adoption follows it before it completes the provision", operation.instanceID, running.ID, deploymentName)

		_, _ = b.followReadoptedTask(ctx, running.ID, logger)

		return inProgress, nil
	}

	unlock, err := b.lockSettle(ctx, operation.instanceID)
	if err != nil {
		return osbapi.LastOperationResponse{}, err
	}
	defer unlock()

	settled, isSettled, err := b.settledProvision(ctx, operation.instanceID)
	if err != nil {
		return osbapi.LastOperationResponse{}, err
	}

	if isSettled {
		return settled, nil
	}

	err = b.OnProvisionCompleted(ctx, logger, operation.instanceID)
	if err != nil {
		return osbapi.LastOperationResponse{}, ErrProvisionTaskCompletedPostHookFailed
	}

	description := fmt.Sprintf("BOSH deployment task %d is no longer known to the director, and deployment %s exists with no task running on it", current.TaskID, deploymentName)
	b.recordSucceeded(ctx, operation.instanceID, description, current.TaskID, logger)

	return osbapi.LastOperationResponse{State: osbapi.StateSucceeded, Description: description}, nil
}

// settleReadoptedAnswer writes a failed answer that a handler returned
// without writing it, so later starts and polls answer from the record.
// It writes under the settle lock, and only while the record still holds
// the operation unsettled. A succeeded answer is already settled by the
// handler that gave it.
func (b *Broker) settleReadoptedAnswer(ctx context.Context, operation readoptOperation, current recordedOperation, response osbapi.LastOperationResponse, logger logger.Logger) {
	if response.State != osbapi.StateFailed {
		return
	}

	b.recordReadoptedFailure(ctx, operation.instanceID, current, response.Description, logger)
}

// recordReadoptedFailure records the operation current holds as failed
// with description, under the settle lock, while the record still holds
// that operation unsettled.
func (b *Broker) recordReadoptedFailure(ctx context.Context, instanceID string, current recordedOperation, description string, logger logger.Logger) {
	unlock, err := b.lockSettle(ctx, instanceID)
	if err != nil {
		logger.Error("The failure of the %s of instance %s was not recorded: %s", current.Action, instanceID, err)

		return
	}
	defer unlock()

	latest, err := b.readoptCurrent(ctx, instanceID)
	if err != nil {
		logger.Error("The failure of the %s of instance %s was not recorded, because its record could not be read again: %s", current.Action, instanceID, err)

		return
	}

	if latest.Action != current.Action || latest.ContextID != current.ContextID || readoptTerminal(latest.State) {
		logger.Info("The %s of instance %s was settled while re-adoption checked it", current.Action, instanceID)

		return
	}

	b.recordTask(ctx, instanceID, current.Action, description, -1, logger)
}

// readoptUnindexed settles a re-adopted operation whose index entry is
// gone, from the director's view of its deployment. It reports false when
// ctx ends first.
func (b *Broker) readoptUnindexed(ctx context.Context, operation readoptOperation, loaded time.Time, checks chan struct{}, logger logger.Logger) bool {
	for {
		current, err := b.readoptCurrent(ctx, operation.instanceID)

		switch {
		case err != nil:
			logger.Error("Re-adoption could not read the record of instance %s again: %s", operation.instanceID, err)
		case current.Action != operation.recorded.Action:
			logger.Info("The record of instance %s now holds a %s, so the re-adopted %s is left to it", operation.instanceID, current.Action, operation.recorded.Action)

			return true
		case readoptTerminal(current.State):
			return true
		case current.Action == operationTypeDeprovision && current.ContextID == "":
			// The resolver settles a delete whose deployment is gone with
			// the record of a finished deprovision, which has no context ID.
			return true
		case current.State == internalVault.TaskStateOutcomeUnknown:
			if current.DeploymentName == "" {
				logger.Warning("Instance %s has no index entry, the outcome of its %s with context ID %s is unknown, and its record names no deployment to check, so it is left to LastOperation",
					operation.instanceID, current.Action, current.ContextID)

				return true
			}

			_, err = b.resolveUnknownOutcome(ctx, operation.instanceID, current.DeploymentName, current, logger)
			if err != nil && ctx.Err() == nil {
				logger.Warning("Re-adoption could not settle the %s of instance %s yet: %s", current.Action, operation.instanceID, err)
			}
		case current.Action == operationTypeDeprovision:
			return b.readoptUnindexedDelete(ctx, operation.instanceID, current, loaded, checks, logger)
		default:
			return b.readoptUnindexedProvision(ctx, operation.instanceID, current, logger)
		}

		if !readoptWait(ctx) {
			return false
		}
	}
}

// readoptUnindexedDelete settles a delete whose index entry is gone. A
// deployment that is gone, or that is gone once the delete's task ends,
// marks the delete deleted. A deployment that outlives the delete's task,
// or the grace for a task with the delete's context ID to show, marks the
// delete failed and is left for an operator. Re-adoption never sends a
// delete of its own.
func (b *Broker) readoptUnindexedDelete(ctx context.Context, instanceID string, current recordedOperation, loaded time.Time, checks chan struct{}, logger logger.Logger) bool {
	deploymentName := current.DeploymentName
	if deploymentName == "" {
		logger.Warning("Instance %s has no index entry, and its delete with context ID %s names no deployment, so it is left to LastOperation", instanceID, current.ContextID)

		return true
	}

	gone, known := b.readoptDeploymentGone(ctx, instanceID, deploymentName, checks, logger)
	if !known {
		return false
	}

	deletedDescription := fmt.Sprintf("Deployment %s is deleted", deploymentName)

	if gone {
		b.recordDeleted(ctx, instanceID, current.ContextID, deletedDescription, logger)

		return true
	}

	outcome, followed := b.followUnindexedDelete(ctx, current, loaded, logger)
	if !followed {
		return false
	}

	gone, known = b.readoptDeploymentGone(ctx, instanceID, deploymentName, nil, logger)
	if !known {
		return false
	}

	if gone {
		b.recordDeleted(ctx, instanceID, current.ContextID, deletedDescription, logger)

		return true
	}

	description := fmt.Sprintf("The delete of deployment %s with context ID %s %s, and the deployment still exists. Blacksmith does not send the delete again, so an operator must delete it", deploymentName, current.ContextID, outcome)
	logger.Error("Instance %s: %s", instanceID, description)

	b.recordReadoptedFailure(ctx, instanceID, current, description, logger)

	return true
}

// followUnindexedDelete follows the task of a delete whose index entry is
// gone until it ends, and says how it ended. A delete with no task is
// looked up by its context ID until the grace from the load passes. It
// reports false when ctx ends first.
func (b *Broker) followUnindexedDelete(ctx context.Context, current recordedOperation, loaded time.Time, logger logger.Logger) (string, bool) {
	taskID := current.TaskID

	if !current.hasTask() {
		found, err := bosh.AwaitTaskByContextID(ctx, b.BOSH, current.ContextID, loaded)
		if ctx.Err() != nil {
			return "", false
		}

		if err != nil {
			logger.Warning("The director never showed a task for the delete with context ID %s: %s", current.ContextID, err)

			return "never showed a task on the director", true
		}

		taskID = found.ID
	}

	task, err := b.followReadoptedTask(ctx, taskID, logger)
	if ctx.Err() != nil {
		return "", false
	}

	if err != nil {
		return fmt.Sprintf("ran as task %d, which the director no longer knows", taskID), true
	}

	return fmt.Sprintf("ended with task %d in state %s", taskID, task.State), true
}

// readoptUnindexedProvision follows the create task of a provision whose
// index entry is gone until it ends, and then records the provision as
// failed, so later starts leave it alone.
func (b *Broker) readoptUnindexedProvision(ctx context.Context, instanceID string, current recordedOperation, logger logger.Logger) bool {
	if current.hasTask() {
		_, _ = b.followReadoptedTask(ctx, current.TaskID, logger)
		if ctx.Err() != nil {
			return false
		}
	}

	description := fmt.Sprintf("The index entry of instance %s is gone, so its provision cannot finish", instanceID)
	logger.Warning("Instance %s: %s", instanceID, description)

	b.recordReadoptedFailure(ctx, instanceID, current, description, logger)

	return true
}

// readoptDeploymentGone asks the director whether the deployment of an
// instance without an index entry is gone, and asks again every
// readoptRetryInterval while the answer is unknown. When checks is not
// nil, the first ask holds one of its slots. It reports false as its
// second value when ctx ends first.
func (b *Broker) readoptDeploymentGone(ctx context.Context, instanceID, deploymentName string, checks chan struct{}, logger logger.Logger) (bool, bool) {
	for first := true; ; first = false {
		limited := first && checks != nil
		if limited {
			select {
			case checks <- struct{}{}:
			case <-ctx.Done():
				return false, false
			}
		}

		err := b.deploymentGone(ctx, instanceID, deploymentName, logger)

		if limited {
			<-checks
		}

		switch {
		case errors.Is(err, osbapi.ErrGoneError):
			return true, true
		case err == nil:
			return false, true
		}

		if !readoptWait(ctx) {
			return false, false
		}
	}
}

// followReadoptedTask hands a task started before the restart to the
// director to count against its limit, when it can, and waits for it to
// end. It returns the ended task, or the director's task-not-found answer
// when it no longer has the task. Any other failed wait is tried again
// every readoptRetryInterval until ctx ends.
func (b *Broker) followReadoptedTask(ctx context.Context, taskID int, logger logger.Logger) (*bosh.Task, error) {
	if adopting, ok := b.BOSH.(taskAdopter); ok {
		adopting.Adopt(taskID)
	}

	for {
		task, err := b.BOSH.WaitForTask(ctx, taskID)

		switch {
		case err == nil && task != nil && (task.State == taskStateDone || isFailedTaskState(task.State)):
			return task, nil
		case ctx.Err() != nil:
			return nil, fmt.Errorf("stopped following BOSH task %d: %w", taskID, ctx.Err())
		case bosh.IsTaskNotFound(err):
			logger.Info("The director no longer has BOSH task %d: %s", taskID, err)

			return nil, fmt.Errorf("following BOSH task %d: %w", taskID, err)
		case err != nil:
			logger.Warning("Waiting for BOSH task %d failed, and re-adoption waits for it again: %s", taskID, err)
		}

		if !readoptWait(ctx) {
			return nil, fmt.Errorf("stopped following BOSH task %d: %w", taskID, ctx.Err())
		}
	}
}

// readoptCurrent reads the record of the instance again, under a bound of
// its own.
func (b *Broker) readoptCurrent(ctx context.Context, instanceID string) (recordedOperation, error) {
	readCtx, cancel := recordContext(ctx)
	defer cancel()

	return b.readRecordedOperation(readCtx, instanceID)
}

// readoptWait waits readoptRetryInterval, and reports false when ctx ends
// first.
func readoptWait(ctx context.Context) bool {
	timer := time.NewTimer(readoptRetry())
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// readoptBackoff is the backoff between two reads of Vault in the first
// phase of re-adoption.
type readoptBackoff struct {
	delay time.Duration
	limit time.Duration
}

func newReadoptBackoff() *readoptBackoff {
	override := time.Duration(readoptIntervalOverride.Load())
	if override > 0 {
		return &readoptBackoff{delay: override, limit: override}
	}

	return &readoptBackoff{delay: readoptBackoffStart, limit: readoptBackoffMax}
}

// next returns the wait the next call to wait makes.
func (r *readoptBackoff) next() time.Duration {
	return r.delay
}

// wait waits the current delay and doubles it up to the limit. It returns
// the context's error when ctx ends first.
func (r *readoptBackoff) wait(ctx context.Context) error {
	timer := time.NewTimer(r.delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		return fmt.Errorf("the wait for the next read of Vault ended early: %w", ctx.Err())
	}

	r.delay = min(readoptBackoffFactor*r.delay, r.limit)

	return nil
}

// readoptRetry returns how often a re-adopted operation is checked again.
func readoptRetry() time.Duration {
	override := readoptIntervalOverride.Load()
	if override > 0 {
		return time.Duration(override)
	}

	return readoptRetryInterval
}

// readoptBound returns how long a re-adoption goroutine for an operation
// of action may run.
func readoptBound(action string) time.Duration {
	override := readoptBoundOverride.Load()
	if override > 0 {
		return time.Duration(override)
	}

	if action == operationTypeProvision {
		return provisionTimeout
	}

	return taskMonitorTimeout
}
