package bosh_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/pkg/logger"
)

const (
	healthTimeout = 50 * time.Millisecond
	healthWindow  = 10 * time.Second
	gateDeadline  = 30 * time.Millisecond
	secretText    = "https://admin:hunter2@director.invalid:25555"
)

// infoDirector is a director whose info call answers as scripted, and which
// counts the calls. Any other call panics through the nil embedded Director.
type infoDirector struct {
	bosh.Director

	calls atomic.Int32
	err   error
	block bool
}

func (d *infoDirector) GetInfo(ctx context.Context) (*bosh.Info, error) {
	d.calls.Add(1)

	if d.block {
		<-ctx.Done()

		return nil, fmt.Errorf("asking the director: %w", ctx.Err())
	}

	if d.err != nil {
		return nil, d.err
	}

	return &bosh.Info{}, nil
}

// vaultProbe is a vault health check as scripted, which counts the calls.
type vaultProbe struct {
	calls atomic.Int32
	err   error
	block bool
}

func (p *vaultProbe) check(ctx context.Context) error {
	p.calls.Add(1)

	if p.block {
		<-ctx.Done()

		return fmt.Errorf("asking vault: %w", ctx.Err())
	}

	return p.err
}

// gatedProbe is a vault check that waits until every expected caller has
// arrived, or a short deadline passes, before it answers.
type gatedProbe struct {
	calls   atomic.Int32
	arrived atomic.Int32
	want    int32
}

func (p *gatedProbe) check(context.Context) error {
	p.calls.Add(1)
	p.arrived.Add(1)

	deadline := time.Now().Add(gateDeadline)
	for p.arrived.Load() < p.want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	return nil
}

// fakeClock is a clock the test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(by)
}

func healthHandler(director *infoDirector, probe *vaultProbe, clock *fakeClock) *boshhandler.Handler {
	return boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:         &logger.NoOpLogger{},
		Config:         statusConfig{},
		Director:       director,
		VaultHealth:    probe.check,
		HealthTimeout:  healthTimeout,
		HealthCacheTTL: healthWindow,
		Now:            clock.Now,
	})
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
}

// healthBody requests /b/status and returns the raw body with the decoded
// health fields.
func healthBody(t *testing.T, handler *boshhandler.Handler) (string, map[string]interface{}) {
	t.Helper()

	recorder := getWithin(t, waitLimit, handler.GetStatus, "/b/status")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decoding the status: %v", err)
	}

	return recorder.Body.String(), body
}

func expectHealth(t *testing.T, body map[string]interface{}, healthy bool, boshState, vaultState string) {
	t.Helper()

	if body["healthy"] != healthy || body["bosh"] != boshState || body["vault"] != vaultState {
		t.Fatalf("expected healthy=%v bosh=%s vault=%s, got healthy=%v bosh=%v vault=%v",
			healthy, boshState, vaultState, body["healthy"], body["bosh"], body["vault"])
	}
}

func TestStatusReportsBothConnectedWhenBothAnswer(t *testing.T) {
	t.Parallel()

	_, body := healthBody(t, healthHandler(&infoDirector{}, &vaultProbe{}, newClock()))

	expectHealth(t, body, true, "connected", "connected")
}

func TestStatusReportsBoshUnreachableAndUnhealthy(t *testing.T) {
	t.Parallel()

	director := &infoDirector{err: errors.New("dial tcp: " + secretText)}
	raw, body := healthBody(t, healthHandler(director, &vaultProbe{}, newClock()))

	expectHealth(t, body, false, "unreachable", "connected")

	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "dial tcp") {
		t.Fatalf("the response leaks the underlying error: %s", raw)
	}
}

func TestStatusReportsVaultUnreachableAndUnhealthy(t *testing.T) {
	t.Parallel()

	probe := &vaultProbe{err: errors.New("vault is sealed at " + secretText)}
	raw, body := healthBody(t, healthHandler(&infoDirector{}, probe, newClock()))

	expectHealth(t, body, false, "connected", "unreachable")

	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "sealed") {
		t.Fatalf("the response leaks the underlying error: %s", raw)
	}
}

func TestStatusCountsASlowCheckAsUnreachable(t *testing.T) {
	t.Parallel()

	director := &infoDirector{block: true}
	probe := &vaultProbe{block: true}

	_, body := healthBody(t, healthHandler(director, probe, newClock()))

	expectHealth(t, body, false, "unreachable", "unreachable")
}

func TestStatusReportsAnUnconfiguredCheckAsUnreachable(t *testing.T) {
	t.Parallel()

	handler := boshhandler.NewHandler(boshhandler.Dependencies{
		Logger: &logger.NoOpLogger{},
		Config: statusConfig{},
	})

	_, body := healthBody(t, handler)

	expectHealth(t, body, false, "unreachable", "unreachable")
}

