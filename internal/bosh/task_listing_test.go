package bosh_test

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
)

// historyServer answers task and event lists the way the director does. Its
// tasks are numbered from total down to 1, newest first, and every task is
// done except the ones in failed. It honours the state and limit parameters.
func historyServer(t *testing.T, total int, failed map[int]bool) *boshtest.Director {
	t.Helper()

	return boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != taskListRoute {
			return false
		}

		wanted := map[string]bool{}

		if state := request.URL.Query().Get("state"); state != "" {
			for _, one := range strings.Split(state, ",") {
				wanted[one] = true
			}
		}

		limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))

		var out []interface{}

		for number := total; number >= 1 && (limit == 0 || len(out) < limit); number-- {
			state := batchTaskDone
			if failed[number] {
				state = batchTaskError
			}

			if len(wanted) > 0 && !wanted[state] {
				continue
			}

			out = append(out, directorTask(number, state))
		}

		boshtest.WriteJSON(writer, http.StatusOK, out)

		return true
	})
}

const (
	eventDeployment = "deployment"
	eventTimeKey    = "timestamp"
)

func lister(t *testing.T, server *boshtest.Director) bosh.TaskLister {
	t.Helper()

	found, ok := newReadAdapter(t, server, nil).(bosh.TaskLister)
	if !ok {
		t.Fatal("the director adapter does not list tasks exactly")
	}

	return found
}

// An error task behind more than 400 newer tasks is still found, because the
// director filters by state before it applies the limit.
func TestListTasksFindsAnErrorBehindManyNewerTasks(t *testing.T) {
	t.Parallel()

	listing, err := lister(t, historyServer(t, 6000, map[int]bool{12: true})).ListTasks(t.Context(), "recent", 200, []string{batchTaskError}, "")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	if len(listing.Tasks) != 1 || listing.Tasks[0].ID != 12 {
		t.Fatalf("expected the error task 12, got %+v", listing.Tasks)
	}

	if listing.More || !listing.Exact {
		t.Errorf("More=%v Exact=%v, want a complete answer", listing.More, listing.Exact)
	}
}

// The listing asks for one task beyond the limit, so it can say that more
// match without reading them all.
func TestListTasksReportsWhetherMoreMatch(t *testing.T) {
	t.Parallel()

	list := lister(t, historyServer(t, 30, nil))

	page, err := list.ListTasks(t.Context(), "recent", 10, nil, "")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	if len(page.Tasks) != 10 || !page.More || page.Tasks[0].ID != 30 {
		t.Fatalf("first page: %d tasks, More=%v, first %d", len(page.Tasks), page.More, page.Tasks[0].ID)
	}

	rest, err := list.ListTasks(t.Context(), "recent", 100, nil, "")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	if len(rest.Tasks) != 30 || rest.More || !rest.Exact {
		t.Fatalf("all tasks: %d, More=%v Exact=%v", len(rest.Tasks), rest.More, rest.Exact)
	}
}

// The state filter goes to the director, so the director does the filtering
// before it limits.
func TestListTasksSendsTheStatesToTheDirector(t *testing.T) {
	t.Parallel()

	server := historyServer(t, 20, map[int]bool{5: true, 9: true})

	_, err := lister(t, server).ListTasks(t.Context(), "recent", 5, []string{batchTaskError, taskCancelled}, "")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	lists := requestsTo(server, http.MethodGet, "/tasks")
	if len(lists) != 1 || !strings.Contains(lists[0].RawQuery, "state=error%2Ccancelled") {
		t.Fatalf("expected one request with both states, got %+v", lists)
	}
}

// A team that keeps few tasks widens the request until the page is full, and
// stops when the director has no more.
func TestListTasksWidensForATeamFilter(t *testing.T) {
	t.Parallel()

	server := historyServer(t, 100, nil)

	listing, err := lister(t, server).ListTasks(t.Context(), "recent", 10, nil, "service-instances")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	if len(listing.Tasks) != 0 || listing.More || !listing.Exact {
		t.Fatalf("expected no matches and an exact answer once the director ran dry, got %d More=%v Exact=%v", len(listing.Tasks), listing.More, listing.Exact)
	}

	if lists := requestsTo(server, http.MethodGet, "/tasks"); len(lists) < 2 {
		t.Errorf("expected the request to widen, got %d requests", len(lists))
	}
}

// With more tasks than the scan ceiling and none that match the team, the
// listing says it is not exact instead of claiming there are none.
func TestListTasksSaysWhenTheScanCeilingStoppedIt(t *testing.T) {
	t.Parallel()

	listing, err := lister(t, historyServer(t, 6000, nil)).ListTasks(t.Context(), "recent", 10, nil, "service-instances")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}

	if listing.Exact || !listing.More {
		t.Errorf("Exact=%v More=%v, want an inexact answer that says more may exist", listing.Exact, listing.More)
	}
}

// Events follow the director's before_id cursor across pages.
func TestListEventsFollowsTheCursorAndReportsMore(t *testing.T) {
	t.Parallel()

	const total = 450

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodGet || request.URL.Path != "/events" {
			return false
		}

		before := total + 1
		if value := request.URL.Query().Get("before_id"); value != "" {
			before, _ = strconv.Atoi(value)
		}

		var page []interface{}

		for id := before - 1; id >= 1 && len(page) < 200; id-- {
			page = append(page, map[string]interface{}{"id": strconv.Itoa(id), eventTimeKey: 1759766400, "action": writeCreate, "object_type": eventDeployment, "object_name": detailsDeployment, eventDeployment: detailsDeployment})
		}

		boshtest.WriteJSON(writer, http.StatusOK, page)

		return true
	})

	events, ok := newReadAdapter(t, server, nil).(bosh.EventLister)
	if !ok {
		t.Fatal("the director adapter does not list events")
	}

	first, err := events.ListEvents(t.Context(), "valkey-1", 250)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	if len(first.Events) != 250 || !first.More || first.Events[0].ID != "450" || first.Events[249].ID != "201" {
		t.Fatalf("first listing: %d events, More=%v", len(first.Events), first.More)
	}

	all, err := events.ListEvents(t.Context(), "valkey-1", 1000)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	if len(all.Events) != total || all.More {
		t.Fatalf("full listing: %d events, More=%v", len(all.Events), all.More)
	}
}

// plainDirector is a director that has no exact listing.
type plainDirector struct{ bosh.Director }

// A director with no exact listing is refused, so a states query is never
// answered from a plain window that can miss tasks.
func TestListTasksFromRefusesADirectorThatCannotListExactly(t *testing.T) {
	t.Parallel()

	_, err := bosh.ListTasksFrom(t.Context(), plainDirector{}, "recent", 10, []string{batchTaskError}, "")
	if !errors.Is(err, bosh.ErrExactListingUnsupported) {
		t.Fatalf("expected ErrExactListingUnsupported, got %v", err)
	}
}
