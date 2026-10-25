package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/pkg/logger"
	"golang.org/x/crypto/ssh"
)

// DefaultOutputLimit is how much of each output stream a run keeps when
// its caller names no limit.
const DefaultOutputLimit int64 = 1 << 20

// runnerCloseGrace is how long a run whose context has ended waits for
// its session to wind down after the connection is closed, before it
// returns anyway.
const runnerCloseGrace = 500 * time.Millisecond

// errStdinStopped is what the session's copy of stdin reads once the run
// has returned.
var errStdinStopped = errors.New("the run has returned, so its stdin is closed")

var (
	// ErrContentInArgs means a program or an argument carried text that
	// may only travel on stdin, such as a PEM marker, a newline, or a NUL.
	// Nothing was sent.
	ErrContentInArgs = errors.New("a command line carries text that may only be sent on stdin, so nothing was sent")
	// ErrNoConnection means the runner was made without a connection.
	ErrNoConnection = errors.New("the runner has no SSH connection")
	// ErrRunnerClosed means the runner's connection is closed, by Close or
	// by a run whose context ended.
	ErrRunnerClosed = errors.New("the runner's SSH connection is closed")
	// ErrSessionNotOpened means the connection would not open a session
	// for the command.
	ErrSessionNotOpened = errors.New("failed to open an SSH session on the connection")
	// ErrCommandNotStarted means the VM did not start the command.
	ErrCommandNotStarted = errors.New("the VM did not start the command")
	// ErrNoExitStatus means the command's session ended without an exit
	// status, so whether the command succeeded is unknown.
	ErrNoExitStatus = errors.New("the command ended without an exit status")
	// ErrSessionFailed means the session failed after the command started,
	// for example while its input was being sent.
	ErrSessionFailed = errors.New("the SSH session failed while the command ran")
)

// Result is what one command left behind. Stdout and Stderr each hold at
// most the run's limit. StdoutTruncated and StderrTruncated say which
// stream was cut, and Truncated says that at least one of them was.
// StdoutReceived and StderrReceived count every byte the command wrote to
// each stream, whether it was kept or dropped. The streams may hold anything the VM printed, so they reach a log
// line only through ForLog, and never reach an error, a stored record, or
// an API response.
type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
	Truncated      bool

	StdoutTruncated, StderrTruncated bool
	StdoutReceived, StderrReceived   int64
}

