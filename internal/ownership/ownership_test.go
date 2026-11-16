package ownership_test

import (
	"context"
	"errors"
	"testing"

	"blacksmith/internal/ownership"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	valkeyService = "valkey"
	goneID        = "gone"
	liveID        = "live"
)

var errDown = errors.New("vault is down")

type brokenStore struct{}

func (brokenStore) Get(context.Context, string, interface{}) (bool, error) {
	return false, errDown
}

func TestValidIDRefusesIDsThatCouldReachOtherPaths(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"abc-123":   true,
		"":          false,
		"a/b":       false,
		"/a":        false,
		"a/":        false,
		"a//b":      false,
		"..":        false,
		"a..b":      false,
		"../secret": false,
		`a\b`:       false,
	}

	for id, want := range cases {
		if got := ownership.ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %t, want %t", id, got, want)
		}
	}
}

func TestLiveInstanceSeparatesLiveFromGone(t *testing.T) {
	t.Parallel()

	store := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		liveID: {ServiceID: valkeyService, PlanID: "small"},
		goneID: {Status: vaultPkg.StatusDeleted},
	}}

	instance, err := ownership.LiveInstance(t.Context(), store, liveID)
	if err != nil || instance == nil || instance.ServiceID != valkeyService {
		t.Fatalf("a live instance = %v, %v", instance, err)
	}

	for instanceID, want := range map[string]error{
		goneID:    ownership.ErrNotLive,
		"unknown": ownership.ErrNotLive,
		"a/b":     ownership.ErrInvalidID,
		"":        ownership.ErrInvalidID,
	} {
		_, err = ownership.LiveInstance(t.Context(), store, instanceID)
		if !errors.Is(err, want) {
			t.Errorf("LiveInstance(%q) error = %v, want %v", instanceID, err, want)
		}
	}
}

func TestLiveInstanceReportsAnUnreadableIndex(t *testing.T) {
	t.Parallel()

	_, err := ownership.LiveInstance(t.Context(), brokenStore{}, liveID)
	if !errors.Is(err, errDown) || errors.Is(err, ownership.ErrNotLive) {
		t.Fatalf("an unreadable index error = %v", err)
	}
}

func TestManagedDeploymentSkipsTombstones(t *testing.T) {
	t.Parallel()

	store := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		liveID: {ServiceID: valkeyService, PlanID: "small", DeploymentName: "small-live"},
		goneID: {Status: vaultPkg.StatusDeleted, DeploymentName: "small-gone"},
	}}

	for name, want := range map[string]bool{"small-live": true, "small-gone": false, "other": false} {
		got, err := ownership.ManagedDeployment(t.Context(), store, name)
		if err != nil || got != want {
			t.Errorf("ManagedDeployment(%q) = %t, %v, want %t", name, got, err, want)
		}
	}
}

// A deployment is retired when only a tombstone of the index records it.
func TestRetiredDeploymentFindsOnlyTombstones(t *testing.T) {
	t.Parallel()

	store := &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		liveID: {ID: liveID, ServiceID: valkeyService, PlanID: valkeyService, DeploymentName: "dep-live"},
		goneID: {ID: goneID, ServiceID: valkeyService, PlanID: valkeyService, DeploymentName: "dep-gone", Status: vaultPkg.StatusDeleted},
	}}

	for name, want := range map[string]bool{"dep-gone": true, "dep-live": false, "dep-never": false} {
		got, err := ownership.RetiredDeployment(t.Context(), store, name)
		if err != nil || got != want {
			t.Errorf("RetiredDeployment(%q) = %t, %v, want %t", name, got, err, want)
		}
	}
}
