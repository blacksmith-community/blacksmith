package parity_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"blacksmith/internal/parity"
)

// stopRecorder is a testing.TB that records a Fatal or a Skip and ends
// the goroutine the way the testing package does, without failing or
// skipping the test that runs it.
type stopRecorder struct {
	*testing.T

	fataled bool
	skipped bool
}

func (r *stopRecorder) Helper() {}

func (r *stopRecorder) Fatal(...any) {
	r.fataled = true

	runtime.Goexit()
}

func (r *stopRecorder) Skip(...any) {
	r.skipped = true

	runtime.Goexit()
}

// runRuby calls parity.Ruby with a recorder on a goroutine of its own, so
// the recorder's Goexit ends only that goroutine.
func runRuby(t *testing.T) (*stopRecorder, string) {
	t.Helper()

	recorder := &stopRecorder{T: t}

	var (
		path string
		done sync.WaitGroup
	)

	done.Add(1)

	go func() {
		defer done.Done()

		path = parity.Ruby(recorder)
	}()

	done.Wait()

	return recorder, path
}

// The cases change PATH and CI, which t.Setenv refuses in a parallel test.
//
//nolint:paralleltest // t.Setenv cannot run in a parallel test
func TestRuby(t *testing.T) {
	t.Run("missing outside CI skips", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("CI", "")

		recorder, _ := runRuby(t)
		if !recorder.skipped || recorder.fataled {
			t.Errorf("skipped %v, failed %v, want a skip", recorder.skipped, recorder.fataled)
		}
	})

	t.Run("missing in CI fails", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("CI", "true")

		recorder, _ := runRuby(t)
		if !recorder.fataled || recorder.skipped {
			t.Errorf("skipped %v, failed %v, want a failure", recorder.skipped, recorder.fataled)
		}
	})

	t.Run("present returns its path", func(t *testing.T) {
		dir := t.TempDir()
		ruby := filepath.Join(dir, "ruby")

		err := os.WriteFile(ruby, []byte("#!/bin/sh\n"), 0o700) //nolint:gosec // the stand-in must be executable
		if err != nil {
			t.Fatal(err)
		}

		t.Setenv("PATH", dir)
		t.Setenv("CI", "true")

		recorder, path := runRuby(t)
		if recorder.skipped || recorder.fataled {
			t.Errorf("skipped %v, failed %v, want neither", recorder.skipped, recorder.fataled)
		}

		if path != ruby {
			t.Errorf("Ruby returned %q, want the stand-in on PATH", path)
		}
	})
}
