package audittest

import (
	"sync"
	"time"
)

// overlapWait is how long the first reader waits for a second one. A
// handler that serialises its saves never sends the second reader in
// time, so the wait runs out and the save goes on.
const overlapWait = 300 * time.Millisecond

// Overlap's reads are counted from one, so these name the first and the
// second.
const (
	firstRead  = 1
	secondRead = 2
)

// Overlap lets a fake's read of an old value wait briefly for a second
// reader. Two saves that read the old value at the same time then both
// see it, so a test can show whether a handler serialises its saves.
// The zero value is ready to use.
type Overlap struct {
	mu     sync.Mutex
	reads  int
	second chan struct{}
}

// Read marks one read of the old value. The first read waits until a
// second read arrives or the wait runs out. Later reads return at once.
func (o *Overlap) Read() {
	o.mu.Lock()

	if o.second == nil {
		o.second = make(chan struct{})
	}

	o.reads++
	reads, second := o.reads, o.second
	o.mu.Unlock()

	switch reads {
	case firstRead:
		select {
		case <-second:
		case <-time.After(overlapWait):
		}
	case secondRead:
		close(second)
	}
}

// Chained reports whether two changes, each given as its old and new
// value, follow one another from initial: one starts at initial, and the
// other starts where the first one ended.
func Chained(initial string, first, second [2]string) bool {
	return (first[0] == initial && second[0] == first[1]) ||
		(second[0] == initial && first[0] == second[1])
}
