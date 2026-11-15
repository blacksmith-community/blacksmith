package blacksmith_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
)

var errOrphanVault = errors.New("vault unreachable")

// orphanVault serves one "db" index and nothing else.
type orphanVault struct {
	interfaces.Vault

	index map[string]interface{}
	err   error
}

func (v *orphanVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	if v.err != nil {
		return false, v.err
	}

	if path != "db" {
		return false, nil
	}

	raw, err := json.Marshal(v.index)
	if err != nil {
		return false, fmt.Errorf("marshal index: %w", err)
	}

	err = json.Unmarshal(raw, result)
	if err != nil {
		return false, fmt.Errorf("unmarshal index: %w", err)
	}

	return true, nil
}

// unclaimedReconciler reports a fixed set of unclaimed deployments.
type unclaimedReconciler struct {
	intervalRecorder

	report reconciler.UnclaimedReport
}

func (r *unclaimedReconciler) UnclaimedDeployments() reconciler.UnclaimedReport { return r.report }

type orphanBody struct {
	Orphans []struct {
		InstanceID     string `json:"instance_id"`
		InstanceName   string `json:"instance_name"`
		DeploymentName string `json:"deployment_name"`
		OrphanedAt     string `json:"orphaned_at"`
		Reason         string `json:"reason"`
		SecondsLeft    *int64 `json:"seconds_left"`
		Basis          string `json:"basis"`
		Due            bool   `json:"due"`
	} `json:"orphans"`
	Unclaimed []struct {
		InstanceID     string `json:"instance_id"`
		DeploymentName string `json:"deployment_name"`
		FirstSeen      string `json:"first_seen"`
	} `json:"unclaimed"`
	UnclaimedKnown bool  `json:"unclaimed_known"`
	RemovalAfter   int64 `json:"removal_after_seconds"`
}

func getOrphans(t *testing.T, deps handlerpkg.Dependencies, method string) (*httptest.ResponseRecorder, orphanBody) {
	t.Helper()

	deps.Logger = logger.Get().Named("test")
	deps.Config = createTestConfig()

	handler := handlerpkg.NewHandler(deps)
	request := httptest.NewRequestWithContext(t.Context(), method, "/b/orphans", nil)
	response := httptest.NewRecorder()

	handler.GetOrphans(response, request)

	var body orphanBody

	_ = json.Unmarshal(response.Body.Bytes(), &body)

	return response, body
}

const (
	orphanTestStatus   = "status"
	orphanTestCount    = "orphan_check_count"
	orphanTestDeleted  = "deleted"
	orphanTestReason   = "no_bosh_deployment"
	orphanTestPlan     = "plan_id"
	orphanTestPlanName = "standalone"
	orphanTestMarked   = "orphaned"
)

// orphanData builds an index entry marked orphaned the given time ago.
func orphanData(ago time.Duration, extra map[string]interface{}) map[string]interface{} {
	data := map[string]interface{}{
		"service_id": "valkey", orphanTestPlan: orphanTestPlanName, orphanTestMarked: true,
		"orphaned_at": time.Now().Add(-ago).Format(time.RFC3339),
	}
	for key, value := range extra {
		data[key] = value
	}

	return data
}

func TestGetOrphansListsMarkedEntriesWithTimeLeft(t *testing.T) {
	t.Parallel()

	vault := &orphanVault{index: map[string]interface{}{
		"older": orphanData(20*time.Hour, map[string]interface{}{
			"instance_name": "cache-a", "deployment_name": "valkey-standalone-older", "orphaned_reason": orphanTestReason,
		}),
		"newer":     orphanData(time.Hour, nil),
		"checked":   orphanData(2*time.Hour, map[string]interface{}{orphanTestCount: 24}),
		"expired":   orphanData(30*time.Hour, nil),
		"healthy":   orphanData(time.Hour, map[string]interface{}{orphanTestMarked: false}),
		"tombstone": orphanData(5*time.Hour, map[string]interface{}{orphanTestStatus: orphanTestDeleted}),
	}}

	response, body := getOrphans(t, handlerpkg.Dependencies{Vault: vault}, http.MethodGet)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}

	if body.RemovalAfter != int64((24 * time.Hour).Seconds()) {
		t.Errorf("removal_after_seconds = %d", body.RemovalAfter)
	}

	// Soonest removal first, and the entries already due ahead of the rest.
	wantOrder := []string{"checked", "expired", "older", "tombstone", "newer"}
	if got := orphanIDs(body); strings.Join(got, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("order = %v, want %v", got, wantOrder)
	}

	older := body.Orphans[2]
	if older.InstanceName != "cache-a" || older.DeploymentName != "valkey-standalone-older" || older.Reason != orphanTestReason {
		t.Errorf("older = %+v", older)
	}

	if older.SecondsLeft == nil || *older.SecondsLeft < 3*3600-60 || *older.SecondsLeft > 4*3600 {
		t.Errorf("older seconds left = %v, want about 4 hours", older.SecondsLeft)
	}

	for position, due := range []bool{true, true, false, false, false} {
		if body.Orphans[position].Due != due {
			t.Errorf("%s due = %v, want %v", body.Orphans[position].InstanceID, body.Orphans[position].Due, due)
		}
	}

	if left := body.Orphans[1].SecondsLeft; left == nil || *left != 0 {
		t.Errorf("an expired entry has %v seconds left, want 0", left)
	}
}

func orphanIDs(body orphanBody) []string {
	ids := make([]string, 0, len(body.Orphans))
	for _, orphan := range body.Orphans {
		ids = append(ids, orphan.InstanceID)
	}

	return ids
}

