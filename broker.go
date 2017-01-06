package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v2"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-golang/lager"
	"github.com/smallfish/simpleyaml"
)

const findPlanKey = "findPlan"

type Broker struct {
	Catalog []brokerapi.Service
	Plans   map[string]Plan
	BOSH    *gogobosh.Client
	Vault   *Vault
	logger  lager.Logger
}

type Job struct {
	Name string
	IPs  []string
}

func (b Broker) FindPlan(planID string, serviceID string) (Plan, error) {
	b.logger.Debug("find-plan", lager.Data{
		"plan_id":    planID,
		"service_id": serviceID,
	})

	key := fmt.Sprintf("%s/%s", planID, serviceID)
	if plan, ok := b.Plans[key]; ok {
		return plan, nil
	}
	return Plan{}, fmt.Errorf("plan %s not found", key)
}

func (b *Broker) Services() []brokerapi.Service {
	b.logger.Info("fetching-catalog")
	return b.Catalog
}

func (b *Broker) ReadServices(dir ...string) error {
	ss, err := ReadServices(dir...)
	if err != nil {
		return err
	}

	b.Catalog = Catalog(ss)
	b.Plans = make(map[string]Plan)
	for _, s := range ss {
		for _, p := range s.Plans {
			b.logger.Debug("read-services", lager.Data{
				"plan_id":    p.ID,
				"service_id": s.ID,
			})
			b.Plans[fmt.Sprintf("%s/%s", s.ID, p.ID)] = p
		}
	}

	return nil
}

func (b *Broker) Provision(instanceID string, details brokerapi.ProvisionDetails, asyncAllowed bool) (brokerapi.ProvisionedServiceSpec, error) {
	spec := brokerapi.ProvisionedServiceSpec{IsAsync: true}

	logger := b.logger.Session("provision", lager.Data{
		"instance_id": instanceID,
	})

	logger.Info("starting-provision")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed-to-find-plan", err)
		return spec, err
	}

	defaults := make(map[interface{}]interface{})
	//TODO parse params from json to yaml
	params := make(map[interface{}]interface{})
	defaults["name"] = plan.Name + "-" + instanceID

	info, err := b.BOSH.GetInfo()
	if err != nil {
		logger.Error("failed-to-get-bosh-info", err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}
	defaults["director_uuid"] = info.UUID

	os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID))
	err = InitManifest(b.logger, plan, instanceID)
	if err != nil {
		logger.Error("failed-to-init-manifest", err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}

	manifest, err := GenManifest(plan, defaults, wrap("meta.params", params))
	if err != nil {
		logger.Error("failed-to-generate-manifest", err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}

	logger.Debug("generated-manifest", lager.Data{
		"manifest": manifest,
	})
	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		logger.Error("failed-to-create-deployment", err)
		return spec, fmt.Errorf("backend BOSH deployment failed")
	}

	err = b.Vault.Track(instanceID, "provision", task.ID, params)
	if err != nil {
		logger.Error("failed-to-track-deployment", err)
		return spec, fmt.Errorf("Vault tracking failed")
	}
	logger.Info("stared-provisioning")
	return spec, nil
}

func (b *Broker) Deprovision(instanceID string, details brokerapi.DeprovisionDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	logger := b.logger.Session("deprovision", lager.Data{
		"instance_id": instanceID,
	})

	logger.Info("starting-deprovision")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed-to-find-plan", err)
		return true, err
	}

	deploymentName := plan.Name + "-" + instanceID
	/* FIXME: what if we still have a valid task for deployment? */
	task, err := b.BOSH.DeleteDeployment(deploymentName)
	if err != nil {
		return true, err
	}

	b.Vault.Track(instanceID, "deprovision", task.ID, nil)
	logger.Info("finished-deprovision")
	return true, nil
}

