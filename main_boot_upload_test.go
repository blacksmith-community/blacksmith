package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
)

const (
	bootUploadTaskID  = 5
	bootKindRelease   = "release"
	bootKindStemcell  = "stemcell"
	bootStateRunning  = "processing"
	bootStateFinished = "done"
)

var errBootUploadStillRunning = errors.New("boot upload task still running")

// uploadingDirector is a director for the boot uploads. Each upload starts
// task 5, which reports its states in turn, one per read, or never finishes
// when hang is set. It records every call, so a test can tell what the boot
// asked for and in which order. A call it does not script panics through the
// nil embedded Director.
type uploadingDirector struct {
	bosh.Director

	polls []string
	hang  bool

	mu    sync.Mutex
	calls []string
}

func (d *uploadingDirector) GetReleases(context.Context) ([]bosh.Release, error) {
	return []bosh.Release{}, nil
}

func (d *uploadingDirector) GetStemcells(context.Context) ([]bosh.Stemcell, error) {
	return []bosh.Stemcell{}, nil
}

func (d *uploadingDirector) UploadRelease(_ context.Context, url, _ string) (*bosh.Task, error) {
	d.record("upload " + url)

	return &bosh.Task{ID: bootUploadTaskID, State: bootStateRunning}, nil
}

func (d *uploadingDirector) UploadStemcell(_ context.Context, url, _ string) (*bosh.Task, error) {
	d.record("upload " + url)

	return &bosh.Task{ID: bootUploadTaskID, State: bootStateRunning}, nil
}

func (d *uploadingDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.record(fmt.Sprintf("wait %d", taskID))

	if d.hang {
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

		if state != bootStateRunning {
			return &bosh.Task{ID: taskID, State: state}, nil
		}
	}

	return nil, fmt.Errorf("%w: task %d", errBootUploadStillRunning, taskID)
}

func (d *uploadingDirector) CancelTask(_ context.Context, taskID int) error {
	d.record(fmt.Sprintf("cancel %d", taskID))

	return nil
}

func (d *uploadingDirector) record(call string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, call)
}

func (d *uploadingDirector) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.calls...)
}

// bootUploadConfig configures one upload of the given kind.
func bootUploadConfig(kind string) *config.Config {
	upload := []config.Uploadable{{
		Name:    "redis",
		Version: "18",
		URL:     "https://uploads.example.test/" + kind + ".tgz",
		SHA1:    "0123456789abcdef",
	}}

	cfg := &config.Config{}
	if kind == bootKindRelease {
		cfg.BOSH.Releases = upload
	} else {
		cfg.BOSH.Stemcells = upload
	}

	return cfg
}

// runBootUpload runs the boot upload of the given kind.
func runBootUpload(ctx context.Context, kind string, director bosh.Director, logger *recordingLogger) {
	if kind == bootKindRelease {
		uploadBOSHReleases(ctx, bootUploadConfig(kind), director, logger)
	} else {
		uploadBOSHStemcells(ctx, bootUploadConfig(kind), director, logger)
	}
}

//nolint:paralleltest // it shares the boot upload deadline with the test that shortens it
func TestBootUploadWaitsForTheTask(t *testing.T) {
	for _, kind := range []string{bootKindRelease, bootKindStemcell} {
		t.Run(kind, func(t *testing.T) {
			director := &uploadingDirector{polls: []string{bootStateRunning, bootStateRunning, bootStateFinished}}
			logger := newRecordingLogger()

			runBootUpload(context.Background(), kind, director, logger)

			want := []string{
				"upload https://uploads.example.test/" + kind + ".tgz",
				"wait 5",
				"task 5 processing",
				"task 5 processing",
				"task 5 done",
			}

			got := director.recorded()
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("expected the boot to wait for the upload task\nwant: %q\ngot:  %q", want, got)
			}

			info := strings.Join(logger.lines("info"), "\n")
			if !strings.Contains(info, "BOSH task 5") || !strings.Contains(info, "redis/18") || !strings.Contains(info, "ended in state done") {
				t.Errorf("expected an Info line naming the task, the %s, and its outcome, got:\n%s", kind, info)
			}
		})
	}
}

//nolint:paralleltest // it shortens the boot upload deadline, a package variable
func TestBootUploadDeadlineLeavesTheTaskRunningAndSaysSo(t *testing.T) {
	previous := bootUploadTimeout
	bootUploadTimeout = 50 * time.Millisecond

	t.Cleanup(func() { bootUploadTimeout = previous })

	for _, kind := range []string{bootKindRelease, bootKindStemcell} {
		t.Run(kind, func(t *testing.T) {
			director := &uploadingDirector{hang: true}
			logger := newRecordingLogger()

			finished := make(chan struct{})

			go func() {
				defer close(finished)

				runBootUpload(context.Background(), kind, director, logger)
			}()

			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("the boot upload did not stop at its deadline")
			}

			errorLines := strings.Join(logger.lines("error"), "\n")
			for _, want := range []string{"BOSH task 5", "redis/18", "left it running", "next boot repeats the upload"} {
				if !strings.Contains(errorLines, want) {
					t.Errorf("expected the Error line to say %q, got:\n%s", want, errorLines)
				}
			}

			for _, call := range director.recorded() {
				if strings.HasPrefix(call, "cancel") {
					t.Errorf("expected no cancel, got: %q", director.recorded())
				}
			}
		})
	}
}
