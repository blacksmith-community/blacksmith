package broker

import (
	"slices"
	"strings"
	"time"

	"blacksmith/internal/interfaces"
)

// CleanupAttention returns the failed provisions whose owed cleanup the
// broker refused to run, ordered by instance ID. The overview shows them,
// so an operator can delete the deployment or clear the record by hand.
func (b *Broker) CleanupAttention() []interfaces.CleanupAttention {
	var entries []interfaces.CleanupAttention

	b.cleanupAttention.Range(func(_, value any) bool {
		entry, ok := value.(interfaces.CleanupAttention)
		if ok {
			entries = append(entries, entry)
		}

		return true
	})

	slices.SortFunc(entries, func(a, b interfaces.CleanupAttention) int {
		return strings.Compare(a.InstanceID, b.InstanceID)
	})

	return entries
}

// noteCleanupAttention records that the owed cleanup of instanceID was
// refused for reason, and reports whether the refusal is new. It is new
// unless an entry is already held for the same deployment and reason,
// and that entry keeps the time it was first noted.
func (b *Broker) noteCleanupAttention(instanceID, deploymentName, reason string) bool {
	entry := interfaces.CleanupAttention{InstanceID: instanceID, DeploymentName: deploymentName, Reason: reason, Since: time.Now().UTC()}

	held, loaded := b.cleanupAttention.LoadOrStore(instanceID, entry)
	if !loaded {
		return true
	}

	previous, ok := held.(interfaces.CleanupAttention)
	if ok && previous.DeploymentName == deploymentName && previous.Reason == reason {
		return false
	}

	b.cleanupAttention.Store(instanceID, entry)

	return true
}

// holdCleanupAttention records that the owed cleanup of instanceID was
// refused for reason only when no entry is held for it, and reports
// whether it recorded it. A refusal that may not hold on the next try,
// such as one because Vault could not be read, goes through it, so it
// never replaces the refusal an operator is already looking at.
func (b *Broker) holdCleanupAttention(instanceID, deploymentName, reason string) bool {
	entry := interfaces.CleanupAttention{InstanceID: instanceID, DeploymentName: deploymentName, Reason: reason, Since: time.Now().UTC()}

	_, loaded := b.cleanupAttention.LoadOrStore(instanceID, entry)

	return !loaded
}

// forgetCleanupAttention drops the entry of instanceID, once its cleanup
// may run or it owes none.
func (b *Broker) forgetCleanupAttention(instanceID string) {
	b.cleanupAttention.Delete(instanceID)
}

// deleteRefusalLead begins the reason of every entry that a refused
// delete from the platform notes, which tells it from a cleanup's.
const deleteRefusalLead = "The platform's delete of this instance was refused, "

// isDeleteRefusal reports whether entry was noted by a refused delete.
func isDeleteRefusal(entry interfaces.CleanupAttention) bool {
	return strings.HasPrefix(entry.Reason, deleteRefusalLead)
}

// forgetDeleteRefusals drops the entry of each of instanceIDs that a
// refused delete noted for deploymentName, and leaves every other entry,
// such as one a cleanup noted, or one noted after it was read.
func (b *Broker) forgetDeleteRefusals(deploymentName string, instanceIDs ...string) {
	for _, instanceID := range instanceIDs {
		held, loaded := b.cleanupAttention.Load(instanceID)
		if !loaded {
			continue
		}

		entry, ok := held.(interfaces.CleanupAttention)
		if ok && entry.DeploymentName == deploymentName && isDeleteRefusal(entry) {
			b.cleanupAttention.CompareAndDelete(instanceID, held)
		}
	}
}
