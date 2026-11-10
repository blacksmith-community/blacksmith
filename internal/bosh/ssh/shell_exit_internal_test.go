package ssh

import (
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

// fakeShell stands in for the SSH session whose shell ended. Wait gives
// the result that the connection reports for the end of the shell.
type fakeShell struct{ result error }

func (f fakeShell) Wait() error { return f.result }

// A shell that exited ends the output with ErrShellExited, and a
// connection that dropped ends it with ErrConnectionLost. The two differ
// only in the result of Wait, because both pipes reach end of file in
// either case.
func TestOutputEndTellsAShellExitFromALostConnection(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		wait error
		want error
	}{
		"clean exit":        {wait: nil, want: ErrShellExited},
		"nonzero exit":      {wait: &ssh.ExitError{Waitmsg: ssh.Waitmsg{}}, want: ErrShellExited},
		"missing exit":      {wait: &ssh.ExitMissingError{}, want: ErrConnectionLost},
		"transport failure": {wait: io.ErrUnexpectedEOF, want: ErrConnectionLost},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			session := &SessionImpl{
				status:     SessionStatusActive,
				outputChan: make(chan []byte, 1),
				errorChan:  make(chan error, 1),
				closeChan:  make(chan struct{}),
				stdout:     strings.NewReader("bye\r\n"),
				stderr:     strings.NewReader(""),
			}

			session.readOutput(fakeShell{result: testCase.wait})

			output, err := session.ReadOutput()
			if err != nil || string(output) != "bye\r\n" {
				t.Fatalf("ReadOutput = %q, %v, want the output first", output, err)
			}

			_, err = session.ReadOutput()
			if !errors.Is(err, testCase.want) {
				t.Fatalf("ReadOutput at the end = %v, want %v", err, testCase.want)
			}

			if errors.Is(err, ErrShellExited) == errors.Is(err, ErrConnectionLost) {
				t.Errorf("error %v is both or neither of exit and lost", err)
			}
		})
	}
}

// A session that Close already ended sees its connection fail as part of
// the close, which is not a lost link.
func TestClosedSessionIsNotReportedAsALostConnection(t *testing.T) {
	t.Parallel()

	session := &SessionImpl{
		status:     SessionStatusActive,
		outputChan: make(chan []byte, 1),
		errorChan:  make(chan error, 1),
		closeChan:  make(chan struct{}),
		stdout:     strings.NewReader(""),
		stderr:     strings.NewReader(""),
	}

	close(session.closeChan)
	session.readOutput(fakeShell{result: &ssh.ExitMissingError{}})

	if len(session.errorChan) != 0 {
		t.Errorf("a closed session queued %v, want no connection error", <-session.errorChan)
	}
}
