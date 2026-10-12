package bosh_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
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

func TestDeployWithACancelledCallerContextStillReturnsOutcomeUnknown(t *testing.T) {
	checkCancelledLostAnswer(t, createCall())
}

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

// neverAuthenticatedReason is how the follower says why it freed the slot
// of a deploy whose UAA token request failed.
const neverAuthenticatedReason = "the deploy request was never authenticated, so it was never sent"

// resetConnection ends the connection behind writer with a TCP reset, the
// way a UAA that drops the connection does.
func resetConnection(writer http.ResponseWriter) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		panic("the response writer cannot reset the connection")
	}

	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic("resetting the connection failed: " + err.Error())
	}

	raw := conn
	if tlsConn, isTLS := conn.(*tls.Conn); isTLS {
		raw = tlsConn.NetConn()
	}

	if tcpConn, isTCP := raw.(*net.TCPConn); isTCP {
		_ = tcpConn.SetLinger(0)
	}

	_ = raw.Close()
}

// newResettingUAAAdapter returns a real adapter over a test director whose
// GET /info names a UAA, and that UAA ends every token request in a
// connection reset. It also returns the director, the log of every other
// request the director got, and the UAA.
func newResettingUAAAdapter(t *testing.T) (bosh.Director, *boshtest.Director, *mutationLog, *boshtest.Director) {
	t.Helper()

	uaa := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/oauth/token" {
			return false
		}

		resetConnection(writer)

		return true
	})

	log := &mutationLog{}

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method == http.MethodGet && request.URL.Path == infoRoute {
			directorInfo(writer, uaaAuthentication(uaa.URL))

			return true
		}

		log.add(request)
		redirectToTask(writer)

		return true
	})

	director := newReadAdapter(t, server, func(config *bosh.Config) {
		config.Username, config.Password = "uaa-client", "uaa-secret"
	})

	return director, server, log, uaa
}

// expectNeverAuthenticated checks that err is the failed UAA token request
// and that the director was asked only how it authenticates.
func expectNeverAuthenticated(t *testing.T, err error, uaa, server *boshtest.Director, log *mutationLog) {
	t.Helper()

	var authErr *boshapi.AuthError
	if !errors.As(err, &authErr) || authErr.Stage != boshapi.AuthStageToken {
		t.Fatalf("expected the UAA token request to fail, got %v", err)
	}

	if len(uaa.Requests()) == 0 {
		t.Fatal("the UAA got no token request")
	}

	if sent := log.all(); len(sent) != 0 {
		t.Fatalf("expected nothing to reach the director, got %+v", sent)
	}

	if requests := server.APIRequests(); len(requests) != 0 {
		t.Fatalf("expected no request but GET /info to reach the director, got %+v", requests)
	}
}

// A deploy whose UAA token request failed was never sent, so the pool frees
// its slot as soon as the call returns and follows nothing.
func TestPooledAuthErrorFreesTheSlotAtOnce(t *testing.T) {
	director, server, log, uaa := newResettingUAAAdapter(t)
	pool, _, logger := newTestPool(t, director, 1)

	_, err := pool.CreateDeployment(t.Context(), mutationManifest)
	expectNeverAuthenticated(t, err, uaa, server, log)

	if active := activeSlots(t, pool); active != 0 {
		t.Fatalf("expected the slot to free when the call returned, got %d held", active)
	}

	if running := bosh.PoolWatchersRunning(pool); running != 0 {
		t.Fatalf("expected no watcher, got %d", running)
	}

	if logger.count("INFO", "Released the pool slot of deployment "+deploymentName+" at once", neverAuthenticatedReason) != 1 {
		t.Fatalf("expected the release to be logged with %q, got %q", neverAuthenticatedReason, poolLogLines(logger))
	}
}

