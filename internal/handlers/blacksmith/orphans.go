package blacksmith

import (
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
	// SecondsLeft is the estimated time before the reconciler removes the
	// entry, the earlier of the age limit and the check-count limit. It is
	// null when neither can be timed.
	SecondsLeft *int64 `json:"seconds_left"`
	// Due means the removal is already owed and happens on the next run.
	Due bool `json:"due"`
	// Basis names the limit behind the time left, "age" or "checks". The
	// time is an estimate, because the sweep runs once per reconciler
	// interval and the interval can change.
	Basis string `json:"basis,omitempty"`
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
		Orphans:             orphansFromIndex(index, now, h.reconcilerInterval()),
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

// reconcilerInterval is how often the reconciler runs now, or zero when there
// is no reconciler to ask.
func (h *Handler) reconcilerInterval() time.Duration {
	if h.reconciler == nil {
		return 0
	}

	return h.reconciler.GetInterval()
}

// orphansFromIndex picks the entries the sweep would remove or is waiting to
// remove, soonest first. It asks the reconciler's own rule, so a tombstone that
// is still marked orphaned is listed exactly when the sweep treats it as one.
func orphansFromIndex(index map[string]interface{}, now time.Time, interval time.Duration) []orphanEntry {
	orphans := make([]orphanEntry, 0)

	for instanceID, raw := range index {
		data, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		removal := reconciler.OrphanRemovalFor(data, now, interval)
		if !removal.Orphaned {
			continue
		}

		orphans = append(orphans, buildOrphanEntry(instanceID, data, removal))
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

func buildOrphanEntry(instanceID string, data map[string]interface{}, removal reconciler.OrphanRemoval) orphanEntry {
	entry := orphanEntry{InstanceID: instanceID, CheckCount: removal.Count, OrphanedAt: removal.OrphanedAt, Due: removal.Due, Basis: removal.Basis}
	entry.InstanceName, _ = data["instance_name"].(string)
	entry.DeploymentName, _ = data["deployment_name"].(string)
	entry.ServiceID, _ = data["service_id"].(string)
	entry.PlanID, _ = data["plan_id"].(string)
	entry.Reason, _ = data["orphaned_reason"].(string)

	if removal.Known {
		left := int64(removal.Remaining.Seconds())
		entry.SecondsLeft = &left
	}

	return entry
}
