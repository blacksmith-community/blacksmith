package ssh

import (
	"errors"
	"testing"
)

// When the remote shell exits, its output pipes reach end of file and
// readOutput closes the output channel. ReadOutput must report that end
// instead of returning empty data forever, which is what a receive on a
// closed channel gives.
func TestReadOutputReportsTheShellExit(t *testing.T) {
	t.Parallel()

	session := &SessionImpl{
		status:     SessionStatusActive,
		outputChan: make(chan []byte, 1),
		errorChan:  make(chan error, 1),
		closeChan:  make(chan struct{}),
	}

	session.outputChan <- []byte("logout\r\n")

	close(session.outputChan)

	output, err := session.ReadOutput()
	if err != nil || string(output) != "logout\r\n" {
		t.Fatalf("ReadOutput = %q, %v, want the buffered output first", output, err)
	}

	output, err = session.ReadOutput()
	if !errors.Is(err, ErrShellExited) {
		t.Fatalf("ReadOutput after the shell exited = %q, %v, want ErrShellExited", output, err)
	}
}
