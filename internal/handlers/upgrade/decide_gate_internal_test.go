package upgrade

import (
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

const decideGateCommand = "echo decide-gate-canary-4e7a"

// commandDecisionTasks answers a decision as a manager would that applied
// it, and answers GetTask with a command task, which carries the command
// text.
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

// A decision that succeeds answers with the task, and the detail of a
// command task holds the command text, so the answer takes the read gate
// the task detail takes. With commands off the caller gets the gate's own
// refusal and never the text.
func TestDecideOnACommandTaskAnswersWithTheReadGate(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"reconcile":  `{"action":"adopt","variable":"v"}`,
		"approve":    `{}`,
		"resolve":    `{"action":"verify"}`,
		"candidates": ``,
	}

	for action, body := range bodies {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			log := logger.NewNoOpLogger()
			handler := &Handler{logger: log, manager: upgrade.NewManager(log, nil, emptyVault{}), tasks: &commandDecisionTasks{}}

			status, answer := postDecision(t, handler, "/b/upgrade/tasks/x/instances/i1/"+action, body)
			if status != http.StatusForbidden || answer["code"] != apierr.CodeCommandsDisabled {
				t.Fatalf("answered %d with %v, want 403 %s", status, answer, apierr.CodeCommandsDisabled)
			}

			for _, value := range answer {
				if text, ok := value.(string); ok && strings.Contains(text, decideGateCommand) {
					t.Fatal("the refusal holds the command text")
				}
			}
		})
	}
}
