package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

const (
	testDeployment = "blacksmith-1"
	testGroup      = "blacksmith"
	testInstance   = testGroup + "/" + boshtest.DefaultSSHInstanceID

	// waitLimit bounds every wait on a condition. A test that passes meets
	// its conditions long before it.
	waitLimit = 10 * time.Second
	// testPoll is how often the tool and the tests read state.
	testPoll = 5 * time.Millisecond
	// quietPeriod is how long a test watches for a request that must not
	// come.
	quietPeriod = 200 * time.Millisecond

	// shortSettle is the bound on a settle that is meant to run out.
	shortSettle = 500 * time.Millisecond

	uploadPrefix = "umask 077 && cat > "

	aliasName     = "lab-director"
	flagBinary    = "-binary"
	adviceLinux   = "make linux"
	adviceNotSet  = "not set"
	adviceAddress = "director's address"
	urlIP         = "https://10.0.0.6:25555"
	urlName       = "https://bosh.example.com:25555"
)

// syncBuffer is a buffer the tool and the test can use at the same time.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p) //nolint:wrapcheck // a bytes.Buffer write never fails
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

// fakeVM is an in-process SSH server on a loopback address. It keeps a file
// system of its own, and runs the few commands the tool sends.
type fakeVM struct {
	port        string
	failInstall bool
	failUpload  bool
	// hold, when set, keeps the upload command running after the file has
	// arrived until the channel is closed, and reached is closed when the
	// upload is held.
	hold    chan struct{}
	reached chan struct{}

	mu        sync.Mutex
	commands  []string
	files     map[string][]byte
	installed map[string][]byte
}

// startFakeVM serves SSH on a loopback port the system picks. It presents
// the director's host key and accepts the key that the latest setup sent.
func startFakeVM(t *testing.T, director *boshtest.SSHDirector) *fakeVM {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the fake VM: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("read the fake VM's port: %v", err)
	}

	machine := &fakeVM{port: port, files: map[string][]byte{}, installed: map[string][]byte{}}

	config := &ssh.ServerConfig{PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if allowedKey(director, conn.User(), key) {
			return &ssh.Permissions{}, nil
		}

		return nil, errors.New("the fake VM accepts only the key of the latest setup") //nolint:err113 // a test server's refusal
	}}
	config.AddHostKey(director.HostKey)

	go machine.accept(listener, config)

	return machine
}

// allowedKey reports whether the latest setup created user with key.
func allowedKey(director *boshtest.SSHDirector, user string, key ssh.PublicKey) bool {
	calls := director.CallsOf(boshtest.SSHSetup)
	if len(calls) == 0 || calls[len(calls)-1].User() != user {
		return false
	}

	params, _ := calls[len(calls)-1].Fields["params"].(map[string]any)
	text, _ := params["public_key"].(string)

	sent, _, _, _, err := ssh.ParseAuthorizedKey([]byte(text)) //nolint:dogsled // only the key matters
	if err != nil {
		return false
	}

	return bytes.Equal(sent.Marshal(), key.Marshal())
}

func (v *fakeVM) accept(listener net.Listener, config *ssh.ServerConfig) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		go v.serve(conn, config)
	}
}

func (v *fakeVM) serve(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}

	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")

			continue
		}

		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}

		go v.session(channel, channelRequests)
	}
}

func (v *fakeVM) session(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()

	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)

			continue
		}

		var payload struct{ Command string }

		if ssh.Unmarshal(request.Payload, &payload) != nil {
			_ = request.Reply(false, nil)

			return
		}

		_ = request.Reply(true, nil)

		status := v.exec(payload.Command, channel)

		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))

		return
	}
}

