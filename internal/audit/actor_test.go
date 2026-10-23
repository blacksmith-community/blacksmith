package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/audit"
)

func TestActorFromRequestForwarded(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/x", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	actor := audit.ActorFromRequest(req, "admin")
	if actor.Username != "admin" || actor.RemoteAddr != "10.0.0.5:4000" || actor.ForwardedFor != "203.0.113.9" {
		t.Fatalf("unexpected actor %+v", actor)
	}

	if got := actor.From(); got != "10.0.0.5:4000 via 203.0.113.9" {
		t.Fatalf("From() = %q", got)
	}
}

func TestActorFromWithoutForwarding(t *testing.T) {
	t.Parallel()

	actor := audit.Actor{Username: "admin", RemoteAddr: "10.0.0.5:4000"}
	if got := actor.From(); got != "10.0.0.5:4000" {
		t.Fatalf("From() = %q", got)
	}
}

func TestActorForwardedForIsBoundedAndClean(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/x", nil)
	req.Header.Set("X-Forwarded-For", strings.Repeat("é", 300)+"\r\nforged=1")

	actor := audit.ActorFromRequest(req, "admin")
	if len(actor.ForwardedFor) > 256 || strings.ContainsAny(actor.ForwardedFor, "\r\n") {
		t.Fatalf("forwarded-for not bounded and clean: %d bytes", len(actor.ForwardedFor))
	}

	header := strings.Repeat("a", 10) + "\nb"
	req.Header.Set("X-Forwarded-For", header)

	if got := audit.ActorFromRequest(req, "admin").ForwardedFor; got != "aaaaaaaaaab" {
		t.Fatalf("control characters kept: %q", got)
	}
}

func TestActorContextRoundTrip(t *testing.T) {
	t.Parallel()

	if _, ok := audit.ActorFrom(context.Background()); ok {
		t.Fatal("an empty context must carry no actor")
	}

	want := audit.Actor{Username: "u", RemoteAddr: "r"}

	got, ok := audit.ActorFrom(audit.WithActor(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("got %+v, %v", got, ok)
	}
}

func TestActorJoinsEveryForwardedForHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/x", nil)
	req.RemoteAddr = "10.0.0.5:4000"
	req.Header.Add("X-Forwarded-For", "203.0.113.9")
	req.Header.Add("X-Forwarded-For", "198.51.100.2, 192.0.2.7")

	if got := audit.ActorFromRequest(req, "admin").ForwardedFor; got != "203.0.113.9, 198.51.100.2, 192.0.2.7" {
		t.Fatalf("expected every header line kept, got %q", got)
	}
}
