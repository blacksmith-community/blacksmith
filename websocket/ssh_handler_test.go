package websocket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/websocket"
	wsn "nhooyr.io/websocket"
)

const eventWait = 5 * time.Second

var errTestStartRefused = errors.New("test VM refused the connection")

// eventLog records what the fake SSH service did, in order.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.events...)
}

func (l *eventLog) count(prefix string) int {
	count := 0

	for _, event := range l.snapshot() {
		if strings.HasPrefix(event, prefix) {
			count++
		}
	}

	return count
}

// waitFor waits until the log holds want events that start with prefix.
func (l *eventLog) waitFor(t *testing.T, prefix string, want int) {
	t.Helper()

	deadline := time.Now().Add(eventWait)
	for l.count(prefix) < want {
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %d %q events, have %v", eventWait, want, prefix, l.snapshot())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// recordingService stands in for the SSH service. Each CreateSession is
// one director setup, and closing a session is its cleanup. failStarts
// lists the setups, counted from 1, whose Start fails.
type recordingService struct {
	log        *eventLog
	failStarts map[int]bool

	// When holdStart is set, each Start sends on startEntered and then
	// waits until holdStart is closed.
	holdStart    chan struct{}
	startEntered chan struct{}

	mu     sync.Mutex
	setups int
}

func (s *recordingService) ExecuteCommand(context.Context, *ssh.SSHRequest) (*ssh.SSHResponse, error) {
	return &ssh.SSHResponse{}, nil
}

//nolint:ireturn // The SSHService interface returns an SSHSession.
func (s *recordingService) CreateSession(_ context.Context, _ *ssh.SSHRequest) (ssh.SSHSession, error) {
	s.mu.Lock()
	s.setups++
	number := s.setups
	s.mu.Unlock()

	s.log.add(fmt.Sprintf("setup %d", number))

	return &recordingSession{
		log:          s.log,
		number:       number,
		failStart:    s.failStarts[number],
		holdStart:    s.holdStart,
		startEntered: s.startEntered,
		status:       ssh.SessionStatusCreated,
	}, nil
}

func (s *recordingService) Close() error { return nil }

type recordingSession struct {
	log          *eventLog
	number       int
	failStart    bool
	holdStart    chan struct{}
	startEntered chan struct{}

	mu     sync.Mutex
	status ssh.SessionStatus
	closed bool
}

func (s *recordingSession) Start() error {
	if s.holdStart != nil {
		s.startEntered <- struct{}{}

		<-s.holdStart
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failStart {
		s.status = ssh.SessionStatusError

		return errTestStartRefused
	}

	s.status = ssh.SessionStatusConnected

	return nil
}

func (s *recordingSession) SendInput([]byte) error { return nil }

func (s *recordingSession) ReadOutput() ([]byte, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return nil, ssh.NewSSHError(ssh.SSHErrorCodeConnection, "Session closed", nil)
	}

	time.Sleep(time.Millisecond)

	return []byte{}, nil
}

func (s *recordingSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	s.status = ssh.SessionStatusClosed
	s.log.add(fmt.Sprintf("cleanup %d", s.number))

	return nil
}

func (s *recordingSession) Status() ssh.SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.status
}

func (s *recordingSession) SetWindowSize(int, int) error { return nil }

// openTestTerminal serves the handler on a loopback server and opens a
// WebSocket to it, reading the handshake message.
func openTestTerminal(t *testing.T, service ssh.SSHService) *wsn.Conn {
	t.Helper()

	return openTestTerminalUnder(t, service, (*http.Request).Context)
}

// openTestTerminalUnder is openTestTerminal with the handler running each
// WebSocket under the context that contextOf returns for its request.
func openTestTerminalUnder(t *testing.T, service ssh.SSHService, contextOf func(*http.Request) context.Context) *wsn.Conn {
	t.Helper()

	handler := websocket.NewSSHHandler(service, websocket.Config{}, nil)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		//nolint:contextcheck // The test picks the WebSocket's context, so it can end one while a start runs.
		handler.HandleWebSocket(contextOf(request), writer, request, "redis-1", "redis", 0)
	}))
	t.Cleanup(server.Close)

	conn, response, err := wsn.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}

	t.Cleanup(func() { _ = conn.CloseNow() })

	handshake := readMessage(t, conn)
	if handshake.Type != websocket.MsgTypeHandshake {
		t.Fatalf("first message is %q, want a handshake", handshake.Type)
	}

	return conn
}

func sendStart(t *testing.T, conn *wsn.Conn) {
	t.Helper()

	data, err := json.Marshal(websocket.WSMessage{
		Type: websocket.MsgTypeControl,
		Meta: map[string]interface{}{"action": "start"},
	})
	if err != nil {
		t.Fatalf("marshal start: %v", err)
	}

	err = conn.Write(t.Context(), wsn.MessageText, data)
	if err != nil {
		t.Fatalf("send start: %v", err)
	}
}