// A deploy whose UAA token request failed was never sent, so the batch
// director frees its slot as soon as the call returns and follows nothing.
func TestBatchAuthErrorFreesTheSlotAtOnce(t *testing.T) {
	director, server, log, uaa := newResettingUAAAdapter(t)
	logger := &recordingLogger{}
	batch := bosh.NewBatchDirector(director, 1, time.Minute, logger)
	bosh.SetBatchClock(batch, newFakeBatchClock())
	t.Cleanup(batch.Stop)

	_, err := batch.UpdateDeployment(t.Context(), deploymentName, mutationManifest)
	expectNeverAuthenticated(t, err, uaa, server, log)

	if active, queued, _ := batch.GetBatchPoolStats(); active != 0 || queued != 0 {
		t.Fatalf("expected the slot to free when the call returned, got %d active and %d queued", active, queued)
	}

	if running := bosh.BatchWatchersRunning(batch); running != 0 {
		t.Fatalf("expected no watcher, got %d", running)
	}

	if logger.count("INFO", "Released the batch slot of deployment "+deploymentName+" at once", neverAuthenticatedReason) != 1 {
		t.Fatalf("expected the release to be logged with %q, got %q", neverAuthenticatedReason, poolLogLines(logger))
	}
}

// A gateway's 502 or 504 to a deploy may hide a deploy the director
// accepted, so the batch slot stays held and the lost-answer watcher
// looks for the task.
func TestBatchSlotIsKeptWhenTheRealAdapterGetsA502Or504OnTheDeploy(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
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

// lookedUpTaskID is the task the test director lists under the context ID
// of a request whose answer it dropped.
const lookedUpTaskID = 77

// lostAnswerDirector is a test director that drops the answer to every
// mutating request after recording it, and hands each task list query to
// a lookup answer. A GET of a single task answers with taskState.
type lostAnswerDirector struct {
	server    *boshtest.Director
	mutations *mutationLog
	lists     *mutationLog
	taskState func() string
}

// newLostAnswerDirector starts a lostAnswerDirector whose single tasks are
// done.
func newLostAnswerDirector(t *testing.T, lookup func(http.ResponseWriter, *http.Request)) *lostAnswerDirector {
	t.Helper()

	director := &lostAnswerDirector{
		mutations: &mutationLog{},
		lists:     &mutationLog{},
		taskState: func() string { return batchTaskDone },
	}

	director.server = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet {
			director.mutations.add(request)
			boshtest.DropAnswer(writer)

			return true
		}

		switch {
		case request.URL.Path == taskListRoute:
			director.lists.add(request)
			lookup(writer, request)

			return true
		case strings.HasPrefix(request.URL.Path, "/tasks/"):
			taskID, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, "/tasks/"))
			if err != nil {
				return false
			}

			boshtest.WriteJSON(writer, http.StatusOK, directorTask(taskID, director.taskState()))

			return true
		default:
			return false
		}
	})

	return director
}

// contextIDLookups returns the task list queries that asked for a context
// ID.
func (d *lostAnswerDirector) contextIDLookups() []sentMutation {
	var lookups []sentMutation

	for _, sent := range d.lists.all() {
		if sent.Query.Has("context_id") {
			lookups = append(lookups, sent)
		}
	}

	return lookups
}

// lookedUpTask is the task the director lists under contextID for the
// call, described the way the director describes it.
func lookedUpTask(call mutationCall) map[string]interface{} {
	task := contextTask(lookedUpTaskID, batchTaskProcessing, contextID)
	task["deployment"] = call.deployment

	if call.method == http.MethodDelete {
		task["description"] = "delete deployment " + call.deployment
	}

	return task
}

// listsTheTask answers a query for contextID with the call's task, and
// every other task list with nothing.
func listsTheTask(call mutationCall) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("context_id") != contextID {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

			return
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{lookedUpTask(call)})
	}
}

// listsNothing answers every task list with nothing.
func listsNothing(writer http.ResponseWriter, _ *http.Request) {
	boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})
}

// lookupFailure is the description of the director's error answer to a
// task list.
const lookupFailure = "the task list is unavailable"

// failsEveryList answers every task list with a director error.
func failsEveryList(writer http.ResponseWriter, _ *http.Request) {
	boshtest.WriteError(writer, http.StatusInternalServerError, 100, lookupFailure)
}

// newLoggedAdapter returns an adapter over the test director whose log
// lines a recordingLogger keeps.
func newLoggedAdapter(t *testing.T, server *boshtest.Director, mutate func(*bosh.Config)) (bosh.Director, *recordingLogger) {
	t.Helper()

	logs := &recordingLogger{}
	director := newReadAdapter(t, server, func(config *bosh.Config) {
		config.Logger = logs

		if mutate != nil {
			mutate(config)
		}
	})

	return director, logs
}

