package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/broker"
	internalCF "blacksmith/internal/cf"
	"blacksmith/internal/config"
	"blacksmith/internal/upgrade"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

const (
	// shutdownDeployTaskID is the task the test director starts for the
	// upgrade's deploy.
	shutdownDeployTaskID = 7
	// shutdownInstanceID is the instance the upgrade deploys.
	shutdownInstanceID = "shutdown-instance-1"
	// shutdownDeployment is the deployment of shutdownInstanceID.
	shutdownDeployment = "redis-" + shutdownInstanceID
	// shutdownWait bounds each wait in these tests.
	shutdownWait = 5 * time.Second
)

// shutdownStepNames names the steps shutdown runs, in order.
//
//nolint:gochecknoglobals // a fixed list the tests read
var shutdownStepNames = []string{
	"stop the HTTP servers",
	"send the SSH cleanups",
	"cancel the service context",
	"stop the reconciler",
	"stop the VM monitor",
	"stop the upgrade manager",
	"wait for the broker's background work",
	"stop the batch director",
	"stop the director pool",
}

// shutdownLog records every message shutdown logs.
type shutdownLog struct {
	*loggerPkg.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newShutdownLog() *shutdownLog { return &shutdownLog{NoOpLogger: &loggerPkg.NoOpLogger{}} }

func (l *shutdownLog) Info(msg string, args ...interface{})    { l.record(msg, args) }
func (l *shutdownLog) Warn(msg string, args ...interface{})    { l.record(msg, args) }
func (l *shutdownLog) Warning(msg string, args ...interface{}) { l.record(msg, args) }
func (l *shutdownLog) Error(msg string, args ...interface{})   { l.record(msg, args) }

func (l *shutdownLog) Named(string) loggerPkg.Logger { return l }

func (l *shutdownLog) record(msg string, args []interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}

func (l *shutdownLog) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// stepLog records the order in which shutdown runs its steps. Each fake
// step records its name, and then waits on hang when it is set.
type stepLog struct {
	mu    sync.Mutex
	steps []string
	hang  chan struct{}
}

func (s *stepLog) ran(step string) {
	s.mu.Lock()
	s.steps = append(s.steps, step)
	hang := s.hang
	s.mu.Unlock()

	if hang != nil {
		<-hang
	}
}

func (s *stepLog) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.steps...)
}

type fakeServer struct{ log *stepLog }

func (f fakeServer) Shutdown(context.Context) error {
	f.log.ran("servers")

	return nil
}

type fakeStopper struct {
	log  *stepLog
	name string
}

func (f fakeStopper) Stop() { f.log.ran(f.name) }

type fakeFailingStopper struct{ log *stepLog }

func (f fakeFailingStopper) Stop() error {
	f.log.ran("reconciler")

	return nil
}

// sshShutdownFunc is an SSH shutdown made of a function.
type sshShutdownFunc func(ctx context.Context)

func (f sshShutdownFunc) ShutdownSSH(ctx context.Context) { f(ctx) }

type fakeBackground struct{ log *stepLog }

func (f fakeBackground) WaitForBackground(context.Context) { f.log.ran("background") }

// fakeShutdownDeps returns shutdown deps whose every step records itself
// in log.
func fakeShutdownDeps(log *stepLog, logger loggerPkg.Logger) shutdownDeps {
	return shutdownDeps{
		servers:    []httpServer{fakeServer{log: log}},
		ssh:        sshShutdownFunc(func(context.Context) { log.ran("ssh") }),
		cancel:     func(error) { log.ran("cancel") },
		reconciler: fakeFailingStopper{log: log},
		monitor:    fakeStopper{log: log, name: "monitor"},
		upgrades:   fakeStopper{log: log, name: "upgrades"},
		background: fakeBackground{log: log},
		batch:      fakeStopper{log: log, name: "batch"},
		pool:       fakeStopper{log: log, name: "pool"},
		logger:     logger,
	}
}

// shortenShutdown sets the shutdown deadlines for the test and restores
// them when it ends.
func shortenShutdown(t *testing.T, overall, servers time.Duration) {
	t.Helper()

	previousOverall, previousServers := shutdownTimeout, serverShutdownTimeout
	shutdownTimeout, serverShutdownTimeout = overall, servers

	t.Cleanup(func() { shutdownTimeout, serverShutdownTimeout = previousOverall, previousServers })
}

//nolint:paralleltest // the test reads the package's shutdown deadlines
func TestShutdownRunsItsStepsInOrder(t *testing.T) {
	log := &stepLog{}

	shutdown(fakeShutdownDeps(log, newShutdownLog()))

	want := []string{"servers", "ssh", "cancel", "reconciler", "monitor", "upgrades", "background", "batch", "pool"}
	if got := log.order(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected the shutdown steps in the order %v, got %v", want, got)
	}
}

