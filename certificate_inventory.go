package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

// newCertificateService builds the certificate inventory over the broker's
// director, Vault, and CredHub client. Without a BOSH director the inventory
// stays on but degraded, since the service then fails each instance scan
// with "BOSH director unavailable" and reports rotation as unavailable. A
// missing CredHub client stays a nil pointer in the service, which then
// reports CredHub as not configured.
func newCertificateService(cfg *config.Config, brokerInstance *broker.Broker, vault *internalVault.Vault, boshDirector *bosh.PooledDirector, logger loggerPkg.Logger) *certinventory.Service {
	enabled := cfg.Certificates.InventoryEnabled()

	var scanner certinventory.ScannerDeps

	if boshDirector != nil {
		scanner.Director = boshDirector
	} else if enabled {
		logger.Warn("BOSH director unavailable - the certificate inventory will scan the broker only")
	}

	if vault != nil {
		scanner.Vault = interfaces.Vault(vault)
	}

	scanner.DirectorName = cfg.CredHub.DirectorName

	return certinventory.NewService(certinventory.ServiceDeps{
		Scanner:          scanner,
		CredHubClient:    brokerInstance.CredHubClient,
		List:             listerFor(brokerInstance, vault),
		Broker:           func() certinventory.BrokerSources { return brokerSources(cfg) },
		InventoryEnabled: enabled,
		RotationEnabled:  cfg.Certificates.RotationEnabled(),
		PatchEnabled:     cfg.Certificates.PatchInPlaceEnabled(),
		Log:              logger.Named("certificates"),
	})
}

// indexEntry is the part of a Vault index entry the inventory reads.
type indexEntry struct {
	vaultPkg.Instance

	InstanceName string `json:"instance_name,omitempty"`
}

// instanceRecord is the only part of an instance's own Vault record the
// inventory reads. The record also holds credentials, which a decode into
// this struct drops.
type instanceRecord struct {
	InstanceName   string `json:"instance_name"`
	DeploymentName string `json:"deployment_name"`
}

// indexSource is the part of the Vault client the lister reads.
type indexSource interface {
	GetIndex(ctx context.Context, path string) (*vaultPkg.Index, error)
	Get(ctx context.Context, path string, out interface{}) (bool, error)
}

// instanceLister lists the instances in the Vault index that feeds /b/status.
// It names each instance from its own Vault record, as /b/status does, and
// falls back to the deployment name so every owner has a readable label. The
// catalog supplies the service and plan names.
func instanceLister(plans func() map[string]services.Plan, src indexSource) certinventory.InstanceLister {
	return func(ctx context.Context) ([]certinventory.InstanceRef, error) {
		idx, err := src.GetIndex(ctx, "db")
		if err != nil {
			return nil, fmt.Errorf("list service instances: %w", err)
		}

		catalog := plans()

		var refs []certinventory.InstanceRef

		for id, raw := range idx.Data {
			data, err := json.Marshal(raw)
			if err != nil {
				continue
			}

			var entry indexEntry
			if json.Unmarshal(data, &entry) != nil || entry.PlanID == "" {
				continue
			}

			var record instanceRecord

			found, err := src.Get(ctx, id, &record)
			if err != nil {
				found = false
			}

			if found {
				if record.InstanceName != "" {
					entry.InstanceName = record.InstanceName
				}

				if record.DeploymentName != "" {
					entry.DeploymentName = record.DeploymentName
				}
			}

			deployment := entry.DeploymentNameFor(id)

			name := entry.InstanceName
			if name == "" {
				name = deployment
			}

			ref := certinventory.InstanceRef{
				ID:         id,
				Name:       name,
				Deployment: deployment,
				ServiceID:  entry.ServiceID,
				PlanID:     entry.PlanID,
			}

			if plan, ok := catalog[entry.ServiceID+"/"+entry.PlanID]; ok {
				ref.PlanName = plan.Name
				if plan.Service != nil {
					ref.ServiceName = plan.Service.Name
				}
			}

			refs = append(refs, ref)
		}

		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })

		return refs, nil
	}
}

// brokerSources names where the broker's own certificates come from.
func brokerSources(cfg *config.Config) certinventory.BrokerSources {
	src := certinventory.BrokerSources{
		BOSHCACert:    cfg.BOSH.CACert,
		CredHubCACert: cfg.CredHub.CACert,
		VaultCACert:   cfg.Vault.CACert,
		DirectorURL:   cfg.BOSH.Address,
		UAAURL:        cfg.CredHub.UAAURL,
		CredHubURL:    cfg.CredHub.URL,
	}

	if cfg.Broker.TLS.Enabled {
		src.HTTPSCertPEM = cfg.Broker.TLS.Certificate
	}

	if len(cfg.Broker.CF.APIs) > 0 {
		src.CFAPICAs = map[string]string{}

		for name, api := range cfg.Broker.CF.APIs {
			if api.CACert != "" {
				src.CFAPICAs[name] = api.CACert
			}
		}
	}

	return src
}

// listerFor returns the instance lister over the broker's catalog and Vault.
// With no Vault it lists nothing, and it never puts a nil *Vault in the
// interface.
func listerFor(brokerInstance *broker.Broker, vault *internalVault.Vault) certinventory.InstanceLister {
	if vault == nil {
		return func(context.Context) ([]certinventory.InstanceRef, error) { return nil, nil }
	}

	return instanceLister(brokerInstance.GetPlans, vault)
}