func TestGetOrphansListsUnclaimedDeployments(t *testing.T) {
	t.Parallel()

	seen := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	source := &unclaimedReconciler{report: reconciler.UnclaimedReport{
		Known: true,
		AsOf:  time.Now(),
		Deployments: []reconciler.UnclaimedDeployment{
			{InstanceID: "stray", DeploymentName: "valkey-standalone-stray", FirstSeen: seen},
		},
	}}

	_, body := getOrphans(t, handlerpkg.Dependencies{Vault: &orphanVault{}, Reconciler: source}, http.MethodGet)

	if !body.UnclaimedKnown || len(body.Unclaimed) != 1 {
		t.Fatalf("unclaimed = %+v known=%v", body.Unclaimed, body.UnclaimedKnown)
	}

	if got := body.Unclaimed[0]; got.InstanceID != "stray" || got.DeploymentName != "valkey-standalone-stray" || got.FirstSeen != seen.Format(time.RFC3339) {
		t.Errorf("unclaimed = %+v", got)
	}
}

// An entry the sweep removes is listed even when it is also a tombstone, since
// a tombstone keeps its orphaned mark and the sweep does not skip it.
func TestGetOrphansListsTombstonesTheSweepRemoves(t *testing.T) {
	t.Parallel()

	vault := &orphanVault{index: map[string]interface{}{
		"gone": orphanData(30*time.Hour, map[string]interface{}{orphanTestStatus: orphanTestDeleted}),
		"flag": orphanData(2*time.Hour, map[string]interface{}{orphanTestDeleted: true}),
	}}

	_, body := getOrphans(t, handlerpkg.Dependencies{Vault: vault}, http.MethodGet)

	if got := strings.Join(orphanIDs(body), ","); got != "gone,flag" {
		t.Fatalf("listed %q, want gone,flag", got)
	}

	if !body.Orphans[0].Due || body.Orphans[1].Due {
		t.Errorf("due flags = %v,%v, want true,false", body.Orphans[0].Due, body.Orphans[1].Due)
	}
}

// The check-count limit can come before the clock. At a five minute interval
// an entry with 22 checks is two runs from the limit, long before 24 hours.
func TestGetOrphansReportsTheEarlierOfTheTwoLimits(t *testing.T) {
	t.Parallel()

	vault := &orphanVault{index: map[string]interface{}{
		"by-checks": orphanData(10*time.Minute, map[string]interface{}{orphanTestCount: 22}),
		"by-clock":  orphanData(23*time.Hour, map[string]interface{}{orphanTestCount: 1}),
		// A tombstone's count stops growing, so only the clock counts for it.
		"tomb": orphanData(time.Hour, map[string]interface{}{orphanTestCount: 22, orphanTestStatus: orphanTestDeleted}),
	}}

	source := &unclaimedReconciler{}
	source.interval = 5 * time.Minute

	_, body := getOrphans(t, handlerpkg.Dependencies{Vault: vault, Reconciler: source}, http.MethodGet)

	found := map[string]int{}
	for position, orphan := range body.Orphans {
		found[orphan.InstanceID] = position
	}

	checks := body.Orphans[found["by-checks"]]
	if checks.Basis != "checks" || checks.SecondsLeft == nil || *checks.SecondsLeft != 600 {
		t.Errorf("by-checks = basis %q left %v, want checks and 600", checks.Basis, checks.SecondsLeft)
	}

	clock := body.Orphans[found["by-clock"]]
	if clock.Basis != "age" || clock.SecondsLeft == nil || *clock.SecondsLeft < 3600-60 || *clock.SecondsLeft > 3600 {
		t.Errorf("by-clock = basis %q left %v, want age and about 3600", clock.Basis, clock.SecondsLeft)
	}

	tomb := body.Orphans[found["tomb"]]
	if tomb.Basis != "age" || tomb.SecondsLeft == nil || *tomb.SecondsLeft < 23*3600-60 {
		t.Errorf("tomb = basis %q left %v, want age and about 23 hours", tomb.Basis, tomb.SecondsLeft)
	}
}

// With no reconciler, or one that has not completed a run, the answer says it
// does not know instead of reporting none.
func TestGetOrphansSaysWhenUnclaimedIsUnknown(t *testing.T) {
	t.Parallel()

	_, none := getOrphans(t, handlerpkg.Dependencies{Vault: &orphanVault{}}, http.MethodGet)
	if none.UnclaimedKnown {
		t.Error("unclaimed reported known without a reconciler")
	}

	pending := &unclaimedReconciler{}

	_, notRun := getOrphans(t, handlerpkg.Dependencies{Vault: &orphanVault{}, Reconciler: pending}, http.MethodGet)
	if notRun.UnclaimedKnown {
		t.Error("unclaimed reported known before a complete run")
	}
}

func TestGetOrphansFailsWhenTheIndexCannotBeRead(t *testing.T) {
	t.Parallel()

	response, _ := getOrphans(t, handlerpkg.Dependencies{Vault: &orphanVault{err: errOrphanVault}}, http.MethodGet)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", response.Code, response.Body.String())
	}

	if strings.Contains(response.Body.String(), `"orphans"`) {
		t.Errorf("an unreadable index must not look like an empty list: %s", response.Body.String())
	}
}

func TestGetOrphansRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	response, _ := getOrphans(t, handlerpkg.Dependencies{Vault: &orphanVault{}}, http.MethodPost)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", response.Code)
	}
}
