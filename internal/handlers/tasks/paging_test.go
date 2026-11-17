package tasks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/pkg/logger"
)

// pagedBody is the object form of the list answer.
type pagedBody struct {
	Tasks       []tasks.Task   `json:"tasks"`
	Offset      int            `json:"offset"`
	Limit       int            `json:"limit"`
	HasMore     bool           `json:"has_more"`
	Exact       bool           `json:"exact"`
	StateCounts map[string]int `json:"state_counts"`
	Ceiling     bool           `json:"ceiling"`
}

// historyHandler serves the tasks list over a real director adapter that
// talks to a test director holding total tasks, newest first, all done except
// the ones in failed. The test director filters by state and limit as the
// real one does.
const (
	pagedDone      = "done"
	pagedUser      = "admin"
	pagedLogLevel  = "debug"
	pagedLogFormat = "json"
)

func historyHandler(t *testing.T, total int, failed map[int]bool) *tasks.Handler {
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

		limit, _ := strconv.Atoi(query.Get("limit"))

		var out []interface{}

		for number := total; number >= 1 && (limit == 0 || len(out) < limit); number-- {
			state := pagedDone
			if failed[number] {
				state = errorState
			}

			if len(wanted) > 0 && !wanted[state] {
				continue
			}

			out = append(out, map[string]interface{}{
				"id": number, "state": state, "description": createDeploymentText,
				"timestamp": 1759766400, "started_at": 1759766000, "user": pagedUser, "deployment": managedDeployment,
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

func get(t *testing.T, handler *tasks.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.ListTasks(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", target, recorder.Code, recorder.Body.String())
	}

	return recorder
}

func decodePage(t *testing.T, recorder *httptest.ResponseRecorder) pagedBody {
	t.Helper()

	var body pagedBody

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}

	return body
}

// A failed task behind more than 400 newer tasks still comes back from a
// states=error query, in the bare form and in the object form.
func TestStatesQueryFindsAFailedTaskBehindMoreThan400NewerTasks(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 2500, map[int]bool{7: true})

	bare := get(t, handler, "/b/tasks?type=recent&limit=200&states=error")

	var list []tasks.Task

	err := json.Unmarshal(bare.Body.Bytes(), &list)
	if err != nil {
		t.Fatalf("the plain form must stay a bare array: %v", err)
	}

	if len(list) != 1 || list[0].ID != 7 || list[0].State != errorState {
		t.Fatalf("expected the failed task 7, got %+v", list)
	}

	if bare.Header().Get("X-Blacksmith-Has-More") != "false" || bare.Header().Get("X-Blacksmith-Exact") != "true" {
		t.Errorf("headers = %v, want no more rows and an exact answer", bare.Header())
	}

	page := decodePage(t, get(t, handler, "/b/tasks?type=recent&limit=200&states=error&paged=1"))
	if len(page.Tasks) != 1 || page.Tasks[0].ID != 7 || page.HasMore || !page.Exact {
		t.Fatalf("object form: %+v", page)
	}

	if page.StateCounts != nil {
		t.Errorf("paged=1 alone must not carry counts, got %v", page.StateCounts)
	}
}

// Offset pages follow one another with no overlap, and has_more is true until
// the last page.
func TestOffsetPagesWalkTheHistory(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 25, nil)

	var ids []int

	for offset, more := 0, true; more; {
		page := decodePage(t, get(t, handler, "/b/tasks?paged=1&limit=10&offset="+strconv.Itoa(offset)))
		if page.Offset != offset || page.Limit != 10 {
			t.Fatalf("page echoes offset %d limit %d, want %d and 10", page.Offset, page.Limit, offset)
		}

		for _, task := range page.Tasks {
			ids = append(ids, task.ID)
		}

		offset += len(page.Tasks)
		more = page.HasMore

		if offset > 25 {
			t.Fatal("paging did not end")
		}
	}

	if len(ids) != 25 || ids[0] != 25 || ids[24] != 1 {
		t.Fatalf("expected tasks 25 down to 1 once each, got %v", ids)
	}
}

// With counts, the chips count the director's tasks of each state, so they
// are the same on every page and do not depend on how many rows are loaded.
func TestCountsDoNotChangeFromPageToPage(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 30, map[int]bool{28: true, 5: true})

	for _, query := range []string{"/b/tasks?counts=1&limit=10", "/b/tasks?counts=1&limit=10&offset=10", "/b/tasks?counts=1&limit=10&offset=20"} {
		page := decodePage(t, get(t, handler, query))
		if page.StateCounts["error"] != 2 || page.StateCounts["done"] != 28 {
			t.Fatalf("%s: counts %v, want 2 errors and 28 done", query, page.StateCounts)
		}
	}
}