// Shutdown sends the SSH cleanups right after the servers stop, so no
// request can open a session after them, and before anything else stops,
// so the director is still there to take them. The step gets its own
// budget out of the shutdown's deadline. The pooled director is what
// shutdown hands the step to.
//
//nolint:paralleltest // the test reads the package's shutdown deadlines
func TestShutdownSendsTheSSHCleanupsRightAfterTheServers(t *testing.T) {
	log := &stepLog{}

	var (
		deadline    time.Time
		hasDeadline bool
	)

	deps := fakeShutdownDeps(log, newShutdownLog())
	deps.ssh = sshShutdownFunc(func(ctx context.Context) {
		deadline, hasDeadline = ctx.Deadline()

		log.ran("ssh")
	})

	began := time.Now()

	shutdown(deps)

	ended := time.Now()

	if got := log.order(); len(got) < 3 || got[0] != "servers" || got[1] != "ssh" || got[2] != "cancel" {
		t.Fatalf("expected the SSH cleanups right after the servers and before the service context is cancelled, got %v", got)
	}

	if !hasDeadline {
		t.Fatal("the SSH cleanups ran with no deadline")
	}

	// The step sets its deadline between began and ended, so the deadline
	// lies within that window shifted by the budget.
	if deadline.Before(began.Add(sshShutdownBudget)) || deadline.After(ended.Add(sshShutdownBudget)) {
		t.Errorf("the SSH cleanups got a deadline %v after the shutdown began, want their budget of %v",
			deadline.Sub(began), sshShutdownBudget)
	}

	pool := bosh.NewPooledDirector(nil, 1, time.Second, nil)

	wired := shutdownDepsFor(nil, nil, nil, nil, nil, nil, nil, pool, newShutdownLog())
	if wired.ssh != pool {
		t.Errorf("expected shutdown to send the SSH cleanups through the pooled director, got %#v", wired.ssh)
	}

	if unwired := shutdownDepsFor(nil, nil, nil, nil, nil, nil, nil, nil, newShutdownLog()); unwired.ssh != nil {
		t.Errorf("expected no SSH cleanups without a director, got %#v", unwired.ssh)
	}
}

// Shutdown cancels the service context with ErrBrokerShuttingDown, so a
// wait for a read-only director task can tell a shutdown from a deadline
// or a request's cancel. The plain cancel the deferred cleanup holds ends
// the context with no cause of its own.
//
//nolint:paralleltest // the test reads the package's shutdown deadlines
func TestShutdownCancelsTheServiceContextWithTheShutdownCause(t *testing.T) {
	var causes []error

	deps := fakeShutdownDeps(&stepLog{}, newShutdownLog())
	deps.cancel = func(cause error) { causes = append(causes, cause) }

	shutdown(deps)

	if len(causes) != 1 || !errors.Is(causes[0], bosh.ErrBrokerShuttingDown) {
		t.Fatalf("expected the service context to be cancelled once with ErrBrokerShuttingDown, got %v", causes)
	}

	ctx, cancel, cancelWithCause, sigChan := setupSignalHandlingAndContext()
	defer signal.Stop(sigChan)
	defer cancelWithCause(nil)

	cancel()

	cause := context.Cause(ctx)
	if !errors.Is(cause, context.Canceled) || errors.Is(cause, bosh.ErrBrokerShuttingDown) {
		t.Errorf("expected the plain cancel to leave context.Canceled as the cause, got %v", cause)
	}

	shutdownCtx, _, shutdownCancel, shutdownSigChan := setupSignalHandlingAndContext()
	defer signal.Stop(shutdownSigChan)

	shutdownCancel(bosh.ErrBrokerShuttingDown)

	cause = context.Cause(shutdownCtx)
	if !errors.Is(cause, bosh.ErrBrokerShuttingDown) {
		t.Errorf("expected the cancel with a cause to set ErrBrokerShuttingDown, got %v", cause)
	}
}

// A server that fails shuts the broker down, so it ends the service context
// with ErrBrokerShuttingDown, as a signal does, and the cause keeps the
// server's own error. The address names no port, so neither server ever
// listens.
func TestAFailedServerCancelsTheServiceContextWithTheShutdownCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start func(*sync.WaitGroup, *http.Server, context.CancelCauseFunc, loggerPkg.Logger)
	}{
		{name: "HTTP", start: startHTTPServerGoroutine},
		{name: "HTTPS", start: startHTTPSServerGoroutine},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)

			var waitGroup sync.WaitGroup

			testCase.start(&waitGroup, &http.Server{Addr: "127.0.0.1:-1", ReadHeaderTimeout: time.Second}, cancel, newShutdownLog())
			waitGroup.Wait()

			cause := context.Cause(ctx)
			if !errors.Is(cause, bosh.ErrBrokerShuttingDown) {
				t.Fatalf("expected the failed server to cancel the service context with ErrBrokerShuttingDown, got %v", cause)
			}

			if !strings.Contains(cause.Error(), "server failed") {
				t.Errorf("expected the cause to say the server failed, got %v", cause)
			}
		})
	}
}