// exec runs one command and returns its exit status.
func (v *fakeVM) exec(command string, channel ssh.Channel) uint32 {
	v.mu.Lock()
	v.commands = append(v.commands, command)
	v.mu.Unlock()

	switch {
	case strings.HasPrefix(command, uploadPrefix):
		data, _ := io.ReadAll(channel)

		v.mu.Lock()
		v.files[strings.TrimPrefix(command, uploadPrefix)] = data
		v.mu.Unlock()

		if v.failUpload {
			_, _ = io.WriteString(channel.Stderr(), "cat: write error: No space left on device\n")

			return 1
		}

		if v.hold != nil {
			close(v.reached)

			<-v.hold
		}

		return 0
	case strings.HasPrefix(command, "sudo install "):
		return v.install(command, channel)
	case strings.HasPrefix(command, "rm -f "):
		v.mu.Lock()
		delete(v.files, strings.TrimPrefix(command, "rm -f "))
		v.mu.Unlock()

		return 0
	case command == monitRestart:
		_, _ = io.WriteString(channel, "Process 'blacksmith' restart pending\n")

		return 0
	default:
		_, _ = io.WriteString(channel.Stderr(), "unknown command\n")

		return 127
	}
}

// install runs sudo install -o vcap -g vcap -m 0755 SRC DEST && rm -f SRC.
func (v *fakeVM) install(command string, channel ssh.Channel) uint32 {
	fields := strings.Fields(command)

	const sourceField, destField = 8, 9
	if len(fields) < destField+1 {
		_, _ = io.WriteString(channel.Stderr(), "install: missing file operand\n")

		return 1
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	data, found := v.files[fields[sourceField]]
	if v.failInstall || !found {
		_, _ = io.WriteString(channel.Stderr(), "install: cannot create regular file: No space left on device\n")

		return 1
	}

	v.installed[fields[destField]] = data

	delete(v.files, fields[sourceField])

	return 0
}

func (v *fakeVM) ran() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	return slices.Clone(v.commands)
}

func (v *fakeVM) tempFiles() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return len(v.files)
}

func (v *fakeVM) installedBinary() []byte {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.installed[remoteBinary]
}

// rig is a pusher against a test director and a fake VM.
type rig struct {
	director *boshtest.SSHDirector
	machine  *fakeVM
	pusher   *pusher
	out      *syncBuffer
	binary   string
	payload  []byte
}

// elfFile returns the bytes of a 64-bit little-endian ELF executable header
// for machine and OS ABI, followed by body.
func elfFile(machine elf.Machine, osabi elf.OSABI, body string) []byte {
	const (
		headerSize = 64
		typeOffset = 16
		machOffset = 18
		verOffset  = 20
		ehsOffset  = 52
		phsOffset  = 54
		shsOffset  = 58
	)

	var header [headerSize]byte

	copy(header[:], elf.ELFMAG)

	header[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	header[elf.EI_OSABI] = byte(osabi)

	binary.LittleEndian.PutUint16(header[typeOffset:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[machOffset:], uint16(machine))
	binary.LittleEndian.PutUint32(header[verOffset:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(header[ehsOffset:], headerSize)
	binary.LittleEndian.PutUint16(header[phsOffset:], 56)
	binary.LittleEndian.PutUint16(header[shsOffset:], headerSize)

	return slices.Concat(header[:], []byte(body))
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	err := os.WriteFile(path, data, 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", name, err)
	}

	return path
}

// newRig starts a director that answers with answer, a fake VM that
// accepts the setup's key, and a pusher with short bounds.
func newRig(t *testing.T, answer func(boshtest.SSHCall) boshtest.SSHAnswer) *rig {
	t.Helper()

	director := boshtest.NewSSH(t, answer, nil)
	machine := startFakeVM(t, director)
	payload := elfFile(elf.EM_X86_64, elf.ELFOSABI_NONE, strings.Repeat("blacksmith dev build ", 1000))

	client, err := newClient(t.Context(), environment{
		director: director.URL, caCert: director.CACert, client: "admin", secret: "secret",
	})
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}

	target, err := parseInstance(testInstance)
	if err != nil {
		t.Fatalf("parse the instance: %v", err)
	}

	rig := &rig{director: director, machine: machine, out: &syncBuffer{}, payload: payload, binary: writeFile(t, "blacksmith-linux-amd64", payload)}

	rig.pusher = newPusher(client, testDeployment, target, rig.binary, rig.out)
	rig.pusher.sshPort = machine.port
	rig.pusher.pollInterval = testPoll
	rig.pusher.lostSetupGrace = 2 * testPoll
	rig.pusher.dialTimeout = waitLimit

	return rig
}

// waitFor polls until condition holds, and fails the test when it does not
// within waitLimit.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitLimit)

	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(testPoll)
	}
}

// pushWithin runs the push in the background and returns the channel that
// receives its error.
func (r *rig) pushWithin(ctx context.Context) <-chan error {
	done := make(chan error, 1)

	go func() { done <- r.pusher.push(ctx) }()

	return done
}

// result waits for a push to return.
func result(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(waitLimit):
		t.Fatal("the push did not return")

		return nil
	}
}

