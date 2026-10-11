package websocket

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
)

// lockWait bounds how long a call that takes the handler's lock may
// wait. A call that does not queue behind a close returns long before.
const lockWait = time.Second

// blockingSSH is an SSH session whose Close holds until the test lets it
// return, as a close that waits for the director's cleanup does.
type blockingSSH struct {
	closing chan struct{}
	release chan struct{}
	closes  atomic.Int32
}

func newBlockingSSH() *blockingSSH {
	return &blockingSSH{closing: make(chan struct{}, 1), release: make(chan struct{})}
}

func (s *blockingSSH) Start() error                 { return nil }
func (s *blockingSSH) SendInput([]byte) error       { return nil }
func (s *blockingSSH) ReadOutput() ([]byte, error)  { return []byte{}, nil }
func (s *blockingSSH) Status() ssh.SessionStatus    { return ssh.SessionStatusConnected }
func (s *blockingSSH) SetWindowSize(int, int) error { return nil }

func (s *blockingSSH) Close() error {
	s.closes.Add(1)

	s.closing <- struct{}{}

	<-s.release

	return nil
}

// addStaleSession registers a WebSocket session that has been idle past
// the handler's session timeout and carries sshSession.
func addStaleSession(handler *SSHHandler, sshSession ssh.SSHSession) context.Context {
	ctx, cancel := context.WithCancel(context.Background())

	session := &SSHStreamSession{
		ID:           "ws-ssh-stale",
		Cancel:       cancel,
		LastActivity: time.Now().Add(-2 * handler.config.SessionTimeout),
		Logger:       handler.logger,
		SSHSession:   sshSession,
	}

	handler.sessionsMutex.Lock()
	handler.sessions[session.ID] = session
	handler.sessionsMutex.Unlock()

	return ctx
}

// The sweep and the handler's close take their sessions under the
// handler's lock and close them after releasing it, so a close that waits
// for the director's cleanup does not hold up every other WebSocket.
func TestClosingASessionDoesNotHoldTheHandlersLock(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*SSHHandler){
		"inactive-session sweep": (*SSHHandler).cleanupInactiveSessions,
		"handler close":          func(handler *SSHHandler) { _ = handler.Close() },
	}

	for name, closeSessions := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := NewSSHHandler(nil, Config{SessionTimeout: time.Minute}, nil)
			sshSession := newBlockingSSH()
			sessionCtx := addStaleSession(handler, sshSession)

			done := make(chan struct{})

			go func() {
				defer close(done)

				closeSessions(handler)
			}()

			select {
			case <-sshSession.closing:
			case <-time.After(lockWait):
				close(sshSession.release)
				t.Fatal("the SSH session's close never began")
			}

			counted := make(chan int, 1)

			go func() { counted <- handler.GetActiveSessions() }()

			select {
			case count := <-counted:
				if count != 0 {
					t.Errorf("%d active sessions while the stale one closes, want 0", count)
				}
			case <-time.After(lockWait):
				t.Errorf("GetActiveSessions waited %v behind an SSH session's close", lockWait)
			}

			close(sshSession.release)
			<-done

			if got := sshSession.closes.Load(); got != 1 {
				t.Errorf("%d closes of the SSH session, want 1", got)
			}

			if sessionCtx.Err() == nil {
				t.Error("the WebSocket session's context was not cancelled")
			}
		})
	}
}
