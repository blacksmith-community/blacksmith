package broker_test

import (
	"slices"
	"testing"

	"blacksmith/internal/broker"
)

// The claim check reads the record of each instance a name could have been
// minted for, which is each part of the name after a dash whose head is
// shaped like a plan ID. A part that can't be an instance's folder in
// Vault, such as one of the broker's own state folders or a path, is left
// out.
func TestClaimCandidates_AreTheIDsTheNameEndsWith(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want []string
	}{
		{name: "redis-small-x-abc", want: []string{longerClaimant, "x-" + collidingOwner, collidingOwner}},
		{name: "standalone-5e0a3b4c-1d2e-4f60-8a9b-0c1d2e3f4a5b", want: []string{
			"5e0a3b4c-1d2e-4f60-8a9b-0c1d2e3f4a5b", "1d2e-4f60-8a9b-0c1d2e3f4a5b", "4f60-8a9b-0c1d2e3f4a5b", "8a9b-0c1d2e3f4a5b", "0c1d2e3f4a5b",
		}},
		{name: standalonePlanID},
		{name: "redis-"},
		{name: "-abc"},
		{name: "redis-secret"},
		{name: "redis-blacksmith"},
		{name: "redis-upgrade-tasks", want: []string{"tasks"}},
		{name: "redis-a/b"},
		{name: "redis-.."},
	}

	for _, entry := range cases {
		if got := broker.ClaimCandidates(entry.name); !slices.Equal(got, entry.want) {
			t.Errorf("the candidates for %s are %q, want %q", entry.name, got, entry.want)
		}
	}
}
