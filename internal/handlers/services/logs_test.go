package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/handlers/services"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

const (
	logsInstanceID = "instance-1"
	logsDeployment = "valkey-instance-1"
	logsServiceID  = "valkey"
	logsPlanID     = "standalone"
	mebibyte       = 1024 * 1024

	// The instances logsVMs lists, as <job>/<instance ID>, and the one file
	// the scripted fetches return for each.
	logsNodeA   = "node/id-node-a"
	logsNodeB   = "node/id-node-b"
	logsProxy   = "proxy/id-proxy-1"
	logsNodeLog = "node/node.log"

	// A proxy log, and a node log large enough to be cut.
	logsProxyLog  = "proxy/proxy.log"
	logsStdoutLog = "node/stdout.log"

	// The job and instance IDs of the two node instances.
	logsNodeJob = "node"
	logsNodeAID = "id-node-a"
	logsNodeBID = "id-node-b"
)

// logsVault answers the two reads the logs endpoint makes. Every other
// method panics on the nil embedded Vault, so a test sees a call it did not
// script.
type logsVault struct {
	interfaces.Vault

	values map[string]interface{}
}

func (v *logsVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	value, found := v.values[path]
	if !found {
		return false, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err //nolint:wrapcheck // a test fixture
	}

	return true, json.Unmarshal(encoded, result) //nolint:wrapcheck // a test fixture
}

// newLogsVault holds the index entry of logsInstanceID and, when manifest
// is not empty, its manifest.
func newLogsVault(manifest string) *logsVault {
	vault := &logsVault{values: map[string]interface{}{
		logsInstanceID: map[string]interface{}{
			"service_id": logsServiceID, "plan_id": logsPlanID, "deployment_name": logsDeployment,
		},
	}}

	if manifest != "" {
		vault.values[logsInstanceID+"/manifest"] = map[string]interface{}{"manifest": manifest}
	}

	return vault
}

// logsVMs are the VMs the director lists for the deployment. Their indexes
// and IDs are the ones a deployment that was scaled and recreated has.
func logsVMs() []bosh.VM {
	return []bosh.VM{
		{Job: "proxy", ID: "id-proxy-1", Index: 1},
		{Job: "node", ID: "id-node-b", Index: 2},
		{Job: "node", ID: "id-node-a", Index: 0},
	}
}

// requestLogs asks the handler for the logs of logsInstanceID.
func requestLogs(t *testing.T, handler *services.Handler) map[string]interface{} {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/"+logsInstanceID+"/logs", nil)

	handler.GetInstanceLogs(recorder, request, logsInstanceID)

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("response body is not JSON: %v: %q", err, recorder.Body.String())
	}

	return body
}

// budgetRecorder is a scripted director's log fetch that records the
// budget each call received and takes a share of it, as a fetch that keeps
// files does.
type budgetRecorder struct {
	mu      sync.Mutex
	seen    []*bosh.LogBudget
	before  []int64
	take    int64
	callers []string
}

func (r *budgetRecorder) fetch(_ context.Context, _, job, instanceID string, budget *bosh.LogBudget) (*bosh.LogBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seen = append(r.seen, budget)
	r.callers = append(r.callers, job+"/"+instanceID)

	if budget == nil {
		r.before = append(r.before, -1)
	} else {
		r.before = append(r.before, budget.Remaining)
		budget.Remaining -= r.take
	}

	return &bosh.LogBundle{Files: map[string]string{logsNodeLog: "from " + job + "/" + instanceID}}, nil
}

