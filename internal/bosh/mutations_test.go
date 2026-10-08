package bosh_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// Values the mutation tests below repeat.
const (
	mutationManifest      = "name: dep-1\ninstance_groups: []\n"
	jobStateDeployment    = "x"
	redirectTaskID        = 42
	contextIDHeader       = "X-Bosh-Context-Id"
	callerContextID       = "ctx-caller"
	releaseURL            = "https://example.com/release.tgz"
	stemcellURL           = "https://example.com/stemcell.tgz"
	artifactSHA1          = "0123456789abcdef0123456789abcdef01234567"
	wildcardJobStatePath  = "/deployments/x/jobs/*"
	deploymentPath        = "/deployments/dep-1"
	noLookupClaim         = "looked for a task"
	contextIDSearchAdvice = "Look for a director task with context ID"
)

// sentMutation is a request that changes something on the test director.
type sentMutation struct {
	Method     string
	Path       string
	RequestURI string
	Query      url.Values
	ContextID  string
	Body       string
}

// mutationLog records every request other than a GET that reaches the test
// director.
type mutationLog struct {
	mu   sync.Mutex
	sent []sentMutation
}

func (l *mutationLog) add(request *http.Request) sentMutation {
	body, _ := io.ReadAll(request.Body)

	sent := sentMutation{
		Method:     request.Method,
		Path:       request.URL.Path,
		RequestURI: request.RequestURI,
		Query:      request.URL.Query(),
		ContextID:  request.Header.Get(contextIDHeader),
		Body:       string(body),
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sent = append(l.sent, sent)

	return sent
}

func (l *mutationLog) all() []sentMutation {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]sentMutation(nil), l.sent...)
}

// only returns the one mutation sent, and fails the test when there was
// not exactly one.
func (l *mutationLog) only(t *testing.T) sentMutation {
	t.Helper()

	sent := l.all()
	if len(sent) != 1 {
		t.Fatalf("expected exactly one mutating request, got %d: %+v", len(sent), sent)
	}

	return sent[0]
}

// redirectToTask answers the way the director accepts a request that
// starts a task, with a 302 to task redirectTaskID.
func redirectToTask(writer http.ResponseWriter) {
	writer.Header().Set("Location", "/tasks/"+strconv.Itoa(redirectTaskID))
	writer.WriteHeader(http.StatusFound)
}

// newMutationDirector starts a test director that hands every mutating
// request to answer, after recording it, and answers every task list with
// tasks. A GET of a single task answers that the task is done.
func newMutationDirector(t *testing.T, tasks []map[string]interface{}, answer func(http.ResponseWriter, *http.Request)) (*boshtest.Director, *mutationLog) {
	t.Helper()

	log := &mutationLog{}

	if tasks == nil {
		tasks = []map[string]interface{}{}
	}

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method == http.MethodGet {
			switch {
			case request.URL.Path == taskListRoute:
				boshtest.WriteJSON(writer, http.StatusOK, tasks)

				return true
			case strings.HasPrefix(request.URL.Path, "/tasks/"):
				taskID, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/tasks/"))
				if err != nil {
					return false
				}

				boshtest.WriteJSON(writer, http.StatusOK, directorTask(taskID, batchTaskDone))

				return true
			default:
				return false
			}
		}

		log.add(request)
		answer(writer, request)

		return true
	})

	return server, log
}

// mutationCall is one of the ten adapter methods that change something on
// the director.
type mutationCall struct {
	operation  string
	method     string
	path       string
	deployment string
	call       func(ctx context.Context, director bosh.Director) (*bosh.Task, error)
}

func createCall() mutationCall {
	return mutationCall{"CreateDeployment", http.MethodPost, deploymentsRoute, deploymentName,
		func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
			return director.CreateDeployment(ctx, mutationManifest)
		}}
}

func updateCall() mutationCall {
	return mutationCall{"UpdateDeployment", http.MethodPost, deploymentsRoute, deploymentName,
		func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
			return director.UpdateDeployment(ctx, deploymentName, mutationManifest)
		}}
}