//nolint:paralleltest // the test sets the package's shutdown deadlines
func TestShutdownFinishesWithin15Seconds(t *testing.T) {
	shortenShutdown(t, 300*time.Millisecond, 100*time.Millisecond)

	log := &stepLog{hang: make(chan struct{})}

	t.Cleanup(func() { close(log.hang) })

	logger := newShutdownLog()

	started := time.Now()

	shutdown(fakeShutdownDeps(log, logger))

	elapsed := time.Since(started)
	if elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("expected shutdown to return at its deadline of 300ms, it took %v", elapsed)
	}

	for _, step := range shutdownStepNames {
		if !strings.Contains(logger.output(), fmt.Sprintf("Shutdown step %q overran", step)) {
			t.Errorf("expected the hung step %q to be logged as overrunning, got:\n%s", step, logger.output())
		}
	}
}

//nolint:paralleltest // the test reads the package's shutdown deadlines
func TestShutdownWaitsForAnInFlightRequest(t *testing.T) {
	started := make(chan struct{})

	server := &http.Server{
		ReadHeaderTimeout: shutdownWait,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			close(started)
			time.Sleep(2 * time.Second)

			_, _ = io.WriteString(writer, "finished")
		}),
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}

	go func() { _ = server.Serve(listener) }()

	type answer struct {
		body string
		err  error
	}

	answered := make(chan answer, 1)

	go func() {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+listener.Addr().String()+"/", nil)
		if err != nil {
			answered <- answer{err: err}

			return
		}

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			answered <- answer{err: err}

			return
		}
		defer func() { _ = response.Body.Close() }()

		body, err := io.ReadAll(response.Body)
		answered <- answer{body: string(body), err: err}
	}()

	select {
	case <-started:
	case <-time.After(shutdownWait):
		t.Fatal("the request never reached the handler")
	}

	shutdown(shutdownDeps{servers: []httpServer{server}, cancel: func(error) {}, logger: newShutdownLog()})

	// The broker exits once shutdown returns, so the request has to be
	// answered by then. The short grace covers only the client's read.
	select {
	case got := <-answered:
		if got.err != nil || got.body != "finished" {
			t.Fatalf("expected the request in flight to finish, got %q and %v", got.body, got.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown returned before the request in flight was answered")
	}
}

// heldDeployDirector starts a test director that serves the deployment
// the upgrade reads, accepts its deploy as task 7, and reports task 7
// processing for as long as the test runs.
func heldDeployDirector(t *testing.T) (*boshtest.Director, chan struct{}) {
	t.Helper()

	polled := make(chan struct{})

	var pollOnce sync.Once

	director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/deployments/"+shutdownDeployment:
			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
				"name":      shutdownDeployment,
				"manifest":  "name: " + shutdownDeployment + "\nstemcells:\n- alias: default\n  os: ubuntu-noble\n  version: \"1.1\"\n",
				"releases":  []string{},
				"stemcells": []string{},
				"teams":     []string{},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/deployments":
			writer.Header().Set("Location", fmt.Sprintf("/tasks/%d", shutdownDeployTaskID))
			writer.WriteHeader(http.StatusFound)
		case request.Method == http.MethodGet && request.URL.Path == fmt.Sprintf("/tasks/%d", shutdownDeployTaskID):
			pollOnce.Do(func() { close(polled) })

			boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
				"id":          shutdownDeployTaskID,
				"state":       "processing",
				"description": "create deployment",
				"timestamp":   1759766400,
				"started_at":  1759766000,
				"result":      "",
				"user":        "admin",
				"deployment":  shutdownDeployment,
				"context_id":  "",
			})
		default:
			return false
		}

		return true
	})

	return director, polled
}

// bootConfig returns the config the boot path reads, pointed at the test
// director when one is given.
func bootConfig(director *boshtest.Director) *config.Config {
	cfg := &config.Config{}

	if director != nil {
		cfg.BOSH.Address = director.URL
		cfg.BOSH.Username = bootCredential
		cfg.BOSH.Password = bootCredential
		cfg.BOSH.CACert = director.CACert
		cfg.BOSH.MaxConnections = 2
		cfg.BOSH.MaxBatchConnections = 2
		cfg.BOSH.ConnectionTimeout = 5
		cfg.BOSH.RequestTimeout = 5
	}

	return cfg
}

