package upgrade_test

import (
	"encoding/json"
	"strings"
	"testing"

	"blacksmith/internal/upgrade"
)

const (
	summaryActor = "operator"
	summaryFrom  = "192.0.2.10:51234 via 198.51.100.7"
)

// A summary of every kind of task names who started it and where the
// request came from, so the Activity list can say so without reading each
// task's detail.
func TestToSummaryNamesWhoStartedTheTask(t *testing.T) {
	t.Parallel()

	for _, kind := range []upgrade.TaskKind{upgrade.TaskKindUpgrade, upgrade.TaskKindCertificates, upgrade.TaskKindCommand} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			task := &upgrade.UpgradeTask{ID: "task-" + string(kind), Kind: kind, CreatedBy: summaryActor, CreatedFrom: summaryFrom}

			summary := task.ToSummary()
			if summary.CreatedBy != summaryActor || summary.CreatedFrom != summaryFrom {
				t.Fatalf("summary names %q from %q, want operator from the request address", summary.CreatedBy, summary.CreatedFrom)
			}

			encoded, err := json.Marshal(summary)
			if err != nil {
				t.Fatalf("marshal summary: %v", err)
			}

			if !strings.Contains(string(encoded), `"created_by":"operator"`) || !strings.Contains(string(encoded), `"created_from":"192.0.2.10:51234 via 198.51.100.7"`) {
				t.Fatalf("summary JSON lacks created_by or created_from: %s", encoded)
			}
		})
	}
}

// A task made with no actor in its context, as one a test or an older
// build made, has a summary without either field.
func TestToSummaryOmitsAnUnknownActor(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal((&upgrade.UpgradeTask{ID: "task-unknown"}).ToSummary())
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}

	if strings.Contains(string(encoded), "created_by") || strings.Contains(string(encoded), "created_from") {
		t.Fatalf("summary of a task without an actor names one: %s", encoded)
	}
}
