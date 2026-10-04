package credhub_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

var (
	errDirectorDown     = errors.New("director answered 500")
	errUnknownAuthority = errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority")
	errConnRefused      = errors.New("dial tcp 10.0.0.6:8844: connect: connection refused")
)

// captureLogger records every line at every level.
type captureLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
}

func (c *captureLogger) Debugf(format string, args ...interface{}) { c.record("DEBUG", format, args) }
func (c *captureLogger) Infof(format string, args ...interface{})  { c.record("INFO", format, args) }
func (c *captureLogger) Warnf(format string, args ...interface{})  { c.record("WARN", format, args) }
func (c *captureLogger) Errorf(format string, args ...interface{}) { c.record("ERROR", format, args) }

func (c *captureLogger) record(level, format string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, level+" "+fmt.Sprintf(format, args...))
}

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

func (c *captureLogger) matching(substrings ...string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var found []string

	for _, line := range c.lines {
		all := true

		for _, substring := range substrings {
			if !strings.Contains(line, substring) {
				all = false

				break
			}
		}

		if all {
			found = append(found, line)
		}
	}

	return found
}

// fakeDirector answers the three questions the cleaner asks.
type fakeDirector struct {
	mu            sync.Mutex
	infoName      string
	infoErr       error
	deploymentErr error
	runningTask   *bosh.Task
	taskErr       error
	block         chan struct{}
	entered       chan struct{}
	calls         []string
}

func newFakeDirector() *fakeDirector {
	return &fakeDirector{infoName: labDirector, deploymentErr: bosh.ErrDeploymentNotFound}
}

func (d *fakeDirector) GetInfo() (*bosh.Info, error) {
	d.record("info")

	if d.infoErr != nil {
		return nil, d.infoErr
	}

	return &bosh.Info{Name: d.infoName}, nil
}

func (d *fakeDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error) {
	d.record("deployment " + name)

	if d.entered != nil {
		d.entered <- struct{}{}
	}

	if d.block != nil {
		<-d.block
	}

	if d.deploymentErr != nil {
		return nil, d.deploymentErr
	}

	return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
}

func (d *fakeDirector) FindRunningTaskForDeployment(name string) (*bosh.Task, error) {
	d.record("tasks " + name)

	return d.runningTask, d.taskErr
}

func (d *fakeDirector) record(call string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, call)
}

// fakeCredHubClient scripts FindByPath and Delete answers.
type fakeCredHubClient struct {
	mu        sync.Mutex
	listed    []string
	findErrs  []error
	deleteErr map[string][]error
	finds     []string
	deletes   []string
}

func (f *fakeCredHubClient) FindByPath(_ context.Context, path string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.finds = append(f.finds, path)

	if len(f.findErrs) > 0 {
		err := f.findErrs[0]
		f.findErrs = f.findErrs[1:]

		if err != nil {
			return nil, err
		}
	}

	return slices.Clone(f.listed), nil
}

func (f *fakeCredHubClient) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletes = append(f.deletes, name)

	if errs := f.deleteErr[name]; len(errs) > 0 {
		f.deleteErr[name] = errs[1:]

		return errs[0]
	}

	return nil
}

func (f *fakeCredHubClient) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.finds), slices.Clone(f.deletes)
}

type cleanerWorld struct {
	client   *fakeCredHubClient
	director *fakeDirector
	log      *captureLogger
	cleaner  *credhub.Cleaner
}

func newCleanerWorld(t *testing.T) *cleanerWorld {
	t.Helper()

	world := &cleanerWorld{
		client:   &fakeCredHubClient{deleteErr: map[string][]error{}},
		director: newFakeDirector(),
		log:      newCaptureLogger(),
	}

	world.cleaner = credhub.NewCleaner(world.client, world.director, func() credhub.Policy {
		policy := labPolicy()
		policy.InfoDirectorName = ""

		return policy
	}, world.log)
	credhub.SetCleanerBackoff(world.cleaner, time.Millisecond, time.Millisecond)

	t.Cleanup(func() { assertNoMarkers(t, world.log.output()) })

	return world
}

