package certinventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testWait = 5 * time.Second

// fakeAfter hands out channels the test fires by hand and records every
// duration the scheduler asks to wait.
type fakeAfter struct {
	mu    sync.Mutex
	durs  []time.Duration
	chans []chan time.Time
	asked chan time.Duration
}

func newFakeAfter() *fakeAfter {
	return &fakeAfter{asked: make(chan time.Duration, 64)}
}

func (f *fakeAfter) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)

	f.mu.Lock()
	f.durs = append(f.durs, d)
	f.chans = append(f.chans, ch)
	f.mu.Unlock()

	f.asked <- d

	return ch
}

func (f *fakeAfter) fire(t *testing.T, i int) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if i >= len(f.chans) {
		t.Fatalf("wait %d was never requested", i)
	}

	f.chans[i] <- time.Now()
}

func (f *fakeAfter) next(t *testing.T) time.Duration {
	t.Helper()

	select {
	case d := <-f.asked:
		return d
	case <-time.After(testWait):
		t.Fatal("the scheduler never asked to wait")

		return 0
	}
}

// fakeScanner counts calls, tracks how many scans run at once, and can hold
// any scan until the test releases it.
type fakeScanner struct {
	mu          sync.Mutex
	calls       map[string]int
	inFlight    int
	maxInFlight int
	block       chan struct{}            // holds every scan while open
	gates       map[string]chan struct{} // holds one instance's scans
	started     chan string
	result      func(ref InstanceRef, n int) InstanceScan
}

func newFakeScanner() *fakeScanner {
	return &fakeScanner{
		calls:   map[string]int{},
		gates:   map[string]chan struct{}{},
		started: make(chan string, 256),
	}
}

func (f *fakeScanner) ScanInstance(ctx context.Context, ref InstanceRef) InstanceScan {
	return f.scan(ctx, ref)
}

func (f *fakeScanner) ScanBroker(ctx context.Context, _ BrokerSources) InstanceScan {
	return f.scan(ctx, InstanceRef{ID: BrokerScanID, Name: "broker"})
}

func (f *fakeScanner) scan(ctx context.Context, ref InstanceRef) InstanceScan {
	f.mu.Lock()
	f.calls[ref.ID]++
	n := f.calls[ref.ID]
	f.inFlight++

	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}

	block, gate := f.block, f.gates[ref.ID]
	f.mu.Unlock()

	f.started <- ref.ID

	for _, ch := range []chan struct{}{block, gate} {
		if ch == nil {
			continue
		}

		select {
		case <-ch:
		case <-ctx.Done():
		}
	}

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	if f.result != nil {
		return f.result(ref, n)
	}

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	scan := cachedScan(ref.ID, at)
	scan.Ref = ref

	return scan
}

func (f *fakeScanner) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[id]
}

func (f *fakeScanner) waitStarted(t *testing.T, n int) []string {
	t.Helper()

	var ids []string

	for len(ids) < n {
		select {
		case id := <-f.started:
			ids = append(ids, id)
		case <-time.After(testWait):
			t.Fatalf("only %d of %d scans started", len(ids), n)
		}
	}

	return ids
}

func refs(ids ...string) []InstanceRef {
	out := make([]InstanceRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, InstanceRef{ID: id, Name: "name-" + id, Deployment: "svc-" + id})
	}

	return out
}

// listerOf returns a lister that serves whatever the pointer holds at the
// time of the call.
func listerOf(current *atomic.Pointer[[]InstanceRef]) InstanceLister {
	return func(context.Context) ([]InstanceRef, error) {
		return *current.Load(), nil
	}
}

func staticLister(ids ...string) InstanceLister {
	list := refs(ids...)

	return func(context.Context) ([]InstanceRef, error) { return list, nil }
}

func settingsWith(concurrency, interval int) func() Settings {
	return func() Settings {
		s := DefaultSettings()
		s.ScanConcurrency = concurrency
		s.ScanIntervalMinutes = interval

		return s
	}
}

