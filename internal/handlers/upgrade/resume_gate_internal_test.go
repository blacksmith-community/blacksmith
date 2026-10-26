package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

// resumeTasks is a taskManager whose command task resumes without sending
// anything unless asks is set, in which case the manager asks the gate it
// was given, as it does for a resume that could send the command.
type resumeTasks struct {
	taskManager

	asks bool
}

func (f *resumeTasks) GetTask(string) (*upgrade.UpgradeTask, error) {
	return &upgrade.UpgradeTask{ID: "x", Kind: upgrade.TaskKindCommand, Status: upgrade.TaskStatusCancelled}, nil
}

func (f *resumeTasks) ResumeTaskGated(ctx context.Context, _ string, gate func(context.Context) error) error {
	if f.asks {
		return gate(ctx)
	}

	return nil
}

// emptyVault holds nothing, which is all a manager that only builds its
// authorizer asks of it.
type emptyVault struct {
	interfaces.Vault
}

func (emptyVault) Get(context.Context, string, interface{}) (bool, error) { return false, nil }

func resumeStatus(t *testing.T, tasks *resumeTasks) (int, map[string]interface{}) {
	t.Helper()

	log := logger.NewNoOpLogger()
	handler := &Handler{logger: log, manager: upgrade.NewManager(log, nil, emptyVault{}), tasks: tasks}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/b/upgrade/tasks/x/resume", nil))

	var body map[string]interface{}

	err := json.Unmarshal(rec.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	return rec.Code, body
}

// The handler leaves the decision to the manager. A resume the manager says
// only cleans a cancelled task is let through with commands off, and one it
// says could send the command is refused with the gate's own status and
// code.
func TestResumeLeavesTheCommandGateToTheManager(t *testing.T) {
	t.Parallel()

	status, body := resumeStatus(t, &resumeTasks{})
	if status != http.StatusOK || body["success"] != true {
		t.Fatalf("a resume that only cleans answered %d with %v, want it let through", status, body)
	}

	status, body = resumeStatus(t, &resumeTasks{asks: true})
	if status != http.StatusForbidden || body["code"] != apierr.CodeCommandsDisabled {
		t.Fatalf("a resume that could send answered %d with %v, want 403 %s", status, body, apierr.CodeCommandsDisabled)
	}
}
