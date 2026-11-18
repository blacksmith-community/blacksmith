package broker

import (
	"context"
	"fmt"
	"slices"

	vaultPkg "blacksmith/pkg/vault"
)

// DeploymentNameBackfill reports what a run of BackfillDeploymentNames did.
type DeploymentNameBackfill struct {
	// Recorded lists the instances whose deployment name the run recorded.
	Recorded []string
	// Unconfirmed lists the instances that record no deployment name, and
	// whose deployment the director doesn't have under the name the entry
	// resolves to. The run leaves them as they are.
	Unconfirmed []string
}

// BackfillDeploymentNames records the deployment name of every index entry
// written before the broker recorded one. A deployment keeps the name it
// was created under for life, so after a plan change a name minted from
// the entry's plan would name a deployment that doesn't exist. Recording
// the name now freezes it before any plan change can move the plan.
//
// The run records only the name the entry resolves to today, which is the
// one minted from its plan, and only once the director confirms it has
// that deployment. It never records another deployment, even one of a
// legacy shape that ends with the instance ID, because that would move the
// instance off the deployment it resolves to. It writes only index
// entries, under the index lock, and never asks the director to change a
// deployment. It skips tombstones, entries that record a name already, and
// entries with no plan, so a second run changes nothing. When the director
// can't list its deployments, the run records nothing and returns the
// error, and the next boot tries again.
func (b *Broker) BackfillDeploymentNames(ctx context.Context) (DeploymentNameBackfill, error) {
	deployments, err := b.BOSH.GetDeployments(ctx)
	if err != nil {
		return DeploymentNameBackfill{}, fmt.Errorf("could not list the director's deployments to confirm the names to record: %w", err)
	}

	live := make(map[string]bool, len(deployments))
	for _, deployment := range deployments {
		live[deployment.Name] = true
	}

	var result DeploymentNameBackfill

	err = b.Vault.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		// The change can run again on a fresh read, so it starts over.
		result = DeploymentNameBackfill{}

		for instanceID, raw := range entries {
			minted, needsName := unnamedEntryDeployment(instanceID, raw)
			if !needsName {
				continue
			}

			if !live[minted] {
				result.Unconfirmed = append(result.Unconfirmed, instanceID)

				continue
			}

			fields, _ := raw.(map[string]interface{})
			fields["deployment_name"] = minted

			result.Recorded = append(result.Recorded, instanceID)
		}

		return len(result.Recorded) > 0, nil
	})
	if err != nil {
		return DeploymentNameBackfill{}, fmt.Errorf("could not record the deployment names of the index entries that lack one: %w", err)
	}

	slices.Sort(result.Recorded)
	slices.Sort(result.Unconfirmed)

	return result, nil
}

// unnamedEntryDeployment returns the deployment name an index entry that
// records none resolves to, and whether the entry needs one recorded. A
// tombstone, an entry that isn't a mapping, an entry that records a name
// already, and an entry with no plan need none.
func unnamedEntryDeployment(instanceID string, raw interface{}) (string, bool) {
	fields, isMap := raw.(map[string]interface{})
	if !isMap || vaultPkg.EntryIsTombstone(raw) {
		return "", false
	}

	if recorded, present := fields["deployment_name"]; present && recorded != nil && recorded != "" {
		return "", false
	}

	planID, _ := fields["plan_id"].(string)
	if planID == "" {
		return "", false
	}

	return vaultPkg.MintDeploymentName(planID, instanceID), true
}