// Runner runs commands on one VM over a shared SSH connection, opening
// one SSH session per command. It is the only path by which key and
// certificate content reaches a VM, and that content travels on stdin,
// never on the command line. It logs at debug level only, and only
// through ForLog. Runs may share a Runner from several goroutines.
type Runner struct {
	client *ssh.Client
	log    logger.Logger

	// done closes when the connection is closed.
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// NewRunner returns a runner over client. A nil log logs nothing. Close
// closes client, and so does a run whose context ends before its command
// does. A VM that has stopped answering leaves nothing else on the
// connection that can be trusted to end, so a deadline costs the
// connection, every later run on the runner returns ErrRunnerClosed, and
// the caller opens a new connection with DialSSHHost and a new runner.
func NewRunner(client *ssh.Client, log logger.Logger) *Runner {
	if log == nil {
		log = logger.NewNoOpLogger()
	}

	return &Runner{client: client, log: log, done: make(chan struct{})}
}

// Run runs program with args on the VM, sends stdin to it, and returns
// its output and exit status. Every argument is quoted, and a program or
// an argument that may carry key content, such as a PEM marker, a key
// body, a colon-hex dump, a line break, or a NUL, is refused before
// anything is sent, so content can travel only on stdin. Each stream
// keeps at most limit bytes, or DefaultOutputLimit when limit is not
// positive, and the rest is read and dropped so the command can finish.
//
// A command that exits with any status returns a nil error, and the
// caller reads ExitCode. When ctx ends first, whether the session is
// opening, starting, or running, Run closes the runner's whole SSH
// connection, which ends every channel on it, and returns an error that
// wraps ctx's error, such as context.DeadlineExceeded. The runner is
// closed from then on, and the caller dials a new connection with
// DialSSHHost. No error carries the command's output.
//
// Run does not wait for a read of stdin that is under way, and a read
// can even start just after Run returns, because the stop check and the
// read are not one atomic step. What any such read returns is wiped and
// dropped, never sent. A reader whose read never returns keeps one
// goroutine of the SSH library alive, so stdin should be an in-memory
// reader, such as a bytes.Reader. A caller that wipes the buffer behind
// stdin calls Close on the runner first, so that no late read races the
// wipe.
func (r *Runner) Run(ctx context.Context, program string, args []string, stdin io.Reader, limit int64) (Result, error) {
	command, err := r.prepare(ctx, program, args)
	if err != nil {
		return Result{}, err
	}

	if limit <= 0 {
		limit = DefaultOutputLimit
	}

	shown := ForLog([]byte(command))

	session, err := r.openSession(ctx, shown)
	if err != nil {
		return Result{}, err
	}

	defer func() { _ = session.Close() }()

	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: limit}
	session.Stdout = stdout
	session.Stderr = stderr

	if stdin != nil {
		gate := &stdinGate{reader: stdin}
		session.Stdin = gate

		defer gate.stop()
	}

	end, err := r.execute(ctx, session, command, shown)
	if err != nil {
		return snapshotResult(stdout, stderr, 0), err
	}

	return r.finish(shown, stdout, stderr, end)
}

// Close closes the shared connection, which ends any run still on it. A
// run that starts after Close, or that is still opening its session,
// returns ErrRunnerClosed. Close acts once, and a second call returns the
// first call's answer.
func (r *Runner) Close() error {
	r.closed.Store(true)

	r.closeOnce.Do(func() {
		if r.done != nil {
			close(r.done)
		}

		if r.client == nil {
			return
		}

		err := r.client.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			r.closeErr = fmt.Errorf("failed to close the SSH connection: %w", err)
		}
	})

	return r.closeErr
}

// ended closes the runner's connection for a run whose context ended
// first, and logs and returns the run's error.
func (r *Runner) ended(ctx context.Context, shown string) error {
	_ = r.Close()

	r.log.Debugf("Closed the SSH connection, because the context of the run of %s ended", shown)

	return fmt.Errorf("the command did not finish before its context ended, so the runner closed its SSH connection: %w", ctx.Err())
}

// openedSession is what an attempt to open a session came back with.
type openedSession struct {
	session *ssh.Session
	err     error
}

// openSession opens a session on the shared connection. Opening a channel
// on a connection that is going away can wait without end, so it gives up
// when ctx ends, which closes the connection, or when the runner is
// closed. A session that opens after the run gave up is closed at once.
// A runner that is already closed, or that closes while the open fails,
// returns ErrRunnerClosed every time, whichever of its select cases Go
// would pick.
func (r *Runner) openSession(ctx context.Context, shown string) (*ssh.Session, error) {
	if r.closed.Load() {
		return nil, ErrRunnerClosed
	}

	opened := make(chan openedSession, 1)

	go func() {
		session, err := r.client.NewSession()
		opened <- openedSession{session: session, err: err}
	}()

	select {
	case got := <-opened:
		if got.err != nil && r.closed.Load() {
			return nil, ErrRunnerClosed
		}

		if got.err != nil {
			// The library's error can carry the reason the VM's sshd
			// gave, which is free text, so it reaches only a log line,
			// escaped by ForLog, and never the error.
			r.log.Debugf("Could not open an SSH session to run %s: %s", shown, ForLog([]byte(got.err.Error())))

			return nil, ErrSessionNotOpened
		}

		return got.session, nil
	case <-ctx.Done():
		go closeLateSession(opened)

		return nil, r.ended(ctx, shown)
	case <-r.done:
		go closeLateSession(opened)

		return nil, ErrRunnerClosed
	}
}