func labTarget() credhub.Target {
	return credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment}
}

func TestCleanerHappyPath(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	first, second := labPrefix+"valkey_standalone_crt", labPrefix+"valkey_password"
	world.client.listed = []string{
		first,
		"/" + labDirector + "/" + labBroker + "/blacksmith_services_ca",
		labPrefix + "nested/name",
		second,
	}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	finds, deletes := world.client.calls()
	if !slices.Equal(finds, []string{labPrefix}) {
		t.Fatalf("finds = %q, want one find of %q", finds, labPrefix)
	}

	if !slices.Equal(deletes, []string{first, second}) {
		t.Fatalf("deletes = %q, want exactly the owned names in listed order", deletes)
	}

	if !slices.Equal(result.Deleted, []string{first, second}) || len(result.Failed) != 0 || result.Refused != nil {
		t.Fatalf("unexpected result %+v", result)
	}

	if result.Prefix != labPrefix || len(result.Skipped) != 2 {
		t.Fatalf("unexpected prefix or skipped names %+v", result)
	}

	if summary := world.log.matching("deleted 2 of 2"); len(summary) != 1 {
		t.Fatalf("expected one summary line naming the count, got %q in:\n%s", summary, world.log.output())
	}
}

func TestCleanerRequiresProofOfAbsence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeDirector)
		reason string
	}{
		{name: "deployment still exists", mutate: func(d *fakeDirector) { d.deploymentErr = nil }, reason: "still has deployment"},
		{name: "director error", mutate: func(d *fakeDirector) { d.deploymentErr = errDirectorDown }, reason: "director answered 500"},
		{name: "task running", mutate: func(d *fakeDirector) { d.runningTask = &bosh.Task{ID: 434, State: "processing"} }, reason: "task 434"},
		{name: "task lookup error", mutate: func(d *fakeDirector) { d.taskErr = errDirectorDown }, reason: "director answered 500"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			world := newCleanerWorld(t)
			world.client.listed = []string{labPrefix + "valkey_standalone_crt"}
			testCase.mutate(world.director)

			result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

			finds, deletes := world.client.calls()
			if len(finds) != 0 || len(deletes) != 0 {
				t.Fatalf("expected no CredHub call, got finds=%q deletes=%q", finds, deletes)
			}

			if !errors.Is(result.Refused, credhub.ErrDeploymentNotProvenGone) {
				t.Fatalf("expected ErrDeploymentNotProvenGone, got %v", result.Refused)
			}

			if lines := world.log.matching(labDeployment, testCase.reason, "the deprovision itself succeeded"); len(lines) != 1 {
				t.Fatalf("expected one line with the reason %q, got:\n%s", testCase.reason, world.log.output())
			}
		})
	}
}

func TestCleanerRefusesADirectorNameMismatch(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.director.infoName = otherDirector
	world.client.listed = []string{labPrefix + "valkey_standalone_crt"}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if !errors.Is(result.Refused, credhub.ErrDirectorNameMismatch) {
		t.Fatalf("expected ErrDirectorNameMismatch, got %v", result.Refused)
	}

	if finds, deletes := world.client.calls(); len(finds) != 0 || len(deletes) != 0 {
		t.Fatalf("expected no CredHub call, got finds=%q deletes=%q", finds, deletes)
	}

	if lines := world.log.matching(otherDirector, "deleted nothing"); len(lines) != 1 {
		t.Fatalf("expected one refusal line, got:\n%s", world.log.output())
	}
}

func TestCleanerInfoFailure(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.director.infoErr = errDirectorDown

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if result.Refused == nil {
		t.Fatal("expected a refusal when /info fails")
	}

	if finds, _ := world.client.calls(); len(finds) != 0 {
		t.Fatalf("expected no CredHub call, got %q", finds)
	}
}

func TestCleanerGuardRefusalTouchesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  credhub.Target
		wantErr error
	}{
		{name: "non-GUID instance", target: credhub.Target{InstanceID: "lastop-1", DeploymentName: "valkey-standalone-lastop-1"}, wantErr: credhub.ErrInstanceIDNotGUID},
		{name: "protected deployment", target: credhub.Target{InstanceID: labGUID, DeploymentName: labBroker}, wantErr: credhub.ErrProtectedDeployment},
		{name: "retired plan", target: credhub.Target{InstanceID: labGUID, DeploymentName: "redis-standalone-" + labGUID}, wantErr: credhub.ErrPlanNotInCatalog},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			world := newCleanerWorld(t)

			result := world.cleaner.CleanupDeployment(context.Background(), testCase.target)

			if !errors.Is(result.Refused, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, result.Refused)
			}

			if finds, deletes := world.client.calls(); len(finds) != 0 || len(deletes) != 0 {
				t.Fatalf("expected no CredHub call, got finds=%q deletes=%q", finds, deletes)
			}

			world.director.mu.Lock()
			calls := slices.Clone(world.director.calls)
			world.director.mu.Unlock()

			if len(calls) != 0 {
				t.Fatalf("expected no director call after a guard refusal, got %q", calls)
			}

			if lines := world.log.matching("deleted nothing"); len(lines) != 1 {
				t.Fatalf("expected one refusal line, got:\n%s", world.log.output())
			}
		})
	}
}

func TestCleanerProtectedRefusalGivesNoDeleteHint(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)

	_ = world.cleaner.CleanupDeployment(context.Background(), credhub.Target{InstanceID: labGUID, DeploymentName: labBroker})

	if strings.Contains(world.log.output(), "credhub delete") {
		t.Fatalf("a protected deployment's refusal must not suggest deleting its credentials:\n%s", world.log.output())
	}
}

func TestCleanerListFailure(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	serverErr := &credhub.APIError{Op: findOp, Status: http.StatusInternalServerError, Message: "database unavailable"}
	world.client.findErrs = []error{serverErr, serverErr, serverErr}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	finds, deletes := world.client.calls()
	if len(finds) != 3 || len(deletes) != 0 {
		t.Fatalf("expected three find attempts and no delete, got finds=%d deletes=%d", len(finds), len(deletes))
	}

	if len(result.Failed) != 1 || result.Failed[0].Name != labPrefix {
		t.Fatalf("expected one failure for the prefix, got %+v", result.Failed)
	}

	lines := world.log.matching(labPrefix, "500", "the deprovision itself succeeded", "CredHub or its database is unhealthy")
	if len(lines) != 1 {
		t.Fatalf("expected one operator-grade failure line, got:\n%s", world.log.output())
	}
}

func TestCleanerListRecoversAfterARetry(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.client.findErrs = []error{&credhub.APIError{Op: findOp, Status: http.StatusServiceUnavailable}}
	world.client.listed = []string{labPrefix + "valkey_standalone_crt"}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if len(result.Deleted) != 1 || len(result.Failed) != 0 {
		t.Fatalf("expected the second find to succeed and the name to be deleted, got %+v", result)
	}
}

func TestCleanerDeleteFailureDoesNotStopTheRest(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	first, second := labPrefix+"valkey_standalone_crt", labPrefix+"valkey_password"
	world.client.listed = []string{first, second}
	world.client.deleteErr[first] = []error{&credhub.APIError{Op: deleteOp, Status: http.StatusForbidden, Message: insufficientPerm}}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	_, deletes := world.client.calls()
	if !slices.Equal(deletes, []string{first, second}) {
		t.Fatalf("expected one attempt at the 403 name and one delete of the second, got %q", deletes)
	}

	if !slices.Equal(result.Deleted, []string{second}) || len(result.Failed) != 1 || result.Failed[0].Name != first {
		t.Fatalf("unexpected result %+v", result)
	}

	lines := world.log.matching(first, "403", "credhub.write", "the deprovision itself succeeded", "credhub delete -n "+first)
	if len(lines) != 1 {
		t.Fatalf("expected one 403 line naming credhub.write, got:\n%s", world.log.output())
	}
}

