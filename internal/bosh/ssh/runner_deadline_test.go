package ssh_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	boshssh "blacksmith/internal/bosh/ssh"
	"golang.org/x/crypto/ssh"
)

// freezingProxy forwards TCP between its clients and a target until
// frozen is set, and from then on drops every byte in both directions,
// as a partitioned network or a frozen VM does. Neither end sees the
// connection close.
type freezingProxy struct {
	listener net.Listener
	target   string
	frozen   *atomic.Bool
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

func startFreezingProxy(t *testing.T, target string, frozen *atomic.Bool) *freezingProxy {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	proxy := &freezingProxy{listener: listener, target: target, frozen: frozen}

	proxy.wg.Add(1)

	go func() {
		defer proxy.wg.Done()

		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}

			proxy.forward(client)
		}
	}()

	t.Cleanup(proxy.close)

	return proxy
}

func (p *freezingProxy) forward(client net.Conn) {
	server, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.target)
	if err != nil {
		_ = client.Close()

		return
	}

	p.mu.Lock()
	p.conns = append(p.conns, client, server)
	p.mu.Unlock()

	p.wg.Add(2)

	go func() {
		defer p.wg.Done()

		p.copyUntilFrozen(server, client)
	}()

	go func() {
		defer p.wg.Done()

		p.copyUntilFrozen(client, server)
	}()
}

// copyUntilFrozen copies src to dst, dropping every byte once frozen.
func (p *freezingProxy) copyUntilFrozen(dst io.Writer, src io.Reader) {
	buf := make([]byte, 32<<10)

	for {
		n, err := src.Read(buf)
		if n > 0 && !p.frozen.Load() {
			_, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return
			}
		}

		if err != nil {
			return
		}
	}
}

func (p *freezingProxy) close() {
	_ = p.listener.Close()

	p.mu.Lock()
	for _, conn := range p.conns {
		_ = conn.Close()
	}
	p.mu.Unlock()

	p.wg.Wait()
}

func (p *freezingProxy) addr() string { return p.listener.Addr().String() }

// lateReadWatch is how long a test watches for a read of stdin that
// starts after the read Run left under way has been let go. A gate that
// lets that read through starts the next one at once.
const lateReadWatch = 100 * time.Millisecond

// watchedReader is stdin for a run. It serves its first serve bytes, and
// then holds its next read until end lets it go, so a test can make sure
// that read is under way when Run returns. The held read returns no bytes
// and no error, which a copy of stdin reads past at once, so a gate that
// let it through would start another read. Every read that starts after
// end is late, and it ends the copy, so a broken gate can't spin on it.
type watchedReader struct {
	serve  int
	served int

	held    chan struct{}
	release chan struct{}
	hold    sync.Once
	ended   sync.Once

	returned atomic.Bool
	late     atomic.Int32
	lateRead chan struct{}
	noted    sync.Once
}

// newWatchedReader returns a reader that serves serve bytes and then
// holds, and lets any held read go when the test ends.
func newWatchedReader(t *testing.T, serve int) *watchedReader {
	t.Helper()

	r := &watchedReader{serve: serve, held: make(chan struct{}), release: make(chan struct{}), lateRead: make(chan struct{})}

	t.Cleanup(r.end)

	return r
}

func (r *watchedReader) Read(buf []byte) (int, error) {
	if r.returned.Load() {
		r.late.Add(1)
		r.noted.Do(func() { close(r.lateRead) })

		return 0, io.EOF
	}

	if r.served >= r.serve {
		r.hold.Do(func() { close(r.held) })
		<-r.release

		return 0, nil
	}

	n := min(len(buf), r.serve-r.served)
	for i := range n {
		buf[i] = 'k'
	}

	r.served += n

	return n, nil
}

// end marks Run as returned and lets the held read go.
func (r *watchedReader) end() {
	r.ended.Do(func() {
		r.returned.Store(true)
		close(r.release)
	})
}

