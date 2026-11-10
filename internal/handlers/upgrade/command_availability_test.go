package upgrade_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/config"
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

// The answer holds the two switches and nothing else, so it never hints at
// a configured command key or any other setting, and it answers the same
// with or without the key header.
func TestAvailabilityHasExactlyTwoKeys(t *testing.T) {
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

	if len(got) != 2 || got["enabled"] == nil || got["allow_root"] == nil {
		t.Fatalf("expected exactly enabled and allow_root, got %v", got)
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
