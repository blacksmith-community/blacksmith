package bosh_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Values the errand tests repeat.
const (
	// errandName is the errand the tests run.
	errandName = "smoke-tests"

	// errandRunPath is the route an errand run is sent to.
	errandRunPath = "/deployments/" + detailsDeployment + "/errands/" + errandName + "/runs"

	// errandOperation is the operation an errand run's errors name.
	errandOperation = "RunErrand"

	// errandTaskID is the task the director starts for an errand run.
	errandTaskID = 42

	// errandGroup is the instance group the errand results come from.
	errandGroup = "node"
)

// errandRow is one errand result as the director writes it in the task's
// result output. A nil exitCode leaves the exit_code key out.
func errandRow(group, instanceID string, exitCode *int, stdout string) string {
	row := map[string]interface{}{
		"stdout":      stdout,
		"stderr":      "",
		"logs":        map[string]interface{}{"blobstore_id": "blob-" + instanceID, "sha1": "sha-" + instanceID},
		"errand_name": errandName,
		"instance":    map[string]interface{}{"group": group, "id": instanceID},
	}

	if exitCode != nil {
		row["exit_code"] = *exitCode
	}

	encoded, err := json.Marshal(row)
	if err != nil {
		panic(err)
	}

	return string(encoded)
}

// exitCode returns a pointer to code, for errandRow.
func exitCode(code int) *int {
	return &code
}

// errandConfig says how an errandDirector answers.
type errandConfig struct {
	// run answers the errand run. Nil redirects to task errandTaskID.
	run func(http.ResponseWriter, *http.Request)
	// state returns the state and result text of the task for its nth
	// read, counting from 1. Nil answers done with no result text.
	state func(taskID int, poll int32) (string, string)
	// list answers a task list. Nil answers an empty list.
	list func(http.ResponseWriter, *http.Request)
	// output is the result output of every task.
	output string
	// readOutput answers a read of a task's result output. Nil answers
	// with output.
	readOutput func(http.ResponseWriter, *http.Request)
}

// errandDirector is a test director that runs errands as director tasks.
type errandDirector struct {
	*boshtest.Director

	runs  *mutationLog
	lists *mutationLog
	polls atomic.Int32

	firstPoll     chan struct{}
	firstPollOnce sync.Once
}

func newErrandDirector(t *testing.T, cfg errandConfig) *errandDirector {
	t.Helper()

	director := &errandDirector{runs: &mutationLog{}, lists: &mutationLog{}, firstPoll: make(chan struct{})}

	director.Director = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		path := request.URL.Path

		switch {
		case request.Method == http.MethodPost && path == errandRunPath:
			director.runs.add(request)

			if cfg.run != nil {
				cfg.run(writer, request)

				return true
			}

			redirectToTask(writer)
		case request.Method == http.MethodGet && path == taskListRoute:
			director.lists.add(request)

			if cfg.list != nil {
				cfg.list(writer, request)

				return true
			}

			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})
		case request.Method == http.MethodGet && strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/output"):
			if cfg.readOutput != nil {
				cfg.readOutput(writer, request)

				return true
			}

			boshtest.WriteText(writer, http.StatusOK, cfg.output)
		case request.Method == http.MethodGet && strings.HasPrefix(path, "/tasks/"):
			return director.answerTaskRead(writer, path, cfg)
		case request.Method == http.MethodDelete && strings.HasPrefix(path, "/task/"):
			writer.WriteHeader(http.StatusNoContent)
		default:
			return false
		}

		return true
	})

	return director
}

// answerTaskRead answers a read of a single task with the state cfg gives
// it, and returns false for a path that names no task.
func (d *errandDirector) answerTaskRead(writer http.ResponseWriter, path string, cfg errandConfig) bool {
	taskID, err := strconv.Atoi(strings.TrimPrefix(path, "/tasks/"))
	if err != nil {
		return false
	}

	poll := d.polls.Add(1)
	d.firstPollOnce.Do(func() { close(d.firstPoll) })

	state, result := batchTaskDone, ""
	if cfg.state != nil {
		state, result = cfg.state(taskID, poll)
	}

	task := directorTask(taskID, state)
	task["description"] = "run errand " + errandName
	task["result"] = result
	task["deployment"] = detailsDeployment

	boshtest.WriteJSON(writer, http.StatusOK, task)

	return true
}