// runWithDeadline runs cat on runner with a 200ms deadline and fails the
// test unless Run returns context.DeadlineExceeded within a second.
func runWithDeadline(t *testing.T, runner *boshssh.Runner, stdin io.Reader) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	started := time.Now()

	go func() {
		_, err := runner.Run(ctx, catProgram, nil, stdin, 0)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run returned %v, want context.DeadlineExceeded", err)
		}

		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("Run returned after %s, want within 1s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run was still running 3s after a 200ms deadline")
	}
}

// A VM that stops answering as the exec request arrives never answers it,
// and Run still returns at its deadline. The deadline costs the runner
// its connection, so a later run is refused.
func TestRunnerDeadlineWhenExecIsNeverAnswered(t *testing.T) {
	t.Parallel()

	frozen := &atomic.Bool{}
	fakeVM := startRunnerVM(t, func(_ string, _ ssh.Channel, stop <-chan struct{}) uint32 {
		<-stop

		return 0
	}, func() { frozen.Store(true) })
	proxy := startFreezingProxy(t, fakeVM.listener.Addr().String(), frozen)

	runner := boshssh.NewRunner(fakeVM.dialVia(t, proxy.addr()), &runnerLog{})

	runWithDeadline(t, runner, nil)

	_, err := runner.Run(t.Context(), catProgram, nil, nil, 0)
	if !errors.Is(err, boshssh.ErrRunnerClosed) {
		t.Errorf("a run after the deadline returned %v, want ErrRunnerClosed", err)
	}
}

// A VM that stops answering while its command reads stdin, with a read of
// stdin under way, does not hold Run past its deadline, and no read of
// stdin starts after Run returns.
func TestRunnerDeadlineWhenVMStopsMidRun(t *testing.T) {
	t.Parallel()

	stdin := newWatchedReader(t, 16<<10)
	frozen := &atomic.Bool{}
	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, stop <-chan struct{}) uint32 {
		_, _ = io.CopyN(io.Discard, channel, 16<<10)

		select {
		case <-stdin.held:
		case <-stop:
		}

		frozen.Store(true)

		<-stop

		return 0
	})
	proxy := startFreezingProxy(t, fakeVM.listener.Addr().String(), frozen)

	runner := boshssh.NewRunner(fakeVM.dialVia(t, proxy.addr()), &runnerLog{})

	runWithDeadline(t, runner, stdin)

	expectNoLateRead(t, stdin)
}

// A stdin reader whose read never returns does not hold Run past its
// deadline.
func TestRunnerDeadlineWhenStdinBlocks(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, _ ssh.Channel, stop <-chan struct{}) uint32 {
		<-stop

		return 0
	})
	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	stdin, writer := io.Pipe()

	t.Cleanup(func() { _ = writer.Close() })

	runWithDeadline(t, runner, stdin)
}

// A command that ends without reading all of its stdin, with a read of
// stdin under way, leaves no read of stdin to start after Run returns.
func TestRunnerReadsNoStdinAfterItReturns(t *testing.T) {
	t.Parallel()

	for range 5 {
		stdin := newWatchedReader(t, 4<<10)
		fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, stop <-chan struct{}) uint32 {
			_, _ = io.CopyN(io.Discard, channel, 4<<10)

			select {
			case <-stdin.held:
			case <-stop:
			}

			return 0
		})

		runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

		_, err := runner.Run(t.Context(), catProgram, nil, stdin, 0)
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}

		expectNoLateRead(t, stdin)
	}
}

// expectNoLateRead lets go the read of stdin that was under way when Run
// returned, and fails the test when another read starts. That read began
// before Run stopped the gate, so it is the only read a stop check could
// have passed for, and the gate turns away what it brings back, which
// ends the copy of stdin. Any read after it means the gate let a read
// through after Run returned.
func expectNoLateRead(t *testing.T, stdin *watchedReader) {
	t.Helper()

	select {
	case <-stdin.held:
	default:
		t.Fatal("no read of stdin was under way when Run returned")
	}

	stdin.end()

	select {
	case <-stdin.lateRead:
		t.Errorf("the stdin reader was read %d times after Run returned, want none", stdin.late.Load())
	case <-time.After(lateReadWatch):
	}
}