// checkFoundTask checks that a mutation whose answer was lost returned the
// task the lookup by its context ID found, and that it looked up exactly
// that ID once.
func checkFoundTask(t *testing.T, director *lostAnswerDirector, task *bosh.Task, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("expected the task the lookup found, got %v", err)
	}

	if task == nil || task.ID != lookedUpTaskID || task.ContextID != contextID {
		t.Fatalf("expected task %d with context ID %s, got %+v", lookedUpTaskID, contextID, task)
	}

	lookups := director.contextIDLookups()
	if len(lookups) != 1 {
		t.Fatalf("expected one lookup by context ID, got %+v", lookups)
	}

	if lookups[0].Query.Get("verbose") != "2" || lookups[0].Query.Has("deployment") {
		t.Errorf("expected a lookup at verbose 2 with no deployment, got %q", lookups[0].RequestURI)
	}
}

// A deploy whose answer was lost is found by its context ID, so the caller
// gets the deploy's task and never learns the answer was lost.
func TestCreateDeploymentFindsTheTaskByContextIDAfterALostAnswer(t *testing.T) {
	call := createCall()
	director := newLostAnswerDirector(t, listsTheTask(call))
	adapter, logs := newLoggedAdapter(t, director.server, nil)

	task, err := call.call(boshapi.WithContextID(t.Context(), contextID), adapter)
	checkFoundTask(t, director, task, err)

	if task.Description != bosh.DeployTaskDescription || task.Deployment != deploymentName {
		t.Errorf("expected the deploy of %s as the director lists it, got %+v", deploymentName, task)
	}

	if sent := director.mutations.only(t); sent.Method != http.MethodPost || sent.ContextID != contextID {
		t.Errorf("expected one POST with context ID %s, got %+v", contextID, sent)
	}

	if logs.count("INFO", "lost", contextID, "task "+strconv.Itoa(lookedUpTaskID)) != 1 {
		t.Errorf("expected a log line that the answer was lost and the task was found, got %v", logs.lines)
	}
}

// A delete whose answer was lost is found by its context ID the same way.
func TestDeleteDeploymentFindsTheTaskByContextIDAfterALostAnswer(t *testing.T) {
	call := deleteCall()
	director := newLostAnswerDirector(t, listsTheTask(call))
	adapter, logs := newLoggedAdapter(t, director.server, nil)

	task, err := call.call(boshapi.WithContextID(t.Context(), contextID), adapter)
	checkFoundTask(t, director, task, err)

	if task.Description != "delete deployment "+deploymentName {
		t.Errorf("expected the delete of %s as the director lists it, got %+v", deploymentName, task)
	}

	if sent := director.mutations.only(t); sent.Method != http.MethodDelete || sent.Path != deploymentPath || sent.ContextID != contextID {
		t.Errorf("expected one DELETE %s with context ID %s, got %+v", deploymentPath, contextID, sent)
	}

	if logs.count("INFO", "lost", contextID, "task "+strconv.Itoa(lookedUpTaskID)) != 1 {
		t.Errorf("expected a log line that the answer was lost and the task was found, got %v", logs.lines)
	}
}

// A deploy whose answer was lost is sent once, whatever the lookup finds,
// even when the adapter may retry requests.
func TestCreateDeploymentIsNeverResentAfterALostAnswer(t *testing.T) {
	lookups := map[string]func(http.ResponseWriter, *http.Request){
		"the lookup finds the task": listsTheTask(createCall()),
		"the lookup finds nothing":  listsNothing,
		"the lookup itself fails":   failsEveryList,
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			director := newLostAnswerDirector(t, lookup)
			adapter, _ := newLoggedAdapter(t, director.server, func(config *bosh.Config) { config.MaxRetries = 3 })

			_, _ = adapter.CreateDeployment(boshapi.WithContextID(t.Context(), contextID), mutationManifest)

			if posts := requestsTo(director.server, http.MethodPost, deploymentsRoute); len(posts) != 1 {
				t.Errorf("expected exactly one deploy POST, got %d: %+v", len(posts), director.server.APIRequests())
			}

			if lookups := director.contextIDLookups(); len(lookups) != 1 {
				t.Errorf("expected one lookup by context ID, got %+v", lookups)
			}
		})
	}
}

