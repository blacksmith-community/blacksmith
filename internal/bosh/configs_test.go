package bosh_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Strings the tests below repeat.
const (
	valueTrue             = "true"
	queryType             = "type"
	queryName             = "name"
	queryLatest           = "latest"
	configsPath           = "/configs"
	typeRuntime           = "runtime"
	typeCloud             = "cloud"
	writeCreate           = "create"
	resurrectionConfigRef = "resurrection/blacksmith.dep-1"
	opEnableResurrection  = "EnableResurrection"
	opDeleteResurrection  = "DeleteResurrectionConfig"
	directorAddress       = "https://10.0.0.4:25555"
	deploymentName        = "dep-1"
	contextID             = "ctx-123"
	teamBlacksmith        = "blacksmith"
	opGetConfigByID       = "GetConfigByID"
	opGetConfigContent    = "GetConfigContent"
	opCleanUp             = "CleanUp"
	cloudContentSample    = "azs:\n- name: z1\n"
)

var errConnectionReset = errors.New("connection reset")

// errTransportAdvice has the words boshapi's transport error ends with, which
// send an operator to a director task a config write never creates.
var errTransportAdvice = errors.New("no answer came back. Look for a recent director task for the same deployment before you send it again")

// configEntry is one entry of the director's GET /configs answer.
func configEntry(id, configType, name string) map[string]interface{} {
	return map[string]interface{}{
		"id":         id,
		queryType:    configType,
		queryName:    name,
		"content":    "key: " + id + "\n",
		"created_at": "2026-10-01 12:00:00 UTC",
	}
}

// configsQuery returns the query of the only GET /configs the director saw.
func configsQuery(t *testing.T, server *boshtest.Director) url.Values {
	t.Helper()

	reads := requestsTo(server, http.MethodGet, configsPath)
	if len(reads) != 1 {
		t.Fatalf("expected exactly one GET /configs, got %d", len(reads))
	}

	query, err := url.ParseQuery(reads[0].RawQuery)
	if err != nil {
		t.Fatalf("failed to parse the query %q: %v", reads[0].RawQuery, err)
	}

	return query
}

// A config the director does not hold comes back as an empty list, and that
// is the normal "no such config" answer, not a failure.
func TestGetConfigMapsAnEmptyListToNotFound(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfig(t.Context(), typeRuntime, "absent")
	if !errors.Is(err, bosh.ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound for an empty list, got: %v", err)
	}

	query := configsQuery(t, server)
	if query.Get(queryType) != typeRuntime || query.Get(queryName) != "absent" || query.Get(queryLatest) != valueTrue {
		t.Errorf("expected type=runtime, name=absent, and latest=true, got %v", query)
	}
}

// A failure that is not an empty list keeps the failure, and never reads as a
// missing config, whatever words the error holds.
func TestGetConfigKeepsADirectorFailureAsAnError(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the config was not found in the database layer")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfig(t.Context(), typeCloud, "x")
	if err == nil || errors.Is(err, bosh.ErrConfigNotFound) {
		t.Fatalf("expected a failure that is not ErrConfigNotFound, got: %v", err)
	}

	if !errors.Is(err, bosh.ErrGetConfigFailed) {
		t.Errorf("expected ErrGetConfigFailed, got: %v", err)
	}
}

