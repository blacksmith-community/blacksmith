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
		List:             instanceLister(brokerInstance, vault),
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

// instanceLister lists the instances in the Vault index that feeds /b/status,
// naming each one's service and plan from the broker's catalog.
func instanceLister(brokerInstance *broker.Broker, vault *internalVault.Vault) certinventory.InstanceLister {
	return func(ctx context.Context) ([]certinventory.InstanceRef, error) {
		if vault == nil {
			return nil, nil
		}

		idx, err := vault.GetIndex(ctx, "db")
		if err != nil {
			return nil, fmt.Errorf("list service instances: %w", err)
		}

		plans := brokerInstance.GetPlans()

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

			ref := certinventory.InstanceRef{
				ID:         id,
				Name:       entry.InstanceName,
				Deployment: entry.DeploymentNameFor(id),
				ServiceID:  entry.ServiceID,
				PlanID:     entry.PlanID,
			}

			if plan, ok := plans[entry.ServiceID+"/"+entry.PlanID]; ok {
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