func deleteCall() mutationCall {
	return mutationCall{"DeleteDeployment", http.MethodDelete, deploymentPath, deploymentName,
		func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
			return director.DeleteDeployment(ctx, deploymentName)
		}}
}

func jobStateCalls() map[string]mutationCall {
	return map[string]mutationCall{
		"restart": {"RestartDeployment", http.MethodPut, wildcardJobStatePath, jobStateDeployment,
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.RestartDeployment(ctx, jobStateDeployment, bosh.RestartOpts{})
			}},
		"stop": {"StopDeployment", http.MethodPut, wildcardJobStatePath, jobStateDeployment,
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.StopDeployment(ctx, jobStateDeployment, bosh.StopOpts{})
			}},
		"start": {"StartDeployment", http.MethodPut, wildcardJobStatePath, jobStateDeployment,
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.StartDeployment(ctx, jobStateDeployment, bosh.StartOpts{})
			}},
		"recreate": {"RecreateDeployment", http.MethodPut, wildcardJobStatePath, jobStateDeployment,
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.RecreateDeployment(ctx, jobStateDeployment, bosh.RecreateOpts{})
			}},
	}
}

func uploadAndCleanupCalls() map[string]mutationCall {
	return map[string]mutationCall{
		"upload release": {"UploadRelease", http.MethodPost, "/releases", "",
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.UploadRelease(ctx, releaseURL, artifactSHA1)
			}},
		"upload stemcell": {"UploadStemcell", http.MethodPost, "/stemcells", "",
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.UploadStemcell(ctx, stemcellURL, artifactSHA1)
			}},
		"cleanup": {"Cleanup", http.MethodPost, "/cleanup", "",
			func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.Cleanup(ctx, true)
			}},
	}
}

// allMutationCalls returns the ten mutations by name.
func allMutationCalls() map[string]mutationCall {
	calls := map[string]mutationCall{
		"create": createCall(),
		"update": updateCall(),
		"delete": deleteCall(),
	}

	for name, call := range jobStateCalls() {
		calls[name] = call
	}

	for name, call := range uploadAndCleanupCalls() {
		calls[name] = call
	}

	return calls
}

// checkRedirectTask checks that a mutation returned the task the redirect
// named, as the director reports it on acceptance.
func checkRedirectTask(t *testing.T, task *bosh.Task, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("expected the redirect task, got %v", err)
	}

	if task == nil || task.ID != redirectTaskID || task.State != "queued" {
		t.Fatalf("expected task %d queued, got %+v", redirectTaskID, task)
	}

	if task.Description != "" {
		t.Errorf("the redirect names only the task, so the description must be empty, got %q", task.Description)
	}
}

// A deploy returns the task the director's redirect names, as soon as the
// director accepts it, and sends the manifest as it is.
func TestCreateDeploymentReturnsTheRedirectTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.CreateDeployment(t.Context(), mutationManifest)
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodPost || sent.Path != deploymentsRoute || sent.Body != mutationManifest {
		t.Errorf("expected the manifest in POST /deployments, got %+v", sent)
	}

	if requests := server.APIRequests(); len(requests) != 1 {
		t.Errorf("expected the deploy and nothing else, got %+v", requests)
	}
}

// A newer task that runs on the deployment is never taken for the deploy
// task, because the redirect names the deploy task itself.
func TestCreateDeploymentReturnsTheRedirectTaskNotTheNewestRunningTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	running := directorTask(redirectTaskID+50, batchTaskProcessing)
	running["deployment"] = deploymentName

	server, _ := newMutationDirector(t, []map[string]interface{}{running}, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.CreateDeployment(t.Context(), mutationManifest)
	checkRedirectTask(t, task, err)

	if lists := requestsTo(server, http.MethodGet, taskListRoute); len(lists) != 0 {
		t.Errorf("expected no task list read, got %+v", lists)
	}
}

func TestUpdateDeploymentReturnsTheRedirectTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.UpdateDeployment(t.Context(), deploymentName, mutationManifest)
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodPost || sent.Path != deploymentsRoute || sent.Body != mutationManifest {
		t.Errorf("expected the manifest in POST /deployments, got %+v", sent)
	}

	if requests := server.APIRequests(); len(requests) != 1 {
		t.Errorf("expected the deploy and nothing else, got %+v", requests)
	}
}