func newTestScheduler(t *testing.T, sc instanceScanner, cache *Cache, list InstanceLister, broker func() BrokerSources, settings func() Settings) *Scheduler {
	t.Helper()

	s := NewScheduler(sc, cache, list, broker, settings, time.Now, newFakeAfter().after)
	t.Cleanup(s.Stop)

	return s
}

// waitScan waits until the fleet scan with the given ID has finished.
func waitScan(t *testing.T, s *Scheduler, id string) {
	t.Helper()

	s.mu.Lock()
	run := s.run
	s.mu.Unlock()

	if run == nil || run.id != id {
		t.Fatalf("scan %q is not the current scan", id)
	}

	select {
	case <-run.done:
	case <-time.After(testWait):
		t.Fatalf("scan %s never finished", id)
	}
}

func TestRequestScanSingleFlight(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.block = make(chan struct{})

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a", "b"), nil, settingsWith(4, 60))

	id, running := s.RequestScan(nil)
	if id == "" || running {
		t.Fatalf("first request = %q, %v; want a new scan", id, running)
	}

	sc.waitStarted(t, 2)

	if st := s.State(); st.State != ScanRunning || st.ID != id || st.Total != 2 || st.StartedAt == nil || st.CompletedAt != nil {
		t.Errorf("state while running = %+v", st)
	}

	again, running := s.RequestScan(nil)
	if again != id || !running {
		t.Errorf("second request = %q, %v; want %q, true", again, running, id)
	}

	subset, running := s.RequestScan([]string{"a"})
	if subset != id || !running {
		t.Errorf("subset request = %q, %v; want the running scan %q", subset, running, id)
	}

	close(sc.block)
	waitScan(t, s, id)

	for _, inst := range []string{"a", "b"} {
		if n := sc.count(inst); n != 1 {
			t.Errorf("%s scanned %d times, want 1", inst, n)
		}
	}

	st := s.State()
	if st.State != ScanIdle || st.Done != 2 || st.CompletedAt == nil || len(st.Errors) != 0 {
		t.Errorf("state after the scan = %+v", st)
	}

	next, running := s.RequestScan(nil)
	if next == id || running {
		t.Errorf("a request after the scan = %q, %v; want a new scan", next, running)
	}

	waitScan(t, s, next)
}

func TestFleetScanUsesConcurrency(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.block = make(chan struct{})

	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("i%02d", i)
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister(ids...), nil, settingsWith(3, 60))

	id, _ := s.RequestScan(nil)
	sc.waitStarted(t, 3)

	// Give a fourth worker the chance to start, which it must not.
	select {
	case extra := <-sc.started:
		t.Fatalf("a fourth scan (%s) started while three were held", extra)
	case <-time.After(50 * time.Millisecond):
	}

	close(sc.block)
	waitScan(t, s, id)

	sc.mu.Lock()
	peak := sc.maxInFlight
	sc.mu.Unlock()

	if peak != 3 {
		t.Errorf("peak scans in flight = %d, want 3", peak)
	}

	for _, inst := range ids {
		if n := sc.count(inst); n != 1 {
			t.Errorf("%s scanned %d times, want 1", inst, n)
		}
	}

	if got := len(cache.Snapshot()); got != 10 {
		t.Errorf("cache holds %d scans, want 10", got)
	}
}

func TestFleetScanClampsConcurrency(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ setting, want int }{{0, 4}, {-2, 4}, {1, 1}, {16, 16}, {40, 16}} {
		if got := scanConcurrency(Settings{ScanConcurrency: tc.setting}); got != tc.want {
			t.Errorf("scanConcurrency(%d) = %d, want %d", tc.setting, got, tc.want)
		}
	}

	for _, tc := range []struct {
		setting int
		want    time.Duration
	}{{0, time.Hour}, {3, time.Hour}, {5, 5 * time.Minute}, {90, 90 * time.Minute}} {
		if got := scanInterval(Settings{ScanIntervalMinutes: tc.setting}); got != tc.want {
			t.Errorf("scanInterval(%d) = %s, want %s", tc.setting, got, tc.want)
		}
	}
}