func (r *rig) setupUser(t *testing.T) string {
	t.Helper()

	calls := r.director.CallsOf(boshtest.SSHSetup)
	if len(calls) != 1 {
		t.Fatalf("expected one setup request, got %d", len(calls))
	}

	return calls[0].User()
}

func onlyCleanup(t *testing.T, r *rig) boshtest.SSHCall {
	t.Helper()

	calls := r.director.CallsOf(boshtest.SSHCleanup)
	if len(calls) != 1 {
		t.Fatalf("expected one cleanup request, got %d: %+v", len(calls), calls)
	}

	return calls[0]
}

// assertCleanupFor fails the test unless call removes user from the
// instance the setup targeted, with the anchored pattern.
func assertCleanupFor(t *testing.T, call boshtest.SSHCall, user string) {
	t.Helper()

	if call.Deployment != testDeployment {
		t.Errorf("the cleanup went to deployment %q, want %q", call.Deployment, testDeployment)
	}

	params, _ := call.Fields["params"].(map[string]any)
	if regex, _ := params["user_regex"].(string); regex != sshconn.UserRegex(user) {
		t.Errorf("the cleanup pattern is %q, want %q", regex, sshconn.UserRegex(user))
	}

	target, _ := call.Fields["target"].(map[string]any)
	ids, _ := target["ids"].([]any)

	if target["job"] != testGroup || len(ids) != 1 || ids[0] != boshtest.DefaultSSHInstanceID {
		t.Errorf("the cleanup target is %v, want %s with ids [%s]", target, testGroup, boshtest.DefaultSSHInstanceID)
	}
}

// The tool copies the binary through one session's stdin, installs it, and
// restarts blacksmith, in that order, and then removes the user.
func TestDevpushCopiesTheBinaryAndRestarts(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)

	err := rig.pusher.push(t.Context())
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	user := rig.setupUser(t)
	temp := "/tmp/blacksmith." + user

	want := []string{
		uploadPrefix + temp,
		"sudo install -o vcap -g vcap -m 0755 " + temp + " " + remoteBinary + " && rm -f " + temp,
		"sudo /var/vcap/bosh/bin/monit restart blacksmith",
	}
	if got := rig.machine.ran(); !slices.Equal(got, want) {
		t.Errorf("remote commands:\n got %q\nwant %q", got, want)
	}

	if !bytes.Equal(rig.machine.installedBinary(), rig.payload) {
		t.Errorf("the installed binary is %d bytes, want the %d bytes pushed", len(rig.machine.installedBinary()), len(rig.payload))
	}

	if rig.machine.tempFiles() != 0 {
		t.Errorf("%d temporary file(s) remain on the VM", rig.machine.tempFiles())
	}

	assertCleanupFor(t, onlyCleanup(t, rig), user)

	if !strings.Contains(rig.out.String(), "Restarted blacksmith") {
		t.Errorf("the output does not say blacksmith restarted:\n%s", rig.out.String())
	}
}

// A copy that fails still removes the temporary file and the user.
func TestDevpushCleansUpAfterAFailedCopy(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)
	rig.machine.failInstall = true

	err := rig.pusher.push(t.Context())
	if err == nil {
		t.Fatal("push succeeded, but the install failed")
	}

	for _, want := range []string{"install", rig.machine.port, testDeployment, "No space left on device"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	user := rig.setupUser(t)
	temp := "/tmp/blacksmith." + user

	commands := rig.machine.ran()
	if last := commands[len(commands)-1]; last != "rm -f "+temp {
		t.Errorf("the last remote command is %q, want the removal of the temporary file", last)
	}

	if slices.Contains(commands, monitRestart) {
		t.Error("blacksmith was restarted after a failed install")
	}

	if rig.machine.tempFiles() != 0 {
		t.Errorf("%d temporary file(s) remain on the VM", rig.machine.tempFiles())
	}

	assertCleanupFor(t, onlyCleanup(t, rig), user)
}

