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

// watchedReader yields bytes without end, a little at a time, and counts
// every read that starts once returned is set.
type watchedReader struct {
	returned atomic.Bool
	late     atomic.Int32
}

func (r *watchedReader) Read(buf []byte) (int, error) {
	if r.returned.Load() {
		r.late.Add(1)
	}

	time.Sleep(2 * time.Millisecond)

	n := min(len(buf), 1024)
	for i := range n {
		buf[i] = 'k'
	}

	return n, nil
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

// A VM that stops answering while its command reads a large stdin holds
// the copy of stdin, and Run still returns at its deadline, and no read
// of the stdin reader starts after it returns.
func TestRunnerDeadlineWhenVMStopsMidRun(t *testing.T) {
	t.Parallel()

	frozen := &atomic.Bool{}
	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, stop <-chan struct{}) uint32 {
		_, _ = io.CopyN(io.Discard, channel, 16<<10)

		frozen.Store(true)

		<-stop

		return 0
	})
	proxy := startFreezingProxy(t, fakeVM.listener.Addr().String(), frozen)

	runner := boshssh.NewRunner(fakeVM.dialVia(t, proxy.addr()), &runnerLog{})
	stdin := &watchedReader{}

	runWithDeadline(t, runner, stdin)

	stdin.returned.Store(true)
	time.Sleep(100 * time.Millisecond)

	if late := stdin.late.Load(); late != 0 {
		t.Errorf("the stdin reader was read %d time(s) after Run returned", late)
	}
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

// A command that ends without reading all of its stdin leaves no read of
// the stdin reader to start once Run returns.
func TestRunnerReadsNoStdinAfterItReturns(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		_, _ = io.CopyN(io.Discard, channel, 4<<10)

		time.Sleep(50 * time.Millisecond)

		return 0
	})

	for range 5 {
		runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})
		stdin := &watchedReader{}

		_, _ = runner.Run(t.Context(), catProgram, nil, stdin, 0)

		stdin.returned.Store(true)
		time.Sleep(50 * time.Millisecond)

		if late := stdin.late.Load(); late != 0 {
			t.Errorf("the stdin reader was read %d time(s) after Run returned", late)
		}
	}
}