func TestDeleteDeploymentSendsNoForceAndReturnsTheRedirectTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.DeleteDeployment(t.Context(), deploymentName)
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodDelete || sent.Path != deploymentPath {
		t.Errorf("expected DELETE %s, got %+v", deploymentPath, sent)
	}

	if sent.Query.Has("force") {
		t.Errorf("expected no force parameter, got %q", sent.RequestURI)
	}

	if requests := server.APIRequests(); len(requests) != 1 {
		t.Errorf("expected the delete and nothing else, got %+v", requests)
	}
}

func TestUploadsSendTheURLAndSHA1AndReturnTheRedirectTask(t *testing.T) {
	uploads := map[string]struct {
		path string
		url  string
		call func(ctx context.Context, director bosh.Director) (*bosh.Task, error)
	}{
		"release": {"/releases", releaseURL, func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
			return director.UploadRelease(ctx, releaseURL, artifactSHA1)
		}},
		"stemcell": {"/stemcells", stemcellURL, func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
			return director.UploadStemcell(ctx, stemcellURL, artifactSHA1)
		}},
	}

	for name, upload := range uploads {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				redirectToTask(writer)
			})
			director := newReadAdapter(t, server, nil)

			task, err := upload.call(t.Context(), director)
			checkRedirectTask(t, task, err)

			sent := log.only(t)
			if sent.Method != http.MethodPost || sent.Path != upload.path {
				t.Fatalf("expected POST %s, got %+v", upload.path, sent)
			}

			var body map[string]interface{}

			err = json.Unmarshal([]byte(sent.Body), &body)
			if err != nil {
				t.Fatalf("expected a JSON body, got %q: %v", sent.Body, err)
			}

			if body["location"] != upload.url || body["sha1"] != artifactSHA1 {
				t.Errorf("expected the URL and SHA1 in the body, got %v", body)
			}

			if requests := server.APIRequests(); len(requests) != 1 {
				t.Errorf("expected the upload and nothing else, got %+v", requests)
			}
		})
	}
}

func TestCleanupSendsRemoveAllAndReturnsTheRedirectTask(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.Cleanup(t.Context(), true)
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodPost || sent.Path != "/cleanup" {
		t.Fatalf("expected POST /cleanup, got %+v", sent)
	}

	var body struct {
		Config struct {
			RemoveAll bool `json:"remove_all"`
		} `json:"config"`
	}

	err = json.Unmarshal([]byte(sent.Body), &body)
	if err != nil || !body.Config.RemoveAll {
		t.Errorf("expected remove_all true in the body, got %q (%v)", sent.Body, err)
	}

	if requests := server.APIRequests(); len(requests) != 1 {
		t.Errorf("expected the cleanup and nothing else, got %+v", requests)
	}
}

func TestRestartSendsTheLiteralWildcardJobWithCanaries(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.RestartDeployment(t.Context(), jobStateDeployment, bosh.RestartOpts{Canaries: "2", MaxInFlight: "50%"})
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Method != http.MethodPut || !strings.HasPrefix(sent.RequestURI, wildcardJobStatePath+"?") {
		t.Fatalf("expected PUT %s with a query, got %s %s", wildcardJobStatePath, sent.Method, sent.RequestURI)
	}

	if sent.Query.Get("state") != "restart" || sent.Query.Get("canaries") != "2" || sent.Query.Get("max_in_flight") != "50%" {
		t.Errorf("expected state restart with the canaries and max in flight, got %q", sent.RequestURI)
	}

	if sent.Query.Has("force") {
		t.Errorf("expected no force parameter, got %q", sent.RequestURI)
	}
}

func TestHardStopSendsTheDetachedState(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.StopDeployment(t.Context(), jobStateDeployment, bosh.StopOpts{Hard: true})
	checkRedirectTask(t, task, err)

	sent := log.only(t)
	if sent.Path != wildcardJobStatePath || sent.Query.Get("state") != "detached" {
		t.Errorf("expected state detached on %s, got %q", wildcardJobStatePath, sent.RequestURI)
	}
}