func TestStatusReusesTheHealthInsideTheWindowAndRefreshesAfter(t *testing.T) {
	t.Parallel()

	director := &infoDirector{}
	probe := &vaultProbe{}
	clock := newClock()
	handler := healthHandler(director, probe, clock)

	healthBody(t, handler)
	clock.advance(healthWindow - time.Second)
	healthBody(t, handler)

	if director.calls.Load() != 1 || probe.calls.Load() != 1 {
		t.Fatalf("expected one check each inside the window, got director=%d vault=%d",
			director.calls.Load(), probe.calls.Load())
	}

	clock.advance(2 * time.Second)

	probe.err = errors.New("vault went away")
	_, body := healthBody(t, handler)

	if director.calls.Load() != 2 || probe.calls.Load() != 2 {
		t.Fatalf("expected a second check each after the window, got director=%d vault=%d",
			director.calls.Load(), probe.calls.Load())
	}

	expectHealth(t, body, false, "connected", "unreachable")
}

func TestStatusAnswersConcurrentRequestsWithOneCheck(t *testing.T) {
	t.Parallel()

	const callers = 16

	director := &infoDirector{}
	probe := &gatedProbe{want: callers}
	handler := boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:         &logger.NoOpLogger{},
		Config:         statusConfig{},
		Director:       director,
		VaultHealth:    probe.check,
		HealthTimeout:  healthTimeout,
		HealthCacheTTL: healthWindow,
		Now:            newClock().Now,
	})

	var group sync.WaitGroup

	for range callers {
		group.Add(1)

		go func() {
			defer group.Done()

			recorder := getWithin(t, waitLimit, handler.GetStatus, "/b/status")
			if recorder.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", recorder.Code)
			}
		}()
	}

	group.Wait()

	if director.calls.Load() != 1 || probe.calls.Load() != 1 {
		t.Fatalf("expected one check each, got director=%d vault=%d",
			director.calls.Load(), probe.calls.Load())
	}
}

// slowThenFastDirector blocks on its context for as long as block is set.
type slowThenFastDirector struct {
	bosh.Director

	calls atomic.Int32
	block atomic.Bool
}

func (d *slowThenFastDirector) GetInfo(ctx context.Context) (*bosh.Info, error) {
	d.calls.Add(1)

	if d.block.Load() {
		<-ctx.Done()

		return nil, fmt.Errorf("asking the director: %w", ctx.Err())
	}

	return &bosh.Info{}, nil
}

func TestStatusStopsCheckingWhenTheRequestIsCancelledAndDoesNotCacheIt(t *testing.T) {
	t.Parallel()

	director := &slowThenFastDirector{}
	director.block.Store(true)

	handler := boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:         &logger.NoOpLogger{},
		Config:         statusConfig{},
		Director:       director,
		VaultHealth:    (&vaultProbe{}).check,
		HealthTimeout:  30 * time.Second,
		HealthCacheTTL: healthWindow,
		Now:            newClock().Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)

	request := httptest.NewRequest(http.MethodGet, "/b/status", nil).WithContext(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		handler.GetStatus(httptest.NewRecorder(), request)
	}()

	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("expected a cancelled request to return promptly")
	}

	director.block.Store(false)

	_, body := healthBody(t, handler)

	if director.calls.Load() != 2 {
		t.Fatalf("expected the next request to check again, got %d director calls", director.calls.Load())
	}

	expectHealth(t, body, true, "connected", "connected")
}

// capturingLogger records the debug lines the handler writes.
type capturingLogger struct {
	logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (c *capturingLogger) Named(string) logger.Logger { return c }

func (c *capturingLogger) Debug(msg string, args ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(msg, args...))
}

func TestStatusMasksCredentialedURLsInItsDebugLog(t *testing.T) {
	t.Parallel()

	capture := &capturingLogger{}
	director := &infoDirector{err: errors.New("Get " + secretText + "/info: connection refused")}
	handler := boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:         capture,
		Config:         statusConfig{},
		Director:       director,
		VaultHealth:    (&vaultProbe{}).check,
		HealthTimeout:  healthTimeout,
		HealthCacheTTL: healthWindow,
		Now:            newClock().Now,
	})

	healthBody(t, handler)

	capture.mu.Lock()
	defer capture.mu.Unlock()

	logged := strings.Join(capture.lines, "\n")

	if !strings.Contains(logged, "BOSH health check failed") || !strings.Contains(logged, "connection refused") {
		t.Fatalf("expected the failure in the debug log, got %q", logged)
	}

	if strings.Contains(logged, "hunter2") || strings.Contains(logged, "director.invalid") {
		t.Fatalf("the debug log leaks the credentialed URL: %q", logged)
	}
}