// closeLateSession closes a session that opened after its run gave up.
func closeLateSession(opened <-chan openedSession) {
	got := <-opened
	if got.session != nil {
		_ = got.session.Close()
	}
}

// prepare refuses a command line that carries content, a runner with no
// connection, and a context that has already ended, and returns the
// command line to send.
func (r *Runner) prepare(ctx context.Context, program string, args []string) (string, error) {
	err := checkCommandLine(program, args)
	if err != nil {
		return "", err
	}

	if r.client == nil {
		return "", ErrNoConnection
	}

	if r.closed.Load() {
		return "", ErrRunnerClosed
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("the command was not started, because its context had ended: %w", ctx.Err())
	}

	return Command(program, args), nil
}

// sessionEnd is how the session of a command that started ended. code
// is its exit status, and err says why it ended without one.
type sessionEnd struct {
	code int
	err  error
}

// execute starts command on session and waits for it to end. Any exit
// status ends it with a nil error. A session that ended without one ends
// with an error that carries none of the output. execute returns err when
// the command did not start, the runner was closed, or ctx ended first.
// When ctx ends, the whole connection is closed, because closing only the
// session sends a message that a VM which has stopped answering never
// acknowledges, and a start waiting on its reply would wait for good.
// Closing the connection ends every channel on it at once.
func (r *Runner) execute(ctx context.Context, session *ssh.Session, command, shown string) (sessionEnd, error) {
	stopClosing := context.AfterFunc(ctx, func() { _ = r.Close() })
	defer stopClosing()

	r.log.Debugf("Running %s over SSH", shown)

	err := session.Start(command)
	if err != nil {
		if ctx.Err() != nil {
			return sessionEnd{}, r.ended(ctx, shown)
		}

		if r.closed.Load() {
			return sessionEnd{}, ErrRunnerClosed
		}

		r.log.Debugf("The VM did not start %s", shown)

		return sessionEnd{}, ErrCommandNotStarted
	}

	var waitErr error

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		waitErr = session.Wait()
	}()

	select {
	case <-finished:
	case <-ctx.Done():
		err = r.ended(ctx, shown)

		select {
		case <-finished:
		case <-time.After(runnerCloseGrace):
		}

		return sessionEnd{}, err
	}

	var exitErr *ssh.ExitError

	switch {
	case waitErr == nil:
		return sessionEnd{}, nil
	case errors.As(waitErr, &exitErr):
		// ExitStatus is 128 plus the signal's number for a command a
		// signal ended, as a shell reports it.
		return sessionEnd{code: exitErr.ExitStatus()}, nil
	case ctx.Err() != nil:
		// A session the context closed ends without an exit status, and
		// that is the context's doing, not the command's.
		return sessionEnd{}, r.ended(ctx, shown)
	case r.closed.Load():
		return sessionEnd{}, ErrRunnerClosed
	case errors.As(waitErr, new(*ssh.ExitMissingError)):
		return sessionEnd{err: ErrNoExitStatus}, nil
	default:
		return sessionEnd{err: fmt.Errorf("%w: %w", ErrSessionFailed, waitErr)}, nil
	}
}

// finish turns what the session left behind into the run's result, and
// logs it at debug level with each stream passed through ForLog.
func (r *Runner) finish(shown string, stdout, stderr *boundedBuffer, end sessionEnd) (Result, error) {
	result := snapshotResult(stdout, stderr, end.code)

	r.log.Debugf("%s ended with exit status %d, truncated %t, stdout %s, stderr %s",
		shown, result.ExitCode, result.Truncated, ForLog(result.Stdout), ForLog(result.Stderr))

	return result, end.err
}