func readMessage(t *testing.T, conn *wsn.Conn) websocket.WSMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), eventWait)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read message: %v", err)
	}

	var msg websocket.WSMessage

	err = json.Unmarshal(data, &msg)
	if err != nil {
		t.Fatalf("decode message %q: %v", data, err)
	}

	return msg
}

// requireStarted reads the confirmation that a start connected.
func requireStarted(t *testing.T, conn *wsn.Conn) {
	t.Helper()

	msg := readMessage(t, conn)
	if msg.Type != websocket.MsgTypeStatus || msg.Meta["status"] != "connected" {
		t.Fatalf("got %+v, want the connected status", msg)
	}
}

func requireError(t *testing.T, conn *wsn.Conn, want string) {
	t.Helper()

	msg := readMessage(t, conn)
	if msg.Type != websocket.MsgTypeError || !strings.Contains(msg.Data, want) {
		t.Fatalf("got %+v, want an error that says %q", msg, want)
	}
}

func closeTerminal(t *testing.T, conn *wsn.Conn) {
	t.Helper()

	err := conn.Close(wsn.StatusNormalClosure, "")
	if err != nil {
		t.Logf("close: %v", err)
	}
}

// A second start on a WebSocket that already runs a session is refused,
// so the first session is neither replaced nor leaked.
func TestSecondStartOnOneWebSocketIsRefused(t *testing.T) {
	t.Parallel()

	log := &eventLog{}
	conn := openTestTerminal(t, &recordingService{log: log})

	sendStart(t, conn)
	requireStarted(t, conn)

	sendStart(t, conn)
	requireError(t, conn, "already running")

	if got := log.count("setup"); got != 1 {
		t.Fatalf("%d setups after two starts, want 1: %v", got, log.snapshot())
	}

	closeTerminal(t, conn)
	log.waitFor(t, "cleanup", 1)

	// Give a stray second cleanup time to show up.
	time.Sleep(100 * time.Millisecond)

	if got := log.snapshot(); len(got) != 2 || got[0] != "setup 1" || got[1] != "cleanup 1" {
		t.Errorf("events %v, want [setup 1 cleanup 1]", got)
	}
}

// A session whose Start failed is closed, which removes its user, before
// the next start sets up another.
func TestStartAfterAFailedStartClosesTheFailedSession(t *testing.T) {
	t.Parallel()

	log := &eventLog{}
	conn := openTestTerminal(t, &recordingService{log: log, failStarts: map[int]bool{1: true}})

	sendStart(t, conn)
	requireError(t, conn, errTestStartRefused.Error())

	sendStart(t, conn)
	requireStarted(t, conn)

	closeTerminal(t, conn)
	log.waitFor(t, "cleanup", 2)

	time.Sleep(100 * time.Millisecond)

	want := []string{"setup 1", "cleanup 1", "setup 2", "cleanup 2"}
	if got := log.snapshot(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events %v, want %v", got, want)
	}
}

// The SSH session on a WebSocket is reached only through the accessors
// that take its lock, so no exported field exposes it.
func TestStreamSessionKeepsItsSSHSessionBehindItsLock(t *testing.T) {
	t.Parallel()

	sessionType := reflect.TypeFor[websocket.SSHStreamSession]()
	sshType := reflect.TypeFor[ssh.SSHSession]()

	for field := range sessionType.Fields() {
		if field.IsExported() && field.Type == sshType {
			t.Errorf("SSHStreamSession exports %s, which any package can read or write without its lock", field.Name)
		}
	}
}

// A WebSocket that ends while its SSH session starts closes that session
// as soon as Start returns and reports the error, instead of reporting the
// session connected. Its user is removed exactly once.
func TestStartThatOutlivesItsWebSocketClosesTheSession(t *testing.T) {
	t.Parallel()

	log := &eventLog{}
	service := &recordingService{log: log, holdStart: make(chan struct{}), startEntered: make(chan struct{}, 1)}

	webSocketCtx, endWebSocket := context.WithCancel(context.Background())
	defer endWebSocket()

	conn := openTestTerminalUnder(t, service, func(*http.Request) context.Context { return webSocketCtx })

	sendStart(t, conn)

	select {
	case <-service.startEntered:
	case <-time.After(eventWait):
		t.Fatal("the SSH session's Start never began")
	}

	endWebSocket()
	close(service.holdStart)

	requireError(t, conn, "ended while its SSH session started")
	log.waitFor(t, "cleanup", 1)

	// Give a stray second cleanup time to show up.
	time.Sleep(100 * time.Millisecond)

	if got := strings.Join(log.snapshot(), ","); got != "setup 1,cleanup 1" {
		t.Errorf("events %s, want setup 1,cleanup 1", got)
	}
}
