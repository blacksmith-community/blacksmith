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

func (b Broker) FindPlan(planID string, serviceID string) (brokerapi.ServicePlan, error) {
	for _, s := range b.Catalog {
		if s.ID == serviceID {
			for _, p := range s.Plans {
				if p.ID == planID {
					return p, nil
				}
			}
		}
	}

	return brokerapi.ServicePlan{}, fmt.Errorf("plan %s/%s not found", serviceID, planID)
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

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		log.Printf("[provision %s] failed: %s", instanceID, err)
		return spec, err
	}

	var params map[interface{}]interface{}
	params["name"] = instanceID

	manifest, err := GenManifest(plan, params)
	if err != nil {
		log.Printf("[provision %s] failed: %s", instanceID, err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}

	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		log.Printf("[provision %s] failed: %s", instanceID, err)
		return spec, fmt.Errorf("backend BOSH deployment failed")
	}

	b.Vault.Track(instanceID, "provision", task.ID) /* FIXME: track service creds */
	log.Printf("[provision %s] started", instanceID)
	return spec, nil
}

func (b *Broker) Deprovision(instanceID string, details brokerapi.DeprovisionDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	log.Printf("[deprovision %s] deleting deployment %s", instanceID, instanceID)
	/* FIXME: what if we still have a valid task for deployment? */
	task, err := b.BOSH.DeleteDeployment(instanceID)
	if err != nil {
		return true, err
	}

	b.Vault.Track(instanceID, "deprovision", task.ID)
	log.Printf("[deprovision %s] started", instanceID)
	return true, nil
}

func (b *Broker) LastOperation(instanceID string) (brokerapi.LastOperation, error) {
	typ, taskID, _ := b.Vault.State(instanceID)
	if typ == "provision" {
		task, err := b.BOSH.GetTask(strconv.Atoi(taskID))
		if err != nil {
			log.Printf("[provision %s] failed to get task from BOSH: %s", instanceID, err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			log.Printf("[provision %s] succeeded", instanceID)
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}
		if task.State == "error" {
			log.Printf("[provision %s] failed", instanceID)
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	if typ == "deprovision" {
		task, err := b.BOSH.GetTask(strconv.Atoi(taskID))
		if err != nil {
			log.Printf("[deprovision %s] failed to get task from BOSH: %s", instanceID, err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			log.Printf("[deprovision %s] task completed", instanceID)
			log.Printf("[deprovision %s] clearing secrets under secret/%s", instanceID, instanceID)
			b.Vault.Clear(instanceID)

			log.Printf("[deprovision %s] succeeded", instanceID)
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}

		if task.State == "error" {
			log.Printf("[deprovision %s] clearing secrets under secret/%s", instanceID, instanceID)
			b.Vault.Clear(instanceID)

			log.Printf("[deprovision %s] failed", instanceID)
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	return brokerapi.LastOperation{}, fmt.Errorf("invalid state type '%s'", typ)
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