// One request fetches the logs of every instance, so the instances share one
// budget. A cap on each call would let five instances hold five times the
// limit. A second request starts with a full budget of its own.
func TestFetchLogsSharesOneBudgetAcrossInstances(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{take: 20 * mebibyte}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)
	if body["error"] != nil {
		t.Fatalf("the logs response holds an error: %v", body["error"])
	}

	if len(recorder.seen) != 3 {
		t.Fatalf("the director was asked for %d instances' logs, want 3: %v", len(recorder.seen), recorder.callers)
	}

	for index, budget := range recorder.seen {
		if budget == nil {
			t.Fatalf("call %d got no budget, so its instance had no limit but its own file limit", index)
		}

		if budget != recorder.seen[0] {
			t.Errorf("call %d got a budget of its own, want the one budget of the request", index)
		}
	}

	if want := []int64{64 * mebibyte, 44 * mebibyte, 24 * mebibyte}; recorder.before[0] != want[0] || recorder.before[1] != want[1] || recorder.before[2] != want[2] {
		t.Errorf("the calls saw budgets of %v bytes, want the request's 64 MiB less what each earlier call took: %v", recorder.before, want)
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != 3 {
		t.Fatalf("logs = %v, want one entry for each of 3 instances", body["logs"])
	}

	// A later request does not inherit what this one used.
	_ = requestLogs(t, handler)

	if len(recorder.seen) != 6 || recorder.seen[3] == recorder.seen[0] || recorder.before[3] != 64*mebibyte {
		t.Errorf("a second request must start with its own full budget, saw %v bytes before its first call", recorder.before[3:])
	}
}

// The logs endpoint lists the deployment's instances with the director and
// fetches logs for each one by its instance ID. A manifest that still says
// 5 instances after a scale-down or a recreate asks for no index that does
// not exist, and the response keys are <job>/<id>.
func TestInstanceLogsFetchesEachInstanceByID(t *testing.T) {
	t.Parallel()

	manifest := "name: " + logsDeployment + "\ninstance_groups:\n- name: node\n  instances: 5\n"
	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(manifest), Director: director})

	body := requestLogs(t, handler)
	if body["error"] != nil {
		t.Fatalf("the logs response holds an error: %v", body["error"])
	}

	if body["deployment"] != logsDeployment {
		t.Errorf("deployment = %v, want %s", body["deployment"], logsDeployment)
	}

	if got := director.Calls("GetDeploymentVMs"); len(got) != 1 || got[0] != logsDeployment {
		t.Errorf("the director listed VMs %v, want one listing of %s", got, logsDeployment)
	}

	// The instances are fetched in a fixed order, by job and then index.
	want := []string{logsNodeA, logsNodeB, logsProxy}

	if got := director.Calls("FetchLogs"); !slices.Equal(got, want) {
		t.Fatalf("logs were fetched for %v, want %v", got, want)
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != len(want) {
		t.Fatalf("logs = %v, want one entry for each of %v", body["logs"], want)
	}

	for _, key := range want {
		files, ok := logs[key].(map[string]interface{})
		if !ok || files[logsNodeLog] != "from "+key {
			t.Errorf("logs[%q] = %v, want the files fetched for it", key, logs[key])
		}
	}
}

// active and inactive are the values a VM row's Active points to.
func active() *bool {
	value := true

	return &value
}

func inactive() *bool {
	value := false

	return &value
}

// A deploy that creates a VM before it deletes the old one leaves an
// inactive row beside the active one under the same instance ID. The
// inactive row is not fetched, a repeated row is fetched once, and an
// instance the director lists only with an inactive VM says why it has no
// logs.
func TestInstanceLogsFetchEachActiveInstanceOnce(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		return []bosh.VM{
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0, Active: inactive()},
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0, Active: active()},
			{Job: logsNodeJob, ID: logsNodeBID, Index: 2},
			{Job: logsNodeJob, ID: logsNodeBID, Index: 2},
			{Job: "proxy", ID: "id-proxy-1", Index: 1, Active: inactive()},
		}, nil
	}
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	if got, want := director.Calls("FetchLogs"), []string{logsNodeA, logsNodeB}; !slices.Equal(got, want) {
		t.Errorf("logs were fetched for %v, want each active instance once: %v", got, want)
	}

	logs, _ := body["logs"].(map[string]interface{})

	for _, key := range []string{logsNodeA, logsNodeB} {
		if files, _ := logs[key].(map[string]interface{}); files[logsNodeLog] != "from "+key {
			t.Errorf("logs[%q] = %v, want the files fetched for it", key, logs[key])
		}
	}

	entry, _ := logs[logsProxy].(map[string]interface{})
	if message, _ := entry["error"].(string); !strings.Contains(message, "only an inactive VM") {
		t.Errorf("logs[%q] = %v, want an error that says the director lists only an inactive VM", logsProxy, logs[logsProxy])
	}
}