// A setup that is cut short is cancelled, awaited, and only then cleaned
// up. A setup whose answer is lost is cleaned up once no setup is running.
func TestDevpushCleansUpAfterAnInterruptedSetup(t *testing.T) {
	t.Parallel()

	t.Run("interrupt", testInterruptedSetup)
	t.Run("bound passes", testSetupBoundPasses)
	t.Run("lost answer while a setup runs", testLostAnswerWhileSetupRuns)
	t.Run("lost answer once the setup ended", testLostAnswerAfterSetupEnded)
}

// processingSetup starts every setup in the processing state and answers
// everything else with the defaults.
func processingSetup(call boshtest.SSHCall) boshtest.SSHAnswer {
	if call.Command == boshtest.SSHSetup {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	}

	return boshtest.SSHAnswer{}
}

// lostProcessingSetup is processingSetup with the setup's answer dropped.
func lostProcessingSetup(call boshtest.SSHCall) boshtest.SSHAnswer {
	if call.Command == boshtest.SSHSetup {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing, Drop: true}
	}

	return boshtest.SSHAnswer{}
}

func testInterruptedSetup(t *testing.T) {
	t.Parallel()

	rig := newRig(t, processingSetup)
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	done := rig.pushWithin(ctx)

	var taskID int

	waitFor(t, "the first read of the setup task", func() bool {
		calls := rig.director.CallsOf(boshtest.SSHSetup)
		if len(calls) == 0 {
			return false
		}

		taskID = calls[0].TaskID

		return rig.director.Reads(taskID) > 0
	})
	cancel()

	waitFor(t, "the cancel of the setup task", func() bool { return len(rig.director.Cancels()) > 0 })

	time.Sleep(quietPeriod)

	if cleanups := rig.director.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Fatalf("a cleanup was sent while the setup task was still cancelling: %+v", cleanups)
	}

	rig.director.SetState(taskID, boshtest.TaskCancelled)

	err := result(t, done)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("push returned %v, want an error that wraps context.Canceled", err)
	}

	if cancels := rig.director.Cancels(); len(cancels) != 1 || cancels[0].TaskID != taskID || cancels[0].State != boshtest.TaskProcessing {
		t.Errorf("cancels = %+v, want one cancel of task %d while it was processing", cancels, taskID)
	}

	cleanup := onlyCleanup(t, rig)
	assertCleanupFor(t, cleanup, rig.setupUser(t))

	task, _ := rig.director.Task(taskID)
	if cleanup.At.Before(task.EndedAt) {
		t.Errorf("the cleanup was sent at %v, before the setup task ended at %v", cleanup.At, task.EndedAt)
	}

	if len(rig.machine.ran()) != 0 {
		t.Errorf("the tool ran %q on the VM after its setup was cancelled", rig.machine.ran())
	}
}

func testSetupBoundPasses(t *testing.T) {
	t.Parallel()

	rig := newRig(t, processingSetup)
	rig.pusher.setupBound = quietPeriod / 2

	done := rig.pushWithin(t.Context())

	waitFor(t, "the cancel of the setup task", func() bool { return len(rig.director.Cancels()) > 0 })

	if cleanups := rig.director.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Fatalf("a cleanup was sent while the setup task was still cancelling: %+v", cleanups)
	}

	taskID := rig.director.CallsOf(boshtest.SSHSetup)[0].TaskID

	rig.director.SetState(taskID, boshtest.TaskCancelled)

	err := result(t, done)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("push returned %v, want an error that wraps context.DeadlineExceeded", err)
	}

	assertCleanupFor(t, onlyCleanup(t, rig), rig.setupUser(t))
}

