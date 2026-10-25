package ssh

import (
	"bytes"
	"sync"
	"testing"
)

// Two goroutines write to one buffer, as the session does for a stream
// while a reader looks at it, and received counts every byte offered.
func TestBoundedBufferConcurrentWrites(t *testing.T) {
	t.Parallel()

	const (
		writers = 2
		writes  = 500
		size    = 7
	)

	buffer := &boundedBuffer{limit: 1000}

	var wg sync.WaitGroup

	for range writers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range writes {
				_, _ = buffer.Write(bytes.Repeat([]byte("x"), size))
				_, _, _ = buffer.snapshot()
			}
		}()
	}

	wg.Wait()

	kept, received, cut := buffer.snapshot()
	if want := int64(writers * writes * size); received != want {
		t.Errorf("received %d bytes, want %d", received, want)
	}

	if len(kept) != 1000 || !cut {
		t.Errorf("kept %d bytes, cut %v; want 1000, true", len(kept), cut)
	}
}