// The main list shows the latest version of every config, each marked active
// with a * on its ID, and the director is asked once for all of them.
func TestGetConfigsMarksTheLatestVersionsActive(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{
			configEntry("9", typeRuntime, "dns"),
			configEntry("4", typeCloud, "default"),
			configEntry("7", "cpi", "aws"),
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	configs, err := director.GetConfigs(t.Context(), 0, []string{typeCloud, typeRuntime})
	if err != nil {
		t.Fatalf("GetConfigs failed: %v", err)
	}

	if len(configs) != 2 {
		t.Fatalf("expected the cpi config to be filtered out, got %d configs: %+v", len(configs), configs)
	}

	if configs[0].Type != typeCloud || configs[0].ID != "4*" || !configs[0].IsActive {
		t.Errorf("expected the cloud config first, as 4* and active, got %+v", configs[0])
	}

	if configs[1].Type != typeRuntime || configs[1].ID != "9*" || !configs[1].IsActive {
		t.Errorf("expected the runtime config second, as 9* and active, got %+v", configs[1])
	}

	if configs[0].CreatedAt.IsZero() {
		t.Errorf("expected created_at to parse, got a zero time")
	}

	if query := configsQuery(t, server); query.Get(queryLatest) != "true" {
		t.Errorf("expected latest=true, got %v", query)
	}
}

// The version history asks for every version, not only the latest, and caps
// them at the limit.
func TestGetConfigVersionsSendsLatestFalseAndLimit(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		current := configEntry("12", typeCloud, "default")
		current["current"] = true

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{current, configEntry("11", typeCloud, "default")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	versions, err := director.GetConfigVersions(t.Context(), typeCloud, "default", 5)
	if err != nil {
		t.Fatalf("GetConfigVersions failed: %v", err)
	}

	query := configsQuery(t, server)
	if query.Get(queryType) != typeCloud || query.Get(queryName) != "default" || query.Get(queryLatest) != "false" || query.Get("limit") != "5" {
		t.Errorf("expected type=cloud, name=default, latest=false, and limit=5, got %v", query)
	}

	if len(versions) != 2 || !versions[0].IsActive || versions[1].IsActive {
		t.Errorf("expected only the current version to be active, got %+v", versions)
	}
}

// Resurrection is a config of type resurrection, one per deployment, and the
// director gets it as a POST /configs.
func TestEnableResurrectionCreatesAResurrectionConfig(t *testing.T) {
	var body map[string]string

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodPost || request.URL.Path != configsPath {
			return false
		}

		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)

		boshtest.WriteJSON(writer, http.StatusCreated, configEntry("21", "resurrection", "blacksmith.dep-1"))

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.EnableResurrection(t.Context(), deploymentName, false)
	if err != nil {
		t.Fatalf("EnableResurrection failed: %v", err)
	}

	if body[queryType] != "resurrection" || body[queryName] != "blacksmith.dep-1" {
		t.Errorf("expected type resurrection and name blacksmith.dep-1, got %v", body)
	}

	if !strings.Contains(body["content"], "enabled: false") || !strings.Contains(body["content"], "- dep-1") {
		t.Errorf("expected the content to disable resurrection for dep-1, got %q", body["content"])
	}

	if posts := requestsTo(server, http.MethodPost, configsPath); len(posts) != 1 {
		t.Errorf("expected one POST /configs, got %d", len(posts))
	}
}

// The boot cloud config is a named config, so the director merges it with its
// default cloud config instead of having it replaced. Nothing goes to the
// default cloud config route.
func TestUpdateCloudConfigCreatesTheNamedConfig(t *testing.T) {
	var body map[string]string

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodPost || request.URL.Path != configsPath {
			return false
		}

		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)

		boshtest.WriteJSON(writer, http.StatusCreated, configEntry("31", typeCloud, teamBlacksmith))

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.UpdateCloudConfig(t.Context(), cloudContentSample)
	if err != nil {
		t.Fatalf("UpdateCloudConfig failed: %v", err)
	}

	if body[queryType] != typeCloud || body[queryName] != teamBlacksmith || body["content"] != cloudContentSample {
		t.Errorf("expected type cloud, name blacksmith, and the content, got %v", body)
	}

	if posts := requestsTo(server, http.MethodPost, configsPath); len(posts) != 1 {
		t.Errorf("expected one POST /configs, got %d", len(posts))
	}

	for _, request := range server.Requests() {
		if strings.HasPrefix(request.Path, "/cloud_configs") {
			t.Errorf("expected nothing sent to /cloud_configs, got %s %s", request.Method, request.Path)
		}
	}
}

// A failed write names the director and keeps the director's description.
func TestUpdateCloudConfigKeepsTheDirectorFailure(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteError(writer, http.StatusBadRequest, 440001, "bad cloud config")

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.UpdateCloudConfig(t.Context(), "azs: []\n")
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{server.URL, "400", "bad cloud config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to hold %q, got: %v", want, err)
		}
	}
}