// Logs are fetched by job name and instance ID, so a row that lacks either
// is not fetched, and the response says so under the row's key.
func TestInstanceLogsReportRowsWithNoJobOrID(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		return []bosh.VM{
			{Job: logsNodeJob, ID: "", Index: 3},
			{Job: "", ID: "id-orphan", Index: 0},
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0},
		}, nil
	}
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	if got, want := director.Calls("FetchLogs"), []string{logsNodeA}; !slices.Equal(got, want) {
		t.Errorf("logs were fetched for %v, want only %v", got, want)
	}

	logs, _ := body["logs"].(map[string]interface{})
	if len(logs) != 3 {
		t.Errorf("logs = %v, want an entry for each of the 3 rows", logs)
	}

	for _, key := range []string{"node/", "/id-orphan"} {
		entry, _ := logs[key].(map[string]interface{})
		if message, _ := entry["error"].(string); !strings.Contains(message, "without a job name or an instance ID") {
			t.Errorf("logs[%q] = %v, want an error that says the row has no job name or instance ID", key, logs[key])
		}
	}
}

// A listing the director fails answers with an error, and fetches nothing.
func TestInstanceLogsReportAFailedInstanceListing(t *testing.T) {
	t.Parallel()

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return nil, errListingFailed }
	director.FetchLogsFn = (&budgetRecorder{}).fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	message, _ := body["error"].(string)
	if !strings.Contains(message, errListingFailed.Error()) {
		t.Errorf("error = %v, want one that carries %q", body["error"], errListingFailed)
	}

	if got := director.Calls("FetchLogs"); len(got) != 0 {
		t.Errorf("logs were fetched for %v after the listing failed", got)
	}
}

var (
	errListingFailed          = errors.New("the director could not list the instances")
	errHeldTaskNeverCancelled = errors.New("the held task was never cancelled")
)

// logsConfig is the one setting of the broker the logs endpoint reads. Every
// other method panics on the nil embedded Config, so a test sees a read it
// did not script.
type logsConfig struct {
	interfaces.Config

	broker config.BrokerConfig
}

func (c *logsConfig) GetBrokerConfig() config.BrokerConfig {
	return c.broker
}

// writeTimeoutConfig is a broker config that sets the write timeout to the
// given number of seconds.
func writeTimeoutConfig(seconds int) *logsConfig {
	return &logsConfig{broker: config.BrokerConfig{WriteTimeout: seconds}}
}

// holdNodeB is a log fetch that answers at once for every instance but
// id-node-b, whose task stays open until the request's context ends. It
// records the deadline of that context in heldDeadline. The wait is bounded
// by writeTimeout and a second more, so a request with no deadline fails the
// test and does not hang it.
func holdNodeB(writeTimeout time.Duration, heldDeadline *time.Time) func(context.Context, string, string, string, *bosh.LogBudget) (*bosh.LogBundle, error) {
	return func(ctx context.Context, _, job, instanceID string, _ *bosh.LogBudget) (*bosh.LogBundle, error) {
		if instanceID != logsNodeBID {
			return &bosh.LogBundle{Files: map[string]string{logsNodeLog: "from " + job + "/" + instanceID}}, nil
		}

		*heldDeadline, _ = ctx.Deadline()

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("the director call ended: %w", ctx.Err())
		case <-time.After(writeTimeout + time.Second):
			return nil, errHeldTaskNeverCancelled
		}
	}
}