func TestCleanerDoesNotRetryARefreshed401(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	name := labPrefix + "valkey_standalone_crt"
	world.client.listed = []string{name}
	world.client.deleteErr[name] = []error{&credhub.APIError{Op: deleteOp, Status: http.StatusUnauthorized, Refreshed: true}}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if _, deletes := world.client.calls(); len(deletes) != 1 {
		t.Fatalf("expected one delete attempt, got %d", len(deletes))
	}

	if len(result.Failed) != 1 {
		t.Fatalf("expected one failure, got %+v", result)
	}

	if lines := world.log.matching(name, "401", "signing key rotated"); len(lines) != 1 {
		t.Fatalf("expected the CredHub 401 wording, got:\n%s", world.log.output())
	}
}

func TestCleanerUAAFailureWording(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.client.findErrs = []error{fmt.Errorf("CredHub find: %w", &credhub.UAAError{Status: http.StatusUnauthorized, Code: uaaUnauthorized})}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if finds, _ := world.client.calls(); len(finds) != 1 {
		t.Fatalf("a UAA 401 must not be retried, got %d finds", len(finds))
	}

	if len(result.Failed) != 1 {
		t.Fatalf("expected one failure, got %+v", result)
	}

	if lines := world.log.matching("does not exist on the director's UAA", "uaa clients get"); len(lines) != 1 {
		t.Fatalf("expected the UAA 401 wording, got:\n%s", world.log.output())
	}
}

func TestCleanerRetriesTransientDeleteFailures(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	name := labPrefix + "valkey_standalone_crt"
	world.client.listed = []string{name}
	world.client.deleteErr[name] = []error{
		&credhub.APIError{Op: deleteOp, Status: http.StatusTooManyRequests},
		&credhub.APIError{Op: deleteOp, Status: http.StatusBadGateway},
	}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if _, deletes := world.client.calls(); len(deletes) != 3 {
		t.Fatalf("expected three delete attempts, got %d", len(deletes))
	}

	if !slices.Equal(result.Deleted, []string{name}) {
		t.Fatalf("expected the third attempt to delete the name, got %+v", result)
	}
}

func TestCleanerCountsANotFoundAsDeleted(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	name := labPrefix + "valkey_standalone_crt"
	world.client.listed = []string{name}
	world.client.deleteErr[name] = []error{fmt.Errorf("%w: %q", credhub.ErrNotFound, name)}

	result := world.cleaner.CleanupDeployment(context.Background(), labTarget())

	if !slices.Equal(result.Deleted, []string{name}) || len(result.Failed) != 0 {
		t.Fatalf("expected a 404 to count as deleted, got %+v", result)
	}
}

func TestCleanerDeduplicatesConcurrentRuns(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.client.listed = []string{labPrefix + "valkey_standalone_crt"}
	world.director.block = make(chan struct{})
	world.director.entered = make(chan struct{}, 1)

	firstDone := make(chan credhub.Result, 1)

	go func() { firstDone <- world.cleaner.CleanupDeployment(context.Background(), labTarget()) }()

	<-world.director.entered

	second := world.cleaner.CleanupDeployment(context.Background(), labTarget())
	if !second.Duplicate {
		t.Fatalf("expected the second concurrent run to return as a duplicate, got %+v", second)
	}

	close(world.director.block)

	first := <-firstDone
	if len(first.Deleted) != 1 {
		t.Fatalf("expected the first run to delete the name, got %+v", first)
	}

	if finds, deletes := world.client.calls(); len(finds) != 1 || len(deletes) != 1 {
		t.Fatalf("expected one run against CredHub, got finds=%d deletes=%d", len(finds), len(deletes))
	}

	world.director.block = nil
	world.director.entered = nil

	third := world.cleaner.CleanupDeployment(context.Background(), labTarget())
	if third.Duplicate {
		t.Fatal("a run after the first finished must not be treated as a duplicate")
	}
}

