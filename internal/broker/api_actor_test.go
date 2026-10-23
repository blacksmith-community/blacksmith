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