// The server cuts a response off at its write timeout, so a request that
// fetches logs stops before then. The current fetch is cancelled through its
// context, and its error gives the reason the request stopped followed by
// what the director call said. The instances after it are not fetched, and
// the response holds what the request got along with an error for each
// instance it skipped. A row with no instance ID still says why it was not
// fetched, rather than that the request ran out of time.
func TestInstanceLogsStopBeforeTheWriteTimeout(t *testing.T) {
	t.Parallel()

	const (
		writeTimeout = 4 // seconds, so the request stops after 2
		cause        = "the request ran out of time, because the write timeout of 4 seconds is near"
		noIDKey      = "node/"
	)

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		return append(logsVMs(), bosh.VM{Job: logsNodeJob, Index: 3}), nil
	}

	var heldDeadline time.Time

	director.FetchLogsFn = holdNodeB(writeTimeout*time.Second, &heldDeadline)

	handler := services.NewHandler(services.Dependencies{
		Logger:   logger.Get().Named("test"),
		Config:   &logsConfig{broker: config.BrokerConfig{WriteTimeout: writeTimeout}},
		Vault:    newLogsVault(""),
		Director: director,
	})

	started := time.Now()
	body := requestLogs(t, handler)
	elapsed := time.Since(started)

	if elapsed >= writeTimeout*time.Second {
		t.Errorf("the request took %v, want it to stop before the %d second write timeout", elapsed, writeTimeout)
	}

	if heldDeadline.IsZero() || heldDeadline.Sub(started) >= writeTimeout*time.Second {
		t.Errorf("the held fetch had a deadline %v after the start, want one under the write timeout", heldDeadline.Sub(started))
	}

	// The third instance is never asked for.
	want := []string{logsNodeA, logsNodeB}
	if got := director.Calls("FetchLogs"); !slices.Equal(got, want) {
		t.Fatalf("logs were fetched for %v, want %v", got, want)
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != 4 {
		t.Fatalf("logs = %v, want an entry for each of the 4 rows", body["logs"])
	}

	if files, _ := logs[logsNodeA].(map[string]interface{}); files[logsNodeLog] != "from node/id-node-a" {
		t.Errorf("logs of the first instance = %v, want the files it fetched", logs[logsNodeA])
	}

	wantErrors := map[string]string{
		logsNodeB: "Failed to fetch logs: " + cause + ": the director call ended: context deadline exceeded",
		logsProxy: "Failed to fetch logs: " + cause,
		noIDKey:   "Logs were not fetched, because the director listed this instance without a job name or an instance ID",
	}

	for key, want := range wantErrors {
		entry, _ := logs[key].(map[string]interface{})
		if message, _ := entry["error"].(string); message != want {
			t.Errorf("logs[%q] error = %q, want %q", key, message, want)
		}
	}
}

// The deadline is the write timeout less 10 seconds, or half the write
// timeout when that is later, so a short write timeout still leaves the
// request time to fetch. The write timeout is 120 seconds when the broker
// does not set one, as the server does.
func TestInstanceLogsDeadlineFollowsTheWriteTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config interfaces.Config
		want   time.Duration
	}{
		{name: "no config", config: nil, want: 110 * time.Second},
		{name: "no write timeout set", config: &logsConfig{}, want: 110 * time.Second},
		{name: "a set write timeout", config: &logsConfig{broker: config.BrokerConfig{WriteTimeout: 300}}, want: 290 * time.Second},
		{name: "a write timeout under 10 seconds", config: &logsConfig{broker: config.BrokerConfig{WriteTimeout: 6}}, want: 3 * time.Second},
		{name: "a write timeout of 10 seconds", config: writeTimeoutConfig(10), want: 5 * time.Second},
		{name: "a write timeout of 11 seconds", config: writeTimeoutConfig(11), want: 5500 * time.Millisecond},
		{name: "a write timeout of 14 seconds", config: writeTimeoutConfig(14), want: 7 * time.Second},
		{name: "a write timeout of 15 seconds", config: writeTimeoutConfig(15), want: 7500 * time.Millisecond},
		{name: "a write timeout of 120 seconds", config: writeTimeoutConfig(120), want: 110 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var deadline time.Time

			director := testutil.NewScriptedBOSHDirector()
			director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
			director.FetchLogsFn = func(ctx context.Context, _, job, instanceID string, _ *bosh.LogBudget) (*bosh.LogBundle, error) {
				deadline, _ = ctx.Deadline()

				return &bosh.LogBundle{Files: map[string]string{logsNodeLog: "from " + job + "/" + instanceID}}, nil
			}

			handler := services.NewHandler(services.Dependencies{
				Logger: logger.Get().Named("test"), Config: test.config, Vault: newLogsVault(""), Director: director,
			})

			started := time.Now()
			_ = requestLogs(t, handler)

			if deadline.IsZero() {
				t.Fatal("the fetch had no deadline")
			}

			if got := deadline.Sub(started); got < test.want-time.Second || got > test.want+time.Second {
				t.Errorf("the fetch deadline was %v after the start, want about %v", got, test.want)
			}
		})
	}
}

