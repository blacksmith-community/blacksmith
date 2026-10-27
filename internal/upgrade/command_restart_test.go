package upgrade

import (
	"slices"
	"testing"
)

// A restart that finds one node staged and two sent settles each of them
// through its own cleanup, sends nothing else, skips the pending node, and
// fails the instance. The layouts put the staged node first and in the
// middle.
func TestRestartSettlesStagedAndSentNodesThroughCleanupOnly(t *testing.T) {
	for _, layout := range []map[int]CommandPhase{
		{0: CommandPhaseStaged, 1: CommandPhaseSent, 2: CommandPhaseSent},
		{0: CommandPhaseSent, 1: CommandPhaseStaged, 2: CommandPhaseSent},
	} {
		fix := newTaskFixtureFrom(t, 1, 4, restartedWith(layout), withParallel(3))
		fix.cleanups["dep-0/1"] = "cleanup started=yes exit=0 killed=no"

		done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

		for index := 0; index < 3; index++ {
			if ops := fix.opsOf("dep-0", index); !slices.Equal(ops, []string{opRunCleanup}) {
				t.Errorf("node %d (%s) saw %v, want exactly [%s]", index, layout[index], ops, opRunCleanup)
			}

			fix.assertNeverSentTo("dep-0", index)
		}

		if ops := fix.opsOf("dep-0", 3); len(ops) != 0 {
			t.Errorf("the pending node saw %v, want nothing", ops)
		}

		got := phases(done.Instances[0])
		if !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped}) {
			t.Errorf("phases = %v", got)
		}

		if done.Instances[0].Status != InstanceStatusFailed {
			t.Errorf("the instance is %s, want failed", done.Instances[0].Status)
		}

		fix.waitReleased()
		fix.assertNoCommandText()
	}
}
