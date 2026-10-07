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

var errConnectionReset = errors.New("connection reset")

// errTransportAdvice has the words boshapi's transport error ends with, which
// send an operator to a director task a config write never creates.
var errTransportAdvice = errors.New("no answer came back. Look for a recent director task for the same deployment before you send it again.")

// configEntry is one entry of the director's GET /configs answer.
func configEntry(id, configType, name string) map[string]interface{} {
	return map[string]interface{}{
		"id":         id,
		"type":       configType,
		"name":       name,
		"content":    "key: " + id + "\n",
		"created_at": "2026-10-01 12:00:00 UTC",
	}
}

// configsQuery returns the query of the only GET /configs the director saw.
func configsQuery(t *testing.T, server *boshtest.Director) url.Values {
	t.Helper()

	reads := requestsTo(server, http.MethodGet, "/configs")
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
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfig(t.Context(), "runtime", "absent")
	if !errors.Is(err, bosh.ErrConfigNotFound) {
		t.Fatalf("expected ErrConfigNotFound for an empty list, got: %v", err)
	}

	query := configsQuery(t, server)
	if query.Get("type") != "runtime" || query.Get("name") != "absent" || query.Get("latest") != "true" {
		t.Errorf("expected type=runtime, name=absent, and latest=true, got %v", query)
	}
}

// A failure that is not an empty list keeps the failure, and never reads as a
// missing config, whatever words the error holds.
func TestGetConfigKeepsADirectorFailureAsAnError(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the config was not found in the database layer")

		return true
	})

	director := newReadAdapter(t, server, nil)

	_, err := director.GetConfig(t.Context(), "cloud", "x")
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
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{
			configEntry("9", "runtime", "dns"),
			configEntry("4", "cloud", "default"),
			configEntry("7", "cpi", "aws"),
		})

		return true
	})

	director := newReadAdapter(t, server, nil)

	configs, err := director.GetConfigs(t.Context(), 0, []string{"cloud", "runtime"})
	if err != nil {
		t.Fatalf("GetConfigs failed: %v", err)
	}

	if len(configs) != 2 {
		t.Fatalf("expected the cpi config to be filtered out, got %d configs: %+v", len(configs), configs)
	}

	if configs[0].Type != "cloud" || configs[0].ID != "4*" || !configs[0].IsActive {
		t.Errorf("expected the cloud config first, as 4* and active, got %+v", configs[0])
	}

	if configs[1].Type != "runtime" || configs[1].ID != "9*" || !configs[1].IsActive {
		t.Errorf("expected the runtime config second, as 9* and active, got %+v", configs[1])
	}

	if configs[0].CreatedAt.IsZero() {
		t.Errorf("expected created_at to parse, got a zero time")
	}

	if query := configsQuery(t, server); query.Get("latest") != "true" {
		t.Errorf("expected latest=true, got %v", query)
	}
}

// The version history asks for every version, not only the latest, and caps
// them at the limit.
func TestGetConfigVersionsSendsLatestFalseAndLimit(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs" {
			return false
		}

		current := configEntry("12", "cloud", "default")
		current["current"] = true

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{current, configEntry("11", "cloud", "default")})

		return true
	})

	director := newReadAdapter(t, server, nil)

	versions, err := director.GetConfigVersions(t.Context(), "cloud", "default", 5)
	if err != nil {
		t.Fatalf("GetConfigVersions failed: %v", err)
	}

	query := configsQuery(t, server)
	if query.Get("type") != "cloud" || query.Get("name") != "default" || query.Get("latest") != "false" || query.Get("limit") != "5" {
		t.Errorf("expected type=cloud, name=default, latest=false, and limit=5, got %v", query)
	}

	if len(versions) != 2 || !versions[0].IsActive || versions[1].IsActive {
		t.Errorf("expected only the current version to be active, got %+v", versions)
	}
}

// Resurrection is a config of type resurrection, one per deployment, and the
// director gets it as a POST /configs.
func TestEnableResurrectionCreatesAResurrectionConfig(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	var body map[string]string

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodPost || request.URL.Path != "/configs" {
			return false
		}

		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)

		boshtest.WriteJSON(writer, http.StatusCreated, configEntry("21", "resurrection", "blacksmith.dep-1"))

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.EnableResurrection(t.Context(), "dep-1", false)
	if err != nil {
		t.Fatalf("EnableResurrection failed: %v", err)
	}

	if body["type"] != "resurrection" || body["name"] != "blacksmith.dep-1" {
		t.Errorf("expected type resurrection and name blacksmith.dep-1, got %v", body)
	}

	if !strings.Contains(body["content"], "enabled: false") || !strings.Contains(body["content"], "- dep-1") {
		t.Errorf("expected the content to disable resurrection for dep-1, got %q", body["content"])
	}

	if posts := requestsTo(server, http.MethodPost, "/configs"); len(posts) != 1 {
		t.Errorf("expected one POST /configs, got %d", len(posts))
	}
}

