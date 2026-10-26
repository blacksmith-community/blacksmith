package upgrade_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

const (
	cmdText      = "echo top-secret-marker"
	cmdKey       = "operator-key"
	cmdRequestID = "req-17"
	cmdStdout    = "hello from the node\n"
)

// vmsDirector answers the VMs of a deployment and nothing else.
type vmsDirector struct {
	bosh.Director

	vms map[string][]bosh.VM
}

func (d *vmsDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	vms, ok := d.vms[deployment]
	if !ok {
		return nil, bosh.ErrDeploymentNotFound
	}

	return slices.Clone(vms), nil
}

// cmdEnv is a loaded handler over a fake director and vault. The commands
// configuration can change while the handler runs, as it can in production.
type cmdEnv struct {
	handler *upgradehandler.Handler
	vault   *memoryVault
	cfg     atomic.Pointer[config.CommandsConfig]
}

func (e *cmdEnv) set(cfg config.CommandsConfig) { e.cfg.Store(&cfg) }

// commandHandler starts a loaded handler with the commands configuration the
// test gives.
func commandHandler(t *testing.T, cfg config.CommandsConfig) *cmdEnv {
	t.Helper()

	director := &vmsDirector{vms: map[string][]bosh.VM{
		"redis-i1": {
			{ID: "id-0", CID: "cid-0", Job: "redis", Index: 0, AZ: "z1", IPs: []string{"10.0.0.4"}, State: "running", JobState: "running"},
		},
	}}

	env := &cmdEnv{vault: newMemoryVault()}
	env.set(cfg)
	env.vault.store(t, "i1", map[string]interface{}{"deployment_name": "redis-i1", "instance_name": "one", "service_id": "redis", "plan_id": "small"})

	env.handler = upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: director,
		Vault:    env.vault,
		Commands: func() config.CommandsConfig { return *env.cfg.Load() },
	})
	t.Cleanup(env.handler.Stop)

	select {
	case <-env.handler.GetManager().Loaded():
	case <-time.After(loadTimeout):
		t.Fatal("the manager never finished loading the stored tasks")
	}

	return env
}

func commandsOn(key string, root bool) config.CommandsConfig {
	return config.CommandsConfig{Root: &root, Credential: config.CommandCredential{Key: key}}
}

func commandsOff() config.CommandsConfig {
	off := false

	return config.CommandsConfig{Operation: config.CommandSwitch{On: &off}}
}

// send sends a request with the key header when key is not empty.
func send(t *testing.T, handler http.Handler, method, target, body, key string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if key != "" {
		request.Header.Set(upgrade.CommandKeyHeader, key)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	return recorder
}

// expectCode fails the test unless the answer has the status and error code.
func expectCode(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	var answer struct {
		Code string `json:"code"`
	}

	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)

	if recorder.Code != status || answer.Code != code {
		t.Fatalf("expected status %d with code %q, got %d with code %q", status, code, recorder.Code, answer.Code)
	}
}

func expectNoCommandText(t *testing.T, where string, recorder *httptest.ResponseRecorder) {
	t.Helper()

	if strings.Contains(recorder.Body.String(), cmdText) {
		t.Fatalf("%s carried the command text", where)
	}
}

func previewBody(runAs, extra string) string {
	return `{"instance_ids":["i1"],"command":"` + cmdText + `","run_as":"` + runAs + `"` + extra + `}`
}

func createBody(previewID, runAs string) string {
	return `{"kind":"command","preview_id":"` + previewID + `","client_request_id":"` + cmdRequestID +
		`","command":"` + cmdText + `","run_as":"` + runAs + `","timeout_seconds":600,"name":"flush","reason":"CHG-1"}`
}