// Deleting a resurrection config sends the type and name, and a config that
// was not there reads as ErrResurrectionConfigNotFound.
func TestDeleteResurrectionConfigSendsTypeAndName(t *testing.T) {
	for name, status := range map[string]int{"deleted": http.StatusNoContent, "absent": http.StatusNotFound} {
		t.Run(name, func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodDelete || request.URL.Path != configsPath {
					return false
				}

				writer.WriteHeader(status)

				return true
			})

			director := newReadAdapter(t, server, nil)

			err := director.DeleteResurrectionConfig(t.Context(), deploymentName)

			if status == http.StatusNotFound {
				if !errors.Is(err, bosh.ErrResurrectionConfigNotFound) {
					t.Errorf("expected ErrResurrectionConfigNotFound, got: %v", err)
				}
			} else if err != nil {
				t.Errorf("expected the delete to succeed, got: %v", err)
			}

			deletes := requestsTo(server, http.MethodDelete, configsPath)
			if len(deletes) != 1 {
				t.Fatalf("expected one DELETE /configs, got %d", len(deletes))
			}

			query, _ := url.ParseQuery(deletes[0].RawQuery)
			if query.Get(queryType) != "resurrection" || query.Get(queryName) != "blacksmith.dep-1" {
				t.Errorf("expected type=resurrection and name=blacksmith.dep-1, got %v", query)
			}
		})
	}
}

// A config write creates no director task, so a write whose answer was lost
// has nothing to look up. The caller gets an OutcomeUnknownError that names
// the config, and Blacksmith sends the request once.
func TestLostConfigWriteAnswerIsOutcomeUnknown(t *testing.T) {
	writes := map[string]struct {
		method string
		call   func(director bosh.Director) error
	}{
		writeCreate: {http.MethodPost, func(director bosh.Director) error {
			return director.EnableResurrection(t.Context(), deploymentName, true)
		}},
		"delete": {http.MethodDelete, func(director bosh.Director) error {
			return director.DeleteResurrectionConfig(t.Context(), deploymentName)
		}},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != configsPath {
					return false
				}

				boshtest.DropAnswer(writer)

				return true
			})

			director := newReadAdapter(t, server, nil)

			err := write.call(director)
			if !errors.Is(err, bosh.ErrOutcomeUnknown) {
				t.Fatalf("expected ErrOutcomeUnknown, got: %v", err)
			}

			var unknown *bosh.OutcomeUnknownError
			if !errors.As(err, &unknown) || unknown.Config != resurrectionConfigRef {
				t.Fatalf("expected an OutcomeUnknownError for resurrection/blacksmith.dep-1, got: %v", err)
			}

			if sent := requestsTo(server, write.method, configsPath); len(sent) != 1 {
				t.Errorf("expected the write to be sent once, got %d", len(sent))
			}

			if reads := requestsTo(server, http.MethodGet, configsPath); len(reads) != 0 {
				t.Errorf("expected no lookup after the lost answer, got %d reads", len(reads))
			}

			if strings.Contains(err.Error(), "Look for a recent director task") {
				t.Errorf("a config write has no task, so the message must not send the operator to one: %v", err)
			}
		})
	}
}

// A config write names the config it was for and leaves the operator no task to look for.
func TestOutcomeUnknownErrorNamesTheConfig(t *testing.T) {
	t.Parallel()

	err := error(&bosh.OutcomeUnknownError{
		Operation: opEnableResurrection,
		Director:  directorAddress,
		Config:    resurrectionConfigRef,
		Err:       errConnectionReset,
	})

	message := err.Error()
	for _, want := range []string{
		"may have accepted the request",
		"did not resend it",
		"creates no director task",
		"GET https://10.0.0.4:25555/configs?type=resurrection&name=blacksmith.dep-1&latest=true",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to contain %q, got: %s", want, message)
		}
	}

	if strings.Contains(message, "/b/tasks") {
		t.Errorf("a config write has no task, so the message must not send the operator to /b/tasks: %s", message)
	}

	var unknown *bosh.OutcomeUnknownError

	if !errors.Is(err, bosh.ErrOutcomeUnknown) || !errors.Is(err, errConnectionReset) || !errors.As(err, &unknown) {
		t.Errorf("expected errors.Is for the sentinel and the cause, and errors.As for the type, got: %v", err)
	}
}

// The message for a config write leaves out the text of its cause, which tells the operator to look for a task, and keeps the cause reachable.
func TestOutcomeUnknownErrorLeavesOutTheCauseOfAConfigWrite(t *testing.T) {
	t.Parallel()

	err := error(&bosh.OutcomeUnknownError{
		Operation: opEnableResurrection,
		Director:  directorAddress,
		Config:    resurrectionConfigRef,
		Err:       errTransportAdvice,
	})

	message := err.Error()
	for _, unwanted := range []string{"Look for a recent director task", "no answer came back", "task for the same deployment"} {
		if strings.Contains(message, unwanted) {
			t.Errorf("expected no %q in a config message, got: %s", unwanted, message)
		}
	}

	if !errors.Is(err, errTransportAdvice) || !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("expected the cause and the sentinel to stay reachable through Unwrap, got: %v", err)
	}
}

