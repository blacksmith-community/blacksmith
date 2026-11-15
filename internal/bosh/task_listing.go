package bosh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

const (
	// maxListedTasks is the most tasks one listing returns. A caller that
	// pages by offset asks for offset plus page size, so this also bounds
	// how deep a page can start.
	plainTaskWindow = 200 // what the director's plain task read holds
	maxListedTasks  = 2000

	// maxTaskScan is the most tasks one listing asks the director for. A
	// listing that needs more than this to fill its page stops there and
	// says it is not exact.
	maxTaskScan = 5000

	// taskScanGrowth is how much a listing widens its request when the
	// team filter left too few tasks.
	taskScanGrowth = 4

	// maxListedEvents is the most events one listing reads from the
	// director, across its pages.
	maxListedEvents = 2000

	teamBlacksmith = "blacksmith"
)

// TaskListing is the answer to an exact task listing.
type TaskListing struct {
	// Tasks holds at most the requested number of matching tasks, newest
	// first.
	Tasks []Task
	// More is true when a matching task exists beyond Tasks. When Exact is
	// false it is true too, because the listing cannot rule one out.
	More bool
	// Exact is false when the listing stopped asking the director before it
	// could tell whether it had seen every match.
	Exact bool
}

// TaskLister lists tasks so that a state filter is exact. A director that
// does not implement it can still list tasks with GetTasks, whose state
// filter only sees the tasks the director returned in one window.
type TaskLister interface {
	ListTasks(ctx context.Context, taskType string, limit int, states []string, team string) (TaskListing, error)
}

// EventListing is the answer to a bounded event listing.
type EventListing struct {
	// Events holds at most the requested number of events, newest first.
	Events []Event
	// More is true when the director holds events beyond Events.
	More bool
}

// EventLister lists a deployment's events beyond the one page the director
// returns for a plain read.
type EventLister interface {
	ListEvents(ctx context.Context, deployment string, limit int) (EventListing, error)
}

var (
	_ TaskLister  = (*DirectorAdapter)(nil)
	_ TaskLister  = (*PooledDirector)(nil)
	_ TaskLister  = (*BatchDirector)(nil)
	_ EventLister = (*DirectorAdapter)(nil)
	_ EventLister = (*PooledDirector)(nil)
	_ EventLister = (*BatchDirector)(nil)
)

// ListTasks lists up to limit tasks that match the states and the team,
// newest first, and says whether more match beyond them. The states are
// sent to the director, so an error task behind any number of newer tasks is
// still found. The team filter runs here, so the request widens until it
// has enough matches, the director has no more tasks, or maxTaskScan is
// reached.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) ListTasks(ctx context.Context, taskType string, limit int, states []string, team string) (_ TaskListing, err error) {
	defer d.explainContext(ctx, "ListTasks", &err)

	if limit <= 0 {
		limit = 50
	}

	if limit > maxListedTasks {
		limit = maxListedTasks
	}

	need := limit + 1

	d.log.Debugf("Listing tasks (type: %s, limit: %d, states: %v, team: %s)", taskType, limit, states, team)

	scan := need
	if team != "" && team != teamBlacksmith {
		scan = need * taskFetchMultiplier
	}

	var matches []Task

	exact := true

	for {
		raw, exhausted, scanErr := d.scanTasks(ctx, taskType, scan, states)
		if scanErr != nil {
			return TaskListing{}, scanErr
		}

		matches = d.filterTasksByTeam(d.filterTasksByStates(raw, states), team)

		if len(matches) >= need || exhausted {
			break
		}

		if scan >= maxTaskScan {
			exact = false

			break
		}

		scan = min(scan*taskScanGrowth, maxTaskScan)
	}

	more := len(matches) > limit || !exact
	if len(matches) > limit {
		matches = matches[:limit]
	}

	return TaskListing{Tasks: matches, More: more, Exact: exact}, nil
}

// scanTasks reads up to scan tasks of the type from the director, with the
// states filtered by the director. exhausted is true when the director had no
// more tasks to give.
//
//nolint:nonamedreturns // the results are named to say which bool is which.
func (d *DirectorAdapter) scanTasks(ctx context.Context, taskType string, scan int, states []string) (tasks []Task, exhausted bool, err error) {
	if taskType == "current" {
		tasks, err = d.listTasks(ctx, "list running tasks", &boshapi.TaskListOptions{State: runningTaskStates, Verbose: 1})

		return tasks, true, err
	}

	state := strings.Join(states, ",")

	if taskType == "all" {
		var recent int

		tasks, recent, err = d.listAllTasksWith(ctx, scan, state)
		if err != nil {
			return nil, false, err
		}

		return tasks, recent < scan, nil
	}

	tasks, err = d.listTasks(ctx, "list recent tasks", &boshapi.TaskListOptions{State: state, Limit: scan, Verbose: 1})
	if err != nil {
		return nil, false, err
	}

	return tasks, len(tasks) < scan, nil
}