func testLostAnswerWhileSetupRuns(t *testing.T) {
	t.Parallel()

	rig := newRig(t, lostProcessingSetup)
	rig.pusher.settleBound = shortSettle

	err := rig.pusher.push(t.Context())
	if err == nil {
		t.Fatal("push succeeded without an answer to its setup")
	}

	if cleanups := rig.director.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Fatalf("a cleanup was sent while a setup was still listed as running: %+v", cleanups)
	}

	user := rig.setupUser(t)
	for _, want := range []string{user, "blacksmith/" + boshtest.DefaultSSHInstanceID, testDeployment} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	if len(rig.director.Listings()) == 0 {
		t.Error("the tool never listed the deployment's running tasks")
	}
}

func testLostAnswerAfterSetupEnded(t *testing.T) {
	t.Parallel()

	rig := newRig(t, lostProcessingSetup)
	done := rig.pushWithin(t.Context())

	waitFor(t, "a listing of running tasks", func() bool { return len(rig.director.Listings()) > 0 })
	time.Sleep(quietPeriod)

	if cleanups := rig.director.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Fatalf("a cleanup was sent while a setup was still running: %+v", cleanups)
	}

	rig.director.SetState(rig.director.CallsOf(boshtest.SSHSetup)[0].TaskID, boshtest.TaskDone)

	err := result(t, done)
	if !errors.Is(err, errSetupLost) {
		t.Errorf("push returned %v, want an error that wraps errSetupLost", err)
	}

	assertCleanupFor(t, onlyCleanup(t, rig), rig.setupUser(t))
}

// A refused setup created no user, so it needs no cleanup.
func TestDevpushSendsNoCleanupAfterARefusedSetup(t *testing.T) {
	t.Parallel()

	rig := newRig(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{Status: 403, Code: 600000, Description: "Not authorized"}
	})

	err := rig.pusher.push(t.Context())
	if !errors.Is(err, errSetupRefused) || !strings.Contains(err.Error(), "bosh.admin") {
		t.Errorf("push returned %v, want a refusal that says to give the client bosh.admin", err)
	}

	if cleanups := rig.director.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Errorf("a cleanup was sent for a setup the director refused: %+v", cleanups)
	}
}

// A setup result the tool cannot use, such as one that names a gateway,
// still gets its user removed, because the setup task created it.
func TestDevpushCleansUpAfterAnUnusableSetupResult(t *testing.T) {
	t.Parallel()

	rig := newRig(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		entry := boshtest.SSHSuccess("ssh-ed25519 AAAA")
		entry.GatewayHost = "gateway.example.com"

		return boshtest.SSHAnswer{Output: boshtest.SSHSetupOutput(entry)}
	})

	err := rig.pusher.push(t.Context())
	if !errors.Is(err, sshconn.ErrGatewayUnsupported) {
		t.Errorf("push returned %v, want an error that wraps ErrGatewayUnsupported", err)
	}

	assertCleanupFor(t, onlyCleanup(t, rig), rig.setupUser(t))

	if len(rig.machine.ran()) != 0 {
		t.Errorf("the tool ran %q on a VM behind a gateway", rig.machine.ran())
	}
}

// panicWriter is the tool's output with a fault in it. It panics once, on
// the first line that holds trigger.
type panicWriter struct {
	out     *syncBuffer
	trigger string
	fired   sync.Once
}

func (w *panicWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.trigger) {
		w.fired.Do(func() { panic(errPanicWriter) })
	}

	return w.out.Write(p) //nolint:wrapcheck // a bytes.Buffer write never fails
}

var errPanicWriter = errors.New("the output panicked")

// A panic after the setup still removes the user, and the panic goes on.
func TestDevpushCleansUpAfterAPanic(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)
	rig.pusher.out = &panicWriter{out: rig.out, trigger: "Uploading"}

	var recovered any

	func() {
		defer func() { recovered = recover() }()

		_ = rig.pusher.push(t.Context())
	}()

	if !errors.Is(asError(recovered), errPanicWriter) {
		t.Fatalf("push recovered %v, want the panic to go on", recovered)
	}

	assertCleanupFor(t, onlyCleanup(t, rig), rig.setupUser(t))

	if !strings.Contains(rig.out.String(), "panicked") {
		t.Errorf("the output does not say the push panicked:\n%s", rig.out.String())
	}
}