// cancels returns the cancel requests the director received.
func (d *errandDirector) cancels() []boshtest.Request {
	var found []boshtest.Request

	for _, request := range d.APIRequests() {
		if request.Method == http.MethodDelete {
			found = append(found, request)
		}
	}

	return found
}

// runErrand runs the test errand with opts, and fails the test when it
// does not return within ten seconds.
func runErrand(t *testing.T, ctx context.Context, director bosh.Director, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	t.Helper()

	var result *bosh.ErrandResult

	err := callWithin(t, 10*time.Second, func() error {
		var err error

		result, err = director.RunErrand(ctx, detailsDeployment, errandName, opts)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})

	return result, err
}

// An errand run carries a context ID and the body the director reads. An
// instance written as group/id reaches the director as a group and an ID,
// a bare group stays a group, and an empty entry, such as the one a
// trailing comma leaves, is not sent.
func TestRunErrandSendsTheContextIDAndTheDirectorsBody(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{output: errandRow(errandGroup, "uuid-0", exitCode(0), "all good")})
	director := newTaskAdapter(t, server.Director)

	ctx := boshapi.WithContextID(t.Context(), callerContextID)

	result, err := runErrand(t, ctx, director, bosh.ErrandOpts{
		KeepAlive:   true,
		WhenChanged: true,
		Instances:   []string{"node/0", " worker ", ""},
	})
	if err != nil {
		t.Fatalf("expected the errand to run, got: %v", err)
	}

	if result == nil || result.ExitCode != 0 || result.Stdout != "all good" || result.InstanceID != "uuid-0" {
		t.Errorf("expected the errand's result, got %+v", result)
	}

	sent := server.runs.only(t)
	if sent.ContextID != callerContextID {
		t.Errorf("expected the context ID %s on the errand run, got %q", callerContextID, sent.ContextID)
	}

	var body struct {
		KeepAlive   bool              `json:"keep-alive"`
		WhenChanged bool              `json:"when-changed"`
		Instances   []json.RawMessage `json:"instances"`
	}

	err = json.Unmarshal([]byte(sent.Body), &body)
	if err != nil {
		t.Fatalf("decoding the errand run body %q: %v", sent.Body, err)
	}

	if !body.KeepAlive || !body.WhenChanged {
		t.Errorf("expected keep-alive and when-changed to be sent, got %s", sent.Body)
	}

	want := []string{`{"group":"node","id":"0"}`, `{"group":"worker"}`}
	if len(body.Instances) != len(want) {
		t.Fatalf("expected the instances %v, got %s", want, sent.Body)
	}

	for index, instance := range body.Instances {
		if string(instance) != want[index] {
			t.Errorf("instance %d is %s, want %s (body %s)", index, instance, want[index], sent.Body)
		}
	}
}

// Each instance is trimmed on both sides of its slash, so a spaced
// "node / 0" reaches the director as the group node and the ID 0, and a
// group with a trailing slash stays a bare group.
func TestRunErrandTrimsBothHalvesOfAnInstance(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{output: errandRow(errandGroup, "uuid-0", exitCode(0), "ok")})
	director := newTaskAdapter(t, server.Director)

	_, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{
		Instances: []string{"node / 0", " node/9f3c1b2a-0000-4000-8000-000000000001 ", "worker/", " api /"},
	})
	if err != nil {
		t.Fatalf("expected the errand to run, got: %v", err)
	}

	var body struct {
		Instances []json.RawMessage `json:"instances"`
	}

	sent := server.runs.only(t)

	err = json.Unmarshal([]byte(sent.Body), &body)
	if err != nil {
		t.Fatalf("decoding the errand run body %q: %v", sent.Body, err)
	}

	want := []string{
		`{"group":"node","id":"0"}`,
		`{"group":"node","id":"9f3c1b2a-0000-4000-8000-000000000001"}`,
		`{"group":"worker"}`,
		`{"group":"api"}`,
	}

	if len(body.Instances) != len(want) {
		t.Fatalf("expected the instances %v, got %s", want, sent.Body)
	}

	for index, instance := range body.Instances {
		if string(instance) != want[index] {
			t.Errorf("instance %d is %s, want %s (body %s)", index, instance, want[index], sent.Body)
		}
	}
}

