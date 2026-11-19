package broker

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// The broker names a deployment by joining the plan ID and the instance
// ID with a dash, so two instances can be given the same name. Instance
// abc under plan redis-small-x and instance x-abc under plan redis-small
// are both redis-small-x-abc. A cleanup owed for one of them would then
// delete the other's deployment, so a provision refuses a name that
// another instance already claims, before it writes anything.

// refuseProvisionOverClaimedName refuses the provision of instanceID with
// 400 Bad Request when another instance already claims the deployment
// name the provision would mint. An instance claims a name when its index
// entry records it or mints it under the entry's plan, and when its record
// owes the cleanup of a failed provision and names it, or names nothing
// while request details are stored under it. A deleted instance's entry
// still claims its name, because its delete may not have finished. Only
// instances whose IDs can mint the name have their records read. A read
// that fails refuses the provision too, because a claim it would have
// found cannot be ruled out.
func (b *Broker) refuseProvisionOverClaimedName(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) error {
	requested := vaultPkg.MintDeploymentName(details.PlanID, instanceID)

	readCtx, cancel := recordContext(ctx)
	defer cancel()

	claimant, where, err := b.nameClaimant(readCtx, instanceID, requested)
	if err != nil {
		logger.Error("Refusing provision of instance %s: %s", instanceID, err)

		return fmt.Errorf("could not check whether another instance claims deployment %s, so the provision of instance %s was refused: %w", requested, instanceID, err)
	}

	if claimant != "" {
		return refuseClaimedName(instanceID, requested, claimant, where, logger)
	}

	return nil
}

// nameClaimant returns the first instance other than instanceID that
// claims deploymentName, and says where it claims it, or returns "" when
// no instance does. It reads the index, then lists the instances in Vault
// and reads the record of each one whose ID can mint the name. A read
// that fails returns its error, because a claim it would have found
// cannot be ruled out.
func (b *Broker) nameClaimant(ctx context.Context, instanceID, deploymentName string) (string, string, error) {
	idx, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		return "", "", fmt.Errorf("could not read the index to check whether another instance claims deployment %s: %w", deploymentName, err)
	}

	if claimant := indexClaimant(idx, instanceID, deploymentName); claimant != "" {
		return claimant, "its index entry", nil
	}

	ids, _, err := b.Vault.ListOperationRecordIDs(ctx)
	if err != nil {
		return "", "", fmt.Errorf("could not list the instances in Vault to check whether another one claims deployment %s: %w", deploymentName, err)
	}

	sort.Strings(ids)

	for _, otherID := range ids {
		if otherID == instanceID || !mintedShape(deploymentName, otherID) {
			continue
		}

		claims, err := b.recordClaimsName(ctx, otherID, deploymentName)
		if err != nil {
			return "", "", fmt.Errorf("could not read the record of instance %s, which can claim deployment %s: %w", otherID, deploymentName, err)
		}

		if claims {
			return otherID, "its record, which owes the cleanup of a failed provision", nil
		}
	}

	return "", "", nil
}

// indexClaimant returns the first instance other than instanceID, in ID
// order, whose index entry records deploymentName or mints it under the
// entry's plan, or "" when none does.
func indexClaimant(idx *vaultPkg.Index, instanceID, deploymentName string) string {
	ids := make([]string, 0, len(idx.Data))
	for otherID := range idx.Data {
		ids = append(ids, otherID)
	}

	sort.Strings(ids)

	for _, otherID := range ids {
		entry, ok := idx.Data[otherID].(map[string]interface{})
		if otherID == instanceID || !ok {
			continue
		}

		recordedName, _ := entry["deployment_name"].(string)
		planID, _ := entry["plan_id"].(string)

		if recordedName == deploymentName || mintedFor(deploymentName, otherID, planID) {
			return otherID
		}
	}

	return ""
}

// recordClaimsName reports whether the record of otherID owes the cleanup
// of a failed provision that can delete deploymentName. That holds when
// the record names it, and when the record names nothing and request
// details are stored under the name, since the cleanup of such a record
// recovers its name from the director and owns any name those details
// were stored under.
func (b *Broker) recordClaimsName(ctx context.Context, otherID, deploymentName string) (bool, error) {
	recorded, err := b.readRecordedOperation(ctx, otherID)
	if err != nil {
		return false, err
	}

	switch {
	case !recorded.CleanupPending:
		return false, nil
	case recorded.DeploymentName != "":
		return recorded.DeploymentName == deploymentName, nil
	}

	var stored map[string]interface{}

	found, err := b.Vault.Get(ctx, otherID+"/"+deploymentName, &stored)
	if err != nil {
		return false, fmt.Errorf("failed to read the request details stored under %s/%s: %w", otherID, deploymentName, err)
	}

	return found, nil
}

// refuseClaimedName logs and returns the refusal of a provision whose
// deployment name another instance claims through where.
func refuseClaimedName(instanceID, deploymentName, claimant, where string, logger logger.Logger) error {
	logger.Warning("Refusing provision of instance %s: instance %s already claims deployment %s through %s", instanceID, claimant, deploymentName, where)

	return fmt.Errorf("%w: the deployment this instance would be named, %s, is already claimed by another service instance, so the provision is refused. Provision it under another instance ID", osbapi.ErrBadRequest, deploymentName)
}

// refuseProvisionOverUnownedDeployment asks the director for the
// deployment a provision of instanceID, which the index does not have,
// would create. It returns nil when the director says the deployment does
// not exist, and when it exists and the instance's own record names it
// for a provision that owes no cleanup, so a retried provision of the
// same instance goes on. When the record names it for a provision that
// failed, the request answers 409 Conflict, as it does for an indexed
// instance whose provision failed, because a deploy over it would leave
// the owed cleanup pointed at the new deployment. A deployment that exists
// with no record naming it answers 400 Bad Request, because the broker
// cannot show it belongs to this instance, and any other answer fails the
// request.
func (b *Broker) refuseProvisionOverUnownedDeployment(ctx context.Context, instanceID, deploymentName string, recorded recordedOperation, logger logger.Logger) error {
	_, err := b.BOSH.GetDeployment(ctx, deploymentName)

	switch {
	case errors.Is(err, bosh.ErrDeploymentNotFound):
		return nil
	case err != nil:
		logger.Error("Refusing provision of instance %s: the director could not confirm whether deployment %s exists: %s", instanceID, deploymentName, err)

		return fmt.Errorf("could not confirm whether deployment %s of instance %s exists, so the provision was refused: %w", deploymentName, instanceID, err)
	case recorded.Action != operationTypeProvision || recorded.DeploymentName != deploymentName:
	case recorded.CleanupPending || recorded.State == recordedStateFailed:
		logger.Warning("Refusing provision of instance %s: its provision of deployment %s failed (%s)", instanceID, deploymentName, recorded.Description)

		return fmt.Errorf("%w: the provision of instance %s failed, and it must be deleted before a provision with the same ID", osbapi.ErrInstanceAlreadyExists, instanceID)
	default:
		logger.Info("Deployment %s exists, and the record of instance %s names it for its provision, so the provision goes on", deploymentName, instanceID)

		return nil
	}

	logger.Warning("Refusing provision of instance %s: deployment %s exists, and the instance's record does not name it", instanceID, deploymentName)

	return fmt.Errorf("%w: deployment %s already exists, and nothing the broker holds for this instance shows it belongs to it, so the provision is refused", osbapi.ErrBadRequest, deploymentName)
}