// The message for a config write says what a landed create and a landed delete look like.
func TestOutcomeUnknownErrorSaysWhatEachResultMeans(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation: opDeleteResurrection,
		Director:  directorAddress,
		Config:    resurrectionConfigRef,
		Err:       errConnectionReset,
	}).Error()

	for _, want := range []string{
		"For a create, the config is present with the new content",
		"For a delete, the latest list for that type and name is empty",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to contain %q, got: %s", want, message)
		}
	}

	// Each forbidden phrase is built from parts so that a search for bosh
	// commands in the source does not match this guard.
	for _, forbidden := range []string{"bosh" + " cli", "bosh" + " configs"} {
		if strings.Contains(strings.ToLower(message), forbidden) {
			t.Errorf("the message must not send the operator to %q: %s", forbidden, message)
		}
	}
}

// A request with a context ID names it, and sends the operator to its task.
func TestOutcomeUnknownErrorNamesTheContextID(t *testing.T) {
	t.Parallel()

	err := error(&bosh.OutcomeUnknownError{
		Operation:  "CreateDeployment",
		Director:   directorAddress,
		Deployment: deploymentName,
		ContextID:  contextID,
		Err:        errConnectionReset,
	})

	message := err.Error()
	for _, want := range []string{
		"may have accepted the request",
		"did not resend it",
		contextID,
		"found none yet",
		"/b/tasks",
		deploymentName,
	} {
		if !strings.Contains(message, want) {
			t.Errorf("expected the message to contain %q, got: %s", want, message)
		}
	}

	if strings.Contains(message, "/configs?") {
		t.Errorf("a request with no config must not point at /configs: %s", message)
	}

	if !errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("expected errors.Is for the sentinel, got: %v", err)
	}
}

// A config read by ID returns the content exactly as the director stores
// it. Its comments, its key order, and a capitalised key under a job's
// properties, which the job may read by that name, all survive.
func TestGetConfigByIDReturnsTheContentAsStored(t *testing.T) {
	t.Parallel()

	const stored = `# The runtime config the broker's forges add to every deployment.
releases:
- name: os-conf
  version: 22.2.1
addons:
- name: tagger
  jobs:
  - name: tags
    release: os-conf
    properties:
      Tags:
        Owner: blacksmith # who to ask about the VM
      AvailabilityZones: [z1]
`

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs/12" {
			return false
		}

		entry := configEntry("12", "runtime", "default")
		entry["content"] = stored

		boshtest.WriteJSON(writer, http.StatusOK, entry)

		return true
	})

	director := newReadAdapter(t, server, nil)

	detail, err := director.GetConfigByID(t.Context(), "12")
	if err != nil {
		t.Fatalf("GetConfigByID failed: %v", err)
	}

	if detail.Content != stored {
		t.Errorf("expected the content as the director stores it\n%s\ngot\n%s", stored, detail.Content)
	}
}

// A config read by ID goes to GET /configs/<id>, which answers one object, and
// the director's team lands in BoshConfig.Team.
func TestGetConfigByIDReadsTheConfigPathAndDecodesTheTeam(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs/7" {
			return false
		}

		entry := configEntry("7", typeCloud, "default")
		entry["team"] = teamBlacksmith

		boshtest.WriteJSON(writer, http.StatusOK, entry)

		return true
	})

	director := newReadAdapter(t, server, nil)

	detail, err := director.GetConfigByID(t.Context(), "7*")
	if err != nil {
		t.Fatalf("GetConfigByID failed: %v", err)
	}

	if detail.Team != teamBlacksmith {
		t.Errorf("expected the team blacksmith, got %q", detail.Team)
	}

	if detail.Content != "key: 7\n" {
		t.Errorf("expected the config content, got %q", detail.Content)
	}

	if reads := requestsTo(server, http.MethodGet, "/configs/7"); len(reads) != 1 {
		t.Errorf("expected exactly one GET /configs/7, got %d", len(reads))
	}

	if reads := requestsTo(server, http.MethodGet, configsPath); len(reads) != 0 {
		t.Errorf("expected no GET /configs list read, got %d", len(reads))
	}
}