// listedPath is a file the truncated object lists, as it reads once decoded.
func listedPath(instance, path string) map[string]interface{} {
	return map[string]interface{}{"instance": instance, "path": path}
}

// scriptedBundles makes a scripted director answer each instance's log fetch
// with the bundle in bundles, keyed by <job>/<instance ID>.
func scriptedBundles(bundles map[string]*bosh.LogBundle) *testutil.ScriptedBOSHDirector {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
	director.FetchLogsFn = func(_ context.Context, _, job, instanceID string, _ *bosh.LogBudget) (*bosh.LogBundle, error) {
		return bundles[job+"/"+instanceID], nil
	}

	return director
}

// A response that left out log files says which ones and how many, so a
// reader knows the logs are not the whole of what the instances held.
func TestInstanceLogsReportTheSkippedFiles(t *testing.T) {
	t.Parallel()

	director := scriptedBundles(map[string]*bosh.LogBundle{
		logsNodeA: {
			Files:        map[string]string{logsNodeLog: "a"},
			Skipped:      []string{"node/big.log", "node/bigger.log"},
			SkippedCount: 3,
		},
		logsNodeB: {Files: map[string]string{logsNodeLog: "b"}, StoppedEarly: true},
		logsProxy: {Files: map[string]string{logsProxyLog: "p"}},
	})

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	truncated, isObject := body["truncated"].(map[string]interface{})
	if !isObject {
		t.Fatalf("truncated = %v, want an object that says what was skipped", body["truncated"])
	}

	wantFiles := []interface{}{
		listedPath(logsNodeA, "node/big.log"),
		listedPath(logsNodeA, "node/bigger.log"),
	}
	if !reflect.DeepEqual(truncated["skipped_files"], wantFiles) {
		t.Errorf("skipped_files = %v, want %v", truncated["skipped_files"], wantFiles)
	}

	// The count is exact, though the list holds two of the three.
	if truncated["skipped_count"] != float64(3) {
		t.Errorf("skipped_count = %v, want 3", truncated["skipped_count"])
	}

	if want := []interface{}{logsNodeB}; !reflect.DeepEqual(truncated["stopped_early"], want) {
		t.Errorf("stopped_early = %v, want %v", truncated["stopped_early"], want)
	}

	if !reflect.DeepEqual(truncated["cut_files"], []interface{}{}) || truncated["cut_count"] != float64(0) {
		t.Errorf("cut_files = %v and cut_count = %v, want no cut files", truncated["cut_files"], truncated["cut_count"])
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != 3 {
		t.Errorf("logs = %v, want the files of all 3 instances", body["logs"])
	}
}

// A file larger than the per-file limit keeps only its first 10 MiB, so the
// response says which files were cut and how many, even when no file was
// left out.
func TestInstanceLogsReportTheCutFiles(t *testing.T) {
	t.Parallel()

	director := scriptedBundles(map[string]*bosh.LogBundle{
		logsNodeA: {Files: map[string]string{logsStdoutLog: "head"}, Cut: []string{logsStdoutLog}, CutCount: 1},
		logsNodeB: {Files: map[string]string{logsNodeLog: "b"}},
		logsProxy: {Files: map[string]string{logsProxyLog: "p"}, Cut: []string{logsProxyLog}, CutCount: 1},
	})

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	truncated, isObject := requestLogs(t, handler)["truncated"].(map[string]interface{})
	if !isObject {
		t.Fatal("the response has no truncated object, want one that says which files were cut")
	}

	want := map[string]interface{}{
		"cut_files": []interface{}{
			listedPath(logsNodeA, logsStdoutLog),
			listedPath(logsProxy, logsProxyLog),
		},
		"cut_count":     float64(2),
		"skipped_files": []interface{}{},
		"skipped_count": float64(0),
		"stopped_early": []interface{}{},
	}
	if !reflect.DeepEqual(truncated, want) {
		t.Errorf("truncated = %v, want %v", truncated, want)
	}
}

// A director that answers a fetch with no bundle and no error leaves that
// instance with an error, and the request goes on to the other instances.
func TestInstanceLogsReportAnInstanceWithNoBundle(t *testing.T) {
	t.Parallel()

	director := scriptedBundles(map[string]*bosh.LogBundle{
		logsNodeA: {Files: map[string]string{logsNodeLog: "a"}},
		logsProxy: {Files: map[string]string{logsProxyLog: "p"}},
	})

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	if got, want := director.Calls("FetchLogs"), []string{logsNodeA, logsNodeB, logsProxy}; !slices.Equal(got, want) {
		t.Errorf("logs were fetched for %v, want %v", got, want)
	}

	logs, _ := body["logs"].(map[string]interface{})
	want := map[string]interface{}{
		logsNodeA: map[string]interface{}{logsNodeLog: "a"},
		logsNodeB: map[string]interface{}{"error": "Failed to fetch logs: the director returned no log bundle"},
		logsProxy: map[string]interface{}{logsProxyLog: "p"},
	}

	if !reflect.DeepEqual(logs, want) {
		t.Errorf("logs = %v, want %v", logs, want)
	}
}

// A response that left nothing out has no truncated object.
func TestInstanceLogsOmitTruncatedWhenNothingWasSkipped(t *testing.T) {
	t.Parallel()

	director := scriptedBundles(map[string]*bosh.LogBundle{
		logsNodeA: {Files: map[string]string{logsNodeLog: "a"}},
		logsNodeB: {Files: map[string]string{}},
		logsProxy: {Files: map[string]string{logsProxyLog: "p"}},
	})

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)
	if _, found := body["truncated"]; found {
		t.Errorf("truncated = %v, want no truncated object when nothing was skipped", body["truncated"])
	}
}