// Each job-state option the caller sets reaches the director's query. The
// options a struct lacks, such as Fix on a start, are not sent.
func TestJobStateOptionsMapAcross(t *testing.T) {
	changes := map[string]struct {
		call  func(ctx context.Context, director bosh.Director) (*bosh.Task, error)
		state string
		want  map[string]string
	}{
		"start": {
			call: func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.StartDeployment(ctx, jobStateDeployment, bosh.StartOpts{Canaries: "1", MaxInFlight: "3"})
			},
			state: "started",
			want:  map[string]string{"canaries": "1", "max_in_flight": "3"},
		},
		"stop": {
			call: func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.StopDeployment(ctx, jobStateDeployment, bosh.StopOpts{SkipDrain: true, Canaries: "1", MaxInFlight: "3"})
			},
			state: "stopped",
			want:  map[string]string{"skip_drain": valueTrue, "canaries": "1", "max_in_flight": "3"},
		},
		"restart": {
			call: func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.RestartDeployment(ctx, jobStateDeployment, bosh.RestartOpts{SkipDrain: true, Canaries: "1", MaxInFlight: "3"})
			},
			state: "restart",
			want:  map[string]string{"skip_drain": valueTrue, "canaries": "1", "max_in_flight": "3"},
		},
		"recreate": {
			call: func(ctx context.Context, director bosh.Director) (*bosh.Task, error) {
				return director.RecreateDeployment(ctx, jobStateDeployment, bosh.RecreateOpts{
					SkipDrain: true, Fix: true, DryRun: true, Canaries: "1", MaxInFlight: "3",
				})
			},
			state: "recreate",
			want: map[string]string{
				"skip_drain": valueTrue, "fix": valueTrue, "dry_run": valueTrue, "canaries": "1", "max_in_flight": "3",
			},
		},
	}

	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				redirectToTask(writer)
			})
			director := newReadAdapter(t, server, nil)

			task, err := change.call(t.Context(), director)
			checkRedirectTask(t, task, err)

			sent := log.only(t)
			if sent.Path != wildcardJobStatePath || sent.Query.Get("state") != change.state {
				t.Fatalf("expected state %s on %s, got %q", change.state, wildcardJobStatePath, sent.RequestURI)
			}

			for key, value := range change.want {
				if got := sent.Query.Get(key); got != value {
					t.Errorf("expected %s=%s, got %q in %q", key, value, got, sent.RequestURI)
				}
			}

			if len(sent.Query) != len(change.want)+1 {
				t.Errorf("expected only state and %v in the query, got %q", change.want, sent.RequestURI)
			}
		})
	}
}

func TestCreateDeploymentUsesTheCallersContextID(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.CreateDeployment(boshapi.WithContextID(t.Context(), callerContextID), mutationManifest)
	checkRedirectTask(t, task, err)

	if sent := log.only(t); sent.ContextID != callerContextID {
		t.Errorf("expected the caller's context ID %s in the header, got %q", callerContextID, sent.ContextID)
	}
}

// A caller that brings no context ID gets a fresh UUID on the request, and
// a lost answer names the ID that was sent.
func TestCreateDeploymentGeneratesAContextIDWhenTheCallerSetsNone(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	director := newReadAdapter(t, server, nil)

	task, err := director.CreateDeployment(t.Context(), mutationManifest)
	checkRedirectTask(t, task, err)

	sent := log.only(t)

	_, err = uuid.Parse(sent.ContextID)
	if err != nil {
		t.Fatalf("expected a UUID in the context ID header, got %q: %v", sent.ContextID, err)
	}

	lostServer, lostLog := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.DropAnswer(writer)
	})
	lostDirector := newReadAdapter(t, lostServer, nil)

	_, err = lostDirector.CreateDeployment(t.Context(), mutationManifest)

	var unknown *bosh.OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected an unknown outcome, got %v", err)
	}

	lost := lostLog.only(t)
	if lost.ContextID == "" || unknown.ContextID != lost.ContextID {
		t.Errorf("expected the error to name the context ID %q that was sent, got %q", lost.ContextID, unknown.ContextID)
	}

	if lost.ContextID == sent.ContextID {
		t.Errorf("expected each call to get a fresh context ID, got %q twice", lost.ContextID)
	}
}