func TestFleetScanDropsInstancesRemovedFromIndex(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var current atomic.Pointer[[]InstanceRef]

	both := refs("a", "b")
	current.Store(&both)

	var cache Cache

	broker := func() BrokerSources { return BrokerSources{} }
	s := newTestScheduler(t, sc, &cache, listerOf(&current), broker, settingsWith(2, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	for _, want := range []string{"a", "b", BrokerScanID} {
		if _, ok := cache.Get(want); !ok {
			t.Fatalf("after the first scan the cache lacks %s", want)
		}
	}

	onlyA := refs("a")
	current.Store(&onlyA)

	id, _ = s.RequestScan(nil)
	waitScan(t, s, id)

	if _, ok := cache.Get("b"); ok {
		t.Error("b stayed in the cache after leaving the index")
	}

	if _, ok := cache.Get("a"); !ok {
		t.Error("a left the cache")
	}

	if _, ok := cache.Get(BrokerScanID); !ok {
		t.Error("the broker scan left the cache")
	}

	if st := s.State(); st.Total != 2 || st.Done != 2 {
		t.Errorf("progress = %d of %d, want 2 of 2 (a and the broker)", st.Done, st.Total)
	}
}

func TestRequestScanSubset(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a", "b", "c"), func() BrokerSources { return BrokerSources{} }, settingsWith(2, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	id, _ = s.RequestScan([]string{"b", "unknown"})
	waitScan(t, s, id)

	for inst, want := range map[string]int{"a": 1, "b": 2, "c": 1, BrokerScanID: 1} {
		if n := sc.count(inst); n != want {
			t.Errorf("%s scanned %d times, want %d", inst, n, want)
		}
	}

	if got := len(cache.Snapshot()); got != 4 {
		t.Errorf("a subset scan changed the cache to %d entries, want 4", got)
	}

	if st := s.State(); st.Total != 1 {
		t.Errorf("subset total = %d, want 1", st.Total)
	}
}

func TestFailedRescanKeepsStaleData(t *testing.T) {
	t.Parallel()

	first := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	sc := newFakeScanner()
	sc.result = func(ref InstanceRef, n int) InstanceScan {
		if n == 1 {
			scan := cachedScan(ref.ID, first)
			scan.Ref = ref

			return scan
		}

		return InstanceScan{Ref: ref, ScannedAt: second, Err: "the instance scan timed out after 1m0s"}
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a"), nil, settingsWith(4, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	id, _ = s.RequestScan(nil)
	waitScan(t, s, id)

	got, ok := cache.Get("a")
	if !ok {
		t.Fatal("the failed rescan dropped the instance")
	}

	if !got.Stale || len(got.Material.Raw) != 2 || got.Ref.Name != "name-a" {
		t.Fatalf("after the failed rescan: stale %v, %d observations, ref %+v", got.Stale, len(got.Material.Raw), got.Ref)
	}

	for _, r := range got.Material.Raw {
		if r.State != StateStale || !r.ObservedAt.Equal(first) {
			t.Errorf("%s: state %q observed %v", r.Locator, r.State, r.ObservedAt)
		}
	}

	st := s.State()
	if len(st.Errors) != 1 {
		t.Fatalf("errors = %+v, want one", st.Errors)
	}

	e := st.Errors[0]
	if e.InstanceID != "a" || e.Error != "the instance scan timed out after 1m0s" || e.LastSuccessAt == nil || !e.LastSuccessAt.Equal(first) {
		t.Errorf("error = %+v, want a with the first scan's time", e)
	}

	if st.State != ScanFailed {
		t.Errorf("state = %q, want failed when every scan failed", st.State)
	}
}

func TestFirstFailureHasNoLastSuccess(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.result = func(ref InstanceRef, _ int) InstanceScan {
		if ref.ID == "bad" {
			return InstanceScan{Ref: ref, ScannedAt: time.Now(), Err: "list the VMs: refused"}
		}

		scan := cachedScan(ref.ID, time.Now())
		scan.Ref = ref

		return scan
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("bad", "good"), nil, settingsWith(4, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	st := s.State()
	if st.State != ScanIdle || len(st.Errors) != 1 || st.Errors[0].InstanceID != "bad" || st.Errors[0].LastSuccessAt != nil {
		t.Errorf("state = %+v", st)
	}

	got, ok := cache.Get("bad")
	if !ok || got.Err != "list the VMs: refused" || got.Ref.Name != "name-bad" || got.Stale {
		t.Errorf("first failure entry = %+v, %v", got, ok)
	}
}

func TestFleetScanListerFailure(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var cache Cache

	cache.Put(cachedScan("a", time.Now()))

	list := func(context.Context) ([]InstanceRef, error) { return nil, errors.New("vault sealed") }
	s := newTestScheduler(t, sc, &cache, list, func() BrokerSources { return BrokerSources{} }, settingsWith(4, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	st := s.State()
	if st.State != ScanFailed || len(st.Errors) != 1 || st.Errors[0].Error != "list the instances: vault sealed" {
		t.Errorf("state = %+v", st)
	}

	if _, ok := cache.Get("a"); !ok {
		t.Error("a failed listing dropped cached instances")
	}

	if sc.count(BrokerScanID) != 1 {
		t.Error("the broker was not scanned when the listing failed")
	}
}

func TestForcedScanSerialisesWithFleet(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	gate := make(chan struct{})
	sc.gates["a"] = gate

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a", "b"), nil, settingsWith(4, 60))

	type result struct {
		scan InstanceScan
		err  error
	}

	forced := make(chan result, 1)

	go func() {
		scan, err := s.ScanInstanceNow(context.Background(), "a")
		forced <- result{scan, err}
	}()

	if got := sc.waitStarted(t, 1); got[0] != "a" {
		t.Fatalf("the forced scan started %s", got[0])
	}

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	if n := sc.count("a"); n != 1 {
		t.Errorf("the fleet scanned a while the forced scan held it (%d calls)", n)
	}

	if n := sc.count("b"); n != 1 {
		t.Errorf("b scanned %d times, want 1", n)
	}

	if st := s.State(); st.Done != 2 || st.Total != 2 {
		t.Errorf("progress = %d of %d, want the skipped instance counted", st.Done, st.Total)
	}

	close(gate)

	select {
	case r := <-forced:
		if r.err != nil || r.scan.Ref.ID != "a" || len(r.scan.Material.Raw) != 2 {
			t.Errorf("forced scan = %+v, %v", r.scan, r.err)
		}
	case <-time.After(testWait):
		t.Fatal("the forced scan never returned")
	}

	if _, ok := cache.Get("a"); !ok {
		t.Error("the forced scan was not cached")
	}
}

func TestScanInstanceNowUnknown(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a"), nil, settingsWith(4, 60))

	if _, err := s.ScanInstanceNow(context.Background(), "nope"); !errors.Is(err, ErrUnknownInstance) {
		t.Errorf("unknown instance err = %v, want ErrUnknownInstance", err)
	}

	if _, err := s.ScanInstanceNow(context.Background(), BrokerScanID); !errors.Is(err, ErrUnknownInstance) {
		t.Errorf("broker without sources err = %v, want ErrUnknownInstance", err)
	}

	if sc.count("nope") != 0 || sc.count(BrokerScanID) != 0 {
		t.Error("an unknown instance was scanned")
	}
}

func TestScanInstanceNowBroker(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a"), func() BrokerSources { return BrokerSources{} }, settingsWith(4, 60))

	scan, err := s.ScanInstanceNow(context.Background(), BrokerScanID)
	if err != nil || scan.Ref.ID != BrokerScanID {
		t.Errorf("broker scan = %+v, %v", scan, err)
	}
}

func TestScanInstanceNowCancelled(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.gates["a"] = make(chan struct{})
	sc.result = func(ref InstanceRef, _ int) InstanceScan {
		return InstanceScan{Ref: ref, ScannedAt: time.Now(), Err: "the scan was cancelled"}
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a"), nil, settingsWith(4, 60))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := s.ScanInstanceNow(ctx, "a")
		done <- err
	}()

	sc.waitStarted(t, 1)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(testWait):
		t.Fatal("the cancelled scan never returned")
	}

	if _, ok := cache.Get("a"); ok {
		t.Error("a cancelled scan was stored")
	}
}

func TestFirstScanDelay(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	fa := newFakeAfter()

	var interval atomic.Int64

	interval.Store(60)

	settings := func() Settings {
		s := DefaultSettings()
		s.ScanIntervalMinutes = int(interval.Load())

		return s
	}

	var cache Cache

	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := NewScheduler(sc, &cache, staticLister("a", "b"), nil, settings, func() time.Time { return start }, fa.after)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Start(ctx)
	defer s.Stop()

	first := fa.next(t)
	if first < 30*time.Second || first > 35*time.Second {
		t.Errorf("first wait = %s, want 30s to 35s", first)
	}

	if next := s.State().NextAt; !next.Equal(start.Add(first)) {
		t.Errorf("next_at = %v, want %v", next, start.Add(first))
	}

	time.Sleep(20 * time.Millisecond)

	if sc.count("a")+sc.count("b") != 0 {
		t.Fatal("the scanner ran before the first wait ended")
	}

	interval.Store(17)
	fa.fire(t, 0)

	if second := fa.next(t); second != 17*time.Minute {
		t.Errorf("second wait = %s, want the 17m read when it began", second)
	}

	if sc.count("a") != 1 || sc.count("b") != 1 {
		t.Errorf("scans after the first wait: a %d, b %d, want exactly one fleet scan", sc.count("a"), sc.count("b"))
	}

	if next := s.State().NextAt; !next.Equal(start.Add(17 * time.Minute)) {
		t.Errorf("next_at = %v, want the interval from now", next)
	}

	interval.Store(5)
	fa.fire(t, 1)

	if third := fa.next(t); third != 5*time.Minute {
		t.Errorf("third wait = %s, want 5m", third)
	}

	if sc.count("a") != 2 {
		t.Errorf("a scanned %d times after two waits, want 2", sc.count("a"))
	}
}

func TestStopEndsSchedulerAndRefusesScans(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.block = make(chan struct{})
	fa := newFakeAfter()

	var cache Cache

	s := NewScheduler(sc, &cache, staticLister("a"), nil, settingsWith(4, 60), nil, fa.after)
	s.Start(context.Background())
	fa.next(t)

	id, _ := s.RequestScan(nil)
	sc.waitStarted(t, 1)

	stopped := make(chan struct{})

	go func() {
		s.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(testWait):
		t.Fatal("Stop did not cancel the running scan and the wait")
	}

	if id == "" {
		t.Error("the scan before Stop had no ID")
	}

	if again, running := s.RequestScan(nil); again != "" || running {
		t.Errorf("a request after Stop = %q, %v; want nothing started", again, running)
	}

	s.Stop() // a second Stop is harmless
}

func TestStartContextCancelStopsLoop(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	fa := newFakeAfter()

	var cache Cache

	s := NewScheduler(sc, &cache, staticLister("a"), nil, settingsWith(4, 60), nil, fa.after)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	fa.next(t)
	cancel()

	stopped := make(chan struct{})

	go func() {
		s.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(testWait):
		t.Fatal("the loop kept running after its context ended")
	}

	if sc.count("a") != 0 {
		t.Error("a scan ran after the context ended")
	}
}

func TestScanStateJSON(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	completed := started.Add(72 * time.Second)
	last := started.Add(-time.Hour)

	st := ScanState{
		ID: "c1f0", State: ScanIdle, StartedAt: &started, CompletedAt: &completed,
		NextAt: started.Add(time.Hour), Done: 58, Total: 58,
		Errors: []ScanError{{InstanceID: "7a1b", Error: "scan timed out after 60s", LastSuccessAt: &last}},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"id":"c1f0","state":"idle","started_at":"2026-10-21T14:00:00Z","completed_at":"2026-10-21T14:01:12Z",` +
		`"next_at":"2026-10-21T15:00:00Z","progress":{"done":58,"total":58},` +
		`"errors":[{"instance_id":"7a1b","error":"scan timed out after 60s","last_success_at":"2026-10-21T13:00:00Z"}]}`
	if string(data) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", data, want)
	}

	empty, err := json.Marshal(ScanState{State: ScanIdle})
	if err != nil {
		t.Fatal(err)
	}

	if string(empty) != `{"id":"","state":"idle","progress":{"done":0,"total":0},"errors":[]}` {
		t.Errorf("empty JSON = %s", empty)
	}
}

func TestSchedulerAcceptsScannerInterface(t *testing.T) {
	t.Parallel()

	var _ instanceScanner = (*Scanner)(nil)
}

// TestKeepLastGoodStaleObservationShowsLatest checks that the stale CredHub
// observation a failed rescan carries describes the latest version, like
// the observation of a good scan.
func TestKeepLastGoodStaleObservationShowsLatest(t *testing.T) {
	t.Parallel()

	deployed := issue(t, issueSpec{cn: "valkey", serial: 1})
	latest := issue(t, issueSpec{cn: "valkey", serial: 2})
	name := "/ws-bosh/svc-a/valkey_crt"
	owner := Owner{Kind: OwnerInstance, InstanceID: "a"}
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	prev := InstanceScan{ScannedAt: at, Material: ScanMaterial{CredHub: []CredHubCert{{
		Name: name, Owner: owner, Deployed: deployed.cert, Latest: latest.cert,
		Versions: Versions{DeployedVersionID: "v1", LatestVersionID: "v2"},
	}}}}
	next := ScanMaterial{CredHub: []CredHubCert{{Name: name, Owner: owner, Err: msgCredHubUnavailable}}}

	got := keepLastGood(next, prev)

	if len(got.Raw) != 1 {
		t.Fatalf("got %d raw observations, want 1", len(got.Raw))
	}

	r := got.Raw[0]
	if r.Source != SourceCredHub || r.State != StateStale || r.Error != "" || !r.ObservedAt.Equal(at) {
		t.Errorf("stale observation %+v", r.Observation)
	}

	if r.VersionID != "v2" || r.Serial != latest.cert.SerialNumber.Text(16) ||
		r.Fingerprint != Fingerprint(latest.cert) || r.Cert != latest.cert {
		t.Errorf("stale observation version %q, serial %q, want the latest version", r.VersionID, r.Serial)
	}
}

func TestRescanKeepsLastGoodCertificate(t *testing.T) {
	t.Parallel()

	good := issue(t, issueSpec{cn: "redis.bosh", serial: 9})
	name := "/ws-bosh/svc-a/redis_crt"
	first := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	notAfter := good.cert.NotAfter

	sc := newFakeScanner()
	sc.result = func(ref InstanceRef, n int) InstanceScan {
		at := first.Add(time.Duration(n-1) * time.Hour)
		owner := Owner{Kind: OwnerInstance, InstanceID: ref.ID}

		ch := CredHubCert{Name: name, Owner: owner, Versions: Versions{DeployedVersionID: "v1"}}
		if n == 1 {
			ch.Deployed, ch.Latest = good.cert, good.cert
			ch.Versions = Versions{CertificateID: "cert-id", DeployedVersionID: "v1", LatestVersionID: "v1",
				DeployedNotAfter: &notAfter, LatestNotAfter: &notAfter}
		} else {
			ch.Err = "CredHub unavailable: certificate metadata: 503"
		}

		return InstanceScan{Ref: ref, Material: ScanMaterial{CredHub: []CredHubCert{ch}}, ScannedAt: at, LastSuccessAt: at}
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("a"), nil, settingsWith(4, 60))

	for round := 1; round <= 3; round++ {
		id, _ := s.RequestScan(nil)
		waitScan(t, s, id)

		if round == 1 {
			continue
		}

		got, _ := cache.Get("a")
		if len(got.Material.CredHub) != 1 {
			t.Fatalf("round %d: CredHub = %d entries", round, len(got.Material.CredHub))
		}

		c := got.Material.CredHub[0]
		if c.Deployed == nil || Fingerprint(c.Deployed) != Fingerprint(good.cert) {
			t.Fatalf("round %d: the last good certificate was lost", round)
		}

		if c.Err != "CredHub unavailable: certificate metadata: 503" {
			t.Errorf("round %d: Err = %q, want the new error", round, c.Err)
		}

		if c.Versions.CertificateID != "cert-id" || c.Versions.DeployedNotAfter == nil {
			t.Errorf("round %d: versions = %+v, want the last good ones", round, c.Versions)
		}

		var stale []RawObservation

		for _, r := range got.Material.Raw {
			if r.Source == SourceCredHub && r.Locator == name && r.State == StateStale {
				stale = append(stale, r)
			}
		}

		if len(stale) != 1 || !stale[0].ObservedAt.Equal(first) || stale[0].Fingerprint != Fingerprint(good.cert) {
			t.Errorf("round %d: stale observations = %+v, want one from the first scan", round, stale)
		}

		cov := coverageFor(t, got.Material, name)
		if !cov.Stale || cov.Complete {
			t.Errorf("round %d: coverage = %+v, want stale and incomplete", round, cov)
		}
	}

	if sc.count("a") != 3 {
		t.Errorf("scans = %d, want 3", sc.count("a"))
	}
}

func TestTimedScanFollowsSubsetRun(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.gates["a"] = make(chan struct{})
	fa := newFakeAfter()

	var cache Cache

	s := NewScheduler(sc, &cache, staticLister("a", "b"), nil, settingsWith(4, 60), time.Now, fa.after)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Start(ctx)
	defer s.Stop()

	fa.next(t)

	subset, _ := s.RequestScan([]string{"a"})
	sc.waitStarted(t, 1)

	// The timed scan comes due while the subset run holds the slot.
	fa.fire(t, 0)
	time.Sleep(50 * time.Millisecond)

	if sc.count("b") != 0 {
		t.Fatal("b was scanned while the subset run held the slot")
	}

	close(sc.gates["a"])
	waitScan(t, s, subset)

	if second := fa.next(t); second != 60*time.Minute {
		t.Errorf("wait after the full scan = %s, want 60m", second)
	}

	if sc.count("a") != 2 || sc.count("b") != 1 {
		t.Errorf("scans: a %d, b %d; want a full scan after the subset run", sc.count("a"), sc.count("b"))
	}

	if s.State().ID == subset {
		t.Error("the state still shows the subset run")
	}
}

func TestDropSkipsLockedInstances(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()

	var current atomic.Pointer[[]InstanceRef]

	both := refs("a", "b")
	current.Store(&both)

	var cache Cache

	s := newTestScheduler(t, sc, &cache, listerOf(&current), nil, settingsWith(2, 60))

	id, _ := s.RequestScan(nil)
	waitScan(t, s, id)

	onlyA := refs("a")
	current.Store(&onlyA)

	// A forced scan of b holds its lock while the fleet scan finishes.
	lock := s.lockFor("b")
	lock <- struct{}{}

	id, _ = s.RequestScan(nil)
	waitScan(t, s, id)

	if _, ok := cache.Get("b"); !ok {
		t.Error("b was dropped while its lock was held")
	}

	<-lock

	id, _ = s.RequestScan(nil)
	waitScan(t, s, id)

	if _, ok := cache.Get("b"); ok {
		t.Error("b stayed in the cache once its lock was free")
	}
}

func TestStopCancelsScanInstanceNow(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.gates["a"] = make(chan struct{})

	var cache Cache

	s := NewScheduler(sc, &cache, staticLister("a"), nil, settingsWith(4, 60), time.Now, newFakeAfter().after)

	done := make(chan error, 1)

	go func() {
		_, err := s.ScanInstanceNow(context.Background(), "a")
		done <- err
	}()

	sc.waitStarted(t, 1)
	s.Stop()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(testWait):
		t.Fatal("Stop left the forced scan running")
	}

	if _, ok := cache.Get("a"); ok {
		t.Error("a scan cancelled by Stop was stored")
	}
}

// startLoggedScheduler starts a scheduler on the timer the test drives,
// logging to a recorder, and waits for its first wait.
func startLoggedScheduler(t *testing.T, sc instanceScanner, cache *Cache, list InstanceLister) (*Scheduler, *fakeAfter, *recLogger) {
	t.Helper()

	fa := newFakeAfter()
	log := newRecLogger()

	s := NewScheduler(sc, cache, list, nil, settingsWith(4, 60), nil, fa.after)
	s.SetLogger(log)
	s.Start(t.Context())
	t.Cleanup(s.Stop)

	fa.next(t)

	return s, fa, log
}

// expectPanicLogged fails the test unless one line logs the panic with
// the stack of the goroutine it happened on.
func expectPanicLogged(t *testing.T, log *recLogger, value, frame string) {
	t.Helper()

	if lines := log.logged(value, "goroutine ", frame); len(lines) != 1 {
		t.Fatalf("expected the panic %q logged once with its stack, got %q", value, log.logged())
	}
}

// A scheduled scan that panics fails that scan alone, which the cache and
// the state record, logs the panic with its stack, and the timer scans
// again.
func TestScheduledScanPanicFailsThatScan(t *testing.T) {
	t.Parallel()

	sc := newFakeScanner()
	sc.result = func(ref InstanceRef, n int) InstanceScan {
		if ref.ID == "a" && n == 1 {
			panic("the scan of a blew up")
		}

		scan := cachedScan(ref.ID, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
		scan.Ref = ref

		return scan
	}

	var cache Cache

	s, fa, log := startLoggedScheduler(t, sc, &cache, staticLister("a", "b"))

	fa.fire(t, 0)
	fa.next(t)

	st := s.State()
	if st.State != ScanIdle || len(st.Errors) != 1 || st.Errors[0].InstanceID != "a" || st.Errors[0].Error != scanPanicked {
		t.Fatalf("expected the scan of a alone failed, got %+v", st)
	}

	if a, ok := cache.Get("a"); !ok || a.Err != scanPanicked || a.Ref.Deployment != "svc-a" {
		t.Fatalf("expected a's failed scan cached with its ref, got %+v (cached %v)", a, ok)
	}

	if b, ok := cache.Get("b"); !ok || b.Err != "" {
		t.Fatalf("expected b's scan cached, got %+v (cached %v)", b, ok)
	}

	expectPanicLogged(t, log, "the scan of a blew up", "(*fakeScanner).scan")

	fa.fire(t, 1)
	fa.next(t)

	if n := sc.count("a"); n != 2 {
		t.Fatalf("expected the timer to scan a again, got %d scans", n)
	}

	if st := s.State(); st.State != ScanIdle || len(st.Errors) != 0 {
		t.Fatalf("expected the next scan of a to clear its failure, got %+v", st)
	}
}

// A fleet scan whose instance list panics fails, logs the panic with the
// stack of the call that panicked, and the timer scans again.
func TestScheduledScanListPanicFailsTheFleetScan(t *testing.T) {
	t.Parallel()

	var lists atomic.Int32

	list := func(context.Context) ([]InstanceRef, error) {
		if lists.Add(1) == 1 {
			panic("the instance index blew up")
		}

		return refs("a"), nil
	}

	sc := newFakeScanner()

	var cache Cache

	s, fa, log := startLoggedScheduler(t, sc, &cache, list)

	fa.fire(t, 0)
	fa.next(t)

	if st := s.State(); st.State != ScanFailed || st.CompletedAt == nil {
		t.Fatalf("expected the fleet scan to fail and finish, got %+v", st)
	}

	expectPanicLogged(t, log, "the instance index blew up", "TestScheduledScanListPanicFailsTheFleetScan")

	fa.fire(t, 1)
	fa.next(t)

	if st := s.State(); st.State != ScanIdle || sc.count("a") != 1 {
		t.Fatalf("expected the timer to scan again, got %+v and %d scans of a", st, sc.count("a"))
	}
}