// asError returns what a recover gave as an error, or nil.
func asError(recovered any) error {
	err, _ := recovered.(error)

	return err
}

// An upload that fails removes the partial file and the user, and installs
// nothing.
func TestDevpushCleansUpAfterAFailedUpload(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)
	rig.machine.failUpload = true

	err := rig.pusher.push(t.Context())
	if !errors.Is(err, errStepFailed) {
		t.Fatalf("push returned %v, want errStepFailed", err)
	}

	for _, want := range []string{"upload", rig.machine.port, testDeployment, "No space left on device"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	user := rig.setupUser(t)
	temp := "/tmp/blacksmith." + user

	want := []string{uploadPrefix + temp, "rm -f " + temp}
	if got := rig.machine.ran(); !slices.Equal(got, want) {
		t.Errorf("remote commands:\n got %q\nwant %q", got, want)
	}

	if rig.machine.tempFiles() != 0 {
		t.Errorf("%d temporary file(s) remain on the VM", rig.machine.tempFiles())
	}

	assertCleanupFor(t, onlyCleanup(t, rig), user)
}

// An interrupt during the upload removes the partial file and the user.
func TestDevpushCleansUpAfterAnInterruptedUpload(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)
	rig.machine.hold, rig.machine.reached = make(chan struct{}), make(chan struct{})

	t.Cleanup(func() { close(rig.machine.hold) })

	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	done := rig.pushWithin(ctx)

	select {
	case <-rig.machine.reached:
	case <-time.After(waitLimit):
		t.Fatal("the upload never reached the VM")
	}

	cancel()

	err := result(t, done)
	if !errors.Is(err, errStepFailed) || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("push returned %v, want a failed upload step that says it was stopped", err)
	}

	user := rig.setupUser(t)

	if commands := rig.machine.ran(); commands[len(commands)-1] != "rm -f /tmp/blacksmith."+user {
		t.Errorf("the last remote command is %q, want the removal of the temporary file", commands[len(commands)-1])
	}

	if rig.machine.tempFiles() != 0 {
		t.Errorf("%d temporary file(s) remain on the VM", rig.machine.tempFiles())
	}

	assertCleanupFor(t, onlyCleanup(t, rig), user)
}

// A cleanup task that fails is reported with the user, the instance, and
// the task ID, because the user may remain on the VM.
func TestDevpushReportsAFailedCleanup(t *testing.T) {
	t.Parallel()

	rig := newRig(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup {
			return boshtest.SSHAnswer{State: boshtest.TaskError, Result: "the agent did not answer"}
		}

		return boshtest.SSHAnswer{}
	})

	err := rig.pusher.push(t.Context())
	if !errors.Is(err, errCleanupFailed) {
		t.Fatalf("push returned %v, want errCleanupFailed", err)
	}

	cleanup := onlyCleanup(t, rig)
	taskID := strconv.Itoa(cleanup.TaskID)

	user := rig.setupUser(t)

	for _, want := range []string{
		"BOSH task " + taskID + ", which removes SSH user " + user + " from " + testInstance + " of deployment " + testDeployment,
		"the agent did not answer",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}

	if !bytes.Equal(rig.machine.installedBinary(), rig.payload) {
		t.Error("the binary was not installed, but only the cleanup should have failed")
	}
}

// A one-word director address is read as an alias, and the error says so
// and says what to give instead.
func TestDevpushExplainsAOneWordDirectorAddress(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{aliasName, "bosh", "https://bosh:25555", "localhost", "https://localhost:25555"} {
		_, err := directorURL(raw)
		if !errors.Is(err, errEnvironment) {
			t.Errorf("directorURL(%q) = %v, want errEnvironment", raw, err)

			continue
		}

		for _, want := range []string{envDirector, raw, "one-word", "bosh CLI environment alias", "IP address", "fully qualified name", "127.0.0.1"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("directorURL(%q): the error does not mention %q: %v", raw, want, err)
			}
		}
	}
}