// An instance with no group names nothing the director can match, so the
// run is refused before anything is sent. Sending it, or dropping it,
// could run the errand on instances the caller did not name.
func TestRunErrandRefusesAnInstanceWithNoGroup(t *testing.T) {
	for _, instance := range []string{"/", "/0", "/9f3c1b2a-0000-4000-8000-000000000001", " / ", "  /0"} {
		t.Run(instance, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server := newErrandDirector(t, errandConfig{})
			director := newTaskAdapter(t, server.Director)

			result, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{Instances: []string{"node/0", instance}})
			if result != nil {
				t.Errorf("expected no result, got %+v", result)
			}

			if !errors.Is(err, bosh.ErrInvalidErrandInstance) {
				t.Fatalf("expected ErrInvalidErrandInstance, got: %v", err)
			}

			if !strings.Contains(err.Error(), strconv.Quote(instance)) {
				t.Errorf("expected the error to quote the entry %q, got: %v", instance, err)
			}

			if sent := server.runs.all(); len(sent) != 0 {
				t.Errorf("expected nothing to be sent, got %+v", sent)
			}
		})
	}
}

// An errand can change state, so a run whose caller's context ends leaves
// the errand's task running and sends no cancel.
func TestRunErrandLeavesTheTaskRunningWhenTheContextEnds(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{state: func(int, int32) (string, string) {
		return batchTaskProcessing, ""
	}})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		<-server.firstPoll
		cancel()
	}()

	_, err := runErrand(t, ctx, director, bosh.ErrandOpts{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected an error that wraps context.Canceled, got: %v", err)
	}

	_ = checkCallError(t, err, errandOperation, bosh.TaskLeftRunning)

	if cancels := server.cancels(); len(cancels) != 0 {
		t.Errorf("an errand's task must be left running, but cancels were sent: %+v", cancels)
	}
}

// An errand task that ends in any state but done fails the run with a
// TaskFailedError that carries the director's result text.
func TestRunErrandFailsWhenTheTaskEndsInError(t *testing.T) {
	for _, state := range []string{batchTaskError, taskCancelled, taskTimedOut} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			result := "the errand task ended " + state
			server := newErrandDirector(t, errandConfig{state: func(int, int32) (string, string) {
				return state, result
			}})
			director := newTaskAdapter(t, server.Director)

			_, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{})

			var failed *bosh.TaskFailedError
			if !errors.As(err, &failed) {
				t.Fatalf("expected a *bosh.TaskFailedError, got %T: %v", err, err)
			}

			if failed.TaskID != errandTaskID || failed.State != state || failed.Result != result ||
				failed.Deployment != detailsDeployment || failed.Operation != errandOperation {
				t.Errorf("unexpected TaskFailedError fields: %+v", failed)
			}

			if !strings.Contains(err.Error(), result) {
				t.Errorf("expected the error to carry the result text %q, got: %v", result, err)
			}

			if outputs := requestsTo(server.Director, http.MethodGet, detailsTaskPath+"/output"); len(outputs) != 0 {
				t.Errorf("a task that failed has no result to read, but its output was read %d times", len(outputs))
			}
		})
	}
}

