package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/pivotal-cf/brokerapi"
	"github.com/smallfish/simpleyaml"
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

func (b Broker) FindPlan(planID string, serviceID string) (Plan, error) {
	key := fmt.Sprintf("%s/%s", planID, serviceID)
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
		b.Vault.Index(instanceID, nil)

		/* return a 410 Gone to the caller */
		return false, brokerapi.ErrInstanceDoesNotExist
	}

	deploymentName := instance.PlanID + "-" + instanceID
	l.Debug("determined BOSH deployment name to be %s", deploymentName)

	manifest, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil || manifest.Manifest == "" {
		l.Debug("removing defunct service from vault index")
		b.Vault.Index(instanceID, nil)

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
	b.Vault.Index(instanceID, nil)
	l.Debug("updating service status in the vault")
	b.Vault.Track(instanceID, "deprovision", task.ID, nil)

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
	var jobs []*Job
	jobsYAML := make(map[string][]*Job)

	l := Logger.Wrap("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID)
	l.Info("bind operation started")

	l.Debug("looking for plan in blacksmith catalog")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return binding, err
	}

	deploymentName := plan.ID + "-" + instanceID
	l.Debug("looking up BOSH VM information for %s", deploymentName)
	vms, err := b.BOSH.GetDeploymentVMs(deploymentName)
	if err != nil {
		l.Error("failed to retrieve BOSH VM information for %s: %s", deploymentName, err)
		return binding, err
	}

	os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID))
	byType := make(map[string]*Job)
	for _, vm := range vms {
		job := Job{vm.JobName + "/" + strconv.Itoa(vm.Index), vm.IPs}
		l.Debug("found job %s with IPs [%s]", job.Name, strings.Join(vm.IPs, ", "))
		jobs = append(jobs, &job)

		if typ, ok := byType[vm.JobName]; ok {
			for _, ip := range vm.IPs {
				typ.IPs = append(typ.IPs, ip)
			}
		} else {
			byType[vm.JobName] = &Job{vm.JobName, vm.IPs}
		}
	}
	for _, job := range byType {
		jobs = append(jobs, job)
	}
	jobsYAML["jobs"] = jobs
	l.Debug("marshaling BOSH VM information")
	jobsMarshal, err := yaml.Marshal(jobsYAML)
	if err != nil {
		l.Error("failed to marshal BOSH VM information (for credentials.yml merge): %s", err)
		return binding, err
	}
	l.Debug("converting BOSH VM information to YAML")
	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		l.Error("failed to convert BOSH VM information to YAML (for credentials.yml merge): %s", err)
		return binding, err
	}
	jobsIfc, err := yamlJobs.Map()
	l.Debug("parsing BOSH VM information from YAML (don't ask)")
	if err != nil {
		l.Error("failed to parse BOSH VM information from YAML (for credentials.yml merge): %s", err)
		return binding, err
	}

	l.Debug("merging service deployment manifest with credentials.yml (for retrieve/bind)")
	manifest, err := GenManifest(plan, jobsIfc, plan.Credentials)
	if err != nil {
		l.Error("failed to merge service deployment manifest with credentials.yml: %s", err)
		return binding, err
	}

	l.Debug("parsing merged YAML super-structure, to retrieve `credentials' top-level key")
	yamlManifest, err := simpleyaml.NewYaml([]byte(manifest))
	if err != nil {
		l.Error("failed to parse merged YAML; unable to retrieve credentials for bind: %s", err)
		return binding, err
	}

	l.Debug("retrieving `credentials' top-level key, to return to the caller")
	yamlCreds := yamlManifest.Get("credentials")
	yamlMap, err := yamlCreds.Map()
	if err != nil {
		l.Error("failed to retrieve `credentials' top-level key: %s", err)
		return binding, err
	}

	binding.Credentials = deinterfaceMap(yamlMap)
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
