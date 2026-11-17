package tasks_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/pkg/logger"
)

const (
	createDeploymentText = "create deployment"
	countsError          = "error"
	countsTeamQuery      = "&team=valkey"
	countsAllStates      = "/b/tasks?type=recent&limit=200&counts=1"
	countsErrorsOnly     = "/b/tasks?type=recent&limit=200&states=error&paged=1"
)

// directorTask is one task the counting test director holds.
type directorTask struct {
	id         int
	state      string
	deployment string
}

// countsBody is the answer to a counts request.
type countsBody struct {
	Tasks              []tasks.Task   `json:"tasks"`
	HasMore            bool           `json:"has_more"`
	StateCounts        map[string]int `json:"state_counts"`
	StateCountsCapped  []string       `json:"state_counts_capped"`
	StateCountsUnknown []string       `json:"state_counts_unknown"`
}

// countsHandler serves the tasks list over a real director adapter backed by
// a test director that holds the given tasks, newest first. The director
// filters by state and limit as the real one does, and refuses a request for
// any state in refused with a server error.
func countsHandler(t *testing.T, held []directorTask, refused ...string) *tasks.Handler {
	t.Helper()

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/tasks" {
			return false
		}

		query := request.URL.Query()
		wanted := map[string]bool{}

		if state := query.Get("state"); state != "" {
			for _, one := range strings.Split(state, ",") {
				wanted[one] = true
			}
		}

		for _, state := range refused {
			if wanted[state] {
				writer.WriteHeader(http.StatusInternalServerError)

				return true
			}
		}

		limit, _ := strconv.Atoi(query.Get("limit"))

		var out []interface{}

		for _, one := range held {
			if limit != 0 && len(out) >= limit {
				break
			}

			if len(wanted) > 0 && !wanted[one.state] {
				continue
			}

			out = append(out, map[string]interface{}{
				"id": one.id, "state": one.state, "description": createDeploymentText,
				"timestamp": 1759766400, "started_at": 1759766000, "user": pagedUser, "deployment": one.deployment,
			})
		}

		boshtest.WriteJSON(writer, http.StatusOK, out)

		return true
	})

	director, err := bosh.NewDirectorAdapter(bosh.Config{Address: server.URL, Username: pagedUser, Password: pagedUser, CACert: server.CACert})
	if err != nil {
		t.Fatalf("director adapter: %v", err)
	}

	log, err := logger.New(logger.Config{Level: pagedLogLevel, Format: pagedLogFormat})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	return tasks.NewHandler(tasks.Dependencies{Logger: log, Director: director})
}

func decodeCounts(t *testing.T, target string, handler *tasks.Handler) countsBody {
	t.Helper()

	var body countsBody

	err := json.Unmarshal(get(t, handler, target).Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}

	return body
}

// history builds tasks numbered total down to 1, newest first. Every task is
// done in the deployment named by deploymentOf, except those whose number
// stateOf maps to another state.
func history(total int, deploymentOf func(int) string, stateOf func(int) string) []directorTask {
	out := make([]directorTask, 0, total)

	for number := total; number >= 1; number-- {
		state := pagedDone

		if stateOf != nil {
			if other := stateOf(number); other != "" {
				state = other
			}
		}

		out = append(out, directorTask{id: number, state: state, deployment: deploymentOf(number)})
	}

	return out
}

func valkeyDeployment(int) string { return managedDeployment }

// An error task older than the newest 200 tasks is counted, so the chip
// matches the rows that pressing it lists.
func TestErrorChipCountsErrorsBeyondTheNewest200Tasks(t *testing.T) {
	t.Parallel()

	handler := countsHandler(t, history(1000, valkeyDeployment, func(number int) string {
		if number <= 9 {
			return countsError
		}

		return ""
	}))

	counts := decodeCounts(t, countsAllStates, handler)
	rows := decodeCounts(t, countsErrorsOnly, handler)

	if counts.StateCounts[countsError] != 9 || len(rows.Tasks) != 9 {
		t.Fatalf("error chip %d, rows %d, want both 9", counts.StateCounts[countsError], len(rows.Tasks))
	}

	if counts.StateCounts[pagedDone] != 200 || !slices.Contains(counts.StateCountsCapped, pagedDone) {
		t.Fatalf("done chip %d capped %v, want 200 and capped", counts.StateCounts[pagedDone], counts.StateCountsCapped)
	}

	if len(counts.StateCountsUnknown) != 0 {
		t.Fatalf("no count failed, got unknown %v", counts.StateCountsUnknown)
	}
}

