package main

import (
	"errors"
	"fmt"
	"os"
	"time"
  "io/ioutil"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/pivotal-cf/brokerapi"
  "gopkg.in/yaml.v2"
)

type Broker struct {
	Catalog []brokerapi.Service
	Plans   map[string]Plan
	BOSH    *gogobosh.Client
	Vault   *Vault
}

type Job struct {
	Name string
	IPs  []string
}

func WriteDataFile(instanceID string, data []byte) (error) {
  filepath := "/var/vcap/data/blacksmith/"
  filename := filepath + instanceID + ".json"
  err := ioutil.WriteFile(filename, data, 0644)
  return err
}

func WriteYamlFile(instanceID string, data []byte) (error) {
	l := Logger.Wrap("%s", instanceID)
  m := make(map[interface{}]interface{})
  err := yaml.Unmarshal(data, &m)
  if err != nil {
    l.Debug("Error unmarshalling data: %s, %s", err, data)
  }
  b, err := yaml.Marshal(m)
  if err != nil {
    l.Debug("Error marshalling data: %s, %s", err, m)
  }
  filepath := "/var/vcap/data/blacksmith/"
  filename := filepath + instanceID + ".yml"
  err = ioutil.WriteFile(filename, b, 0644)
  return err
}

func (b Broker) FindPlan(serviceID string, planID string) (Plan, error) {
	key := fmt.Sprintf("%s/%s", serviceID, planID)
	if plan, ok := b.Plans[key]; ok {
		return plan, nil
	}
	return Plan{}, fmt.Errorf("plan %s not found", key)
}

func (b *Broker) Services() []brokerapi.Service {
	return b.Catalog
}

func (b *Broker) ReadServices(dir ...string) error {
	l := Logger.Wrap("catalog")
	l.Info("reading catalog")

	ss, err := ReadServices(dir...)
	if err != nil {
		return err
	}

	b.Catalog = Catalog(ss)
	b.Plans = make(map[string]Plan)
	for _, s := range ss {
		for _, p := range s.Plans {
			l.Info("adding service/plan %s/%s to catalog", s.ID, p.ID)
			b.Plans[p.String()] = p
		}
	}

	return nil
}

func (b *Broker) Provision(instanceID string, details brokerapi.ProvisionDetails, asyncAllowed bool) (brokerapi.ProvisionedServiceSpec, error) {
	spec := brokerapi.ProvisionedServiceSpec{IsAsync: true}

	l := Logger.Wrap("%s %s/%s", instanceID, details.ServiceID, details.PlanID)
	l.Info("provisioning new service instance")

	l.Debug("looking for plan in blacksmith catalog")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return spec, err
	}

	l.Debug("retrieving vault 'db' index (for tracking service usage)")
	db, err := b.Vault.GetIndex("db")
	if err != nil {
		l.Error("failed to get 'db' index out of the vault: %s", err)
		return spec, err
	}

	l.Debug("checking if we are over out service and/or plan limits")
	if plan.OverLimit(db) {
		l.Error("service limit exceeded for %s/%s", plan.Service.Name, plan.Name)
		return spec, brokerapi.ErrPlanQuotaExceeded
	}

	defaults := make(map[interface{}]interface{})
	//TODO parse params from json to yaml
  l.Debug("Param raw data: %s", details.RawParameters)
  err = WriteDataFile(instanceID, details.RawParameters)
  if err != nil {
    l.Debug("WriteDataFile write failed with '%s'", err)
  }
  err = WriteYamlFile(instanceID, details.RawParameters)
  if err != nil {
    l.Debug("WriteYamlFile write failed with '%s'", err)
  }
	params := make(map[interface{}]interface{})
	defaults["name"] = plan.ID + "-" + instanceID

	l.Debug("querying BOSH director for director UUID")
	info, err := b.BOSH.GetInfo()
	if err != nil {
		l.Error("failed to get information about BOSH director: %s", err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}
	l.Debug("found BOSH director UUID: %s", info.UUID)
	defaults["director_uuid"] = info.UUID

	os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID))
	l.Debug("setting vault prefix to %s", os.Getenv("CREDENTIALS"))
	l.Debug("running service init script")
	err = InitManifest(plan, instanceID)
	if err != nil {
		l.Error("service deployment initialization script failed: %s", err)
		return spec, fmt.Errorf("BOSH service deployment initial setup failed")
	}

  l.Debug("Provision defaults: %s", defaults)
  l.Debug("Provision params: %s", params)

	l.Debug("generating manifest for service deployment")
	manifest, err := GenManifest(plan, defaults, wrap("meta.params", params))
	if err != nil {
		l.Error("failed to generate service deployment manifest: %s", err)
		return spec, fmt.Errorf("BOSH service deployment manifest generation failed")
	}
	err = b.Vault.Put(fmt.Sprintf("%s/manifest", instanceID), map[string]interface{}{
		"manifest": manifest,
	})
	if err != nil {
		l.Error("failed to store manifest in the vault (non-fatal): %s", err)
	}

	l.Debug("uploading releases (if necessary) to BOSH director")
	err = UploadReleasesFromManifest(manifest, b.BOSH, l)
	if err != nil {
		l.Error("failed to upload service deployment releases: %s", err)
		return spec, fmt.Errorf("BOSH service deployment failed")
	}

	l.Debug("deploying to BOSH director")
	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		l.Error("failed to create service deployment: %s", err)
		return spec, fmt.Errorf("BOSH service deployment failed")
	}
	l.Debug("deployment started, BOSH task %d", task.ID)

	l.Debug("tracking service instance in the vault 'db' index")
	err = b.Vault.Index(instanceID, map[string]interface{}{
		"service_id": details.ServiceID,
		"plan_id":    plan.ID,
		"created":    time.Now().Unix(),
	})
	if err != nil {
		l.Error("failed to track new service in the vault index: %s", err)
		return spec, fmt.Errorf("Failed to track new service in Vault")
	}

	l.Debug("updating service status in the vault")
	err = b.Vault.Track(instanceID, "provision", task.ID, params)
	if err != nil {
		l.Error("failed to store service status in the vault: %s", err)
		return spec, fmt.Errorf("Failed to store service deployment status")
	}

	l.Debug("started provisioning")
	return spec, nil
}

