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

// One write at a time, so the counts hold without the race detector. A
// zero-length write changes nothing, even once the buffer is full, and a
// write that crosses the limit keeps what fits.
func TestBoundedBufferCountsEveryWrite(t *testing.T) {
	t.Parallel()

	// The writes below, joined, spell written, and kept is how much of
	// written the buffer holds after each one.
	const written = "abcdefgh"

	steps := []struct {
		write    string
		kept     int
		received int64
		cut      bool
	}{
		{write: "", kept: 0, received: 0},
		{write: "abc", kept: 3, received: 3},
		{write: "", kept: 3, received: 3},
		{write: "de", kept: 5, received: 5},
		{write: "", kept: 5, received: 5},
		{write: "f", kept: 5, received: 6, cut: true},
		{write: "", kept: 5, received: 6, cut: true},
		{write: "gh", kept: 5, received: 8, cut: true},
	}

	buffer := &boundedBuffer{limit: 5}

	for index, step := range steps {
		n, err := buffer.Write([]byte(step.write))
		if n != len(step.write) || err != nil {
			t.Fatalf("step %d: Write returned %d, %v; want %d, nil", index, n, err, len(step.write))
		}

		kept, received, cut := buffer.snapshot()
		if string(kept) != written[:step.kept] || received != step.received || cut != step.cut {
			t.Fatalf("step %d: kept %q, received %d, cut %v; want %q, %d, %v",
				index, kept, received, cut, written[:step.kept], step.received, step.cut)
		}
	}

	crossing := &boundedBuffer{limit: 4}

	n, err := crossing.Write([]byte(written[:6]))
	if n != 6 || err != nil {
		t.Fatalf("Write returned %d, %v; want 6, nil", n, err)
	}

	kept, received, cut := crossing.snapshot()
	if string(kept) != written[:4] || received != 6 || !cut {
		t.Errorf("kept %q, received %d, cut %v; want %q, 6, true", kept, received, cut, written[:4])
	}
}