func previewID(t *testing.T, handler http.Handler, runAs, key string) string {
	t.Helper()

	recorder := send(t, handler, http.MethodPost, "/b/upgrade/commands/preview", previewBody(runAs, `,"timeout_seconds":600`), key)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview answered %d", recorder.Code)
	}

	var preview struct {
		ID string `json:"preview_id"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decoding the preview: %v", err)
	}

	return preview.ID
}

// ranCommandTask creates a command task as the user and saves the output its
// one node printed, as a finished run leaves it. It returns the task ID.
func ranCommandTask(t *testing.T, env *cmdEnv, runAs, key string) string {
	t.Helper()

	id := previewID(t, env.handler, runAs, key)

	created := send(t, env.handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, runAs), key)
	if created.Code != http.StatusCreated {
		t.Fatalf("create answered %d", created.Code)
	}

	var task struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil {
		t.Fatalf("decoding the task: %v", err)
	}

	env.vault.store(t, "command-output/"+task.ID+"/i1/redis-0", upgrade.CommandOutput{
		Stdout: []byte(cmdStdout), Stderr: []byte{}, StdoutKept: int64(len(cmdStdout)), StdoutReceived: int64(len(cmdStdout)),
	})

	return task.ID
}

func outputURL(taskID string) string { return "/b/upgrade/tasks/" + taskID + "/output?vm=redis-0" }

// A finished root run stays readable while root runs are off, because the
// read authorizes with the feature switch and the key and never the user the
// run used.
func TestOutputReadOfRootRunWithRootOff(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", true))
	taskID := ranCommandTask(t, env, "root", "")

	env.set(commandsOn("", false))

	recorder := send(t, env.handler, http.MethodGet, outputURL(taskID), "", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Stdout         string `json:"stdout"`
		StdoutKept     int    `json:"stdout_kept"`
		StdoutReceived int    `json:"stdout_received"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decoding the answer: %v", err)
	}

	if answer.Stdout != cmdStdout || answer.StdoutKept != len(cmdStdout) || answer.StdoutReceived != len(cmdStdout) {
		t.Fatalf("unexpected output answer: %s", recorder.Body.String())
	}

	env.set(commandsOn(cmdKey, false))

	expectCode(t, send(t, env.handler, http.MethodGet, outputURL(taskID), "", ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)

	if got := send(t, env.handler, http.MethodGet, outputURL(taskID), "", cmdKey); got.Code != http.StatusOK {
		t.Fatalf("expected 200 with the key, got %d", got.Code)
	}
}

func TestOutputReadRefusals(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", false))
	taskID := ranCommandTask(t, env, "vcap", "")
	base := "/b/upgrade/tasks/" + taskID + "/output"

	expectCode(t, send(t, env.handler, http.MethodGet, base+"?vm=redis-7", "", ""), http.StatusNotFound, apierr.CodeNotFound)
	expectCode(t, send(t, env.handler, http.MethodGet, base+"?vm=redis", "", ""), http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectCode(t, send(t, env.handler, http.MethodGet, base+"?vm=redis-x", "", ""), http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectCode(t, send(t, env.handler, http.MethodGet, base, "", ""), http.StatusBadRequest, apierr.CodeInvalidRequest)
	expectCode(t, send(t, env.handler, http.MethodGet, base+"?vm=redis-0&instance=other", "", ""), http.StatusNotFound, apierr.CodeNotFound)
	expectCode(t, send(t, env.handler, http.MethodGet, "/b/upgrade/tasks/nope/output?vm=redis-0", "", ""), http.StatusNotFound, apierr.CodeNotFound)

	env.set(commandsOff())
	expectCode(t, send(t, env.handler, http.MethodGet, outputURL(taskID), "", ""), http.StatusForbidden, apierr.CodeCommandsDisabled)

	env.set(commandsOn(cmdKey, false))
	expectCode(t, send(t, env.handler, http.MethodGet, outputURL(taskID), "", ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)
	expectCode(t, send(t, env.handler, http.MethodGet, outputURL(taskID), "", "wrong"), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)
}

func TestTaskDetailOfCommandTaskIsGated(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", false))
	taskID := ranCommandTask(t, env, "vcap", "")
	detail := "/b/upgrade/tasks/" + taskID

	env.set(commandsOff())

	denied := send(t, env.handler, http.MethodGet, detail, "", "")
	expectCode(t, denied, http.StatusForbidden, apierr.CodeCommandsDisabled)
	expectNoCommandText(t, "the refused detail", denied)

	env.set(commandsOn(cmdKey, false))
	expectCode(t, send(t, env.handler, http.MethodGet, detail, "", ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)

	allowed := send(t, env.handler, http.MethodGet, detail, "", cmdKey)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), cmdText) {
		t.Fatalf("expected 200 with the command text, got %d", allowed.Code)
	}

	// The node's output is in the vault, so a detail that read it would
	// carry it.
	if strings.Contains(allowed.Body.String(), strings.TrimSpace(cmdStdout)) || strings.Contains(allowed.Body.String(), `"stdout"`) {
		t.Fatal("the detail carried output bytes")
	}
}

func TestResumeOfCommandTaskIsGatedAndPauseAndCancelAreNot(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn(cmdKey, false))
	taskID := ranCommandTask(t, env, "vcap", cmdKey)
	resume := "/b/upgrade/tasks/" + taskID + "/resume"

	expectCode(t, send(t, env.handler, http.MethodPost, resume, "", ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)

	env.set(commandsOff())
	expectCode(t, send(t, env.handler, http.MethodPost, resume, "", cmdKey), http.StatusForbidden, apierr.CodeCommandsDisabled)

	env.set(commandsOn(cmdKey, false))

	// With the key, the resume reaches the manager, which turns away a task
	// that is not paused. The gate is not what answers.
	passed := send(t, env.handler, http.MethodPost, resume, "", cmdKey)
	if passed.Code == http.StatusUnauthorized || passed.Code == http.StatusForbidden {
		t.Fatalf("a resume with the key was refused by the gate with %d", passed.Code)
	}

	for _, action := range []string{"pause", "cancel"} {
		answer := send(t, env.handler, http.MethodPost, "/b/upgrade/tasks/"+taskID+"/"+action, "", "")
		if answer.Code == http.StatusUnauthorized || answer.Code == http.StatusForbidden {
			t.Errorf("%s needs only the broker credential, and answered %d", action, answer.Code)
		}
	}
}

func TestTaskDetailOfCertificateTaskIgnoresCommands(t *testing.T) {
	t.Parallel()

	const taskID = "cert-1"

	vault := newMemoryVault()
	created := time.Now().Add(-time.Hour)

	vault.store(t, "certificate-tasks/"+taskID, upgrade.UpgradeTask{
		ID: taskID, Kind: upgrade.TaskKindCertificates, Status: upgrade.TaskStatusCompleted, CreatedAt: created, CreationConfirmed: true,
	})
	vault.store(t, "certificate-tasks", map[string]interface{}{
		taskID: map[string]interface{}{"status": upgrade.TaskStatusCompleted, "created_at": created.Unix()},
	})

	handler := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger: &logger.NoOpLogger{}, Director: &vmsDirector{}, Vault: vault,
		Commands: commandsOff,
	})
	t.Cleanup(handler.Stop)

	select {
	case <-handler.GetManager().Loaded():
	case <-time.After(loadTimeout):
		t.Fatal("the manager never finished loading the stored tasks")
	}

	if got := send(t, handler, http.MethodGet, "/b/upgrade/tasks/"+taskID, "", ""); got.Code != http.StatusOK {
		t.Fatalf("expected the certificate task to answer 200 with commands off, got %d", got.Code)
	}
}