func (b *Broker) Deprovision(instanceID string, details brokerapi.DeprovisionDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	l := Logger.Wrap(fmt.Sprintf("%s %s/%s", instanceID, details.ServiceID, details.PlanID))
	l.Info("deprovisioning service instance")

	instance, exists, err := b.Vault.FindInstance(instanceID)
	if err != nil {
		l.Error("unable to retrieve instance details from vault index: %s", err)
		return false, err
	}
	if !exists {
		l.Debug("removing defunct service from vault index")
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove defunct service instance '%s' from vault: %s", instanceID, err)
		}

		/* return a 410 Gone to the caller */
		return false, brokerapi.ErrInstanceDoesNotExist
	}

	deploymentName := instance.PlanID + "-" + instanceID
	l.Debug("determined BOSH deployment name to be %s", deploymentName)

	manifest, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil || manifest.Manifest == "" {
		l.Debug("removing defunct service from vault index")
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove defunct service instance '%s' from vault: %s", instanceID, err)
		}

		/* return a 410 Gone to the caller */
		return false, brokerapi.ErrInstanceDoesNotExist
	}

	l.Debug("deleting BOSH deployment")
	/* FIXME: what if we still have a valid task for deployment? */
	task, err := b.BOSH.DeleteDeployment(deploymentName)
	if err != nil {
		l.Error("failed to delete BOSH deployment %s: %s", deploymentName, err)
		return false, err
	}
	l.Debug("delete operation started, BOSH task %d", task.ID)

	l.Debug("removing service from vault 'db' index")
	if err := b.Vault.Index(instanceID, nil); err != nil {
		l.Error("failed to remove service '%s' from vault 'db' index: %s", instanceID, err)
	}

	l.Debug("updating service status in the vault")
	if err := b.Vault.Track(instanceID, "deprovision", task.ID, nil); err != nil {
		l.Error("failed to track deprovision BOSH task #%d in vault: %s", task.ID, err)
	}

	l.Info("started deprovisioning")
	return true, nil
}