// The setup names the instance by its ID, and a numeric ID is refused
// before the tool sends anything.
func TestDevpushTargetsTheInstanceByID(t *testing.T) {
	t.Parallel()

	rig := newRig(t, nil)

	err := rig.pusher.push(t.Context())
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	setup := rig.director.CallsOf(boshtest.SSHSetup)[0]
	target, _ := setup.Fields["target"].(map[string]any)
	ids, _ := target["ids"].([]any)

	if target["job"] != testGroup || len(ids) != 1 || ids[0] != boshtest.DefaultSSHInstanceID {
		t.Errorf("the setup target is %v, want %s with ids [%s]", target, testGroup, boshtest.DefaultSSHInstanceID)
	}

	if _, found := target["indexes"]; found || strings.Contains(setup.Body, "indexes") {
		t.Errorf("the setup body names indexes: %s", setup.Body)
	}

	for _, value := range []string{"blacksmith/", "/" + boshtest.DefaultSSHInstanceID, "blacksmith", "blacksmith/z/" + boshtest.DefaultSSHInstanceID} {
		other := boshtest.New(t, nil)

		err := run(t.Context(), []string{"-d", testDeployment, "-i", value, flagBinary, rig.binary}, directorEnv(other, nil), io.Discard, io.Discard)
		if !errors.Is(err, errInstance) {
			t.Errorf("-i %q: run returned %v, want errInstance", value, err)
		}

		if requests := other.Requests(); len(requests) != 0 {
			t.Errorf("-i %q: the tool sent %+v", value, requests)
		}
	}

	numeric := boshtest.New(t, nil)

	err = run(t.Context(), []string{"-d", testDeployment, "-i", "blacksmith/0", flagBinary, rig.binary}, directorEnv(numeric, nil), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "/b/deployments/<name>/instances") {
		t.Errorf("a numeric ID got %v, want the advice to use the ID from /b/deployments/<name>/instances", err)
	}

	if requests := numeric.Requests(); len(requests) != 0 {
		t.Errorf("a numeric ID was refused only after the tool sent %+v", requests)
	}
}

// directorEnv answers getenv with the director's settings, overridden by
// changes. A change to the empty string unsets the variable.
func directorEnv(director *boshtest.Director, changes map[string]string) func(string) string {
	values := map[string]string{
		envDirector: director.URL,
		envCACert:   director.CACert,
		envClient:   "admin",
		envSecret:   "secret",
	}

	for name, value := range changes {
		values[name] = value
	}

	return func(name string) string { return values[name] }
}

// A binary built for another system is refused before the tool sends
// anything to the director.
func TestDevpushRefusesANonLinuxBinary(t *testing.T) {
	t.Parallel()

	machO := slices.Concat([]byte{0xcf, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x01}, make([]byte, 64))
	cases := map[string]struct {
		data []byte
		want []string
	}{
		"mach-o":                {data: machO, want: []string{"Mach-O", adviceLinux}},
		"linux arm64":           {data: elfFile(elf.EM_AARCH64, elf.ELFOSABI_NONE, "body"), want: []string{"linux arm64", adviceLinux}},
		"freebsd x86-64":        {data: elfFile(elf.EM_X86_64, elf.ELFOSABI_FREEBSD, "body"), want: []string{"freebsd x86-64", adviceLinux}},
		"script":                {data: []byte("#!/bin/sh\necho hi\n"), want: []string{"script", adviceLinux}},
		"empty":                 {data: nil, want: []string{"only 0 bytes", adviceLinux}},
		"damaged elf":           {data: []byte("\x7fELF123"), want: []string{"does not parse", adviceLinux}},
		"windows executable":    {data: []byte("MZ\x90\x00\x03\x00"), want: []string{"Windows", adviceLinux}},
		"something else":        {data: []byte("hello, world"), want: []string{"not an ELF", adviceLinux}},
		"linux x86-64 accepted": {data: elfFile(elf.EM_X86_64, elf.ELFOSABI_LINUX, "body")},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			director := boshtest.New(t, nil)
			path := writeFile(t, "blacksmith-test", testCase.data)

			err := run(t.Context(), []string{"-d", testDeployment, "-i", testInstance, flagBinary, path}, directorEnv(director, nil), io.Discard, io.Discard)

			if testCase.want == nil {
				if errors.Is(err, errBinary) {
					t.Errorf("a linux x86-64 executable was refused: %v", err)
				}

				return
			}

			if !errors.Is(err, errBinary) {
				t.Fatalf("run returned %v, want errBinary", err)
			}

			for _, want := range append([]string{path}, testCase.want...) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not mention %q: %v", want, err)
				}
			}

			if requests := director.Requests(); len(requests) != 0 {
				t.Errorf("the tool sent %+v before it refused the binary", requests)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()

		director := boshtest.New(t, nil)
		path := filepath.Join(t.TempDir(), "absent")

		err := run(t.Context(), []string{"-d", testDeployment, "-i", testInstance, flagBinary, path}, directorEnv(director, nil), io.Discard, io.Discard)
		if !errors.Is(err, errBinary) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), adviceLinux) {
			t.Errorf("run returned %v, want errBinary that names %s and make linux", err, path)
		}
	})
}