func TestCleanerHonorsACanceledContext(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	world.client.listed = []string{labPrefix + "valkey_standalone_crt"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := world.cleaner.CleanupDeployment(ctx, labTarget())

	if _, deletes := world.client.calls(); len(deletes) != 0 {
		t.Fatalf("expected no delete under a canceled context, got %q", deletes)
	}

	if result.Refused == nil && len(result.Failed) == 0 {
		t.Fatalf("expected the canceled run to report why it stopped, got %+v", result)
	}
}

func TestCleanerLogsNeverCarryCredentials(t *testing.T) {
	t.Parallel()

	world := newCleanerWorld(t)
	name := labPrefix + "valkey_standalone_crt"
	world.client.listed = []string{name}
	world.client.deleteErr[name] = []error{&credhub.APIError{Op: deleteOp, Status: http.StatusForbidden, Message: insufficientPerm}}

	_ = world.cleaner.CleanupDeployment(context.Background(), labTarget())

	output := world.log.output()
	if output == "" {
		t.Fatal("expected log output")
	}

	for _, marker := range []string{secretMarker, tokenMarker} {
		if strings.Contains(output, marker) {
			t.Fatalf("logs carry %q", marker)
		}
	}
}

func TestExplainFailureCoversEveryRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		op     string
		err    error
		causes string
		check  string
	}{
		{name: "uaa 401", op: findOp, err: &credhub.UAAError{Status: 401}, causes: "does not exist on the director's UAA", check: "uaa clients get"},
		{name: "uaa unreachable", op: findOp, err: fmt.Errorf("%w: dial tcp: connection refused", credhub.ErrUAAUnreachable), causes: "UAA is down", check: "bosh.cacert"},
		{name: "uaa 503", op: findOp, err: &credhub.UAAError{Status: 503}, causes: "UAA is down", check: "bosh.cacert"},
		{name: "credhub 401", op: deleteOp, err: &credhub.APIError{Op: deleteOp, Status: 401, Refreshed: true}, causes: "signing key rotated", check: "credhub.uaa_url"},
		{name: "credhub 403", op: deleteOp, err: &credhub.APIError{Op: deleteOp, Status: 403}, causes: "credhub.write", check: "/api/v2/permissions"},
		{name: "credhub 500", op: findOp, err: &credhub.APIError{Op: findOp, Status: 500}, causes: "CredHub or its database is unhealthy", check: "CredHub logs"},
		{name: "credhub 429", op: deleteOp, err: &credhub.APIError{Op: deleteOp, Status: 429}, causes: "rate", check: "CredHub logs"},
		{name: "x509", op: findOp, err: fmt.Errorf("CredHub find: %w", errUnknownAuthority), causes: "credhub_tls", check: "credhub_ca_cert"},
		{name: "refused", op: findOp, err: fmt.Errorf("CredHub find: %w", errConnRefused), causes: "CredHub on the director is down", check: "monit summary"},
		{name: "timeout", op: findOp, err: fmt.Errorf("CredHub find: %w", context.DeadlineExceeded), causes: "CredHub on the director is down", check: "monit summary"},
		{name: "protected", op: deleteOp, err: credhub.ErrProtectedName, causes: "protected", check: "on purpose"},
		{name: "bad answer", op: findOp, err: credhub.ErrBadFindAnswer, causes: credhubURLField, check: credhubURLField},
		{name: "unexpected status", op: deleteOp, err: &credhub.APIError{Op: deleteOp, Status: 400}, causes: "unexpected", check: credhubURLField},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			causes, check := credhub.ExplainFailure(testCase.op, testCase.err)
			if !strings.Contains(causes, testCase.causes) {
				t.Errorf("causes %q do not contain %q", causes, testCase.causes)
			}

			if !strings.Contains(check, testCase.check) {
				t.Errorf("check %q does not contain %q", check, testCase.check)
			}
		})
	}
}