func TestPreviewRoute(t *testing.T) {
	t.Parallel()

	const target = "/b/upgrade/commands/preview"

	env := commandHandler(t, commandsOn("", false))
	handler := env.handler

	ok := send(t, handler, http.MethodPost, target, previewBody("vcap", ""), "")
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"preview_id"`) {
		t.Fatalf("expected a preview, got %d: %s", ok.Code, ok.Body.String())
	}

	expectNoCommandText(t, "the preview", ok)

	expectCode(t, send(t, handler, http.MethodPost, target, previewBody("root", ""), ""), http.StatusForbidden, apierr.CodeRootDisabled)

	cases := map[string]struct {
		body string
		code string
	}{
		"bad user":      {previewBody("bob", ""), apierr.CodeInvalidRunUser},
		"short timeout": {previewBody("vcap", `,"timeout_seconds":1`), apierr.CodeTimeoutOutOfRange},
		"long command":  {`{"instance_ids":["i1"],"command":"` + strings.Repeat("a", upgrade.MaxCommandBytes+1) + `","run_as":"vcap"}`, apierr.CodeCommandTooLong},
		"in flight":     {previewBody("vcap", `,"parallel":true,"max_in_flight":99`), apierr.CodeMaxInFlightTooHigh},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			expectCode(t, send(t, handler, http.MethodPost, target, tc.body, ""), http.StatusBadRequest, tc.code)
		})
	}

	off := commandHandler(t, commandsOff())
	expectCode(t, send(t, off.handler, http.MethodPost, target, previewBody("vcap", ""), ""), http.StatusForbidden, apierr.CodeCommandsDisabled)

	keyed := commandHandler(t, commandsOn(cmdKey, false))
	expectCode(t, send(t, keyed.handler, http.MethodPost, target, previewBody("vcap", ""), ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)
	expectCode(t, send(t, keyed.handler, http.MethodPost, target, previewBody("vcap", ""), "wrong"), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)

	if got := send(t, keyed.handler, http.MethodPost, target, previewBody("vcap", ""), cmdKey); got.Code != http.StatusOK {
		t.Fatalf("expected 200 with the key, got %d", got.Code)
	}
}

func TestCreateCommandRoute(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", false))
	id := previewID(t, env.handler, "vcap", "")

	created := send(t, env.handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "vcap"), "")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), cmdText) {
		t.Fatalf("expected 201 with the command text, got %d", created.Code)
	}

	list := send(t, env.handler, http.MethodGet, "/b/upgrade/tasks", "", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"command_digest"`) {
		t.Fatalf("expected the summary to carry the digest, got %d", list.Code)
	}

	expectNoCommandText(t, "the task list", list)
}