// Deleting a resurrection config sends the type and name, and a config that
// was not there reads as ErrResurrectionConfigNotFound.
func TestDeleteResurrectionConfigSendsTypeAndName(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	for name, status := range map[string]int{"deleted": http.StatusNoContent, "absent": http.StatusNotFound} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.Method != http.MethodDelete || request.URL.Path != "/configs" {
					return false
				}

				writer.WriteHeader(status)

				return true
			})

			director := newReadAdapter(t, server, nil)

			err := director.DeleteResurrectionConfig(t.Context(), "dep-1")

			if status == http.StatusNotFound {
				if !errors.Is(err, bosh.ErrResurrectionConfigNotFound) {
					t.Errorf("expected ErrResurrectionConfigNotFound, got: %v", err)
				}
			} else if err != nil {
				t.Errorf("expected the delete to succeed, got: %v", err)
			}

			deletes := requestsTo(server, http.MethodDelete, "/configs")
			if len(deletes) != 1 {
				t.Fatalf("expected one DELETE /configs, got %d", len(deletes))
			}

			query, _ := url.ParseQuery(deletes[0].RawQuery)
			if query.Get("type") != "resurrection" || query.Get("name") != "blacksmith.dep-1" {
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
		"create": {http.MethodPost, func(director bosh.Director) error {
			return director.EnableResurrection(t.Context(), "dep-1", true)
		}},
		"delete": {http.MethodDelete, func(director bosh.Director) error {
			return director.DeleteResurrectionConfig(t.Context(), "dep-1")
		}},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != "/configs" {
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
			if !errors.As(err, &unknown) || unknown.Config != "resurrection/blacksmith.dep-1" {
				t.Fatalf("expected an OutcomeUnknownError for resurrection/blacksmith.dep-1, got: %v", err)
			}

			if sent := requestsTo(server, write.method, "/configs"); len(sent) != 1 {
				t.Errorf("expected the write to be sent once, got %d", len(sent))
			}

			if reads := requestsTo(server, http.MethodGet, "/configs"); len(reads) != 0 {
				t.Errorf("expected no lookup after the lost answer, got %d reads", len(reads))
			}

			if strings.Contains(err.Error(), "Look for a recent director task") {
				t.Errorf("a config write has no task, so the message must not send the operator to one: %v", err)
			}
		})
	}
}