// startShutdownVault starts the in-process vault and stores the instance
// the upgrade deploys.
func startShutdownVault(t *testing.T) *internalVault.Vault {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	vault := internalVault.New(server.Addr, server.RootToken, true)

	err = vault.Put(t.Context(), shutdownInstanceID, map[string]interface{}{"deployment_name": shutdownDeployment})
	if err != nil {
		t.Fatalf("storing the instance: %v", err)
	}

	return vault
}

// bootAPI builds the API and the shutdown deps the way runService does.
func bootAPI(t *testing.T, cfg *config.Config, vault *internalVault.Vault, logger loggerPkg.Logger) (*broker.API, shutdownDeps) {
	t.Helper()

	t.Setenv("BOSH_CLIENT", "")
	t.Setenv("BOSH_CLIENT_SECRET", "")

	boshDirector, batchDirector := initializeBOSH(cfg, logger)

	brokerInstance := &broker.Broker{Vault: vault, Config: cfg}

	// The health status reads the CF manager whenever its interface is not
	// nil, and a nil *cf.Manager would fail that read, so the API gets a
	// manager with no endpoints.
	cfManager := internalCF.NewManagerFromExternal(nil, logger)

	apiHandler, upgradeHandler := createAPIHandler(cfg, brokerInstance, vault, boshDirector, batchDirector, cfManager, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, logger)

	_, cancel := context.WithCancelCause(context.Background())

	return apiHandler, shutdownDepsFor(nil, cancel, nil, nil, upgradeHandler, brokerInstance, batchDirector, boshDirector, logger)
}

//nolint:paralleltest // the boot path sets the BOSH client environment
func TestShutdownStopsTheUpgradeManagerWiredAtBoot(t *testing.T) {
	director, polled := heldDeployDirector(t)
	vault := startShutdownVault(t)
	logger := newShutdownLog()

	_, deps := bootAPI(t, bootConfig(director), vault, logger)

	handler, ok := deps.upgrades.(interface{ GetManager() *upgrade.Manager })
	if !ok || handler == nil {
		t.Fatalf("the upgrade handler wired at boot never reached shutdown, got %#v", deps.upgrades)
	}

	manager := handler.GetManager()

	task, err := manager.CreateTask(t.Context(), upgrade.CreateTaskRequest{
		InstanceIDs:    []string{shutdownInstanceID},
		TargetStemcell: upgrade.StemcellTarget{OS: "ubuntu-noble", Version: "1.2"},
	})
	if err != nil {
		t.Fatalf("creating the upgrade: %v", err)
	}

	select {
	case <-polled:
	case <-time.After(shutdownWait):
		t.Fatal("the upgrade's deploy never reached the director")
	}

	shutdown(deps)

	stopped, err := manager.GetTask(task.ID)
	if err != nil {
		t.Fatalf("reading the upgrade: %v", err)
	}

	instance := stopped.Instances[0]
	if instance.Status != upgrade.InstanceStatusInterrupted || instance.BOSHTaskID != shutdownDeployTaskID {
		t.Fatalf("expected the instance to be interrupted with BOSH task %d, got %+v\nlog:\n%s", shutdownDeployTaskID, instance, logger.output())
	}
}

//nolint:paralleltest // the boot path sets the BOSH client environment
func TestBootAndShutdownWithoutABOSHDirector(t *testing.T) {
	vault := startShutdownVault(t)
	logger := newShutdownLog()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("booting or shutting down without a BOSH director panicked: %v", r)
		}
	}()

	apiHandler, deps := bootAPI(t, bootConfig(nil), vault, logger)

	// The API reports BOSH available whenever its director is not nil, so a
	// nil pointer handed over as an interface shows up here.
	recorder := httptest.NewRecorder()
	apiHandler.Internal.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/internal/health", nil))

	var health struct {
		BOSH struct {
			Available bool `json:"available"`
		} `json:"bosh"`
	}

	err := json.Unmarshal(recorder.Body.Bytes(), &health)
	if err != nil {
		t.Fatalf("reading the health status %q: %v", recorder.Body.String(), err)
	}

	if health.BOSH.Available {
		t.Fatalf("expected the API to have no BOSH director, got %s", recorder.Body.String())
	}

	if deps.batch != nil || deps.pool != nil || deps.ssh != nil {
		t.Fatalf("expected no director to stop, got batch %#v, pool %#v, and SSH %#v", deps.batch, deps.pool, deps.ssh)
	}

	shutdown(deps)
}
