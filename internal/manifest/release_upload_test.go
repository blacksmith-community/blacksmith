package manifest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/pkg/logger"
)

const (
	uploadManifest = `releases:
- name: redis
  version: "18"
  url: https://releases.example.test/redis-18.tgz
  sha1: 0123456789abcdef
`
	uploadTaskID = 7
	uploadResult = "Release tarball sha1 does not match the expected sha1"

	stateProcessing = "processing"
	stateDone       = "done"
	stateError      = "error"
	eventDeploy     = "deploy"
	eventProcessing = "task 7 processing"
)

var (
	errUnexpectedDirectorCall = errors.New("the test director did not expect this call")
	errAnswerDropped          = errors.New("the connection closed before the director answered")
)

// uploadDirector is a bosh.Director for the plan release upload. It records
// every call in order, so a test can tell whether the deploy arrived before
// the upload task finished. A call it does not script panics through the
// nil embedded Director.
type uploadDirector struct {
	bosh.Director

	mu     sync.Mutex
	events []string

	// listings answers each GetReleases call in turn, and the last answer
	// repeats.
	listings [][]bosh.Release
	listed   int

	// polls are the states the upload task reports, one per read, and the
	// wait ends at the first terminal one.
	polls []string

	// waitBlocks makes WaitForTask wait for its context to end, as a wait on
	// a task that never finishes does.
	waitBlocks bool
	// waitStarted is closed when WaitForTask starts, when it is not nil.
	waitStarted chan struct{}

	// uploadErr, when it is not nil, is what UploadRelease returns in
	// place of a task.
	uploadErr error
	// listedSignal, when it is not nil, gets the count of listings after
	// each GetReleases call, without blocking.
	listedSignal chan int
}

func (d *uploadDirector) GetReleases(context.Context) ([]bosh.Release, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.events = append(d.events, "list releases")

	index := d.listed
	d.listed++

	if d.listedSignal != nil {
		select {
		case d.listedSignal <- d.listed:
		default:
		}
	}

	if len(d.listings) == 0 {
		return []bosh.Release{}, nil
	}

	if index >= len(d.listings) {
		index = len(d.listings) - 1
	}

	return d.listings[index], nil
}

func (d *uploadDirector) UploadRelease(_ context.Context, url, _ string) (*bosh.Task, error) {
	d.record("upload " + url)

	if d.uploadErr != nil {
		return nil, d.uploadErr
	}

	return &bosh.Task{ID: uploadTaskID, State: stateProcessing, Description: "create release"}, nil
}

func (d *uploadDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.record(fmt.Sprintf("wait %d", taskID))

	if d.waitStarted != nil {
		close(d.waitStarted)
	}

	if d.waitBlocks {
		<-ctx.Done()

		return nil, &bosh.DirectorCallError{
			Operation: "WaitForTask",
			Director:  "https://director.example.test:25555",
			Cause:     ctx.Err(),
			Err:       ctx.Err(),
			TaskID:    taskID,
			Task:      bosh.TaskLeftRunning,
		}
	}

	for _, state := range d.polls {
		d.record(fmt.Sprintf("task %d %s", taskID, state))

		switch state {
		case stateDone, stateError, "cancelled", "timeout":
			task := &bosh.Task{ID: taskID, State: state}
			if state != stateDone {
				task.Result = uploadResult
			}

			return task, nil
		}
	}

	return nil, fmt.Errorf("%w: task %d never finished", errUnexpectedDirectorCall, taskID)
}

func (d *uploadDirector) CancelTask(_ context.Context, taskID int) error {
	d.record(fmt.Sprintf("cancel %d", taskID))

	return nil
}

func (d *uploadDirector) CreateDeployment(_ context.Context, _ string) (*bosh.Task, error) {
	d.record(eventDeploy)

	return &bosh.Task{ID: 8, State: stateProcessing}, nil
}

func (d *uploadDirector) record(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.events = append(d.events, event)
}

func (d *uploadDirector) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.events...)
}

// lineLogger keeps every line logged at every level.
type lineLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func newLineLogger() *lineLogger { return &lineLogger{NoOpLogger: &logger.NoOpLogger{}} }

func (l *lineLogger) Debug(msg string, args ...interface{}) { l.add("DEBUG", msg, args) }
func (l *lineLogger) Info(msg string, args ...interface{})  { l.add("INFO", msg, args) }
func (l *lineLogger) Warn(msg string, args ...interface{})  { l.add("WARN", msg, args) }
func (l *lineLogger) Error(msg string, args ...interface{}) { l.add("ERROR", msg, args) }

func (l *lineLogger) add(level, msg string, args []interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, level+" "+fmt.Sprintf(msg, args...))
}

func (l *lineLogger) output() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// redis18 is a release listing that holds the plan's release.
func redis18() []bosh.Release {
	return []bosh.Release{{Name: "redis", ReleaseVersions: []bosh.ReleaseVersion{{Version: "18"}}}}
}