// A lost deploy whose task the lookup did not find is an unknown outcome
// that names the deployment and the context ID, and says Blacksmith looked
// for the task.
func TestCreateDeploymentLostAnswerReturnsOutcomeUnknownWithDeploymentAndContextID(t *testing.T) {
	call := createCall()
	director := newLostAnswerDirector(t, listsNothing)
	adapter, _ := newLoggedAdapter(t, director.server, nil)

	task, err := call.call(boshapi.WithContextID(t.Context(), contextID), adapter)
	if task != nil {
		t.Errorf("expected no task, got %+v", task)
	}

	unknown := checkLostAnswer(t, err, call, contextID)
	if unknown.NoLookup {
		t.Errorf("expected NoLookup false once the adapter looked, got %+v", unknown)
	}

	message := err.Error()
	for _, want := range []string{"looked for a task with that context ID and found none yet", deploymentName, contextID} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to contain %q, got: %s", want, message)
		}
	}

	lookups := director.contextIDLookups()
	if len(lookups) != 1 || lookups[0].Query.Get("context_id") != contextID {
		t.Errorf("expected one lookup by context ID %s, got %+v", contextID, lookups)
	}
}

// A lookup that fails never replaces the unknown outcome with its own
// error, and the adapter logs the lookup's error with the context ID.
func TestLostAnswerLookupFailureStillReturnsOutcomeUnknown(t *testing.T) {
	calls := map[string]mutationCall{"create": createCall(), "delete": deleteCall()}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			director := newLostAnswerDirector(t, failsEveryList)
			adapter, logs := newLoggedAdapter(t, director.server, nil)

			_, err := call.call(boshapi.WithContextID(t.Context(), contextID), adapter)

			unknown := checkLostAnswer(t, err, call, contextID)
			if unknown.NoLookup {
				t.Errorf("expected NoLookup false once the adapter looked, got %+v", unknown)
			}

			var apiErr *boshapi.APIError
			if errors.As(err, &apiErr) {
				t.Errorf("the lookup's error must not reach the caller, got %v", err)
			}

			if sent := director.mutations.only(t); sent.Method != call.method || sent.ContextID != contextID {
				t.Errorf("expected one %s with context ID %s, got %+v", call.method, contextID, sent)
			}

			if len(director.contextIDLookups()) != 1 {
				t.Errorf("expected one lookup by context ID, got %+v", director.contextIDLookups())
			}

			if logs.count("ERROR", "lookup", contextID, lookupFailure) != 1 {
				t.Errorf("expected one Error line about the lookup with the context ID and its error, got %v", logs.lines)
			}
		})
	}
}

// A lost answer whose lookup by context ID failed says Blacksmith could
// not look for the task, and never claims it looked and found none.
func TestFailedLookupSaysTheTaskCouldNotBeSought(t *testing.T) {
	calls := map[string]mutationCall{"create": createCall(), "delete": deleteCall()}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			director := newLostAnswerDirector(t, failsEveryList)
			adapter, _ := newLoggedAdapter(t, director.server, nil)

			_, err := call.call(boshapi.WithContextID(t.Context(), contextID), adapter)

			unknown := checkLostAnswer(t, err, call, contextID)
			if !unknown.LookupFailed {
				t.Errorf("expected LookupFailed once the lookup failed, got %+v", unknown)
			}

			message := err.Error()
			if !strings.Contains(message, "Blacksmith could not look for a task with that context ID") {
				t.Errorf("expected the message to say the task could not be sought, got: %s", message)
			}

			for _, unwanted := range []string{"looked for a task", "found none"} {
				if strings.Contains(message, unwanted) {
					t.Errorf("expected no %q in the message, got: %s", unwanted, message)
				}
			}
		})
	}
}

