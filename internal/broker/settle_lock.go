package broker

import (
	"context"
	"fmt"
	"sync"
)

// settleLocks serializes the settling of each instance's operation in this
// process. LastOperation, the deprovision monitor, and the settling of a
// failed provision can reach the same record at once. Each takes the lock,
// reads the record again, and returns when another has already settled it,
// and each holds the lock across the writes that settle it, so a delete or
// a failed provision is settled once. Settles for different instances never
// wait on each other.
type settleLocks struct {
	mu    sync.Mutex
	locks map[string]*settleLock
}

// settleLock is the settle lock of one instance. Its channel holds a value
// while a settler holds the lock, so a waiter can give up when its context
// ends. refs counts the settlers holding or waiting for it, and the lock is
// dropped from the map when the last of them is done.
type settleLock struct {
	held chan struct{}
	refs int
}

// lock takes the settle lock of instanceID and returns the function that
// releases it. It returns an error, holding nothing, when ctx ends first.
func (l *settleLocks) lock(ctx context.Context, instanceID string) (func(), error) {
	l.mu.Lock()

	if l.locks == nil {
		l.locks = make(map[string]*settleLock)
	}

	entry, ok := l.locks[instanceID]
	if !ok {
		entry = &settleLock{held: make(chan struct{}, 1)}
		l.locks[instanceID] = entry
	}

	entry.refs++
	l.mu.Unlock()

	select {
	case entry.held <- struct{}{}:
		return func() {
			<-entry.held
			l.done(instanceID, entry)
		}, nil
	case <-ctx.Done():
		l.done(instanceID, entry)

		return nil, fmt.Errorf("gave up waiting to settle the operation of instance %s: %w", instanceID, ctx.Err())
	}
}

// done drops one reference to entry, and removes entry from the map once
// nobody holds or waits for it.
func (l *settleLocks) done(instanceID string, entry *settleLock) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, instanceID)
	}
}

// lockSettle takes the settle lock of instanceID. See settleLocks.
func (b *Broker) lockSettle(ctx context.Context, instanceID string) (func(), error) {
	return b.settling.lock(ctx, instanceID)
}

// deleteSettled reports whether a delete of the instance has already been
// settled, which is when the instance is no longer indexed and its
// metadata holds the deleted_at of its current lifecycle. The reads run
// under bounds of their own, and a failed read returns an error, because
// whether the delete was settled is then unknown.
func (b *Broker) deleteSettled(ctx context.Context, instanceID string) (bool, error) {
	indexCtx, cancelIndex := recordContext(ctx)
	_, indexed, err := b.Vault.FindInstance(indexCtx, instanceID)

	cancelIndex()

	if err != nil {
		return false, fmt.Errorf("failed to read the index entry of instance %s: %w", instanceID, err)
	}

	if indexed {
		return false, nil
	}

	var metadata map[string]interface{}

	metadataCtx, cancelMetadata := recordContext(ctx)
	_, err = b.Vault.Get(metadataCtx, instanceID+"/metadata", &metadata)

	cancelMetadata()

	if err != nil {
		return false, fmt.Errorf("failed to read %s/metadata: %w", instanceID, err)
	}

	return deletionAlreadyRecorded(metadata, nil), nil
}
