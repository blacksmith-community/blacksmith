package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// eventsDirector holds total events, newest first, and lists them as the
// director adapter does.
type eventsDirector struct {
	*testutil.MockBOSHDirector

	total int
	reads int
}

func newestEvents(total, count int) []bosh.Event {
	out := make([]bosh.Event, 0, count)

	for id := total; id >= 1 && len(out) < count; id-- {
		out = append(out, bosh.Event{ID: strconv.Itoa(id), Action: "update", ObjectType: "deployment", ObjectName: logsDeployment})
	}

	return out
}

// GetEvents is the plain read, which holds one director page of 200.
func (d *eventsDirector) GetEvents(_ context.Context, _ string) ([]bosh.Event, error) {
	d.reads++

	return newestEvents(d.total, 200), nil
}

func (d *eventsDirector) ListEvents(_ context.Context, _ string, limit int) (bosh.EventListing, error) {
	listing := bosh.EventListing{Events: newestEvents(d.total, limit)}
	listing.More = d.total > len(listing.Events)

	return listing, nil
}

func eventsPage(t *testing.T, director *eventsDirector, target string) *httptest.ResponseRecorder {
	t.Helper()

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})
	recorder := httptest.NewRecorder()

	handler.GetInstanceEvents(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil), logsInstanceID)

	return recorder
}

// A request with no paging parameters reads the director's one page and
// answers a bare array, as before.
func TestInstanceEventsWithoutPagingStaysTheBareArray(t *testing.T) {
	t.Parallel()

	director := &eventsDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), total: 500}
	recorder := eventsPage(t, director, "/b/"+logsInstanceID+"/events")

	var events []bosh.Event

	err := json.Unmarshal(recorder.Body.Bytes(), &events)
	if err != nil || len(events) != 200 || director.reads != 1 {
		t.Fatalf("bare read: %d events, %d plain reads, err %v", len(events), director.reads, err)
	}
}

// The object form pages by offset and says whether another page exists.
func TestInstanceEventsPageByOffset(t *testing.T) {
	t.Parallel()

	director := &eventsDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), total: 250}

	type page struct {
		Events  []bosh.Event `json:"events"`
		Offset  int          `json:"offset"`
		Limit   int          `json:"limit"`
		HasMore bool         `json:"has_more"`
	}

	read := func(target string) page {
		recorder := eventsPage(t, director, target)

		var got page

		err := json.Unmarshal(recorder.Body.Bytes(), &got)
		if err != nil {
			t.Fatalf("decode %q: %v", recorder.Body.String(), err)
		}

		return got
	}

	first := read("/b/" + logsInstanceID + "/events?paged=1&limit=100")
	if len(first.Events) != 100 || first.Events[0].ID != "250" || !first.HasMore || first.Offset != 0 {
		t.Fatalf("first page: %d events, has_more %v", len(first.Events), first.HasMore)
	}

	third := read("/b/" + logsInstanceID + "/events?paged=1&limit=100&offset=200")
	if len(third.Events) != 50 || third.Events[0].ID != "50" || third.HasMore || third.Offset != 200 {
		t.Fatalf("third page: %d events, has_more %v", len(third.Events), third.HasMore)
	}

	if director.reads != 0 {
		t.Errorf("a paged read must not use the plain one-page read, it used it %d times", director.reads)
	}
}

// A bare request that asks for a limit pages too, and says so in a header.
func TestInstanceEventsBareFormCarriesHasMoreInAHeader(t *testing.T) {
	t.Parallel()

	director := &eventsDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), total: 30}
	recorder := eventsPage(t, director, "/b/"+logsInstanceID+"/events?limit=10&offset=10")

	var events []bosh.Event

	err := json.Unmarshal(recorder.Body.Bytes(), &events)
	if err != nil || len(events) != 10 || events[0].ID != "20" {
		t.Fatalf("bare page: %d events, err %v", len(events), err)
	}

	if recorder.Header().Get("X-Blacksmith-Has-More") != "true" {
		t.Errorf("X-Blacksmith-Has-More = %q, want true", recorder.Header().Get("X-Blacksmith-Has-More"))
	}
}
