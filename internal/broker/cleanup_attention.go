package broker

import (
	"slices"
	"strings"
	"time"

	"blacksmith/internal/interfaces"
)

// attentionKind says what noted an entry of the overview.
type attentionKind int

const (
	// cleanupRefusal is an entry that a refused cleanup noted.
	cleanupRefusal attentionKind = iota
	// deleteRefusal is an entry that a refused delete from the platform
	// noted.
	deleteRefusal
)

// attentionRow is what cleanupAttention holds for an instance. It is
// the entry the overview shows, along with what noted it, which the
// overview never shows.
type attentionRow struct {
	interfaces.CleanupAttention

	kind attentionKind
}

// CleanupAttention returns the failed provisions whose owed cleanup the
// broker refused to run, ordered by instance ID. The overview shows them,
// so an operator can delete the deployment or clear the record by hand.
func (b *Broker) CleanupAttention() []interfaces.CleanupAttention {
	rows := b.attentionRows()
	if len(rows) == 0 {
		return nil
	}

	entries := make([]interfaces.CleanupAttention, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, row.CleanupAttention)
	}

	return entries
}

// attentionRows returns the rows held for the overview, ordered by
// instance ID.
func (b *Broker) attentionRows() []attentionRow {
	var rows []attentionRow

	b.cleanupAttention.Range(func(_, value any) bool {
		row, ok := value.(attentionRow)
		if ok {
			rows = append(rows, row)
		}

		return true
	})

	slices.SortFunc(rows, func(a, b attentionRow) int {
		return strings.Compare(a.InstanceID, b.InstanceID)
	})

	return rows
}

// newAttentionRow returns a row of kind for the overview, noted now.
func newAttentionRow(kind attentionKind, instanceID, deploymentName, reason string) attentionRow {
	return attentionRow{
		CleanupAttention: interfaces.CleanupAttention{InstanceID: instanceID, DeploymentName: deploymentName, Reason: reason, Since: time.Now().UTC()},
		kind:             kind,
	}
}

// noteCleanupAttention records that the owed cleanup of instanceID was
// refused for reason, and reports whether the refusal is new. It is new
// unless an entry is already held for the same deployment and reason,
// and that entry keeps the time it was first noted.
func (b *Broker) noteCleanupAttention(instanceID, deploymentName, reason string) bool {
	return b.noteAttention(newAttentionRow(cleanupRefusal, instanceID, deploymentName, reason))
}

// holdCleanupAttention records that the owed cleanup of instanceID was
// refused for reason only when no entry is held for it, and reports
// whether it recorded it. A refusal that may not hold on the next try,
// such as one because Vault could not be read, goes through it, so it
// never replaces the refusal an operator is already looking at.
func (b *Broker) holdCleanupAttention(instanceID, deploymentName, reason string) bool {
	return b.holdAttention(newAttentionRow(cleanupRefusal, instanceID, deploymentName, reason))
}

// noteDeleteRefusal records that the platform's delete of instanceID was
// refused for reason, as noteCleanupAttention records a cleanup's.
func (b *Broker) noteDeleteRefusal(instanceID, deploymentName, reason string) bool {
	return b.noteAttention(newAttentionRow(deleteRefusal, instanceID, deploymentName, reason))
}

// holdDeleteRefusal records that the platform's delete of instanceID was
// refused for reason only when no entry is held for it, as
// holdCleanupAttention records a cleanup's.
func (b *Broker) holdDeleteRefusal(instanceID, deploymentName, reason string) bool {
	return b.holdAttention(newAttentionRow(deleteRefusal, instanceID, deploymentName, reason))
}

// noteAttention holds row for its instance, and reports whether it is
// new. It is new unless a row of the same kind is already held for the
// same deployment and reason, and that row keeps its time.
func (b *Broker) noteAttention(row attentionRow) bool {
	held, loaded := b.cleanupAttention.LoadOrStore(row.InstanceID, row)
	if !loaded {
		return true
	}

	previous, ok := held.(attentionRow)
	if ok && previous.kind == row.kind && previous.DeploymentName == row.DeploymentName && previous.Reason == row.Reason {
		return false
	}

	b.cleanupAttention.Store(row.InstanceID, row)

	return true
}

// holdAttention holds row for its instance only when no row is held for
// it, and reports whether it held it.
func (b *Broker) holdAttention(row attentionRow) bool {
	_, loaded := b.cleanupAttention.LoadOrStore(row.InstanceID, row)

	return !loaded
}

// forgetCleanupAttention drops the entry of instanceID, once its cleanup
// may run or it owes none.
func (b *Broker) forgetCleanupAttention(instanceID string) {
	b.cleanupAttention.Delete(instanceID)
}

// forgetAttentionRow drops row only while it is still the row held for
// its instance, so a row noted in its place after it was read stays.
func (b *Broker) forgetAttentionRow(row attentionRow) {
	b.cleanupAttention.CompareAndDelete(row.InstanceID, row)
}

// isDeleteRefusal reports whether row was noted by a refused delete.
func isDeleteRefusal(row attentionRow) bool {
	return row.kind == deleteRefusal
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

		row, ok := held.(attentionRow)
		if ok && row.DeploymentName == deploymentName && isDeleteRefusal(row) {
			b.cleanupAttention.CompareAndDelete(instanceID, held)
		}
	}
}
