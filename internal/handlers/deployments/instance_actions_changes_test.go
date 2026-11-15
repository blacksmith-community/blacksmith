package deployments_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/mutation"
)

var errChangeVaultSealed = errors.New("vault is sealed")

const outcomeRefused = "refused"

// recordedSetup is an instance action setup whose handler keeps its change
// records in an in-memory store.
type recordedSetup struct {
	instanceActionSetup

	store    *audit.ChangeStore
	vaultKV  *audittest.KV
	storeLog *audittest.Logger
}

func newRecordedSetup(deps deployments.Dependencies) recordedSetup {
	vaultKV, storeLog := audittest.NewKV(), &audittest.Logger{}
	store := audit.NewChangeStore(vaultKV, storeLog, audit.WithRetryDelay(time.Millisecond))
	deps.Changes = store

	return recordedSetup{instanceActionSetup: newInstanceActionSetup(deps), store: store, vaultKV: vaultKV, storeLog: storeLog}
}

func (s recordedSetup) records(t *testing.T) []audit.ChangeRecord {
	t.Helper()

	s.store.Wait()

	records, err := s.store.List(t.Context(), audit.ScopeInstances, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return records
}

// onlyRecord returns the one record an action left, and fails the test
// when it left none or more.
func onlyRecord(t *testing.T, records []audit.ChangeRecord) audit.ChangeRecord {
	t.Helper()

	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}

	return records[0]
}

// An accepted restart or recreate leaves a record of who asked, from where,
// for which instance and deployment, and the task the director queued,
// and its audit line is still logged.
func TestInstanceActionsRecordAnAcceptedAction(t *testing.T) {
	t.Parallel()

	for _, action := range []string{actionRestart, actionRecreate} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			setup := newRecordedSetup(deployments.Dependencies{})

			recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/"+action)
			if recorder.Code != http.StatusAccepted {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}

			rec := onlyRecord(t, setup.records(t))
			if rec.Instance != ownedInstance || rec.Deployment != ownedDeployment || rec.Action != action || rec.Outcome != "accepted" ||
				rec.TaskID != instanceTaskID || rec.Actor != "operator" || rec.From != "192.0.2.10:51234 via 198.51.100.7" || rec.At.IsZero() {
				t.Fatalf("record = %+v", rec)
			}

			if lines := setup.log.audits(); len(lines) != 1 || !strings.Contains(lines[0], `outcome="accepted"`) {
				t.Fatalf("audit lines = %q, want the accepted line", lines)
			}
		})
	}
}

// A refusal before the director, a director refusal, and an answer that
// never came each leave a record with that outcome.
func TestInstanceActionsRecordEveryOutcome(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		deps       deployments.Dependencies
		path       string
		prepare    func(setup recordedSetup)
		outcome    string
		deployment string
	}{
		"an instance the broker does not own": {path: "/b/9f9f-not-ours/restart", outcome: outcomeRefused},
		"busy": {path: "/b/" + ownedInstance + "/recreate", outcome: outcomeRefused, deployment: ownedDeployment, prepare: func(setup recordedSetup) {
			_, err := setup.guard.Acquire(ownedInstance, "", mutation.Holder{Kind: mutation.KindUpgrade, Description: "batch upgrade holding 7a1b"})
			if err != nil {
				panic(err)
			}
		}},
		"director refused": {path: "/b/" + ownedInstance + "/restart", outcome: "failed", deployment: ownedDeployment, prepare: func(setup recordedSetup) {
			setup.director.fail = errInstanceDirectorRefused
		}},
		"answer lost": {deps: deployments.Dependencies{Config: writeTimeoutConfig{seconds: 1}}, path: "/b/" + ownedInstance + "/restart", outcome: "unknown", deployment: ownedDeployment, prepare: func(setup recordedSetup) {
			setup.director.stall = true
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			setup := newRecordedSetup(testCase.deps)
			if testCase.prepare != nil {
				testCase.prepare(setup)
			}

			serveInstanceAction(t, setup.handler, http.MethodPost, testCase.path)

			rec := onlyRecord(t, setup.records(t))
			if rec.Outcome != testCase.outcome || rec.Deployment != testCase.deployment || rec.Actor != "operator" {
				t.Fatalf("record = %+v, want outcome %s on %q", rec, testCase.outcome, testCase.deployment)
			}
		})
	}
}

// A Vault that refuses the record changes nothing about the action's
// answer, and the lost record is logged.
func TestInstanceActionsAnswerTheSameWhenTheRecordFails(t *testing.T) {
	t.Parallel()

	setup := newRecordedSetup(deployments.Dependencies{})
	setup.vaultKV.Fail(errChangeVaultSealed)

	recorder := serveInstanceAction(t, setup.handler, http.MethodPost, "/b/"+ownedInstance+"/restart")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", recorder.Code, recorder.Body.String())
	}

	if body := decodeBody(t, recorder); body["task_id"] != float64(instanceTaskID) || body["operation"] != actionRestart {
		t.Errorf("body = %v, want the queued task", body)
	}

	setup.store.Wait()

	if text := setup.storeLog.Text(); !strings.Contains(text, "was never saved to Vault") {
		t.Fatalf("store log = %q, want the lost record logged", text)
	}

	if lines := setup.log.audits(); len(lines) != 1 || !strings.Contains(lines[0], `outcome="accepted"`) {
		t.Fatalf("audit lines = %q, want the accepted line", lines)
	}
}