// A context ID the director would refuse is refused before anything is
// sent, and the refusal is not an unknown outcome.
func TestMutationRefusesAnInvalidCallersContextID(t *testing.T) {
	ids := map[string]string{
		"65 characters":       strings.Repeat("a", 65),
		"a control character": "ctx-\x01-id",
	}

	for idName, id := range ids {
		for callName, call := range allMutationCalls() {
			t.Run(idName+"/"+callName, func(t *testing.T) {
				t.Setenv("BLACKSMITH_TEST_MODE", "true")

				server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
					redirectToTask(writer)
				})
				director := newReadAdapter(t, server, nil)

				task, err := call.call(boshapi.WithContextID(t.Context(), id), director)
				if !errors.Is(err, bosh.ErrInvalidContextID) {
					t.Fatalf("expected ErrInvalidContextID, got %+v and %v", task, err)
				}

				if errors.Is(err, bosh.ErrOutcomeUnknown) {
					t.Errorf("nothing was sent, so the outcome is known, got %v", err)
				}

				if !strings.Contains(err.Error(), strconv.Itoa(len(id))) {
					t.Errorf("expected the error to give the ID's length %d, got %v", len(id), err)
				}

				if requests := server.APIRequests(); len(requests) != 0 {
					t.Errorf("expected nothing sent, got %+v", requests)
				}
			})
		}
	}
}

// checkLostAnswer checks the error of a mutation whose answer was lost.
func checkLostAnswer(t *testing.T, err error, call mutationCall, wantContextID string) *bosh.OutcomeUnknownError {
	t.Helper()

	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Fatalf("expected an unknown outcome, got %v", err)
	}

	var unknown *bosh.OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected an OutcomeUnknownError, got %T: %v", err, err)
	}

	if unknown.ContextID != wantContextID || unknown.Deployment != call.deployment || unknown.Operation != call.operation {
		t.Errorf("expected %s on deployment %q with context ID %s, got %+v", call.operation, call.deployment, wantContextID, unknown)
	}

	return unknown
}

// A deploy or delete whose answer was lost is an unknown outcome that
// names the context ID it carried, and it is sent once.
func TestDeployAndDeleteLostAnswersAreOutcomeUnknown(t *testing.T) {
	calls := map[string]mutationCall{"create": createCall(), "update": updateCall(), "delete": deleteCall()}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				boshtest.DropAnswer(writer)
			})
			director := newReadAdapter(t, server, nil)

			task, err := call.call(boshapi.WithContextID(t.Context(), contextID), director)
			if task != nil {
				t.Errorf("expected no task, got %+v", task)
			}

			_ = checkLostAnswer(t, err, call, contextID)

			sent := log.only(t)
			if sent.Method != call.method || sent.Path != call.path || sent.ContextID != contextID {
				t.Errorf("expected %s %s with context ID %s, got %+v", call.method, call.path, contextID, sent)
			}

			if mutating := len(server.APIRequests()) - len(requestsTo(server, http.MethodGet, taskListRoute)); mutating != 1 {
				t.Errorf("expected exactly one request other than a task list, got %+v", server.APIRequests())
			}
		})
	}
}

// cancelOnArrival returns an answer that cancels the caller's context once
// the request has reached the director, and then never answers.
func cancelOnArrival(cancel context.CancelFunc) func(http.ResponseWriter, *http.Request) {
	return func(_ http.ResponseWriter, request *http.Request) {
		cancel()

		select {
		case <-request.Context().Done():
		case <-time.After(batchWaitLimit):
		}
	}
}

