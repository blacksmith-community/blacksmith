package broker_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/audit"
	"blacksmith/internal/broker"
)

func TestActorFromForwardedRequest(t *testing.T) {
	t.Parallel()

	var (
		seen  audit.Actor
		found bool
	)

	inner := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		seen, found = audit.ActorFrom(req.Context())
	})

	api := broker.API{Username: "admin", Password: "pw", Internal: inner, Primary: broker.NullHandler{}}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/upgrade/tasks", nil)
	req.SetBasicAuth("admin", "pw")
	req.RemoteAddr = "10.0.0.5:4000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	api.ServeHTTP(httptest.NewRecorder(), req)

	if !found || seen.Username != "admin" || seen.RemoteAddr != "10.0.0.5:4000" {
		t.Fatalf("inner handler saw %+v, found %v", seen, found)
	}

	if got := seen.From(); got != "10.0.0.5:4000 via 203.0.113.9" {
		t.Fatalf("From() = %q", got)
	}
}

// The availability route sits under /b/, so a request with no credentials is
// refused the way every other console route is, and never reaches the
// handler behind it.
func TestAvailabilityRouteNeedsTheBrokerCredential(t *testing.T) {
	t.Parallel()

	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	api := broker.API{Username: "admin", Password: "pw", Internal: inner, Primary: broker.NullHandler{}}

	refusal := func(target string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

		return recorder
	}

	known := refusal("/b/upgrade/tasks")
	got := refusal("/b/upgrade/commands/availability")

	if got.Code != http.StatusUnauthorized || got.Code != known.Code {
		t.Fatalf("availability answered %d, tasks answered %d, want 401 for both", got.Code, known.Code)
	}

	if got.Header().Get("WWW-Authenticate") != known.Header().Get("WWW-Authenticate") {
		t.Fatal("the two routes challenge differently")
	}

	if called {
		t.Fatal("an unauthenticated request reached the handler")
	}
}
