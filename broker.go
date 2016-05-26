package main

import (
	"fmt"
	"log"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/pivotal-cf/brokerapi"
)

type Broker struct {
	Catalog []brokerapi.Service
	BOSH    *gogobosh.Client
	Vault   *Vault
}

func EnvBroker() *Broker {
	v := EnvVault()
	if v == nil {
		return nil
	}
	return &Broker{Vault: v}
}

func (b *Broker) Services() []brokerapi.Service {
	log.Printf("[catalog] returning service catalog")
	return b.Catalog
}

func (b *Broker) Provision(instanceID string, details brokerapi.ProvisionDetails, asyncAllowed bool) (brokerapi.ProvisionedServiceSpec, error) {
	spec := brokerapi.ProvisionedServiceSpec{IsAsync: true}
	log.Printf("[provision %s] provisioning new service", instanceID)

	/* FIXME: generate a manifest */
	manifest := ""

	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		log.Printf("[provision %s] failed: %s", instanceID, err)
		return spec, fmt.Errorf("backend BOSH deployment failed")
	}

	b.Vault.Track(instanceID, "provision", task.ID) /* FIXME: track service creds */

	log.Printf("[provision %s] started", instanceID)
	return spec, nil
}

func (b *Broker) LastOperation(instanceID string) (brokerapi.LastOperation, error) {
	typ, _, _ := b.Vault.State(instanceID)
	if typ == "provision" {
		/* FIXME: go to gogobosh and see what's up */
	}
	if typ == "deprovision" {
		/* FIXME: go to gogobosh and see what's up */
		log.Printf("[deprovision %s] clearing secrets under secret/%s", instanceID, instanceID)
		b.Vault.Clear(instanceID)
	}
	return brokerapi.LastOperation{}, fmt.Errorf("invalid state type '%s'", typ)
}

func (b *Broker) Deprovision(instanceID string, details brokerapi.DeprovisionDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	log.Printf("[deprovision %s] deleteing deployment %s", instanceID, instanceID)
	/* FIXME: what if we still have a valid task for deployment? */
	task, err := b.BOSH.DeleteDeployment(instanceID)
	if err != nil {
		return true, err
	}

	b.Vault.Track(instanceID, "deprovision", task.ID)
	return true, nil
}

func (b *Broker) Bind(instanceID, bindingID string, details brokerapi.BindDetails) (brokerapi.Binding, error) {
	var binding brokerapi.Binding
	log.Printf("[bind %s / %s] binding service", instanceID, bindingID)
	/* FIXME: send creds */
	log.Printf("[bind %s / %s] success", instanceID, bindingID)
	return binding, nil
}

func (b *Broker) Unbind(instanceID, bindingID string, details brokerapi.UnbindDetails) error {
	return nil
}

func (b *Broker) Update(instanceID string, details brokerapi.UpdateDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	// Update instance here
	return false, fmt.Errorf("not implemented")
}