// ListTasks implements TaskLister. A listing is a read, so it takes no slot.
func (p *PooledDirector) ListTasks(ctx context.Context, taskType string, limit int, states []string, team string) (TaskListing, error) {
	return ListTasksFrom(ctx, p.director, taskType, limit, states, team)
}

// ListTasks implements TaskLister.
func (b *BatchDirector) ListTasks(ctx context.Context, taskType string, limit int, states []string, team string) (TaskListing, error) {
	return ListTasksFrom(ctx, b.director, taskType, limit, states, team)
}

// ListEvents implements EventLister.
func (p *PooledDirector) ListEvents(ctx context.Context, deployment string, limit int) (EventListing, error) {
	return ListEventsFrom(ctx, p.director, deployment, limit)
}

// ListEvents implements EventLister.
func (b *BatchDirector) ListEvents(ctx context.Context, deployment string, limit int) (EventListing, error) {
	return ListEventsFrom(ctx, b.director, deployment, limit)
}

// ListTasksFrom lists with the director when it can list exactly, and
// otherwise with its plain window, which is exact only when it returned
// fewer tasks than it was asked for or no state was asked for.
func ListTasksFrom(ctx context.Context, director Director, taskType string, limit int, states []string, team string) (TaskListing, error) {
	if lister, ok := director.(TaskLister); ok {
		listing, err := lister.ListTasks(ctx, taskType, limit, states, team)
		if err != nil {
			return TaskListing{}, fmt.Errorf("list tasks: %w", err)
		}

		return listing, nil
	}

	asked := min(max(limit, 1)+1, plainTaskWindow)

	tasks, err := director.GetTasks(ctx, taskType, asked, states, team)
	if err != nil {
		return TaskListing{}, fmt.Errorf("list tasks: %w", err)
	}

	listing := TaskListing{Tasks: tasks, More: len(tasks) >= asked, Exact: len(tasks) < asked || len(states) == 0}
	if len(listing.Tasks) > limit {
		listing.Tasks = listing.Tasks[:limit]
	}

	return listing, nil
}

// ListEventsFrom lists with the director when it can list past one page, and
// otherwise slices the one page its plain read returns.
func ListEventsFrom(ctx context.Context, director Director, deployment string, limit int) (EventListing, error) {
	if lister, ok := director.(EventLister); ok {
		listing, err := lister.ListEvents(ctx, deployment, limit)
		if err != nil {
			return EventListing{}, fmt.Errorf("list events: %w", err)
		}

		return listing, nil
	}

	events, err := director.GetEvents(ctx, deployment)
	if err != nil {
		return EventListing{}, fmt.Errorf("list events: %w", err)
	}

	listing := EventListing{Events: events}
	if len(events) > limit {
		listing.Events, listing.More = events[:limit], true
	}

	return listing, nil
}

// ListEvents lists up to limit of a deployment's events, newest first. The
// director returns one page per request, so the listing follows its
// before_id cursor until it holds enough or the director has no more.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) ListEvents(ctx context.Context, deployment string, limit int) (_ EventListing, err error) {
	defer d.explainContext(ctx, "ListEvents", &err)

	if limit <= 0 {
		limit = 100
	}

	if limit > maxListedEvents {
		limit = maxListedEvents
	}

	var events []Event

	beforeID := ""

	for len(events) <= limit {
		page, listErr := d.api.Events().List(ctx, &boshapi.EventListOptions{Deployment: deployment, BeforeID: beforeID})
		if listErr != nil {
			d.log.Errorf("Failed to list events for deployment %s: %v", deployment, listErr)

			return EventListing{}, d.requestFailure("list events for deployment "+deployment, listErr)
		}

		if len(page) == 0 {
			break
		}

		converted := convertAPIEvents(page)
		next := converted[len(converted)-1].ID

		events = append(events, converted...)

		if next == "" || next == beforeID {
			break
		}

		beforeID = next
	}

	listing := EventListing{Events: events}
	if len(events) > limit {
		listing.Events, listing.More = events[:limit], true
	}

	return listing, nil
}

// convertAPIEvents maps the director's events to ours.
func convertAPIEvents(directorEvents []boshapi.Event) []Event {
	events := make([]Event, 0, len(directorEvents))

	for _, dirEvent := range directorEvents {
		events = append(events, Event{
			ID:         dirEvent.ID,
			Time:       time.Unix(dirEvent.Timestamp, 0).UTC(),
			User:       dirEvent.User,
			Action:     dirEvent.Action,
			ObjectType: dirEvent.ObjectType,
			ObjectName: dirEvent.ObjectName,
			TaskID:     dirEvent.TaskID,
			Deployment: dirEvent.DeploymentName,
			Instance:   dirEvent.Instance,
			Context:    fmt.Sprintf("%v", dirEvent.Context),
			Error:      dirEvent.Error,
		})
	}

	return events
}
