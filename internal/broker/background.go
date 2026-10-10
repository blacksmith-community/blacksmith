package broker

import (
	"context"
	"sort"
	"strings"
	"sync"

	"blacksmith/pkg/logger"
)

// backgroundWork counts the goroutines the broker starts that outlive a
// request and write operation records, so a shutdown can wait for them
// to write what they have. Once a shutdown starts waiting, the group is
// closed, and no goroutine joins it.
type backgroundWork struct {
	// mu guards closed, running, and next, and is held across each Add to
	// group, so no Add ever races the shutdown's Wait.
	mu      sync.Mutex
	closed  bool
	running map[uint64]string
	next    uint64

	group sync.WaitGroup
}

// track runs work in a goroutine that WaitForBackground waits for, and names
// it for the log. A goroutine launched after WaitForBackground has started
// still runs, but untracked, and track logs a warning that names it.
func (b *Broker) track(name string, work func()) {
	background := &b.background

	background.mu.Lock()

	if background.closed {
		background.mu.Unlock()

		logger.Get().Named("broker").Warning("The broker is shutting down, so %s runs without the shutdown waiting for it", name)

		go work()

		return
	}

	if background.running == nil {
		background.running = make(map[uint64]string)
	}

	key := background.next
	background.next++
	background.running[key] = name
	background.group.Add(1)

	background.mu.Unlock()

	go func() {
		defer func() {
			background.mu.Lock()
			delete(background.running, key)
			background.mu.Unlock()

			background.group.Done()
		}()

		work()
	}()
}

// WaitForBackground closes the broker's background work to new goroutines,
// and waits for the ones it tracks and for the CredHub cleanups to return.
// When ctx ends first, it logs the names of the goroutines still running
// and returns.
func (b *Broker) WaitForBackground(ctx context.Context) {
	work := &b.background

	work.mu.Lock()
	work.closed = true
	work.mu.Unlock()

	done := make(chan struct{})

	go func() {
		defer close(done)

		work.group.Wait()
		b.credentialCleanups.Wait()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		logger.Get().Named("broker").Warning("Stopped waiting for the broker's background work at shutdown, with these still running: %s",
			strings.Join(work.runningNames(), "; "))
	}
}

// runningNames returns the names of the tracked goroutines still running,
// sorted, or a note that only CredHub cleanups are left.
func (w *backgroundWork) runningNames() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	names := make([]string, 0, len(w.running))
	for _, name := range w.running {
		names = append(names, name)
	}

	if len(names) == 0 {
		return []string{"CredHub cleanups"}
	}

	sort.Strings(names)

	return names
}
