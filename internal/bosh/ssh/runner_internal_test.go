package ssh

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// blockedReader blocks each read until release closes, then fills the
// buffer with 'k'. entered closes when the first read starts.
type blockedReader struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockedReader) Read(buf []byte) (int, error) {
	close(r.entered)
	<-r.release

	for i := range buf {
		buf[i] = 'k'
	}

	return len(buf), nil
}

// stop does not wait for a read of the caller's reader that is under way,
// and the bytes that read returns once stop has run never reach the
// session.
func TestStdinGateStopDoesNotWaitForARead(t *testing.T) {
	t.Parallel()

	reader := &blockedReader{entered: make(chan struct{}), release: make(chan struct{})}
	gate := &stdinGate{reader: reader}

	type readResult struct {
		n   int
		err error
	}

	buf := make([]byte, 16)
	read := make(chan readResult, 1)

	go func() {
		n, err := gate.Read(buf)
		read <- readResult{n: n, err: err}
	}()

	<-reader.entered

	stopped := make(chan struct{})

	go func() {
		gate.stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(reader.release)
		t.Fatal("stop waited for a read that was under way")
	}

	close(reader.release)

	got := <-read
	if got.n != 0 || !errors.Is(got.err, errStdinStopped) {
		t.Errorf("the read under way returned %d and %v, want 0 and errStdinStopped", got.n, got.err)
	}

	if bytes.Contains(buf, []byte("k")) {
		t.Errorf("the read under way delivered %q after stop", buf)
	}
}