// listsTheErrandTask answers a query for contextID with a running errand
// task, and every other task list with nothing.
func listsTheErrandTask(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Query().Get("context_id") != contextID {
		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

		return
	}

	task := contextTask(lookedUpTaskID, batchTaskProcessing, contextID)
	task["description"] = "run errand " + errandName

	boshtest.WriteJSON(writer, http.StatusOK, []interface{}{task})
}

// checkOneLostRun checks that the errand run was sent once, with the
// context ID, and looked up once by that ID.
func checkOneLostRun(t *testing.T, server *errandDirector) {
	t.Helper()

	if sent := server.runs.only(t); sent.ContextID != contextID {
		t.Errorf("expected the errand run to carry the context ID %s, got %q", contextID, sent.ContextID)
	}

	var lookups []sentMutation

	for _, sent := range server.lists.all() {
		if sent.Query.Get("context_id") == contextID {
			lookups = append(lookups, sent)
		}
	}

	if len(lookups) != 1 {
		t.Errorf("expected one lookup by context ID, got %+v", server.lists.all())
	}
}

// checkLostRun checks that a lost errand run is an unknown outcome that
// names the run, the deployment, and the context ID.
func checkLostRun(t *testing.T, err error) *bosh.OutcomeUnknownError {
	t.Helper()

	return checkLostAnswer(t, err, mutationCall{operation: errandOperation, deployment: detailsDeployment}, contextID)
}

// An errand run whose answer was lost is found by its context ID, followed
// to its end, and its result returned. It is never sent again.
func TestRunErrandFindsTheTaskByContextIDAfterALostAnswer(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{
		run:    func(writer http.ResponseWriter, _ *http.Request) { boshtest.DropAnswer(writer) },
		list:   listsTheErrandTask,
		output: errandRow(errandGroup, "uuid-0", exitCode(3), "found it"),
	})
	director := newTaskAdapter(t, server.Director)

	result, err := runErrand(t, boshapi.WithContextID(t.Context(), contextID), director, bosh.ErrandOpts{})
	if err != nil {
		t.Fatalf("expected the found task's result, got: %v", err)
	}

	if result == nil || result.ExitCode != 3 || result.Stdout != "found it" {
		t.Errorf("expected the found task's result, got %+v", result)
	}

	checkOneLostRun(t, server)

	if reads := requestsTo(server.Director, http.MethodGet, "/tasks/"+strconv.Itoa(lookedUpTaskID)); len(reads) == 0 {
		t.Errorf("expected the found task %d to be followed, got %+v", lookedUpTaskID, server.APIRequests())
	}
}

// An errand run whose answer was lost, and whose task the director does
// not list, is an unknown outcome with the context ID. It is sent once,
// because a second run of an errand may not be safe.
func TestRunErrandWithALostAnswerAndNoTaskIsOutcomeUnknown(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{
		run: func(writer http.ResponseWriter, _ *http.Request) { boshtest.DropAnswer(writer) },
	})
	director := newTaskAdapter(t, server.Director)

	result, err := runErrand(t, boshapi.WithContextID(t.Context(), contextID), director, bosh.ErrandOpts{})
	if result != nil {
		t.Errorf("expected no result, got %+v", result)
	}

	unknown := checkLostRun(t, err)
	if unknown.NoLookup || unknown.LookupFailed {
		t.Errorf("expected a lookup that found nothing, got %+v", unknown)
	}

	if !strings.Contains(err.Error(), contextID) {
		t.Errorf("expected the error to name the context ID %s, got: %v", contextID, err)
	}

	checkOneLostRun(t, server)
}

// An errand run whose answer was lost, and whose lookup failed, is an
// unknown outcome that says the lookup failed.
func TestRunErrandWithALostAnswerAndAFailedLookupIsOutcomeUnknown(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{
		run:  func(writer http.ResponseWriter, _ *http.Request) { boshtest.DropAnswer(writer) },
		list: failsEveryList,
	})
	director := newTaskAdapter(t, server.Director)

	_, err := runErrand(t, boshapi.WithContextID(t.Context(), contextID), director, bosh.ErrandOpts{})

	unknown := checkLostRun(t, err)
	if !unknown.LookupFailed {
		t.Errorf("expected LookupFailed once the lookup failed, got %+v", unknown)
	}

	checkOneLostRun(t, server)
}