// checkCommandLine refuses a program or an argument that carries text
// that may only travel on stdin. The error names the argument by its
// position and never repeats it.
func checkCommandLine(program string, args []string) error {
	if carriesContent(program) {
		return fmt.Errorf("%w: the program text is refused", ErrContentInArgs)
	}

	for i, arg := range args {
		if carriesContent(arg) {
			return fmt.Errorf("%w: argument %d is refused", ErrContentInArgs, i+1)
		}
	}

	return nil
}

// carriesContent reports whether s holds a line break or a NUL, or text
// Withheld would keep out of a log line, because a command line reaches
// the VM's process list and audit log. That means a PEM marker word,
// bytes that are not text, or a colon-hex key dump. The base64 rule is
// applied to each part of s between slashes, so a path whose parts are
// ordinary names still passes, while a key body, which almost always
// holds 40 or more base64 characters between two slashes, is refused.
func carriesContent(s string) bool {
	if strings.ContainsAny(s, "\n\r\x00") || hasMarkerOrDump([]byte(s)) {
		return true
	}

	for segment := range strings.SplitSeq(s, "/") {
		if hasLongBase64Run([]byte(segment)) {
			return true
		}
	}

	return false
}

// stdinGate passes the caller's stdin to the session until stop, and
// fails every read after it. It never holds its lock across the caller's
// Read, so stop returns at once even while a read is blocked. A read that
// was under way when stop ran reads into a buffer of its own, and once it
// returns its bytes are wiped and dropped, so nothing it read reaches the
// session. A reader whose read never returns still keeps that one copy
// goroutine of the SSH library alive, but it can deliver no bytes.
type stdinGate struct {
	mu      sync.Mutex
	reader  io.Reader
	stopped bool
}

func (g *stdinGate) Read(buf []byte) (int, error) {
	if g.isStopped() {
		return 0, errStdinStopped
	}

	local := make([]byte, len(buf))
	count, err := g.reader.Read(local)

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.stopped {
		clear(local)

		return 0, errStdinStopped
	}

	copy(buf, local[:count])
	clear(local)

	return count, err //nolint:wrapcheck // the caller's reader's own answer, io.EOF included
}

func (g *stdinGate) isStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.stopped
}

func (g *stdinGate) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.stopped = true
}

// snapshotResult copies what the two buffers hold into a Result. It is
// safe while the session still writes, because each buffer is read under
// its own lock.
func snapshotResult(stdout, stderr *boundedBuffer, code int) Result {
	out, outReceived, outCut := stdout.snapshot()
	errOut, errReceived, errCut := stderr.snapshot()

	return Result{
		Stdout:          out,
		Stderr:          errOut,
		ExitCode:        code,
		Truncated:       outCut || errCut,
		StdoutTruncated: outCut,
		StderrTruncated: errCut,
		StdoutReceived:  outReceived,
		StderrReceived:  errReceived,
	}
}

// boundedBuffer keeps the first limit bytes written to it and drops the
// rest, and records that it dropped some and how many bytes it was
// offered in all. Every write reports success, so the session keeps
// reading the stream and the command never blocks on a full window. The
// session writes from its own goroutine while a caller may read, so a
// lock guards every access.
type boundedBuffer struct {
	limit    int64
	mu       sync.Mutex
	buf      bytes.Buffer
	cut      bool
	received int64
}

func (b *boundedBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.received += int64(len(chunk))

	room := b.limit - int64(b.buf.Len())

	switch {
	case room <= 0:
		b.cut = b.cut || len(chunk) > 0
	case int64(len(chunk)) > room:
		b.buf.Write(chunk[:room])
		b.cut = true
	default:
		b.buf.Write(chunk)
	}

	return len(chunk), nil
}

// snapshot returns a copy of the kept bytes, the count of bytes offered,
// and whether any were dropped.
func (b *boundedBuffer) snapshot() ([]byte, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return bytes.Clone(b.buf.Bytes()), b.received, b.cut
}