// A 404 on GET /configs/<id> is a missing config for every read by ID,
// including the two sides of a diff.
func TestConfigReadsByIDMapAMissingIDToNotFound(t *testing.T) {
	reads := map[string]func(director bosh.Director) error{
		opGetConfigByID: func(director bosh.Director) error {
			_, err := director.GetConfigByID(t.Context(), "99")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		opGetConfigContent: func(director bosh.Director) error {
			_, err := director.GetConfigContent(t.Context(), "99")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		"ComputeConfigDiff from": func(director bosh.Director) error {
			_, err := director.ComputeConfigDiff(t.Context(), "99", "7")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		"ComputeConfigDiff to": func(director bosh.Director) error {
			_, err := director.ComputeConfigDiff(t.Context(), "7", "99")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				switch request.URL.Path {
				case "/configs/7":
					entry := configEntry("7", typeCloud, "default")
					entry["team"] = teamBlacksmith

					boshtest.WriteJSON(writer, http.StatusOK, entry)
				case "/configs/99":
					boshtest.WriteError(writer, http.StatusNotFound, 440000, "config not found")
				default:
					return false
				}

				return true
			})

			err := read(newReadAdapter(t, server, nil))
			if !errors.Is(err, bosh.ErrConfigNotFound) {
				t.Fatalf("expected ErrConfigNotFound, got: %v", err)
			}
		})
	}
}

// A config write without a deployment names none and sends the operator to no task.
func TestOutcomeUnknownErrorLeavesOutADeployment(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation: opEnableResurrection,
		Director:  directorAddress,
		Config:    resurrectionConfigRef,
		Err:       errConnectionReset,
	}).Error()

	for _, unwanted := range []string{"for deployment", "the deployment", "/b/tasks"} {
		if strings.Contains(message, unwanted) {
			t.Errorf("expected no %q in the message, got: %s", unwanted, message)
		}
	}
}

// A config write with a deployment still has no task to send the operator to.
func TestOutcomeUnknownErrorAConfigWriteWithADeploymentHasNoTask(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation:  opEnableResurrection,
		Director:   directorAddress,
		Deployment: deploymentName,
		Config:     resurrectionConfigRef,
		Err:        errConnectionReset,
	}).Error()

	if strings.Contains(message, "/b/tasks") {
		t.Errorf("a config write has no task, so the message must not mention /b/tasks: %s", message)
	}
}

// A request with only a context ID sends the operator to the task with that context ID.
func TestOutcomeUnknownErrorWithOnlyAContextID(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation: opCleanUp,
		Director:  directorAddress,
		ContextID: contextID,
		Err:       errConnectionReset,
	}).Error()

	for _, unwanted := range []string{"for deployment", "the deployment", "deployment ."} {
		if strings.Contains(message, unwanted) {
			t.Errorf("expected no %q in the message, got: %s", unwanted, message)
		}
	}

	if !strings.Contains(message, "Check /b/tasks for the task with context ID ctx-123") {
		t.Errorf("expected the message to send the operator to /b/tasks for that context ID, got: %s", message)
	}
}

// A request with a context ID says a task with that context ID was sought.
func TestOutcomeUnknownErrorNamesTheContextIDOnce(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation: opCleanUp,
		Director:  directorAddress,
		ContextID: contextID,
		Err:       errConnectionReset,
	}).Error()

	if !strings.Contains(message, "with that context ID and found none yet") {
		t.Errorf("expected the message to say a task with that context ID was sought, got: %s", message)
	}
}

// A request with no config, deployment, or context ID sends the operator to a recent task.
func TestOutcomeUnknownErrorWithNothingToName(t *testing.T) {
	t.Parallel()

	message := (&bosh.OutcomeUnknownError{
		Operation: opCleanUp,
		Director:  directorAddress,
		Err:       errConnectionReset,
	}).Error()

	if strings.Contains(message, "context ID") {
		t.Errorf("a request with no context ID must not mention one, got: %s", message)
	}

	if !strings.HasSuffix(message, "did not resend it. Check /b/tasks for a recent task") {
		t.Errorf("expected the message to end by sending the operator to /b/tasks for a recent task, got: %s", message)
	}
}

// The message of a request that made no lookup ends with a full stop,
// whether or not it names a deployment.
func TestNoLookupMessageEndsWithAPeriod(t *testing.T) {
	t.Parallel()

	for _, deployment := range []string{deploymentName, ""} {
		message := (&bosh.OutcomeUnknownError{
			Operation:  opCleanUp,
			Director:   directorAddress,
			Deployment: deployment,
			ContextID:  contextID,
			NoLookup:   true,
			Err:        errConnectionReset,
		}).Error()

		if !strings.HasSuffix(message, ".") || strings.HasSuffix(message, "..") {
			t.Errorf("expected the message for deployment %q to end with one full stop, got: %s", deployment, message)
		}
	}
}