// The lookup runs on a context of its own, so a caller whose context ended
// while the answer was lost still gets it.
func TestLostAnswerLookupRunsAfterTheCallersContextEnds(t *testing.T) {
	lookups := map[string]func(http.ResponseWriter, *http.Request){
		"the lookup finds the task": listsTheTask(createCall()),
		"the lookup finds nothing":  listsNothing,
	}

	for name, lookup := range lookups {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(boshapi.WithContextID(t.Context(), contextID))
			defer cancel()

			var mutations mutationLog

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method == http.MethodGet && request.URL.Path == taskListRoute {
					lookup(writer, request)

					return true
				}

				if request.Method == http.MethodGet {
					return false
				}

				mutations.add(request)
				cancelOnArrival(cancel)(writer, request)

				return true
			})
			adapter, _ := newLoggedAdapter(t, server, nil)

			task, err := adapter.CreateDeployment(ctx, mutationManifest)

			lists := requestsTo(server, http.MethodGet, taskListRoute)
			if len(lists) != 1 || !strings.Contains(lists[0].RawQuery, "context_id="+contextID) {
				t.Fatalf("expected the lookup by context ID after the caller's context ended, got %+v", server.APIRequests())
			}

			if len(mutations.all()) != 1 {
				t.Errorf("expected one deploy, got %+v", mutations.all())
			}

			if name == "the lookup finds the task" {
				if err != nil || task == nil || task.ID != lookedUpTaskID {
					t.Errorf("expected the task the lookup found, got %+v and %v", task, err)
				}

				return
			}

			_ = checkLostAnswer(t, err, createCall(), contextID)
		})
	}
}

// The batch director keeps the slot of a deploy whose answer was lost and
// whose task the adapter's lookup did not find yet. Its watcher finds the
// task by the context ID later, follows it to the end, and then frees the
// slot.
func TestBatchDirectorFollowsALostDeployAnswerThroughTheRealAdapter(t *testing.T) {
	var (
		mu       sync.Mutex
		listed   bool
		finished bool
	)

	director := newLostAnswerDirector(t, func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		show := listed
		mu.Unlock()

		if !show || request.URL.Query().Get("context_id") != contextID {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

			return
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{lookedUpTask(updateCall())})
	})
	director.taskState = func() string {
		mu.Lock()
		defer mu.Unlock()

		if finished {
			return batchTaskDone
		}

		return batchTaskProcessing
	}

	adapter := newReadAdapter(t, director.server, nil)
	bosh.SetTaskPollInterval(adapter, testPollInterval)

	batch := bosh.NewBatchDirector(adapter, 1, time.Minute, nil)
	clock := newFakeBatchClock()
	bosh.SetBatchClock(batch, clock)
	t.Cleanup(batch.Stop)

	_, err := batch.UpdateDeployment(boshapi.WithContextID(t.Context(), contextID), deploymentName, mutationManifest)

	unknown := checkLostAnswer(t, err, updateCall(), contextID)
	if unknown.NoLookup {
		t.Errorf("expected NoLookup false once the adapter looked, got %+v", unknown)
	}

	// The adapter's own lookup carries the request's context ID header,
	// and the watcher's carries none.
	inline := 0

	for _, lookup := range director.contextIDLookups() {
		if lookup.ContextID == contextID {
			inline++
		}
	}

	if inline != 1 {
		t.Fatalf("expected the adapter to look for the task once before it returned, got %+v", director.contextIDLookups())
	}

	clock.waitSleeping(t, bosh.BatchLostAnswerInterval)

	if active, _, _ := batch.GetBatchPoolStats(); active != 1 {
		t.Fatalf("expected the slot held while the watcher looks, got %d active", active)
	}

	mu.Lock()
	listed = true
	mu.Unlock()

	clock.advance(bosh.BatchLostAnswerInterval)

	taskPath := "/tasks/" + strconv.Itoa(lookedUpTaskID)
	waitUntil(t, "the watcher reads the task it found", func() bool {
		return len(requestsTo(director.server, http.MethodGet, taskPath)) > 0
	})

	if active, _, _ := batch.GetBatchPoolStats(); active != 1 {
		t.Errorf("expected the slot held while the task runs, got %d active", active)
	}

	mu.Lock()
	finished = true
	mu.Unlock()

	ctx, cancel := context.WithTimeout(t.Context(), batchWaitLimit)
	defer cancel()

	task, err := batch.WaitForContextID(ctx, contextID)
	if err != nil || task == nil || task.ID != lookedUpTaskID || task.State != batchTaskDone {
		t.Fatalf("expected the watcher to follow task %d to done, got %+v and %v", lookedUpTaskID, task, err)
	}

	waitBatchStats(t, batch, 0, 0)

	if posts := requestsTo(director.server, http.MethodPost, deploymentsRoute); len(posts) != 1 {
		t.Errorf("expected exactly one deploy POST, got %d", len(posts))
	}
}
