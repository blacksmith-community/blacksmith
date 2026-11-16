package vmmonitor_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/logger"
)

// concurrencyConfig is a monitor config that sets only the worker count.
type concurrencyConfig struct{ workers int }

func (c concurrencyConfig) GetVMMonitoringMaxConcurrent() int { return c.workers }

// warnCapture records the lines logged at the Warn level.
type warnCapture struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (w *warnCapture) Warn(msg string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.lines = append(w.lines, fmt.Sprintf(msg, args...))
}

func (w *warnCapture) Named(string) logger.Logger { return w }

// peakChecks runs one check cycle over instances whose VM listings wait
// until want of them are listing at once, and returns the most listings
// that ran at the same time. A monitor that runs fewer than want at once
// leaves its listings waiting until the wait ends, and reports its limit.
func peakChecks(t *testing.T, cfg interface{}, want, instances int) int {
	t.Helper()

	var (
		lock    sync.Mutex
		active  int
		peak    int
		reached = make(chan struct{})
		once    sync.Once
	)

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
		lock.Lock()
		active++

		if active > peak {
			peak = active
		}

		if active == want {
			once.Do(func() { close(reached) })
		}

		lock.Unlock()

		select {
		case <-reached:
		case <-time.After(time.Second):
		}

		lock.Lock()
		active--
		lock.Unlock()

		return []bosh.VM{{ID: name + "-vm", JobState: vmStatusRunning}}, nil
	}

	entries := instanceEntries(instances)

	monitor := vmmonitor.New(nil, director, noClaims{}, cfg)
	monitor.UseVault(&indexVault{entries: entries})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	monitor.CheckScheduledServices(t.Context())

	lock.Lock()
	defer lock.Unlock()

	return peak
}

// vm_monitoring.max_concurrent sets how many instances one check cycle
// checks at once.
func TestMonitorWorkerCountComesFromTheConfig(t *testing.T) {
	t.Parallel()

	for _, workers := range []int{1, 2, 5} {
		got := peakChecks(t, concurrencyConfig{workers: workers}, workers, workers+2)
		if got != workers {
			t.Errorf("with max_concurrent %d, %d checks ran at once, want %d", workers, got, workers)
		}
	}
}

// A config that does not offer a worker count gets the default of 3.
func TestMonitorWorkerCountDefaultsToThree(t *testing.T) {
	t.Parallel()

	if got := peakChecks(t, struct{}{}, 3, 5); got != 3 {
		t.Errorf("with no worker count configured, %d checks ran at once, want 3", got)
	}
}

// The loaded config hands the monitor the key's value.
func TestLoadedConfigOffersTheWorkerCount(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.VMMonitoring.MaxConcurrent = 6

	if got := peakChecks(t, cfg, 6, 8); got != 6 {
		t.Errorf("with a loaded max_concurrent of 6, %d checks ran at once, want 6", got)
	}
}

// A worker count below one falls back to 3, and the monitor warns with the
// bad value, the key, and the default. A usable count draws no warning.
func TestMonitorWarnsOfAnUnusableWorkerCount(t *testing.T) { //nolint:paralleltest // swaps the global logger
	// The test swaps the global logger, so it does not run in parallel.
	capture := &warnCapture{NoOpLogger: &logger.NoOpLogger{}}
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	for _, workers := range []int{-2, 0} {
		if got := peakChecks(t, concurrencyConfig{workers: workers}, 3, 5); got != 3 {
			t.Errorf("with max_concurrent %d, %d checks ran at once, want the default of 3", workers, got)
		}

		capture.mu.Lock()
		lines := append([]string(nil), capture.lines...)
		capture.mu.Unlock()

		wants := []string{fmt.Sprintf("is %d", workers), "vm_monitoring.max_concurrent", "default of 3"}
		if !slices.ContainsFunc(lines, func(line string) bool { return containsAll(line, wants) }) {
			t.Errorf("no warning for max_concurrent %d contains %q, got %q", workers, wants, lines)
		}
	}

	capture.mu.Lock()
	before := len(capture.lines)
	capture.mu.Unlock()

	vmmonitor.New(nil, goneDirector(nil), noClaims{}, concurrencyConfig{workers: 4})

	capture.mu.Lock()
	defer capture.mu.Unlock()

	if len(capture.lines) != before {
		t.Errorf("a usable max_concurrent of 4 drew a warning: %q", capture.lines[before:])
	}
}

// containsAll reports whether the line contains every one of the parts.
func containsAll(line string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(line, part) {
			return false
		}
	}

	return true
}

// After a shutdown signal the check cycle stops dispatching. The instances it
// did not reach stay due, so the next cycle checks them.
func TestCheckCycleStopsDispatchingAfterShutdown(t *testing.T) {
	t.Parallel()

	const instances = 4

	var (
		lock    sync.Mutex
		listed  int
		started = make(chan struct{}, instances)
		release = make(chan struct{})
	)

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
		lock.Lock()
		listed++
		lock.Unlock()

		started <- struct{}{}

		<-release

		return []bosh.VM{{ID: name + "-vm", JobState: vmStatusRunning}}, nil
	}

	entries := instanceEntries(instances)

	monitor := vmmonitor.New(nil, director, noClaims{}, concurrencyConfig{workers: 1})
	monitor.UseVault(&indexVault{entries: entries})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan struct{})

	go func() {
		defer close(done)

		monitor.CheckScheduledServices(ctx)
	}()

	<-started
	cancel()
	// The dispatch loop is parked on the full semaphore, so it sees the
	// cancellation at once. The pause lets it do that before the running
	// check frees its slot.
	time.Sleep(100 * time.Millisecond)
	close(release)
	<-done

	lock.Lock()
	first := listed
	lock.Unlock()

	if first != 1 {
		t.Fatalf("the cycle listed %d instances after the shutdown signal, want only the one already running", first)
	}

	monitor.CheckScheduledServices(t.Context())

	lock.Lock()
	defer lock.Unlock()

	if listed != instances {
		t.Errorf("the next cycle brought the listings to %d, want %d, so the undispatched instances must stay due", listed, instances)
	}
}

// instanceEntries returns the vault index entries of that many instances.
func instanceEntries(count int) map[string]interface{} {
	entries := map[string]interface{}{}

	for index := range count {
		id := fmt.Sprintf("instance-%d", index)
		entry := oldEntry()
		entry[nameKey] = "standalone-valkey-" + id
		entries[id] = entry
	}

	return entries
}

// A cycle that starts with a cancelled context dispatches nothing, whether or
// not a slot is free, and the next cycle still finds every instance due.
func TestCancelledCycleDispatchesNothing(t *testing.T) {
	t.Parallel()

	const instances = 3

	var (
		lock   sync.Mutex
		listed int
	)

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
		lock.Lock()
		listed++
		lock.Unlock()

		return []bosh.VM{{ID: name + "-vm", JobState: vmStatusRunning}}, nil
	}

	monitor := vmmonitor.New(nil, director, noClaims{}, concurrencyConfig{workers: instances})
	monitor.UseVault(&indexVault{entries: instanceEntries(instances)})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	monitor.CheckScheduledServices(ctx)

	lock.Lock()
	first := listed
	lock.Unlock()

	if first != 0 {
		t.Fatalf("a cancelled cycle listed %d instances, want none", first)
	}

	monitor.CheckScheduledServices(t.Context())

	lock.Lock()
	defer lock.Unlock()

	if listed != instances {
		t.Errorf("the next cycle brought the listings to %d, want %d", listed, instances)
	}
}