// A request that made no lookup never claims that Blacksmith looked for its
// task, and never sends the operator to search by the context ID, which
// the director may not store. It still names the ID for matching logs.
func TestOutcomeUnknownErrorWithNoLookupLeavesOutTheLookupClaim(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		deployment string
		suffix     string
	}{
		"with a deployment":    {deploymentName, "Check /b/tasks for a task on the deployment dep-1."},
		"without a deployment": {"", "Check /b/tasks for a recent CleanUp task."},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			message := (&bosh.OutcomeUnknownError{
				Operation:  opCleanUp,
				Director:   directorAddress,
				Deployment: tc.deployment,
				ContextID:  contextID,
				NoLookup:   true,
				Err: &boshapi.TransportError{
					Method: "POST", Path: "/cleanup", ContextID: contextID, Sent: true, Mutation: true, Attempts: 1, Err: errConnectionReset,
				},
			}).Error()

			if !strings.HasSuffix(message, tc.suffix) {
				t.Errorf("expected the message to end with %q, got: %s", tc.suffix, message)
			}

			for _, unwanted := range []string{"looked for a task", "found none yet", "the task with context ID", "Look for a director task with context ID"} {
				if strings.Contains(message, unwanted) {
					t.Errorf("expected no %q in the message, got: %s", unwanted, message)
				}
			}

			if strings.Count(message, contextID) != 1 {
				t.Errorf("expected the message to name the context ID once, got: %s", message)
			}

			if !strings.Contains(message, "did not resend it") {
				t.Errorf("expected the message to say the request was not resent, got: %s", message)
			}
		})
	}
}

// A wrapped config error names the failure once, so the log line and the
// UI do not repeat "failed to get config" or "failed to update".
func TestConfigErrorsNameTheFailureOnce(t *testing.T) {
	calls := map[string]struct {
		call     func(director bosh.Director) error
		repeated string
	}{
		"GetConfig": {func(director bosh.Director) error {
			_, err := director.GetConfig(t.Context(), typeRuntime, "x")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		}, "failed to get config"},
		opEnableResurrection: {func(director bosh.Director) error {
			return director.EnableResurrection(t.Context(), deploymentName, true)
		}, "failed to update"},
		opDeleteResurrection: {func(director bosh.Director) error {
			return director.DeleteResurrectionConfig(t.Context(), deploymentName)
		}, "failed to delete"},
	}

	for name, entry := range calls {
		t.Run(name, func(t *testing.T) {
			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if !strings.HasPrefix(request.URL.Path, configsPath) {
					return false
				}

				boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the director broke")

				return true
			})

			err := entry.call(newReadAdapter(t, server, nil))
			if err == nil {
				t.Fatal("expected an error")
			}

			if count := strings.Count(err.Error(), entry.repeated); count != 1 {
				t.Errorf("expected %q once, found it %d times in: %v", entry.repeated, count, err)
			}
		})
	}
}

// A missing ID keeps the director's own 404 text in the chain, so the log
// line shows which route and ID the director refused.
func TestMissingConfigIDKeepsTheDirectorsAnswer(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs/99" {
			return false
		}

		boshtest.WriteError(writer, http.StatusNotFound, 440000, "config not found")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfigByID(t.Context(), "99")
	if !errors.Is(err, bosh.ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound, got: %v", err)
	}

	var missing *boshapi.ConfigNotFoundError
	if !errors.As(err, &missing) || missing.ID != "99" {
		t.Errorf("expected the director's ConfigNotFoundError for 99 in the chain, got: %v", err)
	}

	if !strings.Contains(err.Error(), "GET /configs/<id>") {
		t.Errorf("expected the director's 404 detail in the message, got: %v", err)
	}
}

// configWrite is one of the two config writes, with the error that stands
// for a director that refused it.
type configWrite struct {
	method   string
	call     func(ctx context.Context, director bosh.Director) error
	rejected error
}

func configWrites() map[string]configWrite {
	return map[string]configWrite{
		opEnableResurrection: {
			method: http.MethodPost,
			call: func(ctx context.Context, director bosh.Director) error {
				return director.EnableResurrection(ctx, deploymentName, true)
			},
			rejected: bosh.ErrResurrectionConfigUpdateFailed,
		},
		opDeleteResurrection: {
			method: http.MethodDelete,
			call: func(ctx context.Context, director bosh.Director) error {
				return director.DeleteResurrectionConfig(ctx, deploymentName)
			},
			rejected: bosh.ErrResurrectionConfigDeleteFailed,
		},
	}
}