// provisionReleases runs the release step and then the deploy, the way the
// provisioning phase does, and returns the release step's error.
func provisionReleases(ctx context.Context, director *uploadDirector, log logger.Logger, progress func(string)) error {
	err := manifestPkg.UploadReleasesFromManifest(ctx, uploadManifest, director, log, progress)
	if err != nil {
		return fmt.Errorf("release step: %w", err)
	}

	_, err = director.CreateDeployment(ctx, uploadManifest)

	return err
}

func TestPlanReleaseUploadFinishesBeforeTheDeploy(t *testing.T) {
	t.Parallel()

	director := &uploadDirector{polls: []string{stateProcessing, stateProcessing, stateProcessing, stateDone}}

	err := provisionReleases(context.Background(), director, newLineLogger(), nil)
	if err != nil {
		t.Fatalf("expected the provision to go on, got: %v", err)
	}

	want := []string{
		"list releases",
		"upload https://releases.example.test/redis-18.tgz",
		"wait 7",
		eventProcessing,
		eventProcessing,
		eventProcessing,
		"task 7 done",
		eventDeploy,
	}

	got := director.recorded()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the deploy must wait for the upload task to finish\nwant: %q\ngot:  %q", want, got)
	}
}

func TestPlanReleaseUploadFailureFailsTheProvision(t *testing.T) {
	t.Parallel()

	director := &uploadDirector{polls: []string{stateProcessing, stateError}}

	err := provisionReleases(context.Background(), director, newLineLogger(), nil)
	if err == nil {
		t.Fatal("expected the failed upload to fail the provision")
	}

	for _, want := range []string{"redis", "18", "https://releases.example.test/redis-18.tgz", "task 7", uploadResult} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}

	got := director.recorded()
	if got[len(got)-1] != "list releases" {
		t.Errorf("expected the releases to be listed once more after the failed upload, and no deploy, got: %q", got)
	}
}

func TestPlanReleaseUploadFailureIsIgnoredWhenTheReleaseArrived(t *testing.T) {
	t.Parallel()

	director := &uploadDirector{
		polls:    []string{stateError},
		listings: [][]bosh.Release{{}, redis18()},
	}
	log := newLineLogger()

	err := provisionReleases(context.Background(), director, log, nil)
	if err != nil {
		t.Fatalf("expected the provision to go on once the release is present, got: %v", err)
	}

	got := director.recorded()
	if got[len(got)-1] != eventDeploy {
		t.Errorf("expected the deploy to be sent, got: %q", got)
	}

	out := log.output()
	if !strings.Contains(out, "BOSH task 7") || !strings.Contains(out, uploadResult) {
		t.Errorf("expected the failed upload task to be logged, got:\n%s", out)
	}
}

func TestPlanReleaseUploadReportsTheReleaseAndTaskInProgress(t *testing.T) {
	t.Parallel()

	director := &uploadDirector{polls: []string{stateDone}}

	var reported []string

	err := provisionReleases(context.Background(), director, newLineLogger(), func(message string) {
		reported = append(reported, message)
	})
	if err != nil {
		t.Fatalf("expected the provision to go on, got: %v", err)
	}

	want := "Uploading BOSH release redis/18 (task 7)"
	if len(reported) != 1 || reported[0] != want {
		t.Fatalf("expected progress %q, got: %q", want, reported)
	}
}

func TestPlanReleaseUploadLeftRunningFailsWithADirectorCallError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	director := &uploadDirector{waitBlocks: true, waitStarted: make(chan struct{})}

	finished := make(chan error, 1)

	go func() { finished <- provisionReleases(ctx, director, newLineLogger(), nil) }()

	select {
	case <-director.waitStarted:
	case err := <-finished:
		t.Fatalf("the provision went on without waiting for the upload task: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the upload task was never waited for")
	}

	cancel()

	var err error

	select {
	case err = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the provision did not stop when its context ended")
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a DirectorCallError, got: %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected the error to carry the context's end, got: %v", err)
	}

	if !strings.Contains(err.Error(), "upload") || !strings.Contains(err.Error(), "left running") {
		t.Errorf("expected the error to say the upload task was left running, got: %v", err)
	}

	for _, event := range director.recorded() {
		if strings.HasPrefix(event, "cancel") || event == eventDeploy {
			t.Errorf("expected no cancel and no deploy, got: %q", director.recorded())
		}
	}
}

// lostUploadAnswer is what the adapter returns when the answer to a
// release upload never came back.
func lostUploadAnswer() error {
	return &bosh.OutcomeUnknownError{
		Operation: "UploadRelease",
		Director:  "https://director.example.test:25555",
		ContextID: "ctx-upload",
		NoLookup:  true,
		Err:       errAnswerDropped,
	}
}

// listCount counts the release listings among the recorded calls.
func listCount(events []string) int {
	count := 0

	for _, event := range events {
		if event == "list releases" {
			count++
		}
	}

	return count
}

