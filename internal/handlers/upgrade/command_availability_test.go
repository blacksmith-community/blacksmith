package upgrade_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/config"
	upgradehandler "blacksmith/internal/handlers/upgrade"
	"blacksmith/pkg/logger"
)

const availabilityTarget = "/b/upgrade/commands/availability"

// availabilityOf decodes the answer into a map, so a test sees exactly the
// keys the console would.
func availabilityOf(t *testing.T, cfg config.CommandsConfig) map[string]any {
	t.Helper()

	env := commandHandler(t, cfg)
	answer := send(t, env.handler, http.MethodGet, availabilityTarget, "", "")

	if answer.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", answer.Code, answer.Body.String())
	}

	var got map[string]any

	err := json.Unmarshal(answer.Body.Bytes(), &got)
	if err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	return got
}

func TestAvailabilityEnabledWithRoot(t *testing.T) {
	t.Parallel()

	got := availabilityOf(t, commandsOn("", true))
	if got["enabled"] != true || got["allow_root"] != true {
		t.Fatalf("expected enabled and allow_root, got %v", got)
	}
}

func TestAvailabilityEnabledWithRootOff(t *testing.T) {
	t.Parallel()

	got := availabilityOf(t, commandsOn("", false))
	if got["enabled"] != true || got["allow_root"] != false {
		t.Fatalf("expected enabled without root, got %v", got)
	}
}

func TestAvailabilityDisabled(t *testing.T) {
	t.Parallel()

	got := availabilityOf(t, commandsOff())
	if got["enabled"] != false || got["allow_root"] != false {
		t.Fatalf("expected both off, got %v", got)
	}
}

// The answer holds the two switches and the max in flight limit and
// nothing else, so it never hints at a configured command key or any other
// setting, and it answers the same with or without the key header.
func TestAvailabilityHasExactlyThreeKeys(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn(cmdKey, true))
	plain := send(t, env.handler, http.MethodGet, availabilityTarget, "", "")
	keyed := send(t, env.handler, http.MethodGet, availabilityTarget, "", "wrong-key")

	if plain.Body.String() != keyed.Body.String() || plain.Code != keyed.Code {
		t.Fatalf("the answer changed with the key header: %q vs %q", plain.Body.String(), keyed.Body.String())
	}

	var got map[string]any

	err := json.Unmarshal(plain.Body.Bytes(), &got)
	if err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if len(got) != 3 || got["enabled"] == nil || got["allow_root"] == nil || got["max_in_flight_limit"] == nil {
		t.Fatalf("expected exactly enabled, allow_root, and max_in_flight_limit, got %v", got)
	}

	if strings.Contains(plain.Body.String(), cmdKey) {
		t.Fatal("the answer carries the command key")
	}
}

// Only a read is allowed, and the route runs nothing.
func TestAvailabilityIsReadOnly(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", true))

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if got := send(t, env.handler, method, availabilityTarget, "{}", ""); got.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", method, got.Code)
		}
	}
}

// The answer reflects live configuration, so a cache between the console and
// the broker must never keep one.
func TestAvailabilityIsNotCached(t *testing.T) {
	t.Parallel()

	env := commandHandler(t, commandsOn("", true))
	answer := send(t, env.handler, http.MethodGet, availabilityTarget, "", "")

	if got := answer.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", got)
	}
}

// A handler built with no commands configuration has no authorizer to ask,
// and it reports both switches off through the whole chain.
func TestAvailabilityWithoutAnAuthorizerReportsBothOff(t *testing.T) {
	t.Parallel()

	handler := upgradehandler.NewHandler(upgradehandler.Dependencies{
		Logger:   &logger.NoOpLogger{},
		Director: &vmsDirector{},
		Vault:    newMemoryVault(),
	})
	t.Cleanup(handler.Stop)

	select {
	case <-handler.GetManager().Loaded():
	case <-time.After(loadTimeout):
		t.Fatal("the manager never finished loading the stored tasks")
	}

	answer := send(t, handler, http.MethodGet, availabilityTarget, "", "")
	if answer.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", answer.Code, answer.Body.String())
	}

	var got map[string]any

	err := json.Unmarshal(answer.Body.Bytes(), &got)
	if err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	if len(got) != 3 || got["enabled"] != false || got["allow_root"] != false {
		t.Fatalf("expected both off, got %v", got)
	}

	limit, present := got["max_in_flight_limit"]
	if !present || limit != float64(3) {
		t.Fatalf("expected a max_in_flight_limit of 3 from the default four workers, got %v (present %t)", limit, present)
	}
}

// The answer carries the most nodes a parallel run may use, which is one
// below the director's worker count, the same number a preview reports.
func TestAvailabilityReportsTheMaxInFlightLimit(t *testing.T) {
	t.Parallel()

	got := availabilityOf(t, commandsOn("", true))
	if got["max_in_flight_limit"] != float64(3) {
		t.Fatalf("expected a limit of 3 for the default four workers, got %v", got["max_in_flight_limit"])
	}
}