// Only a lost answer is an unknown outcome. A director that answered with a
// rejection told Blacksmith what happened, except for a 502 or 504 from a
// proxy in front of it, which says nothing about the director's own result.
func TestConfigWriteRejectionIsNotAnUnknownOutcome(t *testing.T) {
	statuses := []struct {
		status  int
		unknown bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusForbidden, false},
		{http.StatusInternalServerError, false},
		{http.StatusBadGateway, true},
		{http.StatusGatewayTimeout, true},
	}

	for writeName, write := range configWrites() {
		for _, answer := range statuses {
			t.Run(writeName+"/"+strconv.Itoa(answer.status), func(t *testing.T) {
				server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
					if request.URL.Path != configsPath {
						return false
					}

					boshtest.WriteError(writer, answer.status, 100, "the director said no")

					return true
				})

				director := newReadAdapter(t, server, nil)

				err := write.call(t.Context(), director)
				if err == nil {
					t.Fatal("expected an error")
				}

				if got := errors.Is(err, bosh.ErrOutcomeUnknown); got != answer.unknown {
					t.Errorf("status %d: ErrOutcomeUnknown is %v, want %v: %v", answer.status, got, answer.unknown, err)
				}

				if !answer.unknown && !errors.Is(err, write.rejected) {
					t.Errorf("status %d: expected %v in the chain, got: %v", answer.status, write.rejected, err)
				}

				if errors.Is(err, bosh.ErrResurrectionConfigNotFound) {
					t.Errorf("status %d is not a missing config, got: %v", answer.status, err)
				}

				if sent := requestsTo(server, write.method, configsPath); len(sent) != 1 {
					t.Errorf("expected the write to be sent once, got %d", len(sent))
				}
			})
		}
	}
}

// Only a 404 says the resurrection config was not there.
func TestDeleteResurrectionConfigMapsOnlyA404ToNotFound(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteError(writer, http.StatusNotFound, 440000, "config not found")

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.DeleteResurrectionConfig(t.Context(), deploymentName)
	if !errors.Is(err, bosh.ErrResurrectionConfigNotFound) || errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("expected ErrResurrectionConfigNotFound and no unknown outcome for a 404, got: %v", err)
	}
}

// A caller whose context ended during the write gets the call error that
// says so, and not an unknown outcome the caller did not cause.
func TestConfigWriteWithAnEndedContextIsNotAnUnknownOutcome(t *testing.T) {
	for writeName, write := range configWrites() {
		t.Run(writeName, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != configsPath {
					return false
				}

				cancel()
				boshtest.DropAnswer(writer)

				return true
			})

			director := newReadAdapter(t, server, nil)

			err := write.call(ctx, director)

			var callErr *bosh.DirectorCallError
			if !errors.As(err, &callErr) {
				t.Fatalf("expected a *bosh.DirectorCallError, got %T: %v", err, err)
			}

			if errors.Is(err, bosh.ErrOutcomeUnknown) {
				t.Errorf("an ended caller context must not read as an unknown outcome: %v", err)
			}
		})
	}
}

// A director failure on a read by ID is not a missing config, whatever the
// route, so the UI does not answer "not found" for an outage or a refusal.
func TestConfigReadsByIDKeepAFailureThatIsNotA404(t *testing.T) {
	reads := map[string]func(director bosh.Director) error{
		opGetConfigByID: func(director bosh.Director) error {
			_, err := director.GetConfigByID(t.Context(), "7")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		opGetConfigContent: func(director bosh.Director) error {
			_, err := director.GetConfigContent(t.Context(), "7")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		"ComputeConfigDiff": func(director bosh.Director) error {
			_, err := director.ComputeConfigDiff(t.Context(), "7", "8")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
	}

	for readName, read := range reads {
		for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden} {
			t.Run(readName+"/"+strconv.Itoa(status), func(t *testing.T) {
				server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
					if !strings.HasPrefix(request.URL.Path, "/configs/") {
						return false
					}

					boshtest.WriteError(writer, status, 100, "the director said no")

					return true
				})

				err := read(newReadAdapter(t, server, nil))
				if err == nil || errors.Is(err, bosh.ErrConfigNotFound) {
					t.Errorf("status %d: expected a failure that is not ErrConfigNotFound, got: %v", status, err)
				}
			})
		}
	}
}

