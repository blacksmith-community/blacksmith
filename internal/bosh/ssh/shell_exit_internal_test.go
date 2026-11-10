package ssh

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

	if session.endErr != nil {
		t.Errorf("a closed session recorded %v, want no connection error", session.endErr)
	}
}

// slowEnd is a pipe that gives its text only after the reader has had time
// to wait on an empty channel, then ends.
type slowEnd struct {
	text string
	done bool
}

func (p *slowEnd) Read(buf []byte) (int, error) {
	if p.done {
		return 0, io.EOF
	}

	p.done = true

	time.Sleep(time.Millisecond)

	return copy(buf, p.text), nil
}

// The last chunk of output reaches the reader before the lost-link error,
// however the reader's waits fall against the end of the pipes.
func TestLastChunkArrivesBeforeALostConnectionError(t *testing.T) {
	t.Parallel()

	for range 300 {
		session := &SessionImpl{
			status:     SessionStatusActive,
			config:     Config{OutputReadTimeout: time.Second},
			outputChan: make(chan []byte, 1),
			errorChan:  make(chan error, 1),
			closeChan:  make(chan struct{}),
			stdout:     &slowEnd{text: "last words\r\n"},
			stderr:     strings.NewReader(""),
		}

		go session.readOutput(fakeShell{result: &ssh.ExitMissingError{}})

		var got string

		for {
			output, err := session.ReadOutput()
			if err != nil {
				if !errors.Is(err, ErrConnectionLost) {
					t.Fatalf("ReadOutput ended with %v, want ErrConnectionLost", err)
				}

				break
			}

			got += string(output)
		}

		if got != "last words\r\n" {
			t.Fatalf("output before the lost-link error is %q, want the last chunk", got)
		}
	}
}

// A remote that half-closes its channel and then holds it open leaves
// Wait blocked. The end of the output must still be reported, as a shell
// exit, once the grace period passes.
func TestBlockedWaitEndsTheOutputAsAShellExit(t *testing.T) {
	previous := shellWaitGrace
	shellWaitGrace = 50 * time.Millisecond

	t.Cleanup(func() { shellWaitGrace = previous })

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })

	session := &SessionImpl{
		status:     SessionStatusActive,
		outputChan: make(chan []byte, 1),
		errorChan:  make(chan error, 1),
		closeChan:  make(chan struct{}),
		stdout:     strings.NewReader(""),
		stderr:     strings.NewReader(""),
	}

	go session.readOutput(blockedShell{hold: hold})

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		_, err := session.ReadOutput()
		if errors.Is(err, ErrShellExited) {
			return
		}

		if err != nil {
			t.Fatalf("ReadOutput = %v, want ErrShellExited", err)
		}
	}

	t.Fatal("the output never ended while Wait was blocked")
}

type blockedShell struct{ hold chan struct{} }

func (b blockedShell) Wait() error {
	<-b.hold

	return nil
}

// The lost-link result is recorded for the close of the output channel to
// reveal and is not queued beside the output, where a reader waiting on
// both could take it ahead of the last chunk.
func TestLostConnectionIsNotQueuedBesideTheOutput(t *testing.T) {
	t.Parallel()

	session := &SessionImpl{
		status:     SessionStatusActive,
		outputChan: make(chan []byte, 1),
		errorChan:  make(chan error, 1),
		closeChan:  make(chan struct{}),
		stdout:     strings.NewReader("last words\r\n"),
		stderr:     strings.NewReader(""),
	}

	session.readOutput(fakeShell{result: &ssh.ExitMissingError{}})

	if len(session.errorChan) != 0 {
		t.Fatalf("the lost link was queued on errorChan: %v", <-session.errorChan)
	}

	if !errors.Is(session.endErr, ErrConnectionLost) {
		t.Fatalf("endErr = %v, want ErrConnectionLost", session.endErr)
	}
}