func TestOutcomeUnknownErrorNamesTheConfig(t *testing.T) {
	t.Parallel()

	base := errConnectionReset

	t.Run("a config write", func(t *testing.T) {
		t.Parallel()

		err := error(&bosh.OutcomeUnknownError{
			Operation: "EnableResurrection",
			Director:  "https://10.0.0.4:25555",
			Config:    "resurrection/blacksmith.dep-1",
			Err:       base,
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

		if !errors.Is(err, bosh.ErrOutcomeUnknown) || !errors.Is(err, base) || !errors.As(err, &unknown) {
			t.Errorf("expected errors.Is for the sentinel and the cause, and errors.As for the type, got: %v", err)
		}
	})

	t.Run("a config write leaves out the text of its cause", func(t *testing.T) {
		t.Parallel()

		err := error(&bosh.OutcomeUnknownError{
			Operation: "EnableResurrection",
			Director:  "https://10.0.0.4:25555",
			Config:    "resurrection/blacksmith.dep-1",
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
	})

	t.Run("a config write says what each result means", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation: "DeleteResurrectionConfig",
			Director:  "https://10.0.0.4:25555",
			Config:    "resurrection/blacksmith.dep-1",
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

		if strings.Contains(strings.ToLower(message), "bosh cli") || strings.Contains(message, "bosh configs") {
			t.Errorf("the message must not send the operator to the bosh CLI: %s", message)
		}
	})

	t.Run("a request with a context ID", func(t *testing.T) {
		t.Parallel()

		err := error(&bosh.OutcomeUnknownError{
			Operation:  "CreateDeployment",
			Director:   "https://10.0.0.4:25555",
			Deployment: "dep-1",
			ContextID:  "ctx-123",
			Err:        base,
		})

		message := err.Error()
		for _, want := range []string{
			"may have accepted the request",
			"did not resend it",
			"ctx-123",
			"found none yet",
			"/b/tasks",
			"dep-1",
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
	})
}

// A config read by ID goes to GET /configs/<id>, which answers one object, and
// the director's team lands in BoshConfig.Team.
func TestGetConfigByIDReadsTheConfigPathAndDecodesTheTeam(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs/7" {
			return false
		}

		entry := configEntry("7", "cloud", "default")
		entry["team"] = "blacksmith"

		boshtest.WriteJSON(writer, http.StatusOK, entry)

		return true
	})

	director := newReadAdapter(t, server, nil)

	detail, err := director.GetConfigByID(t.Context(), "7*")
	if err != nil {
		t.Fatalf("GetConfigByID failed: %v", err)
	}

	if detail.Team != "blacksmith" {
		t.Errorf("expected the team blacksmith, got %q", detail.Team)
	}

	if detail.Content != "key: 7\n" {
		t.Errorf("expected the config content, got %q", detail.Content)
	}

	if reads := requestsTo(server, http.MethodGet, "/configs/7"); len(reads) != 1 {
		t.Errorf("expected exactly one GET /configs/7, got %d", len(reads))
	}

	if reads := requestsTo(server, http.MethodGet, "/configs"); len(reads) != 0 {
		t.Errorf("expected no GET /configs list read, got %d", len(reads))
	}
}

// A 404 on GET /configs/<id> is a missing config for every read by ID,
// including the two sides of a diff.
func TestConfigReadsByIDMapAMissingIDToNotFound(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch request.URL.Path {
		case "/configs/7":
			entry := configEntry("7", "cloud", "default")
			entry["team"] = "blacksmith"

			boshtest.WriteJSON(writer, http.StatusOK, entry)
		case "/configs/99":
			boshtest.WriteError(writer, http.StatusNotFound, 440000, "config not found")
		default:
			return false
		}

		return true
	})

	director := newReadAdapter(t, server, nil)

	reads := map[string]func() error{
		"GetConfigByID": func() error {
			_, err := director.GetConfigByID(t.Context(), "99")

			return err
		},
		"GetConfigContent": func() error {
			_, err := director.GetConfigContent(t.Context(), "99")

			return err
		},
		"ComputeConfigDiff from": func() error {
			_, err := director.ComputeConfigDiff(t.Context(), "99", "7")

			return err
		},
		"ComputeConfigDiff to": func() error {
			_, err := director.ComputeConfigDiff(t.Context(), "7", "99")

			return err
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			if err := read(); !errors.Is(err, bosh.ErrConfigNotFound) {
				t.Fatalf("expected ErrConfigNotFound, got: %v", err)
			}
		})
	}
}

// A message names a deployment only when the request had one, and sends the
// operator to /b/tasks only when the request could have made a task.
func TestOutcomeUnknownErrorLeavesOutWhatIsEmpty(t *testing.T) {
	t.Parallel()

	t.Run("a config write without a deployment", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation: "EnableResurrection",
			Director:  "https://10.0.0.4:25555",
			Config:    "resurrection/blacksmith.dep-1",
			Err:       errConnectionReset,
		}).Error()

		for _, unwanted := range []string{"for deployment", "the deployment", "/b/tasks"} {
			if strings.Contains(message, unwanted) {
				t.Errorf("expected no %q in the message, got: %s", unwanted, message)
			}
		}
	})

	t.Run("a config write with a deployment", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation:  "EnableResurrection",
			Director:   "https://10.0.0.4:25555",
			Deployment: "dep-1",
			Config:     "resurrection/blacksmith.dep-1",
			Err:        errConnectionReset,
		}).Error()

		if strings.Contains(message, "/b/tasks") {
			t.Errorf("a config write has no task, so the message must not mention /b/tasks: %s", message)
		}
	})

	t.Run("neither a config nor a deployment", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation: "CleanUp",
			Director:  "https://10.0.0.4:25555",
			ContextID: "ctx-123",
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
	})
	t.Run("a request with a context ID names it once", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation: "CleanUp",
			Director:  "https://10.0.0.4:25555",
			ContextID: "ctx-123",
			Err:       errConnectionReset,
		}).Error()

		if !strings.Contains(message, "with that context ID and found none yet") {
			t.Errorf("expected the message to say a task with that context ID was sought, got: %s", message)
		}
	})

	t.Run("neither a config, a deployment, nor a context ID", func(t *testing.T) {
		t.Parallel()

		message := (&bosh.OutcomeUnknownError{
			Operation: "CleanUp",
			Director:  "https://10.0.0.4:25555",
			Err:       errConnectionReset,
		}).Error()

		if strings.Contains(message, "context ID") {
			t.Errorf("a request with no context ID must not mention one, got: %s", message)
		}

		if !strings.HasSuffix(message, "did not resend it. Check /b/tasks for a recent task") {
			t.Errorf("expected the message to end by sending the operator to /b/tasks for a recent task, got: %s", message)
		}
	})
}