// The cloud config is the latest version of the one config of type cloud, so
// the director is asked for exactly that, and the content comes back as sent.
func TestGetCloudConfigAsksForTheLatestVersionOnly(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{configEntry("4", typeCloud, "default")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	content, err := director.GetCloudConfig(t.Context())
	if err != nil {
		t.Fatalf("GetCloudConfig failed: %v", err)
	}

	if content != "key: 4\n" {
		t.Errorf("expected the config content, got %q", content)
	}

	reads := requestsTo(server, http.MethodGet, configsPath)
	if len(reads) != 1 {
		t.Fatalf("expected exactly one GET /configs, got %d", len(reads))
	}

	query, err := url.ParseQuery(reads[0].RawQuery)
	if err != nil {
		t.Fatalf("failed to parse the query %q: %v", reads[0].RawQuery, err)
	}

	want := url.Values{queryType: {typeCloud}, queryLatest: {"true"}, "limit": {"1"}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("expected exactly type=cloud&latest=true&limit=1, got %q", reads[0].RawQuery)
	}
}

// The boot cloud config check needs every cloud config the director holds, so
// the read asks for the latest version of type cloud with no limit, and keeps
// the default config, which the director names default.
func TestListCloudConfigsSendsTypeCloudAndLatest(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		first := configEntry("4", typeCloud, "default")

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{first, configEntry("7", typeCloud, "extra")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	entries, err := director.ListCloudConfigs(t.Context())
	if err != nil {
		t.Fatalf("ListCloudConfigs failed: %v", err)
	}

	want := []bosh.CloudConfigEntry{{Name: "default", Content: "key: 4\n"}, {Name: "extra", Content: "key: 7\n"}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("expected %v, got %v", want, entries)
	}

	reads := requestsTo(server, http.MethodGet, configsPath)
	if len(reads) != 1 {
		t.Fatalf("expected exactly one GET /configs, got %d", len(reads))
	}

	if reads[0].RawQuery != "latest=true&type=cloud" && reads[0].RawQuery != "type=cloud&latest=true" {
		t.Errorf("expected exactly type=cloud&latest=true, got %q", reads[0].RawQuery)
	}

	query, err := url.ParseQuery(reads[0].RawQuery)
	if err != nil || len(query) != 2 || query.Get("limit") != "" {
		t.Errorf("expected only type and latest in the query, got %q", reads[0].RawQuery)
	}
}

// A failed read keeps the director's address, status, and description.
func TestListCloudConfigsKeepsTheDirectorFailure(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the database is down")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.ListCloudConfigs(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{server.URL, "500", "the database is down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to hold %q, got: %v", want, err)
		}
	}
}

// The main list reads every config in one call, so it sends no limit that
// would cut the list short before the type filter runs.
func TestGetConfigsSendsNoLimit(t *testing.T) {
	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != configsPath {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{configEntry("4", typeCloud, "default")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfigs(t.Context(), 0, nil)
	if err != nil {
		t.Fatalf("GetConfigs failed: %v", err)
	}

	if query := configsQuery(t, server); query.Has("limit") {
		t.Errorf("expected no limit in the query, got %v", query)
	}
}

// An ID that is not a decimal integer can never name a config, so it reads
// as not found and no request is sent for it.
func TestNonNumericConfigIDIsNotFound(t *testing.T) {
	var requests int

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path == "/configs/7" {
			boshtest.WriteJSON(writer, http.StatusOK, configEntry("7", typeCloud, "default"))

			return true
		}

		if strings.HasPrefix(request.URL.Path, "/configs") {
			requests++
		}

		return false
	})

	director := newReadAdapter(t, server, nil)

	reads := map[string]func() error{
		"ComputeConfigDiff from": func() error {
			_, err := director.ComputeConfigDiff(t.Context(), "abc", "7")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
		"ComputeConfigDiff to": func() error {
			_, err := director.ComputeConfigDiff(t.Context(), "7", "abc")

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
	}

	for name, read := range reads {
		err := read()
		if !errors.Is(err, bosh.ErrConfigNotFound) {
			t.Errorf("%s: expected ErrConfigNotFound, got: %v", name, err)
		} else if !strings.Contains(err.Error(), "abc") {
			t.Errorf("%s: expected the ID in the message, got: %v", name, err)
		}
	}

	if requests != 0 {
		t.Errorf("expected no config request to reach the server, got %d", requests)
	}
}
