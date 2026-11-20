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

// updateAttentionRepairFailed is the reason an update that redeployed the
// stored values after lost ones, and didn't succeed, is listed in
// Attention required.
const updateAttentionRepairFailed = "An update that redeployed the stored values after an earlier update's values were lost didn't succeed, so the stored values may not match what the deployment runs. Sending the update again deploys them"

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
// required when re-adoption couldn't carry it through, or when it repairs
// lost values and settled as anything but succeeded. A plan that has left
// the catalog comes first, because every update is refused until an
// operator restores it, so sending the update again can't help. A
// rollback that restored the deployment leaves it on the stored values, so
// an update whose staged values were lost isn't listed then. A repair's
// rollback restores what the lost update may have deployed, so it is.
func (p *updatePhase) noteUnresumableUpdate() {
	switch {
	case p.planMissing:
		p.broker.noteUpdateAttention(p.instanceID, p.target.deployment, updateAttentionPlanMissing(p.request.planID))
	case p.repairsLostValues && !p.succeeded:
		p.broker.noteUpdateAttention(p.instanceID, p.target.deployment, updateAttentionRepairFailed)
	case p.stagedLost && p.restored:
	case p.stagedLost:
		p.broker.noteUpdateAttention(p.instanceID, p.target.deployment, updateAttentionStagedLost)
	}
}

// noteSettledUnresumableUpdates lists in Attention required each update
// whose record says it settled as failed while its staged values can't be
// read back or the plan they name is not in the catalog. Re-adoption skips
// a settled record, so this is how an entry noted before a restart is found
// again. A plan missing from the catalog comes before the other reasons. It
// reads nothing from Vault.
func (b *Broker) noteSettledUnresumableUpdates(candidates []*readoptCandidate) {
	for _, candidate := range candidates {
		if !candidate.parsed || !candidate.indexed || candidate.recorded.Action != operationTypeUpdate {
			continue
		}

		state := candidate.recorded.State
		settled := state == recordedStateFailed || state == internalVault.TaskStateRollbackFailed

		staged, err := stagedUpdate(candidate.record)
		if err == nil && settled {
			serviceID, _ := candidate.entry["service_id"].(string)
			if _, found := b.Plans[serviceID+"/"+staged.PlanID]; !found {
				b.noteUpdateAttention(candidate.instanceID, candidate.deploymentName, updateAttentionPlanMissing(staged.PlanID))

				continue
			}
		}

		reason := lostValuesReason(candidate.record)
		if reason != "" {
			b.noteUpdateAttention(candidate.instanceID, candidate.deploymentName, reason)
		}
	}
}

// rollbackRestored reports whether the update record holds a rollback that
// restored the deployment, which leaves the deployment on the instance's
// stored values.
func rollbackRestored(record map[string]interface{}) bool {
	rollback := internalVault.StagedRollback(record)

	return rollback != nil && rollback.Phase == internalVault.UpdatePhaseRolledBack
}

// stagedValuesLost reports whether the stored values may not match what
// the deployment runs, as lostValuesReason says.
func stagedValuesLost(record map[string]interface{}) bool {
	return lostValuesReason(record) != ""
}

// lostValuesReason returns the Attention required reason of a task record
// that holds a settled update after which the stored values may not match
// what the deployment runs, or "" when it holds none. That is an update
// whose staged values can't be read back and that no rollback restored,
// because the lost update's deploy may have finished. It is also an update
// that repaired lost values and failed, whatever its rollback did, because
// the rollback restores what the lost update may have deployed.
func lostValuesReason(record map[string]interface{}) string {
	action, _ := record["action"].(string)
	state, _ := record["state"].(string)

	if action != operationTypeUpdate || (state != recordedStateFailed && state != internalVault.TaskStateRollbackFailed) {
		return ""
	}

	staged, err := stagedUpdate(record)

	switch {
	case err != nil && !rollbackRestored(record):
		return updateAttentionStagedLost
	case err == nil && staged.RepairsLostValues:
		return updateAttentionRepairFailed
	}

	return ""
}