// checkCancelledLostAnswer runs a mutation whose caller's context is
// cancelled mid-request, and checks that the error is still an unknown
// outcome behind the context's own explanation.
func checkCancelledLostAnswer(t *testing.T, call mutationCall) {
	t.Helper()
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	ctx, cancel := context.WithCancel(boshapi.WithContextID(t.Context(), contextID))
	defer cancel()

	server, log := newMutationDirector(t, nil, cancelOnArrival(cancel))
	director := newReadAdapter(t, server, nil)

	_, err := call.call(ctx, director)

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) || !errors.Is(err, context.Canceled) {
		t.Errorf("expected the error to explain the cancelled context, got %v", err)
	}

	_ = checkLostAnswer(t, err, call, contextID)
	log.only(t)
}

//nolint:paralleltest // t.Setenv cannot be used with t.Parallel
func TestDeployWithACancelledCallerContextStillReturnsOutcomeUnknown(t *testing.T) {
	checkCancelledLostAnswer(t, createCall())
}

//nolint:paralleltest // t.Setenv cannot be used with t.Parallel
func TestDeleteWithACancelledCallerContextStillReturnsOutcomeUnknown(t *testing.T) {
	checkCancelledLostAnswer(t, deleteCall())
}

// A job-state change, an upload, or Cleanup whose answer was lost is an
// unknown outcome with no lookup, because the director may not store the
// context ID of these calls.
func TestJobStateUploadAndCleanupLostAnswersMakeNoLookup(t *testing.T) {
	calls := jobStateCalls()
	for name, call := range uploadAndCleanupCalls() {
		calls[name] = call
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				boshtest.DropAnswer(writer)
			})
			director := newReadAdapter(t, server, nil)

			_, err := call.call(boshapi.WithContextID(t.Context(), contextID), director)
			unknown := checkLostAnswer(t, err, call, contextID)

			if !unknown.NoLookup {
				t.Errorf("expected NoLookup set, got %+v", unknown)
			}

			requests := server.APIRequests()
			if len(requests) != 1 || requests[0].Method != call.method || requests[0].Path != call.path {
				t.Errorf("expected only %s %s, got %+v", call.method, call.path, requests)
			}

			message := err.Error()

			want := "Check /b/tasks for a recent"
			if call.deployment != "" {
				want = "Check /b/tasks for a task on the deployment " + call.deployment
			}

			if !strings.Contains(message, want) {
				t.Errorf("expected the message to contain %q, got: %s", want, message)
			}

			for _, unwanted := range []string{noLookupClaim, contextIDSearchAdvice} {
				if strings.Contains(message, unwanted) {
					t.Errorf("expected no %q in the message, got: %s", unwanted, message)
				}
			}

			if !strings.Contains(message, contextID) {
				t.Errorf("expected the message to name the context ID, got: %s", message)
			}
		})
	}
}

func TestDeleteDeploymentMapsCode70000ToNotFound(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'dep-1' doesn't exist")
	})
	director := newReadAdapter(t, server, nil)

	_, err := director.DeleteDeployment(t.Context(), deploymentName)
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound {
		t.Errorf("expected the director's answer to stay reachable, got %v", err)
	}
}

func TestDeleteDeploymentRoute404IsARouteNotFoundError(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		boshtest.WriteRouteNotFound(writer, request)
	})
	director := newReadAdapter(t, server, nil)

	_, err := director.DeleteDeployment(t.Context(), deploymentName)
	if !boshapi.IsRouteNotFound(err) {
		t.Fatalf("expected a route not found error, got %v", err)
	}

	if errors.Is(err, bosh.ErrDeploymentNotFound) || errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("a route 404 says nothing about the deployment, got %v", err)
	}
}

// A delete the director answers with code 70000 started no task, so the
// pool frees its slot at once for the next mutation.
func TestPooledSlotIsReleasedAtOnceWhenTheDeleteIsNotFound(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'dep-1' doesn't exist")

			return
		}

		redirectToTask(writer)
	})
	pool, _, _ := newTestPool(t, newReadAdapter(t, server, nil), 1)

	_, err := pool.DeleteDeployment(t.Context(), deploymentName)
	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("expected ErrDeploymentNotFound, got %v", err)
	}

	waitSlotsFree(t, pool)

	ctx, cancel := context.WithTimeout(t.Context(), batchWaitLimit)
	defer cancel()

	task, err := pool.RestartDeployment(ctx, jobStateDeployment, bosh.RestartOpts{})
	if err != nil || task == nil || task.ID != redirectTaskID {
		t.Fatalf("expected the next mutation to get the slot, got %+v and %v", task, err)
	}
}

