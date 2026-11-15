package blacksmith

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"blacksmith/pkg/http/response"
	"blacksmith/pkg/reconciler"
)

// UnclaimedReporter is what a reconciler offers when it can say which BOSH
// deployments have neither an index entry nor a Cloud Foundry service
// instance. The handler asks for it with a type assertion, so a reconciler
// without it still works.
type UnclaimedReporter interface {
	UnclaimedDeployments() reconciler.UnclaimedReport
}

// orphanEntry is one index entry the reconciler marked orphaned.
type orphanEntry struct {
	InstanceID     string `json:"instance_id"`
	InstanceName   string `json:"instance_name,omitempty"`
	DeploymentName string `json:"deployment_name,omitempty"`
	ServiceID      string `json:"service_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	OrphanedAt     string `json:"orphaned_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
	CheckCount     int    `json:"check_count"`
	// SecondsLeft is the time before the reconciler removes the entry. It is
	// null when the entry has no usable orphaned_at and no check count has
	// reached the limit.
	SecondsLeft *int64 `json:"seconds_left"`
	// Due means the removal is already owed and happens on the next run.
	Due bool `json:"due"`
}

// unclaimedEntry is one BOSH deployment with no index entry and no CF
// service instance.
type unclaimedEntry struct {
	InstanceID     string `json:"instance_id,omitempty"`
	DeploymentName string `json:"deployment_name"`
	FirstSeen      string `json:"first_seen,omitempty"`
}

// orphansResponse is the body of GET /b/orphans.
type orphansResponse struct {
	Orphans []orphanEntry `json:"orphans"`
	// Unclaimed lists deployments the reconciler leaves alone. UnclaimedKnown
	// is false until a run with complete CF discovery has happened, and an
	// empty list is only an answer when it is true.
	Unclaimed      []unclaimedEntry `json:"unclaimed"`
	UnclaimedKnown bool             `json:"unclaimed_known"`
	UnclaimedAsOf  string           `json:"unclaimed_as_of,omitempty"`
	// RemovalAfterSeconds is how long an entry stays orphaned before removal.
	RemovalAfterSeconds int64 `json:"removal_after_seconds"`
	Timestamp           int64 `json:"timestamp"`
}

// GetOrphans lists the index entries the reconciler marked orphaned, with the
// time left before it removes each, and the BOSH deployments that have no
// matching Cloud Foundry service instance. It has its own endpoint so the
// Summary can ask for it without the per-instance enrichment /b/status does.
func (h *Handler) GetOrphans(responseWriter http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		responseWriter.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(responseWriter, nil, ErrMethodNotAllowed)

		return
	}

	logger := h.logger.Named("blacksmith-orphans")
	now := time.Now()

	index := make(map[string]interface{})

	if h.vault == nil {
		response.HandleJSON(responseWriter, nil, ErrVaultUnavailable)

		return
	}

	_, err := h.vault.Get(req.Context(), "db", &index)
	if err != nil {
		logger.Error("Failed to read the instance index: %v", err)
		response.HandleJSON(responseWriter, nil, ErrOrphanIndexUnreadable)

		return
	}

	body := orphansResponse{
		Orphans:             orphansFromIndex(index, now),
		Unclaimed:           []unclaimedEntry{},
		RemovalAfterSeconds: int64(reconciler.OrphanRemovalAfter.Seconds()),
		Timestamp:           now.Unix(),
	}

	if reporter, ok := h.reconciler.(UnclaimedReporter); ok {
		report := reporter.UnclaimedDeployments()
		if report.Known {
			body.UnclaimedKnown = true
			body.UnclaimedAsOf = report.AsOf.UTC().Format(time.RFC3339)

			for _, deployment := range report.Deployments {
				body.Unclaimed = append(body.Unclaimed, unclaimedEntry{
					InstanceID:     deployment.InstanceID,
					DeploymentName: deployment.DeploymentName,
					FirstSeen:      deployment.FirstSeen.UTC().Format(time.RFC3339),
				})
			}
		}
	}

	response.HandleJSON(responseWriter, body, nil)
}

// orphansFromIndex picks the orphan-marked entries out of the index, soonest
// removal first. A tombstone is the vm-monitor's record of a deletion, not an
// orphan, and the reconciler's marking skips it too.
func orphansFromIndex(index map[string]interface{}, now time.Time) []orphanEntry {
	orphans := make([]orphanEntry, 0)

	for instanceID, raw := range index {
		data, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		if marked, _ := data["orphaned"].(bool); !marked {
			continue
		}

		if status, _ := data["status"].(string); status == reconciler.StatusDeleted {
			continue
		}

		if deleted, _ := data["deleted"].(bool); deleted {
			continue
		}

		orphans = append(orphans, buildOrphanEntry(instanceID, data, now))
	}

	sort.Slice(orphans, func(first, second int) bool {
		left, right := orphans[first].SecondsLeft, orphans[second].SecondsLeft

		switch {
		case left == nil && right != nil:
			return false
		case left != nil && right == nil:
			return true
		case left != nil && right != nil && *left != *right:
			return *left < *right
		}

		return orphans[first].InstanceID < orphans[second].InstanceID
	})

	return orphans
}

func buildOrphanEntry(instanceID string, data map[string]interface{}, now time.Time) orphanEntry {
	entry := orphanEntry{InstanceID: instanceID, CheckCount: orphanCheckCount(data["orphan_check_count"])}
	entry.InstanceName, _ = data["instance_name"].(string)
	entry.DeploymentName, _ = data["deployment_name"].(string)
	entry.ServiceID, _ = data["service_id"].(string)
	entry.PlanID, _ = data["plan_id"].(string)
	entry.Reason, _ = data["orphaned_reason"].(string)
	entry.OrphanedAt, _ = data["orphaned_at"].(string)

	var left *int64

	marked, err := time.Parse(time.RFC3339, entry.OrphanedAt)
	if err == nil {
		remaining := int64((reconciler.OrphanRemovalAfter - now.Sub(marked)).Seconds())
		if remaining < 0 {
			remaining = 0
		}

		left = &remaining
		entry.Due = remaining == 0
	}

	if entry.CheckCount >= reconciler.OrphanRemovalChecks {
		none := int64(0)
		left = &none
		entry.Due = true
	}

	entry.SecondsLeft = left

	return entry
}

// orphanCheckCount reads the count of runs that found the entry orphaned. The
// index holds it as a number that may come back as an int, a float, or a
// json.Number.
func orphanCheckCount(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed)
		}
	}

	return 0
}