// endsTheContextThenDropsTheAnswer ends the caller's context inside the
// errand run, before it drops the answer, so the lookup that follows has to
// start on a context that has already ended.
func endsTheContextThenDropsTheAnswer(cancel context.CancelFunc) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, _ *http.Request) {
		cancel()
		boshtest.DropAnswer(writer)
	}
}

// The lookup after a lost answer runs on a context of its own, so it still
// answers when the caller's context ended before the lookup began. With
// the lookup on the caller's context it would fail, and the error would say
// so.
func TestRunErrandLooksUpALostAnswerAfterItsContextEnds(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	ctx, cancel := context.WithCancel(boshapi.WithContextID(t.Context(), contextID))
	defer cancel()

	server := newErrandDirector(t, errandConfig{run: endsTheContextThenDropsTheAnswer(cancel)})
	director := newTaskAdapter(t, server.Director)

	_, err := runErrand(t, ctx, director, bosh.ErrandOpts{})

	if ctx.Err() == nil {
		t.Fatal("expected the caller's context to have ended before the lookup")
	}

	unknown := checkLostRun(t, err)
	if unknown.NoLookup || unknown.LookupFailed {
		t.Errorf("expected a lookup that ran and found nothing, got %+v", unknown)
	}

	checkOneLostRun(t, server)
}

// When the lookup after a lost answer finds the errand's task, and the
// caller's context has already ended, the task is the run's task. It is
// left running, and the error names it, so the caller can follow it and
// never runs the errand again.
func TestRunErrandNamesAFoundTaskWhenItsContextEnded(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	ctx, cancel := context.WithCancel(boshapi.WithContextID(t.Context(), contextID))
	defer cancel()

	server := newErrandDirector(t, errandConfig{run: endsTheContextThenDropsTheAnswer(cancel), list: listsTheErrandTask})
	director := newTaskAdapter(t, server.Director)

	_, err := runErrand(t, ctx, director, bosh.ErrandOpts{})

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
	}

	if callErr.Operation != errandOperation || callErr.TaskID != lookedUpTaskID || callErr.Task != bosh.TaskLeftRunning {
		t.Errorf("DirectorCallError names operation %q, task %d, and outcome %v, want %s, %d, and %v: %v",
			callErr.Operation, callErr.TaskID, callErr.Task, errandOperation, lookedUpTaskID, bosh.TaskLeftRunning, err)
	}

	var unknown *bosh.OutcomeUnknownError
	if errors.As(err, &unknown) {
		t.Errorf("a found task is not an unknown outcome, got %+v", unknown)
	}

	checkOneLostRun(t, server)
}

// The director skips a when_changed run when nothing changed since the
// last successful run, and writes no results. That run succeeded, so it is
// reported as skipped with exit code 0.
func TestRunErrandReportsASkippedWhenChangedRun(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{})
	director := newTaskAdapter(t, server.Director)

	result, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{WhenChanged: true})
	if err != nil {
		t.Fatalf("expected a skipped run to succeed, got: %v", err)
	}

	if result == nil || !result.Skipped || result.ExitCode != 0 {
		t.Errorf("expected a skipped result with exit code 0, got %+v", result)
	}
}

