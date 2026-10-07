package vault

import (
	"context"
	"fmt"
	"sync"
)

// taskRecordLocks serializes the writes of each instance's task record made
// through one Vault value. A write that keeps fields of the record it
// replaces reads the record first, and without the lock a second writer
// could land between that read and the write, which would then put back the
// fields the second writer had just replaced. Writers for different
// instances never wait on each other. Another Vault value, in this process
// or another, is not covered.
type taskRecordLocks struct {
	mu    sync.Mutex
	locks map[string]*taskRecordLock
}

// taskRecordLock is the lock of one instance's task record. Its channel
// holds a value while a writer holds the lock, so a waiter can give up when
// its context ends. refs counts the writers holding or waiting for it, and
// the lock is dropped from the map when the last of them is done.
type taskRecordLock struct {
	held chan struct{}
	refs int
}

// lock takes the task record lock of instanceID and returns the function
// that releases it. It returns an error, holding nothing, when ctx ends
// first.
func (l *taskRecordLocks) lock(ctx context.Context, instanceID string) (func(), error) {
	l.mu.Lock()

	if l.locks == nil {
		l.locks = make(map[string]*taskRecordLock)
	}

	entry, ok := l.locks[instanceID]
	if !ok {
		entry = &taskRecordLock{held: make(chan struct{}, 1)}
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

		return nil, fmt.Errorf("gave up waiting to write the task record of instance %s: %w", instanceID, ctx.Err())
	}
}

// done drops one reference to entry, and removes entry from the map once
// nobody holds or waits for it.
func (l *taskRecordLocks) done(instanceID string, entry *taskRecordLock) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry.refs--
	if entry.refs == 0 {
		delete(l.locks, instanceID)
	}
}
