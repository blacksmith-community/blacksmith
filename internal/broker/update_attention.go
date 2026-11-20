package broker

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"blacksmith/internal/interfaces"
	internalVault "blacksmith/internal/vault"
)

// updateAttentionStagedLost is the reason an update whose staged values
// can't be read back is listed in Attention required.
const updateAttentionStagedLost = "The update's values can't be read back from its record, so the stored values may not match what the deployment runs. Sending the update again records them"

// updateAttentionPlanMissing returns the reason an update whose plan has
// left the catalog is listed in Attention required.
func updateAttentionPlanMissing(planID string) string {
	return fmt.Sprintf("Plan %s is no longer in the catalog, so the update settled as failed, and every update of the instance is refused until an operator restores the plan", planID)
}

// UpdateAttention returns the updates the broker couldn't carry through,
// ordered by instance ID. The overview lists them in Attention required,
// because the tenant alone can't put them right.
func (b *Broker) UpdateAttention() []interfaces.UpdateAttention {
	var entries []interfaces.UpdateAttention

	b.updateAttention.Range(func(_, value any) bool {
		entry, ok := value.(interfaces.UpdateAttention)
		if ok {
			entries = append(entries, entry)
		}

		return true
	})

	slices.SortFunc(entries, func(a, b interfaces.UpdateAttention) int {
		return strings.Compare(a.InstanceID, b.InstanceID)
	})

	return entries
}

// noteUpdateAttention lists the update of instanceID in Attention
// required for reason. An entry already held for the same deployment and
// reason keeps the time it was first noted.
func (b *Broker) noteUpdateAttention(instanceID, deploymentName, reason string) {
	entry := interfaces.UpdateAttention{InstanceID: instanceID, DeploymentName: deploymentName, Reason: reason, Since: time.Now().UTC()}

	held, loaded := b.updateAttention.LoadOrStore(instanceID, entry)
	if !loaded {
		return
	}

	previous, ok := held.(interfaces.UpdateAttention)
	if ok && previous.DeploymentName == deploymentName && previous.Reason == reason {
		return
	}

	b.updateAttention.Store(instanceID, entry)
}

// forgetUpdateAttention drops the entry of instanceID, once the record no
// longer holds the update it was noted for.
func (b *Broker) forgetUpdateAttention(instanceID string) {
	b.updateAttention.Delete(instanceID)
}

// noteUnresumableUpdate lists the update of the phase in Attention
// required when re-adoption couldn't carry it through.
func (p *updatePhase) noteUnresumableUpdate() {
	switch {
	case p.stagedLost:
		p.broker.noteUpdateAttention(p.instanceID, p.target.deployment, updateAttentionStagedLost)
	case p.planMissing:
		p.broker.noteUpdateAttention(p.instanceID, p.target.deployment, updateAttentionPlanMissing(p.request.planID))
	}
}

// noteSettledUnresumableUpdates lists in Attention required each update
// whose record says it settled as failed while its staged values can't be
// read back or the plan they name is not in the catalog. Re-adoption
// skips a settled record, so this is how an entry noted before a restart
// is found again. It reads nothing from Vault.
func (b *Broker) noteSettledUnresumableUpdates(candidates []*readoptCandidate) {
	for _, candidate := range candidates {
		state := candidate.recorded.State
		if !candidate.parsed || !candidate.indexed || candidate.recorded.Action != operationTypeUpdate ||
			(state != recordedStateFailed && state != internalVault.TaskStateRollbackFailed) {
			continue
		}

		staged, err := stagedUpdate(candidate.record)
		if err != nil {
			b.noteUpdateAttention(candidate.instanceID, candidate.deploymentName, updateAttentionStagedLost)

			continue
		}

		serviceID, _ := candidate.entry["service_id"].(string)
		if _, found := b.Plans[serviceID+"/"+staged.PlanID]; !found {
			b.noteUpdateAttention(candidate.instanceID, candidate.deploymentName, updateAttentionPlanMissing(staged.PlanID))
		}
	}
}