// A create without the second credential, or with commands off, is turned
// away before the preview is read, so it cannot spend the preview.
func TestCreateCommandRouteAuthorizesBeforeSpendingPreview(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn(cmdKey, false))
	handler := env.handler
	id := previewID(t, handler, "vcap", cmdKey)

	// A refused create made no task and spent no preview, so the list stays
	// empty after each one.
	expectEmptyList := func() {
		t.Helper()

		list := send(t, handler, http.MethodGet, "/b/upgrade/tasks", "", cmdKey)
		if list.Code != http.StatusOK || strings.TrimSpace(list.Body.String()) != "[]" {
			t.Fatal("a refused create left a task behind")
		}
	}

	expectCode(t, send(t, handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "vcap"), ""), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)
	expectEmptyList()
	expectCode(t, send(t, handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "vcap"), "wrong"), http.StatusUnauthorized, apierr.CodeCommandCredentialRequired)
	expectEmptyList()
	expectCode(t, send(t, handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "root"), cmdKey), http.StatusForbidden, apierr.CodeRootDisabled)
	expectEmptyList()

	if got := send(t, handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "vcap"), cmdKey); got.Code != http.StatusCreated {
		t.Fatalf("expected the preview to be unspent and the create to answer 201, got %d", got.Code)
	}

	off := commandHandler(t, commandsOff())
	expectCode(t, send(t, off.handler, http.MethodPost, "/b/upgrade/tasks", createBody("p-1", "vcap"), ""), http.StatusForbidden, apierr.CodeCommandsDisabled)
}

func TestCanHandleCommandPaths(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOff())

	for _, path := range []string{"/b/upgrade/commands/preview", "/b/upgrade/tasks/x/output"} {
		if !env.handler.CanHandle(path) {
			t.Fatalf("expected CanHandle to accept %s", path)
		}
	}

	for _, path := range []string{"/b/other/commands/preview", "/b/upgrades/tasks/x/output", "/b/upgrade"} {
		if env.handler.CanHandle(path) {
			t.Fatalf("expected CanHandle to refuse %s", path)
		}
	}
}

// An upgrade preview of the command kind is turned away as a bad request
// that points at the command routes, not as an unknown kind.
func TestUpgradePreviewRefusesCommandKind(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", false))

	recorder := send(t, env.handler, http.MethodPost, "/b/upgrade/preview", `{"kind":"command","instance_ids":["i1"]}`, "")
	expectError(t, recorder, http.StatusBadRequest, "created through CreateCommandTask")
}

// credentialPresented creates a task with the key header as given and returns
// the flag the task recorded.
func credentialPresented(t *testing.T, cfg config.CommandsConfig, header string) bool {
	t.Helper()

	env := commandHandler(t, cfg)
	id := previewID(t, env.handler, "vcap", header)

	created := send(t, env.handler, http.MethodPost, "/b/upgrade/tasks", createBody(id, "vcap"), header)
	if created.Code != http.StatusCreated {
		t.Fatalf("create answered %d", created.Code)
	}

	var task struct {
		Command struct {
			CredentialPresented bool `json:"credential_presented"`
		} `json:"command"`
	}

	if err := json.Unmarshal(created.Body.Bytes(), &task); err != nil {
		t.Fatalf("decoding the task: %v", err)
	}

	return task.Command.CredentialPresented
}

// The flag is audit information that a configured second credential was
// checked and matched, so a header sent to a deployment with no key does not
// set it.
func TestCreateRecordsWhetherTheCredentialWasChecked(t *testing.T) {
	t.Parallel()

	if !credentialPresented(t, commandsOn(cmdKey, false), cmdKey) {
		t.Fatal("a configured key with a matching header should read as presented")
	}

	if credentialPresented(t, commandsOn("", false), "unused-header") {
		t.Fatal("a header sent with no key configured should not read as presented")
	}

	if credentialPresented(t, commandsOn("", false), "") {
		t.Fatal("no key and no header should not read as presented")
	}
}