// Every bundle lists up to 1000 skipped paths and 1000 cut paths, and a
// request fetches many bundles, so the response caps each list it holds at
// 1000 as well. The counts stay exact.
func TestInstanceLogsCapTheFileLists(t *testing.T) {
	t.Parallel()

	paths := make([]string, 600)
	for index := range paths {
		paths[index] = fmt.Sprintf("node/%d.log", index)
	}

	tests := []struct {
		kind   string
		bundle func(count int) *bosh.LogBundle
	}{
		{kind: "skipped", bundle: func(count int) *bosh.LogBundle {
			return &bosh.LogBundle{Files: map[string]string{}, Skipped: paths, SkippedCount: count}
		}},
		{kind: "cut", bundle: func(count int) *bosh.LogBundle {
			return &bosh.LogBundle{Files: map[string]string{}, Cut: paths, CutCount: count}
		}},
	}

	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()

			director := scriptedBundles(map[string]*bosh.LogBundle{
				logsNodeA: test.bundle(600),
				logsNodeB: test.bundle(700),
				logsProxy: test.bundle(600),
			})

			handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

			truncated, _ := requestLogs(t, handler)["truncated"].(map[string]interface{})

			listed, _ := truncated[test.kind+"_files"].([]interface{})
			if len(listed) != 1000 {
				t.Errorf("%s_files holds %d paths, want 1000", test.kind, len(listed))
			}

			if truncated[test.kind+"_count"] != float64(1900) {
				t.Errorf("%s_count = %v, want 1900", test.kind, truncated[test.kind+"_count"])
			}
		})
	}
}