// A wrapped config error names the failure once, so the log line and the
// UI do not repeat "failed to get config" or "failed to update".
func TestConfigErrorsNameTheFailureOnce(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if !strings.HasPrefix(request.URL.Path, "/configs") {
			return false
		}

		boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the director broke")

		return true
	})

	director := newReadAdapter(t, server, nil)

	calls := map[string]struct {
		call     func() error
		repeated string
	}{
		"GetConfig": {func() error {
			_, err := director.GetConfig(t.Context(), "runtime", "x")

			return err
		}, "failed to get config"},
		"EnableResurrection": {func() error {
			return director.EnableResurrection(t.Context(), "dep-1", true)
		}, "failed to update"},
		"DeleteResurrectionConfig": {func() error {
			return director.DeleteResurrectionConfig(t.Context(), "dep-1")
		}, "failed to delete"},
	}

	for name, entry := range calls {
		t.Run(name, func(t *testing.T) {
			err := entry.call()
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
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

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
		"EnableResurrection": {
			method: http.MethodPost,
			call: func(ctx context.Context, director bosh.Director) error {
				return director.EnableResurrection(ctx, "dep-1", true)
			},
			rejected: bosh.ErrResurrectionConfigUpdateFailed,
		},
		"DeleteResurrectionConfig": {
			method: http.MethodDelete,
			call: func(ctx context.Context, director bosh.Director) error {
				return director.DeleteResurrectionConfig(ctx, "dep-1")
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
				t.Setenv("BLACKSMITH_TEST_MODE", "true")

				server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
					if request.URL.Path != "/configs" {
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

				if sent := requestsTo(server, write.method, "/configs"); len(sent) != 1 {
					t.Errorf("expected the write to be sent once, got %d", len(sent))
				}
			})
		}
	}
}

// Only a 404 says the resurrection config was not there.
func TestDeleteResurrectionConfigMapsOnlyA404ToNotFound(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteError(writer, http.StatusNotFound, 440000, "config not found")

		return true
	})

	director := newReadAdapter(t, server, nil)

	err := director.DeleteResurrectionConfig(t.Context(), "dep-1")
	if !errors.Is(err, bosh.ErrResurrectionConfigNotFound) || errors.Is(err, bosh.ErrOutcomeUnknown) {
		t.Errorf("expected ErrResurrectionConfigNotFound and no unknown outcome for a 404, got: %v", err)
	}
}

// A caller whose context ended during the write gets the call error that
// says so, and not an unknown outcome the caller did not cause.
func TestConfigWriteWithAnEndedContextIsNotAnUnknownOutcome(t *testing.T) {
	for writeName, write := range configWrites() {
		t.Run(writeName, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != "/configs" {
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
		"GetConfigByID": func(director bosh.Director) error {
			_, err := director.GetConfigByID(t.Context(), "7")

			return err
		},
		"GetConfigContent": func(director bosh.Director) error {
			_, err := director.GetConfigContent(t.Context(), "7")

			return err
		},
		"ComputeConfigDiff": func(director bosh.Director) error {
			_, err := director.ComputeConfigDiff(t.Context(), "7", "8")

			return err
		},
	}

	for readName, read := range reads {
		for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden} {
			t.Run(readName+"/"+strconv.Itoa(status), func(t *testing.T) {
				t.Setenv("BLACKSMITH_TEST_MODE", "true")

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
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{configEntry("4", "cloud", "default")})

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

	reads := requestsTo(server, http.MethodGet, "/configs")
	if len(reads) != 1 {
		t.Fatalf("expected exactly one GET /configs, got %d", len(reads))
	}

	query, err := url.ParseQuery(reads[0].RawQuery)
	if err != nil {
		t.Fatalf("failed to parse the query %q: %v", reads[0].RawQuery, err)
	}

	want := url.Values{"type": {"cloud"}, "latest": {"true"}, "limit": {"1"}}
	if !reflect.DeepEqual(query, want) {
		t.Errorf("expected exactly type=cloud&latest=true&limit=1, got %q", reads[0].RawQuery)
	}
}

// The main list reads every config in one call, so it sends no limit that
// would cut the list short before the type filter runs.
func TestGetConfigsSendsNoLimit(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/configs" {
			return false
		}

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{configEntry("4", "cloud", "default")})

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