// A narrowed Scope is applied after the director answers. The newest tasks
// belong to another deployment, so counting a raw 201-task fetch would see
// no errors at all, while the rows for the scope hold five.
func TestChipCountMatchesRowsUnderANarrowedScope(t *testing.T) {
	t.Parallel()

	handler := countsHandler(t, history(700, func(number int) string {
		if number > 300 {
			return "other-1"
		}

		return managedDeployment
	}, func(number int) string {
		if number <= 5 {
			return countsError
		}

		return ""
	}))

	counts := decodeCounts(t, countsAllStates+countsTeamQuery, handler)
	rows := decodeCounts(t, countsErrorsOnly+countsTeamQuery, handler)

	if counts.StateCounts[countsError] != 5 || len(rows.Tasks) != 5 {
		t.Fatalf("error chip %d, rows %d, want both 5", counts.StateCounts[countsError], len(rows.Tasks))
	}

	if counts.StateCounts[pagedDone] != 200 || !slices.Contains(counts.StateCountsCapped, pagedDone) {
		t.Fatalf("done chip %d capped %v, want 200 and capped", counts.StateCounts[pagedDone], counts.StateCountsCapped)
	}
}

// A state with more than 200 tasks stops at 200 and is named as capped, and
// the rows listed for it also begin with 200 and say more follow.
func TestChipCountStopsAt200AndIsFlaggedCapped(t *testing.T) {
	t.Parallel()

	handler := countsHandler(t, history(450, valkeyDeployment, func(number int) string {
		if number > 5 {
			return countsError
		}

		return ""
	}))

	counts := decodeCounts(t, countsAllStates, handler)
	rows := decodeCounts(t, countsErrorsOnly, handler)

	if counts.StateCounts[countsError] != 200 || !slices.Contains(counts.StateCountsCapped, countsError) {
		t.Fatalf("error chip %d capped %v, want 200 and capped", counts.StateCounts[countsError], counts.StateCountsCapped)
	}

	if len(rows.Tasks) != 200 || !rows.HasMore {
		t.Fatalf("error rows %d more %v, want 200 and more", len(rows.Tasks), rows.HasMore)
	}

	if counts.StateCounts[pagedDone] != 5 || slices.Contains(counts.StateCountsCapped, pagedDone) {
		t.Fatalf("done chip %d capped %v, want 5 and not capped", counts.StateCounts[pagedDone], counts.StateCountsCapped)
	}
}

// A state that is exactly 200 is not capped, because nothing lies beyond it.
func TestChipCountAtExactly200IsNotCapped(t *testing.T) {
	t.Parallel()

	counts := decodeCounts(t, countsAllStates, countsHandler(t, history(200, valkeyDeployment, nil)))

	if counts.StateCounts[pagedDone] != 200 || len(counts.StateCountsCapped) != 0 {
		t.Fatalf("done chip %d capped %v, want 200 and not capped", counts.StateCounts[pagedDone], counts.StateCountsCapped)
	}
}

// One state whose count fails is unknown, never zero, and the other states
// and the rows are unaffected.
func TestFailedStateCountIsUnknownNotZero(t *testing.T) {
	t.Parallel()

	handler := countsHandler(t, history(30, valkeyDeployment, func(number int) string {
		if number <= 4 {
			return countsError
		}

		return ""
	}), "queued")

	counts := decodeCounts(t, countsAllStates, handler)

	if !slices.Equal(counts.StateCountsUnknown, []string{"queued"}) {
		t.Fatalf("unknown = %v, want [queued]", counts.StateCountsUnknown)
	}

	if _, present := counts.StateCounts["queued"]; present {
		t.Fatalf("queued must be absent, not zero: %v", counts.StateCounts)
	}

	if counts.StateCounts[countsError] != 4 || counts.StateCounts[pagedDone] != 26 || len(counts.Tasks) != 30 {
		t.Fatalf("other states or rows changed: %v with %d rows", counts.StateCounts, len(counts.Tasks))
	}
}