// A team filter that cannot be settled inside the scan ceiling says so.
func TestAnInexactListingIsMarkedInexact(t *testing.T) {
	t.Parallel()

	page := decodePage(t, get(t, historyHandler(t, 6000, nil), "/b/tasks?paged=1&limit=10&team=service-instances"))
	if page.Exact || !page.HasMore || len(page.Tasks) != 0 {
		t.Fatalf("expected an inexact answer that says more may exist, got %+v", page)
	}
}

// A request that names a state with counts still gets an exact list, and
// every chip counts its state through its own listing.
func TestCountsWithStatesListsExactlyAndCountsEachState(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 2500, map[int]bool{7: true})

	for _, query := range []string{"states=error", "state=error"} {
		page := decodePage(t, get(t, handler, "/b/tasks?type=recent&limit=200&counts=1&"+query))

		if len(page.Tasks) != 1 || page.Tasks[0].ID != 7 || !page.Exact || page.HasMore {
			t.Fatalf("%s: expected only failed task 7, exact, got %+v", query, page)
		}

		if page.StateCounts[pagedDone] != 200 || page.StateCounts[errorState] != 1 {
			t.Fatalf("%s: counts %v, want 200 done and the one error", query, page.StateCounts)
		}
	}
}

// The page that reaches the deepest listable row says so, and a request past
// it is answered with an empty page that says so, never with a 400.
func TestThePagingCeilingIsSignalled(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 2500, nil)

	last := decodePage(t, get(t, handler, "/b/tasks?paged=1&limit=200&offset=1800"))
	if len(last.Tasks) != 200 || last.Tasks[0].ID != 700 || !last.HasMore || !last.Ceiling {
		t.Fatalf("deepest page: %d tasks, more %v, ceiling %v", len(last.Tasks), last.HasMore, last.Ceiling)
	}

	beyond := decodePage(t, get(t, handler, "/b/tasks?paged=1&limit=200&offset=2000"))
	if len(beyond.Tasks) != 0 || !beyond.Ceiling {
		t.Fatalf("past the ceiling: %d tasks, ceiling %v", len(beyond.Tasks), beyond.Ceiling)
	}

	bare := get(t, handler, "/b/tasks?limit=200&offset=1800")
	if bare.Header().Get("X-Blacksmith-Ceiling") != "true" {
		t.Errorf("the bare form must carry the ceiling in a header, got %v", bare.Header())
	}

	short := decodePage(t, get(t, handler, "/b/tasks?paged=1&limit=10&offset=100"))
	if short.Ceiling {
		t.Error("a page far from the ceiling must not report it")
	}

	exact := decodePage(t, get(t, historyHandler(t, 2000, nil), "/b/tasks?paged=1&limit=200&offset=1800"))
	if exact.Ceiling || exact.HasMore {
		t.Errorf("a history that ends at the ceiling has no more and no ceiling: %+v", exact)
	}
}

func TestAnUnusableOffsetIsRejected(t *testing.T) {
	t.Parallel()

	handler := historyHandler(t, 10, nil)

	for _, target := range []string{"/b/tasks?offset=-1", "/b/tasks?offset=abc"} {
		recorder := httptest.NewRecorder()
		handler.ListTasks(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", target, recorder.Code)
		}
	}
}