func (b *Broker) LastOperation(instanceID string) (brokerapi.LastOperation, error) {
	typ, taskID, _, _ := b.Vault.State(instanceID)
	logger := b.logger.Session("last-operation", lager.Data{
		"instance_id": instanceID,
		"type":        typ,
	})
	if typ == "provision" {
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			logger.Error("failed-to-get-task-from-bosh", err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			logger.Info("provision-succeeded")
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}
		if task.State == "error" {
			logger.Error("provision-error", errors.New("deployment failed"))
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	if typ == "deprovision" {
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			logger.Error("failed-to-get-task-from-bosh", err)
			return brokerapi.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			b.Vault.Clear(instanceID)
			logger.Info("deprovision-succeeded")
			return brokerapi.LastOperation{State: "succeeded"}, nil
		}

		if task.State == "error" {
			b.Vault.Clear(instanceID)

			logger.Error("deprovision-error", errors.New("deprovision failed"))
			return brokerapi.LastOperation{State: "failed"}, nil
		}

		return brokerapi.LastOperation{State: "in progress"}, nil
	}

	return brokerapi.LastOperation{}, fmt.Errorf("invalid state type '%s'", typ)
}

func (b *Broker) Bind(instanceID, bindingID string, details brokerapi.BindDetails) (brokerapi.Binding, error) {
	var binding brokerapi.Binding
	var jobs []Job
	jobsYAML := make(map[string][]Job)
	logger := b.logger.Session("bind", lager.Data{
		"instance_id": instanceID,
		"binding_id":  bindingID,
	})
	logger.Info("binding-started")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed-to-find-plan", err)
		return binding, err
	}
	deploymentName := plan.Name + "-" + instanceID
	vms, err := b.BOSH.GetDeploymentVMs(deploymentName)
	if err != nil {
		logger.Error("failed-to-get-vms", err)
		return binding, err
	}

	os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID))

	for _, vm := range vms {
		job := Job{vm.JobName + "/" + strconv.Itoa(vm.Index), vm.IPs}
		jobs = append(jobs, job)
	}
	jobsYAML["jobs"] = jobs
	jobsMarshal, err := yaml.Marshal(jobsYAML)
	if err != nil {
		logger.Error("failed-to-generate-manifest-jobs-yaml", err)
		return binding, err
	}
	yamlJobs, err := simpleyaml.NewYaml(jobsMarshal)
	if err != nil {
		logger.Error("failed-to-generate-manifest-jobs-yaml", err)
		return binding, err
	}
	jobsIfc, err := yamlJobs.Map()
	if err != nil {
		logger.Error("failed-to-generate-jobs-map", err)
		return binding, err
	}

	manifest, err := GenManifest(plan, jobsIfc, plan.Credentials)
	if err != nil {
		logger.Error("failed-to-generate-manifest-credentials", err)
		return binding, err
	}

	yamlManifest, err := simpleyaml.NewYaml([]byte(manifest))
	if err != nil {
		logger.Error("failed-to-generate-manifest", err)
		return binding, err
	}

	yamlCreds := yamlManifest.Get("credentials")
	yamlMap, err := yamlCreds.Map()
	if err != nil {
		logger.Error("failed-to-retrieve-manifest-credentials", err)
		return binding, err
	}

	binding.Credentials = deinterfaceMap(yamlMap)

	logger.Debug("binding-creds", lager.Data{
		"creds": deinterfaceMap(yamlMap)})
	logger.Info("binding-succeeded")
	return binding, nil
}

func (b *Broker) Unbind(instanceID, bindingID string, details brokerapi.UnbindDetails) error {
	logger := b.logger.Session("unbind", lager.Data{
		"instance_id": instanceID,
		"binding_id":  bindingID,
	})

	logger.Info("unbinding-started")
	return nil
}

func (b *Broker) Update(instanceID string, details brokerapi.UpdateDetails, asyncAllowed bool) (brokerapi.IsAsync, error) {
	logger := b.logger.Session("update", lager.Data{
		"instance_id": instanceID,
	})

	logger.Info("update-started")
	// Update instance here
	return false, fmt.Errorf("not implemented")
}