// newRealAdapterBatch returns a batch director of one slot over the real
// adapter and the test director, with a fake clock.
func newRealAdapterBatch(t *testing.T, server *boshtest.Director) (*bosh.BatchDirector, *fakeBatchClock) {
	t.Helper()

	batch := bosh.NewBatchDirector(newReadAdapter(t, server, nil), 1, time.Minute, nil)
	clock := newFakeBatchClock()
	bosh.SetBatchClock(batch, clock)
	t.Cleanup(batch.Stop)

	return batch, clock
}

// The director's rejection of a deploy started no task, so the batch slot
// frees at once and the next deploy gets it.
func TestBatchSlotIsReleasedWhenTheRealAdapterGetsARejection(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	var rejected sync.Once

	server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		first := false

		rejected.Do(func() { first = true })

		if first {
			boshtest.WriteError(writer, http.StatusBadRequest, 440001, "manifest is invalid")

			return
		}

		redirectToTask(writer)
	})
	batch, _ := newRealAdapterBatch(t, server)

	_, err := batch.UpdateDeployment(t.Context(), deploymentName, mutationManifest)

	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected the director's rejection, got %v", err)
	}

	waitBatchStats(t, batch, 0, 0)

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Errorf("expected no watcher for a rejected deploy, got %d", running)
	}

	ctx, cancel := context.WithTimeout(t.Context(), batchWaitLimit)
	defer cancel()

	task, err := batch.UpdateDeployment(ctx, secondDeployment, mutationManifest)
	if err != nil || task == nil || task.ID != redirectTaskID {
		t.Fatalf("expected the next deploy to get the slot, got %+v and %v", task, err)
	}
}

// A deploy that never reached the director started no task, so the batch
// slot frees at once.
func TestBatchSlotIsReleasedWhenTheRealAdapterGetsAnUnsentRequest(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, log := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
		redirectToTask(writer)
	})
	batch, _ := newRealAdapterBatch(t, server)

	// Every later dial to the director fails.
	server.Server.Close()

	_, err := batch.UpdateDeployment(t.Context(), deploymentName, mutationManifest)

	var transportErr *boshapi.TransportError
	if !errors.As(err, &transportErr) || transportErr.Sent {
		t.Fatalf("expected a request that was never sent, got %v", err)
	}

	waitBatchStats(t, batch, 0, 0)

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Errorf("expected no watcher for an unsent deploy, got %d", running)
	}

	if sent := log.all(); len(sent) != 0 {
		t.Errorf("expected nothing to reach the director, got %+v", sent)
	}
}

// A gateway's 502 or 504 to a deploy may hide a deploy the director
// accepted, so the batch slot stays held and the lost-answer watcher
// looks for the task.
func TestBatchSlotIsKeptWhenTheRealAdapterGetsA502Or504OnTheDeploy(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, _ := newMutationDirector(t, nil, func(writer http.ResponseWriter, _ *http.Request) {
				boshtest.WriteText(writer, status, "gateway error")
			})
			batch, clock := newRealAdapterBatch(t, server)

			_, err := batch.UpdateDeployment(boshapi.WithContextID(t.Context(), contextID), deploymentName, mutationManifest)
			if !errors.Is(err, bosh.ErrOutcomeUnknown) || !boshapi.IsOutcomeUnknown(err) {
				t.Fatalf("expected an unknown outcome, got %v", err)
			}

			clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

			if active, queued, _ := batch.GetBatchPoolStats(); active != 1 || queued != 0 {
				t.Errorf("expected the slot held, got %d active and %d queued", active, queued)
			}

			if running := bosh.BatchWatchersRunning(batch); running != 1 {
				t.Errorf("expected the lost-answer watcher to run, got %d watchers", running)
			}
		})
	}
}