// A lost upload answer makes the provision poll the director's releases,
// and the deploy goes ahead once the release appears, with a Warning that
// names it.
//
//nolint:paralleltest // it shortens the release poll, a package variable
func TestLostUploadAnswerDeploysOnceTheReleaseAppears(t *testing.T) {
	manifestPkg.SetReleasePollTimes(t, time.Millisecond, time.Minute)

	director := &uploadDirector{
		uploadErr: lostUploadAnswer(),
		listings:  [][]bosh.Release{{}, {}, redis18()},
	}
	log := newLineLogger()

	err := provisionReleases(t.Context(), director, log, nil)
	if err != nil {
		t.Fatalf("expected the provision to go on once the release appeared, got: %v", err)
	}

	got := director.recorded()
	if got[len(got)-1] != eventDeploy {
		t.Fatalf("expected the deploy to be sent, got: %q", got)
	}

	if lists := listCount(got); lists != 3 {
		t.Errorf("expected the release to be found on the third listing, got %d listings: %q", lists, got)
	}

	var warned bool

	for _, line := range strings.Split(log.output(), "\n") {
		if strings.HasPrefix(line, "WARN ") && strings.Contains(line, "redis/18") && strings.Contains(line, "lost") {
			warned = true
		}
	}

	if !warned {
		t.Errorf("expected a Warning that names redis/18 and says its upload answer was lost, got:\n%s", log.output())
	}
}

// A release that never appears fails the provision at the poll's
// deadline, with a DirectorCallError that says the answer was lost and the
// upload may still be running, and no deploy is sent.
//
//nolint:paralleltest // it shortens the release poll, a package variable
func TestLostUploadAnswerFailsAfterTheDeadline(t *testing.T) {
	const deadline = 100 * time.Millisecond

	manifestPkg.SetReleasePollTimes(t, 5*time.Millisecond, deadline)

	director := &uploadDirector{uploadErr: lostUploadAnswer()}

	started := time.Now()
	err := provisionReleases(t.Context(), director, newLineLogger(), nil)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected the provision to fail once the deadline passed")
	}

	if elapsed < deadline {
		t.Errorf("expected the provision to poll until the deadline of %s, it failed after %s", deadline, elapsed)
	}

	if elapsed > 5*time.Second {
		t.Errorf("expected the provision to fail at the deadline of %s, it took %s", deadline, elapsed)
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a DirectorCallError, got: %v", err)
	}

	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("expected the error to carry the lost answer, got: %v", err)
	}

	for _, want := range []string{"redis/18", "answer", "lost", "may still be running"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}

	got := director.recorded()
	for _, event := range got {
		if event == eventDeploy {
			t.Fatalf("expected no deploy, got: %q", got)
		}
	}

	if lists := listCount(got); lists < 3 {
		t.Errorf("expected the releases to be listed again and again until the deadline, got %d listings", lists)
	}
}

// When the provision's context ends during the poll, the poll stops at
// once, without waiting for its next listing, and no deploy is sent.
//
//nolint:paralleltest // it shortens the release poll, a package variable
func TestLostUploadAnswerStopsWhenTheProvisionEnds(t *testing.T) {
	manifestPkg.SetReleasePollTimes(t, time.Hour, 2*time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	director := &uploadDirector{uploadErr: lostUploadAnswer(), listedSignal: make(chan int, 4)}

	finished := make(chan error, 1)

	go func() { finished <- provisionReleases(ctx, director, newLineLogger(), nil) }()

	// The first listing finds what the director has. The second is the
	// poll's own.
	for polled := false; !polled; {
		select {
		case count := <-director.listedSignal:
			polled = count >= 2
		case err := <-finished:
			t.Fatalf("the provision ended before the poll listed the releases: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("the poll never listed the releases")
		}
	}

	cancel()

	var err error

	select {
	case err = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll did not stop when the provision's context ended")
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a DirectorCallError, got: %v", err)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected the error to carry the context's end, got: %v", err)
	}

	for _, event := range director.recorded() {
		if event == eventDeploy {
			t.Fatalf("expected no deploy, got: %q", director.recorded())
		}
	}
}

// The director's rejection of an upload still fails the provision at once,
// with no poll and no deploy.
//
//nolint:paralleltest // it shortens the release poll, a package variable
func TestRejectedUploadStillFailsAtOnce(t *testing.T) {
	manifestPkg.SetReleasePollTimes(t, time.Millisecond, time.Minute)

	rejection := fmt.Errorf("upload release: %w", errUnexpectedDirectorCall)
	director := &uploadDirector{uploadErr: rejection}

	err := provisionReleases(t.Context(), director, newLineLogger(), nil)
	if !errors.Is(err, rejection) {
		t.Fatalf("expected the rejection to fail the provision, got: %v", err)
	}

	got := director.recorded()
	if lists := listCount(got); lists != 1 || got[len(got)-1] != "upload https://releases.example.test/redis-18.tgz" {
		t.Fatalf("expected one listing, the upload, and nothing after it, got: %q", got)
	}
}