func (b *Broker) LastOperation(instanceID string) (brokerapi.LastOperation, error) {
	l := Logger.Wrap(instanceID)
	l.Debug("last-operation check received; checking state of service deployment")

	typ, taskID, _, _ := b.Vault.State(instanceID)
	l.Debug("instance was last in '%s' state, BOSH task %d", typ, taskID)

	if typ == "provision" {
		l.Debug("retrieving task %d from BOSH director", taskID)
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			l.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			l.Debug("provision operation succeeded")
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}
		if task.State == "error" {
			l.Error("provision operation failed!")
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		l.Debug("provision operation is still in progress")
		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	if typ == "deprovision" {
		l.Debug("retrieving task %d from BOSH director", taskID)
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			l.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			l.Debug("deprovision operation succeeded")
			l.Debug("cleaning up secret/%s from the vault", instanceID)
			b.Vault.Clear(instanceID)
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}

		if task.State == "error" {
			l.Debug("deprovision operation failed!")
			l.Debug("cleaning up secret/%s from the vault", instanceID)
			b.Vault.Clear(instanceID)
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		l.Debug("deprovision operation is still in progress")
		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	l.Error("invalid state '%s' found in the vault", typ)
	return brokerapi.LastOperation{}, fmt.Errorf("invalid state type '%s'", typ)
}

func (b *Broker) Bind(instanceID, bindingID string, details brokerapi.BindDetails) (brokerapi.Binding, error) {
	var binding brokerapi.Binding

	l := Logger.Wrap("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID)
	l.Info("bind operation started")

	l.Debug("looking for plan in blacksmith catalog")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return binding, err
	}

	creds, err := GetCreds(instanceID, plan, b.BOSH, l)
	if err != nil {
		return binding, err
	}

	binding.Credentials = creds
	l.Debug("credentials are: %v", binding)

	l.Info("bind successful")
	return binding, nil
}

func (b *Broker) Unbind(instanceID, bindingID string, details brokerapi.UnbindDetails) error {
	l := Logger.Wrap("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID)
	l.Info("unbind operation started")
	/* nothing to do */
	l.Info("unbind successful")
	return nil
}

func (b *Broker) Update(instanceID string, details brokerapi.UpdateDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	l := Logger.Wrap("%s %s %s", instanceID, details.ServiceID, details.PlanID)
	l.Error("update operation not implemented")

	// FIXME: implement this!

	return false, fmt.Errorf("not implemented")
}

func (b *Broker) serviceWithNoDeploymentCheck() ([]string, error) {
	l := Logger.Wrap("*")
	l.Info("checking for service instances with no backing deployment")
	//grab all current deployments
	deployments, err := b.BOSH.GetDeployments()
	if err != nil {
		return nil, err
	}

	//turn deployments from a slice of deployments into a map of
	//string to bool because all we care about is the name of the deployment (the string here)
	deploymentNames := make(map[string]bool)
	for _, deployment := range deployments {
		deploymentNames[deployment.Name] = true
	}
	//grab the vault DB json blob out of vault
	vaultDB, err := b.Vault.getVaultDB()
	if err != nil {
		return nil, err
	}

	//loop through all current instances in the "db"
	//check bosh director for each instance in the "db"
	var removedDeploymentNames []string
	for instanceID, serviceInstance := range vaultDB.Data {
		l.Debug("current value of instanceID: %v", instanceID)
		if ss, ok := serviceInstance.(map[string]interface{}); ok {
			service, ok := ss["service_id"].(string)
			if !ok {
				return nil, errors.New("could not assert service id to string")
			}
			l.Debug("current value of service: %v", service)
			plan, ok := ss["plan_id"].(string)
			if !ok {
				l.Error("could not assert plan id to string")
				return nil, errors.New("could not assert plan id to string")
			}
			l.Debug("current value of plan: %v", plan)
			currentDeployment := plan + "-" + instanceID
			//deployments are named as instance.PlanID + "-" + instanceID
			if _, ok := deploymentNames[currentDeployment]; !ok {
				//if the deployment name isn't listed in our director then delete it from vault
				l.Debug("found no deployment on bosh director named: %v", currentDeployment)
				removedDeploymentNames = append(removedDeploymentNames, currentDeployment)
				l.Debug("removing service id: " + instanceID + " from vault db")
				if err := b.Vault.Index(instanceID, nil); err != nil {
					l.Error("unable to remove service instance '%s' from vault db: %s", instanceID, err)
				}
			}
		}
	}
	return removedDeploymentNames, nil
}