// A missing variable, an alias for BOSH_ENVIRONMENT, and a BOSH_ALL_PROXY
// each stop the tool at once with a message that names the variable.
func TestDevpushNamesTheMissingVariable(t *testing.T) {
	t.Parallel()

	director := boshtest.New(t, nil)
	binary := writeFile(t, "blacksmith-linux-amd64", elfFile(elf.EM_X86_64, elf.ELFOSABI_NONE, "body"))

	cases := map[string]struct {
		changes map[string]string
		want    []string
	}{
		"no director":     {changes: map[string]string{envDirector: ""}, want: []string{envDirector, adviceNotSet}},
		"no CA":           {changes: map[string]string{envCACert: ""}, want: []string{envCACert, adviceNotSet}},
		"no client":       {changes: map[string]string{envClient: ""}, want: []string{envClient, adviceNotSet}},
		"no secret":       {changes: map[string]string{envSecret: ""}, want: []string{envSecret, adviceNotSet}},
		"two missing":     {changes: map[string]string{envClient: "", envSecret: ""}, want: []string{envClient + ", " + envSecret, "are not set"}},
		"cli alias":       {changes: map[string]string{envDirector: aliasName}, want: []string{envDirector, aliasName, adviceAddress}},
		"spaces":          {changes: map[string]string{envDirector: "my director"}, want: []string{envDirector, adviceAddress}},
		"other scheme":    {changes: map[string]string{envDirector: "ssh://10.0.0.6"}, want: []string{envDirector, adviceAddress}},
		"all proxy":       {changes: map[string]string{envProxy: "ssh+socks5://jump@bastion:22?private-key=/k"}, want: []string{envProxy, "direct access"}},
		"proxy and alias": {changes: map[string]string{envProxy: "socks5://127.0.0.1:1080", envDirector: "lab"}, want: []string{envProxy, "direct access"}},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := run(t.Context(), []string{"-d", testDeployment, "-i", testInstance, flagBinary, binary}, directorEnv(director, testCase.changes), io.Discard, io.Discard)
			if !errors.Is(err, errEnvironment) {
				t.Fatalf("run returned %v, want errEnvironment", err)
			}

			for _, want := range testCase.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not mention %q: %v", want, err)
				}
			}

			if strings.Contains(err.Error(), "jump@bastion") {
				t.Errorf("the error repeats the proxy setting, which may hold a credential: %v", err)
			}

			if requests := director.Requests(); len(requests) != 0 {
				t.Errorf("the tool sent %+v before it checked its settings", requests)
			}
		})
	}
}

// BOSH_ENVIRONMENT gets the scheme and the port that a director URL needs.
func TestDevpushCompletesTheDirectorAddress(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"10.0.0.6":                        urlIP,
		"10.0.0.6:25555":                  urlIP,
		"https://10.0.0.6":                urlIP,
		"https://10.0.0.6:8443":           "https://10.0.0.6:8443",
		"https://bosh.example.com":        urlName,
		"bosh.example.com:25555":          urlName,
		"https://bosh.example.com:25555/": urlName,
		"https://[fd00::6]":               "https://[fd00::6]:25555",
	}

	for raw, want := range cases {
		got, err := directorURL(raw)
		if err != nil || got != want {
			t.Errorf("directorURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}
