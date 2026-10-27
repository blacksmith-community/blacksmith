package upgrade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

const (
	decideGateCommand = "echo decide-gate-canary-4e7a"
	decideGateKey     = "decide-gate-key"
)

// commandDecisionTasks records each decision call as decisionTasks does,
// and answers GetTask with a command task, which carries the command text.
type commandDecisionTasks struct {
	decisionTasks
}

func (c *commandDecisionTasks) GetTask(taskID string) (*upgrade.UpgradeTask, error) {
	return &upgrade.UpgradeTask{
		ID:      taskID,
		Kind:    upgrade.TaskKindCommand,
		Command: &upgrade.CommandTaskSpec{Command: decideGateCommand},
	}, nil
}

func (c *commandDecisionTasks) DecisionTaskKind(string) (upgrade.TaskKind, error) {
	return upgrade.TaskKindCommand, nil
}

func (c *commandDecisionTasks) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.calls...)
}

// decideGateHandler is a handler over a manager whose commands
// configuration is cfg, and over tasks that hold one command task.
func decideGateHandler(t *testing.T, cfg config.CommandsConfig) (*Handler, *commandDecisionTasks) {
	t.Helper()

	log := logger.NewNoOpLogger()
	manager := upgrade.NewManager(log, nil, emptyVault{})

	err := manager.Configure(upgrade.Deps{Commands: func() config.CommandsConfig { return cfg }})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	tasks := &commandDecisionTasks{}

	return &Handler{logger: log, manager: manager, tasks: tasks}, tasks
}

// postDecisionWithKey sends the body to the path with the command key
// header when key is not empty, and returns the status and the raw answer.
func postDecisionWithKey(t *testing.T, handler *Handler, path, body, key string) (int, string) {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		request.Header.Set(upgrade.CommandKeyHeader, key)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)

	return rec.Code, rec.Body.String()
}

// A decision on a command task takes the read gate the task detail takes,
// and it takes it before anything is applied. A caller the gate refuses
// gets the gate's own refusal, never the command text, and the decision
// never reaches the manager. A caller the gate allows has the decision
// applied.
func TestDecideOnACommandTaskTakesTheReadGateFirst(t *testing.T) {
	t.Parallel()

	root := true
	keyed := config.CommandsConfig{Root: &root, Credential: config.CommandCredential{Key: decideGateKey}}
	off := false
	disabled := config.CommandsConfig{Operation: config.CommandSwitch{On: &off}}

	bodies := map[string]string{
		"reconcile":  `{"action":"adopt","variable":"v"}`,
		"approve":    `{}`,
		"resolve":    `{"action":"verify"}`,
		"candidates": ``,
		"preview":    ``,
	}

	cases := map[string]struct {
		cfg    config.CommandsConfig
		key    string
		status int
		code   string
	}{
		"commands off": {disabled, "", http.StatusForbidden, apierr.CodeCommandsDisabled},
		"wrong key":    {keyed, "wrong", http.StatusUnauthorized, apierr.CodeCommandCredentialRequired},
		"right key":    {keyed, decideGateKey, http.StatusOK, ""},
	}

	for name, tc := range cases {
		for action, body := range bodies {
			t.Run(name+"/"+action, func(t *testing.T) {
				t.Parallel()

				handler, tasks := decideGateHandler(t, tc.cfg)

				status, raw := postDecisionWithKey(t, handler, "/b/upgrade/tasks/x/instances/i1/"+action, body, tc.key)
				if status != tc.status {
					t.Fatalf("answered %d, want %d", status, tc.status)
				}

				calls := tasks.recorded()

				if tc.code == "" {
					if len(calls) != 1 || !strings.HasPrefix(calls[0], action+" ") {
						t.Errorf("the manager saw %v, want the %s call", calls, action)
					}

					return
				}

				if !strings.Contains(raw, `"code":"`+tc.code+`"`) {
					t.Errorf("the refusal does not carry the code %s", tc.code)
				}

				if strings.Contains(raw, decideGateCommand) {
					t.Error("the refusal holds the command text")
				}

				if len(calls) != 0 {
					t.Errorf("the manager saw %v before the gate refused", calls)
				}
			})
		}
	}
}

// unreadDecisionTasks records each decision call as commandDecisionTasks
// does, but the task a decision names cannot be read, as while the stored
// tasks load or when the task is missing.
type unreadDecisionTasks struct {
	commandDecisionTasks

	unread error
}

func (u *unreadDecisionTasks) GetTask(string) (*upgrade.UpgradeTask, error) {
	return nil, upgrade.ErrTaskNotFound
}

func (u *unreadDecisionTasks) DecisionTaskKind(string) (upgrade.TaskKind, error) {
	return "", u.unread
}

// A decision whose task the gate cannot read is turned away with the error
// the decision would give, and is never applied, so the gate never lets a
// decision through on a command task it could not see.
func TestDecideOnATaskTheGateCannotReadIsNeverApplied(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		unread error
		status int
		code   string
	}{
		"tasks loading": {
			apierr.Wrap(http.StatusServiceUnavailable, apierr.CodeTasksLoading, upgrade.ErrTasksLoading),
			http.StatusServiceUnavailable, apierr.CodeTasksLoading,
		},
		"missing task": {
			apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound, "Blacksmith has no certificate task x."),
			http.StatusNotFound, apierr.CodeInstanceNotFound,
		},
	}

	for name, tc := range cases {
		for _, action := range []string{"reconcile", "approve", "resolve", "candidates", "preview"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				t.Parallel()

				log := logger.NewNoOpLogger()
				tasks := &unreadDecisionTasks{unread: tc.unread}
				handler := &Handler{logger: log, manager: upgrade.NewManager(log, nil, emptyVault{}), tasks: tasks}

				status, raw := postDecisionWithKey(t, handler, "/b/upgrade/tasks/x/instances/i1/"+action, `{}`, "")
				if status != tc.status {
					t.Fatalf("answered %d, want %d", status, tc.status)
				}

				if !strings.Contains(raw, `"code":"`+tc.code+`"`) {
					t.Errorf("the refusal does not carry the code %s", tc.code)
				}

				if calls := tasks.recorded(); len(calls) != 0 {
					t.Errorf("the manager saw %v after the gate could not read the task", calls)
				}
			})
		}
	}
}