// checkFinishedButUnread checks that err tells the caller the errand's task
// finished, so the errand ran, and that its result could not be read. The
// error carries the task's ID, says where to read the result, and gives no
// advice for a hung director, because the director answered.
func checkFinishedButUnread(t *testing.T, err error) *bosh.DirectorCallError {
	t.Helper()

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
	}

	if callErr.Operation != errandOperation || callErr.TaskID != errandTaskID || callErr.Task != bosh.TaskFinished {
		t.Errorf("DirectorCallError names operation %q, task %d, and outcome %v, want %s, %d, and %v: %v",
			callErr.Operation, callErr.TaskID, callErr.Task, errandOperation, errandTaskID, bosh.TaskFinished, err)
	}

	for _, want := range []string{"BOSH task 42", "finished", "/b/tasks/42", "ran"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}

	if strings.Contains(err.Error(), "hung or overloaded") {
		t.Errorf("the director answered, so the error must not blame a hung director, got: %v", err)
	}

	return callErr
}

// When the caller's context ends while the result of a finished errand is
// read, the errand has already run. The error names the task and says it
// finished, so a caller does not run the errand again believing nothing ran.
func TestRunErrandNamesAFinishedTaskWhenItsContextEndsReadingTheResult(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	server := newErrandDirector(t, errandConfig{readOutput: func(_ http.ResponseWriter, request *http.Request) {
		cancel()
		<-request.Context().Done()
	}})
	director := newTaskAdapter(t, server.Director)

	result, err := runErrand(t, ctx, director, bosh.ErrandOpts{})
	if result != nil {
		t.Errorf("expected no result, got %+v", result)
	}

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected an error that wraps context.Canceled, got: %v", err)
	}

	_ = checkFinishedButUnread(t, err)

	if cancels := server.cancels(); len(cancels) != 0 {
		t.Errorf("a finished task has nothing to cancel, but cancels were sent: %+v", cancels)
	}
}

// When the director refuses the read of a finished errand's result, the
// errand has run as well, so the error names the task in the same way.
func TestRunErrandNamesAFinishedTaskWhenTheResultCannotBeRead(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{readOutput: func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.WriteText(writer, http.StatusBadRequest, "the result is gone")
	}})
	director := newTaskAdapter(t, server.Director)

	result, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{})
	if result != nil {
		t.Errorf("expected no result, got %+v", result)
	}

	_ = checkFinishedButUnread(t, err)

	if !strings.Contains(err.Error(), "the result is gone") {
		t.Errorf("expected the error to carry the director's answer, got: %v", err)
	}
}

// A when_changed run whose task is done, writes no rows, and carries result
// text is still reported as skipped, because the director gives the same
// answer for a skip as for a filter that matched no instance, and Blacksmith
// cannot tell them apart. The result text is logged, so an operator can.
func TestRunErrandReportsASkippedRunWithResultText(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	const resultText = "no instances to run on"

	server := newErrandDirector(t, errandConfig{state: func(int, int32) (string, string) {
		return batchTaskDone, resultText
	}})

	logs := &recordingLogger{}
	director := newReadAdapter(t, server.Director, func(config *bosh.Config) { config.Logger = logs })
	bosh.SetTaskPollInterval(director, testPollInterval)

	result, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{WhenChanged: true})
	if err != nil {
		t.Fatalf("expected a skipped run to succeed, got: %v", err)
	}

	if result == nil || !result.Skipped || result.ExitCode != 0 {
		t.Errorf("expected a skipped result with exit code 0, got %+v", result)
	}

	if logs.count("INFO", "skipped errand "+errandName, resultText) != 1 {
		t.Errorf("expected one line that logs the skip with the result text %q, got %v", resultText, logs.lines)
	}
}

// A run that was not when_changed and wrote no results has nothing to
// report, and the error names the task.
func TestRunErrandWithNoResultsNamesTheTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := newErrandDirector(t, errandConfig{state: func(int, int32) (string, string) {
		return batchTaskDone, "no instances to run on"
	}})
	director := newTaskAdapter(t, server.Director)

	_, err := runErrand(t, t.Context(), director, bosh.ErrandOpts{})
	if !errors.Is(err, bosh.ErrNoErrandResults) {
		t.Fatalf("expected ErrNoErrandResults, got: %v", err)
	}

	for _, want := range []string{"BOSH task " + strconv.Itoa(errandTaskID), "no instances to run on"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}
