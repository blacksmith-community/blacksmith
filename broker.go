package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/shield"
	"github.com/google/uuid"
	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-cf/brokerapi/domain"
	"gopkg.in/yaml.v2"
)

type Broker struct {
	Catalog []brokerapi.Service
	Plans   map[string]Plan
	BOSH    bosh.Director
	Vault   *Vault
	Shield  shield.Client
}

type Job struct {
	Name       string   `json:"name"`
	Deployment string   `json:"deployment"`
	ID         string   `json:"id"`
	PlanID     string   `json:"plan_id"`
	PlanName   string   `json:"plan_name"`
	FQDN       string   `json:"fqdn"`
	IPs        []string `json:"ips"`
	DNS        []string `json:"dns"`
}

func WriteDataFile(
	instanceID string,
	data []byte,
) error {
	l := Logger.Wrap("WriteDataFile")
	filename := GetWorkDir() + instanceID + ".json"
	l.Debug("Writing data file for instance %s to %s (size: %d bytes)", instanceID, filename, len(data))
	err := ioutil.WriteFile(filename, data, 0644)
	if err != nil {
		l.Error("Failed to write data file %s: %s", filename, err)
	} else {
		l.Debug("Successfully wrote data file %s", filename)
	}
	return err
}

func WriteYamlFile(
	instanceID string,
	data []byte,
) error {
	l := Logger.Wrap("WriteYamlFile")
	l.Debug("Writing YAML file for instance %s (input size: %d bytes)", instanceID, len(data))

	m := make(map[interface{}]interface{})
	err := yaml.Unmarshal(data, &m)
	if err != nil {
		l.Error("Failed to unmarshal data for YAML file: %s", err)
		l.Debug("Raw data (first 500 chars): %s", string(data[:min(len(data), 500)]))
		return err
	}

	b, err := yaml.Marshal(m)
	if err != nil {
		l.Error("Failed to marshal data to YAML: %s", err)
		l.Debug("Map content: %+v", m)
		return err
	}

	filename := GetWorkDir() + instanceID + ".yml"
	l.Debug("Writing YAML to file: %s (size: %d bytes)", filename, len(b))
	err = ioutil.WriteFile(filename, b, 0644)
	if err != nil {
		l.Error("Failed to write YAML file %s: %s", filename, err)
	} else {
		l.Debug("Successfully wrote YAML file %s", filename)
	}
	return err
}

func (b Broker) FindPlan(
	serviceID string,
	planID string,
) (Plan, error) {
	l := Logger.Wrap("FindPlan")
	key := fmt.Sprintf("%s/%s", serviceID, planID)
	l.Debug("Looking up plan with key: %s", key)
	l.Debug("Total plans in catalog: %d", len(b.Plans))

	if plan, ok := b.Plans[key]; ok {
		l.Debug("Found plan - Name: %s, Type: %s, Limit: %d", plan.Name, plan.Type, plan.Limit)
		return plan, nil
	}

	l.Error("Plan not found - serviceID: %s, planID: %s, key: %s", serviceID, planID, key)
	l.Debug("Available plan keys in catalog:")
	for k := range b.Plans {
		l.Debug("  - %s", k)
	}
	return Plan{}, fmt.Errorf("plan %s not found", key)
}

func (b *Broker) Services(ctx context.Context) ([]domain.Service, error) {
	l := Logger.Wrap("Services")
	l.Info("Retrieving service catalog")
	l.Debug("Converting %d brokerapi.Service entries to domain.Service", len(b.Catalog))

	// Convert brokerapi.Service to domain.Service
	services := make([]domain.Service, len(b.Catalog))
	for i, svc := range b.Catalog {
		services[i] = domain.Service{
			ID:                   svc.ID,
			Name:                 svc.Name,
			Description:          svc.Description,
			Bindable:             svc.Bindable,
			InstancesRetrievable: svc.InstancesRetrievable,
			BindingsRetrievable:  svc.BindingsRetrievable,
			PlanUpdatable:        svc.PlanUpdatable,
			Plans:                make([]domain.ServicePlan, len(svc.Plans)),
			Tags:                 svc.Tags,
			Requires:             svc.Requires,
			Metadata:             svc.Metadata,
			DashboardClient:      (*domain.ServiceDashboardClient)(svc.DashboardClient),
		}
		for j, plan := range svc.Plans {
			services[i].Plans[j] = domain.ServicePlan{
				ID:          plan.ID,
				Name:        plan.Name,
				Description: plan.Description,
				Free:        plan.Free,
				Bindable:    plan.Bindable,
				Metadata:    plan.Metadata,
			}
		}
		l.Debug("Converted service %s with %d plans", services[i].Name, len(services[i].Plans))
	}
	l.Info("Successfully retrieved %d services from catalog", len(services))
	return services, nil
}

func (b *Broker) ReadServices(dir ...string) error {
	l := Logger.Wrap("Broker.ReadServices")
	l.Info("Starting to read and build service catalog")
	l.Debug("Service directories provided: %v", dir)

	l.Debug("Calling ReadServices to read service definitions")
	ss, err := ReadServices(dir...)
	if err != nil {
		l.Error("Failed to read services: %s", err)
		l.Debug("Error occurred while reading from directories: %v", dir)
		return err
	}
	l.Info("Successfully read %d services", len(ss))

	l.Debug("Converting services to broker catalog format")
	b.Catalog = Catalog(ss)
	l.Debug("Catalog created with %d services", len(b.Catalog))

	b.Plans = make(map[string]Plan)
	totalPlans := 0
	for _, s := range ss {
		l.Debug("Processing service %s (ID: %s) with %d plans", s.Name, s.ID, len(s.Plans))
		for _, p := range s.Plans {
			planKey := p.String()
			l.Info("Adding service/plan %s/%s to catalog (key: %s)", s.ID, p.ID, planKey)
			l.Debug("Plan details - Name: %s, Type: %s, Limit: %d", p.Name, p.Type, p.Limit)
			b.Plans[planKey] = p
			totalPlans++
		}
	}
	l.Info("Successfully built catalog with %d services and %d total plans", len(b.Catalog), totalPlans)

	return nil
}

func (b *Broker) Provision(
	ctx context.Context,
	instanceID string,
	details domain.ProvisionDetails,
	asyncAllowed bool,
) (
	domain.ProvisionedServiceSpec,
	error,
) {
	spec := domain.ProvisionedServiceSpec{IsAsync: true}

	l := Logger.Wrap("%s %s/%s", instanceID, details.ServiceID, details.PlanID)
	l.Info("Starting provision of service instance %s (service: %s, plan: %s)", instanceID, details.ServiceID, details.PlanID)
	l.Debug("Provision details - OrganizationGUID: %s, SpaceGUID: %s", details.OrganizationGUID, details.SpaceGUID)

	l.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)
	l.Debug("Searching in blacksmith catalog for plan")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return spec, err
	}
	l.Debug("Found plan: %s (limit: %d)", plan.Name, plan.Limit)

	l.Debug("retrieving vault 'db' index (for tracking service usage)")
	db, err := b.Vault.GetIndex("db")
	if err != nil {
		l.Error("failed to get 'db' index out of the vault: %s", err)
		return spec, err
	}

	l.Info("Checking service limits for plan %s", plan.Name)
	l.Debug("Current usage count from DB index, checking against limit %d", plan.Limit)
	if plan.OverLimit(db) {
		l.Error("Service limit exceeded for %s/%s (limit: %d)", plan.Service.Name, plan.Name, plan.Limit)
		return spec, brokerapi.ErrPlanQuotaExceeded
	}
	l.Debug("Service limit check passed")

	defaults := make(map[interface{}]interface{})
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
	err = yaml.Unmarshal(details.RawParameters, &params)
	if err != nil {
		l.Debug("Error unmarshalling params: %s, %s", err, details.RawParameters)
	}
	defaults["name"] = plan.ID + "-" + instanceID

	l.Debug("querying BOSH director for director UUID")
	info, err := b.BOSH.GetInfo()
	if err != nil {
		l.Error("failed to get information about BOSH director: %s", err)
		return spec, fmt.Errorf("BOSH deployment manifest generation failed")
	}
	l.Debug("found BOSH director UUID: %s", info.UUID)
	defaults["director_uuid"] = info.UUID

	if err := os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID)); err != nil {
		l.Error("failed to set CREDENTIALS environment variable: %s", err)
		return brokerapi.ProvisionedServiceSpec{}, err
	}
	l.Debug("setting vault prefix to %s", os.Getenv("CREDENTIALS"))
	l.Debug("running service init script")
	err = InitManifest(plan, instanceID)
	if err != nil {
		l.Error("service deployment initialization script failed: %s", err)
		return spec, fmt.Errorf("BOSH service deployment initial setup failed")
	}

	l.Debug("Provision defaults: %s", defaults)
	l.Debug("Provision params: %s", params)

	params["instance_id"] = instanceID

	l.Debug("storing metadata details in Vault")
	err = b.Vault.Put(instanceID, details)
	if err != nil {
		l.Error("failed to store metadata in the vault (non-fatal): %s", err)
	}

	l.Info("Generating BOSH deployment manifest for %s", defaults["name"])
	l.Debug("Calling GenManifest with plan %s and parameters", plan.Name)
	manifest, err := GenManifest(plan, defaults, wrap("meta.params", params))
	if err != nil {
		l.Error("Failed to generate service deployment manifest: %s", err)
		return spec, fmt.Errorf("BOSH service deployment manifest generation failed")
	}
	l.Debug("Generated manifest size: %d bytes", len(manifest))
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

	l.Info("Deploying service instance to BOSH director")
	l.Debug("Submitting deployment manifest to BOSH (size: %d bytes)", len(manifest))
	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		l.Error("Failed to create service deployment: %s", err)
		return spec, fmt.Errorf("BOSH service deployment failed")
	}
	l.Info("Deployment started successfully, BOSH task ID: %d", task.ID)
	l.Debug("Task state: %s, description: %s", task.State, task.Description)

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

	l.Info("Successfully initiated provisioning of service instance %s", instanceID)
	return spec, nil
}

func (b *Broker) Deprovision(
	ctx context.Context,
	instanceID string,
	details domain.DeprovisionDetails,
	asyncAllowed bool,
) (
	domain.DeprovisionServiceSpec,
	error,
) {
	l := Logger.Wrap("%s %s/%s", instanceID, details.ServiceID, details.PlanID)
	l.Info("deprovisioning plan (%s) service (%s) instance (%s)", details.PlanID, details.ServiceID, instanceID)

	instance, exists, err := b.Vault.FindInstance(instanceID)
	if err != nil {
		l.Error("unable to retrieve instance details from vault index: %s", err)
		return domain.DeprovisionServiceSpec{}, err
	}
	if !exists {
		l.Debug("removing defunct service from vault index")
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove defunct service instance '%s' from vault: %s", instanceID, err)
		}

		/* return a 410 Gone to the caller */
		return domain.DeprovisionServiceSpec{}, brokerapi.ErrInstanceDoesNotExist
	}

	deploymentName := instance.PlanID + "-" + instanceID
	l.Info("Found deployment %s for instance %s", deploymentName, instanceID)
	l.Debug("Determined BOSH deployment name from plan ID and instance ID")

	manifest, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil || manifest.Manifest == "" {
		l.Debug("removing defunct service from vault index")
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove defunct service instance '%s' from vault: %s", instanceID, err)
		}

		/* return a 410 Gone to the caller */
		return domain.DeprovisionServiceSpec{}, brokerapi.ErrInstanceDoesNotExist
	}

	l.Info("Initiating deletion of BOSH deployment %s", deploymentName)
	l.Debug("Calling BOSH DeleteDeployment API")
	/* FIXME: what if we still have a valid task for deployment? */
	task, err := b.BOSH.DeleteDeployment(deploymentName)
	if err != nil {
		l.Error("Failed to delete BOSH deployment %s: %s", deploymentName, err)
		return domain.DeprovisionServiceSpec{}, err
	}
	l.Info("Delete operation started successfully, BOSH task ID: %d", task.ID)
	l.Debug("Task state: %s, description: %s", task.State, task.Description)

	l.Debug("removing service from vault 'db' index")
	if err := b.Vault.Index(instanceID, nil); err != nil {
		l.Error("failed to remove service '%s' from vault 'db' index: %s", instanceID, err)
	}

	l.Debug("updating service status in the vault")
	if err := b.Vault.Track(instanceID, "deprovision", task.ID, nil); err != nil {
		l.Error("failed to track deprovision BOSH task #%d in vault: %s", task.ID, err)
	}

	l.Debug("descheduling S.H.I.E.L.D. backup")
	err = b.Shield.DeleteSchedule(instanceID, details)
	if err != nil {
		l.Error("failed to deschedule S.H.I.E.L.D. backup for instance %s: %s", instanceID, err)
	}

	l.Info("started deprovisioning")
	return domain.DeprovisionServiceSpec{IsAsync: true}, nil
}

func (b *Broker) OnProvisionCompleted(
	l *Log,
	instanceID string,
) error {
	l.Debug("provision task was successfully completed; scheduling backup in S.H.I.E.L.D. if required")
	l.Debug("fetching instance provision details from Vault")
	var details brokerapi.ProvisionDetails
	exists, err := b.Vault.Get(instanceID, &details)
	if err != nil {
		l.Error("failed to fetch instance provision details from Vault: %s", err)
		return err
	}
	if !exists {
		return fmt.Errorf("could not find instance provision details in Vault (key: %s)", instanceID)
	}

	l.Debug("fetching deployment VMs metadata")
	deployment := details.PlanID + "-" + instanceID
	vms, err := b.BOSH.GetDeploymentVMs(deployment)
	if err != nil {
		l.Error("failed to fetch VMs metadata for deployment '%s': %s", deployment, err)
		return err
	}
	if len(vms) == 0 {
		return fmt.Errorf("could not find any running VM for the deployment %s", deployment)
	}
	if len(vms[0].IPs) == 0 {
		return fmt.Errorf("could not find any IP for the VM '%s' (deployment: %s)", vms[0].ID, deployment)
	}

	l.Debug("fetching instance plan details")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return err
	}

	l.Debug("fetching instance credentials directly from BOSH")
	creds, err := GetCreds(instanceID, plan, b.BOSH, l)
	if err != nil {
		return err
	}

	l.Debug("scheduling S.H.I.E.L.D. backup for instance '%s'", instanceID)
	err = b.Shield.CreateSchedule(instanceID, details, vms[0].IPs[0], creds)
	if err != nil {
		l.Error("failed to schedule S.H.I.E.L.D. backup: %s", err)
		return fmt.Errorf("Failed to schedule S.H.I.E.L.D. backup")
	}

	l.Debug("scheduling of S.H.I.E.L.D. backup for instance '%s' succesfully completed", instanceID)
	return nil
}

func (b *Broker) LastOperation(
	ctx context.Context,
	instanceID string,
	details domain.PollDetails,
) (
	domain.LastOperation,
	error,
) {
	l := Logger.Wrap("%s", instanceID)
	l.Debug("last-operation check received; checking state of service deployment")

	typ, taskID, _, _ := b.Vault.State(instanceID)
	l.Debug("instance was last in '%s' state, BOSH task %d", typ, taskID)

	if typ == "provision" {
		l.Debug("retrieving task %d from BOSH director", taskID)
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			l.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)
			return domain.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			if err := b.OnProvisionCompleted(l, instanceID); err != nil {
				return domain.LastOperation{}, fmt.Errorf("provision task was successfully completed but the post-hook failed")
			}

			l.Debug("provision operation succeeded")
			return domain.LastOperation{State: domain.Succeeded}, nil
		}
		if task.State == "error" {
			l.Error("provision operation failed!")
			return domain.LastOperation{State: domain.Failed}, nil
		}

		l.Debug("provision operation is still in progress")
		return domain.LastOperation{State: domain.InProgress}, nil
	}

	if typ == "deprovision" {
		l.Debug("retrieving task %d from BOSH director", taskID)
		task, err := b.BOSH.GetTask(taskID)
		if err != nil {
			l.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)
			return domain.LastOperation{}, fmt.Errorf("unrecognized backend BOSH task")
		}

		if task.State == "done" {
			l.Debug("deprovision operation succeeded")
			l.Debug("cleaning up secret/%s from the vault", instanceID)
			b.Vault.Clear(instanceID)
			return domain.LastOperation{State: domain.Succeeded}, nil
		}

		if task.State == "error" {
			l.Debug("deprovision operation failed!")
			l.Debug("cleaning up secret/%s from the vault", instanceID)
			b.Vault.Clear(instanceID)
			return domain.LastOperation{State: domain.Failed}, nil
		}

		l.Debug("deprovision operation is still in progress")
		return domain.LastOperation{State: domain.InProgress}, nil
	}

	l.Error("invalid state '%s' found in the vault", typ)
	return domain.LastOperation{}, fmt.Errorf("invalid state type '%s'", typ)
}

func (b *Broker) Bind(
	ctx context.Context,
	instanceID, bindingID string,
	details domain.BindDetails,
	asyncAllowed bool,
) (
	domain.Binding,
	error,
) {
	var binding domain.Binding

	l := Logger.Wrap("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID)
	l.Info("Starting bind operation for instance %s, binding %s", instanceID, bindingID)
	l.Debug("Bind details - Service: %s, Plan: %s, AppGUID: %s", details.ServiceID, details.PlanID, details.AppGUID)

	l.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)
	l.Debug("Searching in blacksmith catalog for binding plan")
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return binding, err
	}
	l.Debug("Found plan: %s", plan.Name)

	l.Info("Retrieving credentials for instance %s", instanceID)
	l.Debug("Calling GetCreds for plan %s", plan.Name)
	creds, err := GetCreds(instanceID, plan, b.BOSH, l)
	if err != nil {
		l.Error("Failed to retrieve credentials: %s", err)
		return binding, err
	}
	l.Debug("Successfully retrieved credentials")

	if m, ok := creds.(map[string]interface{}); ok {

		if apiUrl, ok := m["api_url"]; ok {
			adminUsername := m["admin_username"].(string)
			adminPassword := m["admin_password"].(string)
			vhost := m["vhost"].(string)

			usernameDynamic := bindingID
			passwordDynamic := uuid.New().String()
			usernameStatic := m["username"].(string)
			passwordStatic := m["password"].(string)

			l.Info("Creating dynamic RabbitMQ user for binding %s", bindingID)
			l.Debug("Creating user %s in RabbitMQ at %s", usernameDynamic, apiUrl)
			err := CreateUserPassRabbitMQ(usernameDynamic, passwordDynamic, adminUsername, adminPassword, apiUrl.(string))
			if err != nil {
				l.Error("Failed to create RabbitMQ user: %s", err)
				return binding, err
			}
			l.Debug("Successfully created RabbitMQ user %s", usernameDynamic)

			l.Debug("Granting permissions to user %s for vhost %s", usernameDynamic, vhost)
			err = GrantUserPermissionsRabbitMQ(usernameDynamic, adminUsername, adminPassword, vhost, apiUrl.(string))
			if err != nil {
				l.Error("Failed to grant permissions to RabbitMQ user %s: %s", usernameDynamic, err)
				return binding, err
			}
			l.Debug("Successfully granted permissions to user %s", usernameDynamic)

			creds, err = yamlGsub(creds, usernameStatic, usernameDynamic)
			if err != nil {
				return binding, err
			}

			creds, err = yamlGsub(creds, passwordStatic, passwordDynamic)
			if err != nil {
				return binding, err
			}
			m["username"] = usernameDynamic
			m["password"] = passwordDynamic
			m["credential_type"] = "dynamic"

		}
	}

	if m, ok := creds.(map[string]interface{}); ok {
		delete(m, "admin_username")
		delete(m, "admin_password")
	}

	binding.Credentials = creds
	l.Debug("credentials are: %v", binding)

	l.Info("Successfully completed bind operation for binding %s", bindingID)
	return binding, nil
}

func yamlGsub(
	obj interface{},
	orig string,
	replacement string,
) (
	interface{},
	error,
) {
	l := Logger.Wrap("yamlGsub")
	l.Debug("Performing YAML string substitution - orig: %s, replacement: %s", orig, replacement)

	m, err := yaml.Marshal(obj)
	if err != nil {
		l.Error("Failed to marshal object to YAML: %s", err)
		return nil, err
	}

	s := string(m)
	replaced := strings.Replace(s, orig, replacement, -1)
	l.Debug("Replaced %d occurrences", strings.Count(s, orig))

	var data map[interface{}]interface{}

	if err = yaml.Unmarshal([]byte(replaced), &data); err != nil {
		l.Error("Failed to unmarshal replaced YAML: %s", err)
		return nil, err
	}

	l.Debug("Successfully completed YAML substitution")
	return deinterfaceMap(data), nil
}

func CreateUserPassRabbitMQ(usernameDynamic, passwordDynamic, adminUsername, adminPassword, apiUrl string) error {
	l := Logger.Wrap("CreateUserPassRabbitMQ")
	l.Info("Creating RabbitMQ user: %s", usernameDynamic)
	l.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	payload := struct {
		Password string `json:"password"`
		Tags     string `json:"tags"`
	}{Password: passwordDynamic, Tags: "management,policymaker"}

	data, err := json.Marshal(payload)
	if err != nil {
		l.Error("Failed to marshal user creation payload: %s", err)
		return err
	}
	l.Debug("User creation payload: %s", string(data))

	createUrl := apiUrl + "/users/" + usernameDynamic
	l.Debug("Creating user at URL: %s", createUrl)

	request, err := http.NewRequest(http.MethodPut, createUrl, bytes.NewBuffer(data))
	if err != nil {
		l.Error("Failed to create HTTP request for user creation: %s", err)
		return err
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("content-type", "application/json")

	l.Debug("Sending PUT request to create user")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		l.Error("HTTP request failed for user creation: %s", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		l.Error("Failed to create user - Status: %d, Response: %s", resp.StatusCode, string(body))
		return fmt.Errorf("failed to create RabbitMQ user, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	l.Info("Successfully created RabbitMQ user: %s", usernameDynamic)
	return nil
}

func GrantUserPermissionsRabbitMQ(usernameDynamic, adminUsername, adminPassword, vhost, apiUrl string) error {
	l := Logger.Wrap("GrantUserPermissionsRabbitMQ")
	l.Info("Granting permissions to user %s on vhost %s", usernameDynamic, vhost)
	l.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	payload := struct {
		Configure string `json:"configure"`
		Write     string `json:"write"`
		Read      string `json:"read"`
	}{Configure: ".*", Write: ".*", Read: ".*"}

	data, err := json.Marshal(payload)
	if err != nil {
		l.Error("Failed to marshal permissions payload: %s", err)
		return err
	}
	l.Debug("Permissions payload: %s", string(data))

	permUrl := apiUrl + "/permissions/" + vhost + "/" + usernameDynamic
	l.Debug("Setting permissions at URL: %s", permUrl)

	request, err := http.NewRequest(http.MethodPut, permUrl, bytes.NewBuffer(data))
	if err != nil {
		l.Error("Failed to create HTTP request for permissions: %s", err)
		return err
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("content-type", "application/json")

	l.Debug("Sending PUT request to grant permissions")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		l.Error("HTTP request failed for granting permissions: %s", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		l.Error("Failed to grant permissions - Status: %d, Response: %s", resp.StatusCode, string(body))
		return fmt.Errorf("failed to grant RabbitMQ permissions, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	l.Info("Successfully granted permissions to user %s on vhost %s", usernameDynamic, vhost)
	return nil
}

func DeletetUserRabbitMQ(bindingID, adminUsername, adminPassword, apiUrl string) error {
	l := Logger.Wrap("DeleteUserRabbitMQ")
	l.Info("Deleting RabbitMQ user: %s", bindingID)
	l.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	deleteUrl := apiUrl + "/users/" + bindingID
	l.Debug("Deleting user at URL: %s", deleteUrl)

	request, err := http.NewRequest("DELETE", deleteUrl, nil)
	if err != nil {
		l.Error("Failed to create HTTP DELETE request: %s", err)
		return err
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("content-type", "application/json")

	l.Debug("Sending DELETE request to remove user")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		l.Error("HTTP request failed for user deletion: %s", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := ioutil.ReadAll(resp.Body)
		l.Error("Failed to delete user - Status: %d, Response: %s", resp.StatusCode, string(body))
		return fmt.Errorf("failed to delete RabbitMQ user, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	l.Info("Successfully deleted RabbitMQ user: %s", bindingID)
	return nil
}

func (b *Broker) Unbind(
	ctx context.Context,
	instanceID, bindingID string,
	details domain.UnbindDetails,
	asyncAllowed bool,
) (domain.UnbindSpec, error) {
	l := Logger.Wrap("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID)
	l.Info("Starting unbind operation for instance %s, binding %s", instanceID, bindingID)
	l.Debug("Unbind details - Service: %s, Plan: %s", details.ServiceID, details.PlanID)

	if strings.Contains(details.PlanID, "rabbitmq") {
		l.Info("Processing unbind for RabbitMQ service")
		l.Debug("RabbitMQ plan detected, will delete dynamic user")
		plan, err := b.FindPlan(details.ServiceID, details.PlanID)
		if err != nil {
			l.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
			return domain.UnbindSpec{}, err
		}
		l.Debug("Retrieving admin credentials for RabbitMQ instance")
		creds, err := GetCreds(instanceID, plan, b.BOSH, l)
		if err != nil {
			l.Error("Failed to retrieve credentials: %s", err)
			return domain.UnbindSpec{}, err
		}
		if m, ok := creds.(map[string]interface{}); ok {
			adminUsername := m["admin_username"].(string)
			adminPassword := m["admin_password"].(string)
			apiUrl := m["api_url"].(string)
			l.Info("Deleting dynamic RabbitMQ user %s", bindingID)
			l.Debug("Calling RabbitMQ API at %s to delete user", apiUrl)
			err = DeletetUserRabbitMQ(bindingID, adminUsername, adminPassword, apiUrl)
			if err != nil {
				l.Error("Failed to delete RabbitMQ user %s: %s", bindingID, err)
				return domain.UnbindSpec{}, err
			}
			l.Debug("Successfully deleted RabbitMQ user %s", bindingID)
		}
	}

	l.Info("Successfully completed unbind operation for binding %s", bindingID)
	return domain.UnbindSpec{}, nil
}

func (b *Broker) Update(
	ctx context.Context,
	instanceID string,
	details domain.UpdateDetails,
	asyncAllowed bool,
) (
	domain.UpdateServiceSpec,
	error,
) {
	l := Logger.Wrap("%s %s %s", instanceID, details.ServiceID, details.PlanID)
	l.Error("Update operation not implemented")
	l.Debug("Update request - InstanceID: %s, ServiceID: %s, CurrentPlanID: %s, NewPlanID: %s",
		instanceID, details.ServiceID, details.PlanID, details.PreviousValues.PlanID)
	l.Debug("Async allowed: %v, Raw parameters: %s", asyncAllowed, string(details.RawParameters))

	// FIXME: implement this!

	return domain.UpdateServiceSpec{}, fmt.Errorf("not implemented")
}

func (b *Broker) GetInstance(ctx context.Context, instanceID string) (domain.GetInstanceDetailsSpec, error) {
	l := Logger.Wrap("GetInstance")
	l.Debug("GetInstance called for instanceID: %s", instanceID)
	l.Info("GetInstance operation not implemented")
	// Not implemented - return empty spec
	return domain.GetInstanceDetailsSpec{}, fmt.Errorf("GetInstance not implemented")
}

func (b *Broker) GetBinding(ctx context.Context, instanceID, bindingID string) (domain.GetBindingSpec, error) {
	l := Logger.Wrap("GetBinding")
	l.Debug("GetBinding called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	l.Info("GetBinding operation not implemented")
	// Not implemented - return empty spec
	return domain.GetBindingSpec{}, fmt.Errorf("GetBinding not implemented")
}

func (b *Broker) LastBindingOperation(ctx context.Context, instanceID, bindingID string, details domain.PollDetails) (domain.LastOperation, error) {
	l := Logger.Wrap("LastBindingOperation")
	l.Debug("LastBindingOperation called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	l.Debug("Returning success immediately as async bindings are not supported")
	// Not implemented - return successful immediately since we don't support async bindings
	return domain.LastOperation{State: domain.Succeeded}, nil
}

func (b *Broker) serviceWithNoDeploymentCheck() (
	[]string,
	error,
) {
	l := Logger.Wrap("serviceWithNoDeploymentCheck")
	l.Info("Starting check for orphaned service instances with no backing BOSH deployment")

	l.Debug("Fetching all current deployments from BOSH director")
	deployments, err := b.BOSH.GetDeployments()
	if err != nil {
		l.Error("Failed to get deployments from BOSH director: %s", err)
		return nil, err
	}
	l.Debug("Found %d deployments in BOSH director", len(deployments))

	//turn deployments from a slice of deployments into a map of
	//string to bool because all we care about is the name of the deployment (the string here)
	deploymentNames := make(map[string]bool)
	for _, deployment := range deployments {
		deploymentNames[deployment.Name] = true
		l.Debug("Found BOSH deployment: %s", deployment.Name)
	}
	//grab the vault DB json blob out of vault
	l.Debug("Fetching vault DB index to check for service instances")
	vaultDB, err := b.Vault.getVaultDB()
	if err != nil {
		l.Error("Failed to get vault DB index: %s", err)
		return nil, err
	}
	l.Debug("Found %d service instances in vault DB", len(vaultDB.Data))

	//loop through all current instances in the "db"
	//check bosh director for each instance in the "db"
	var removedDeploymentNames []string
	l.Debug("Checking each service instance for corresponding BOSH deployment")
	for instanceID, serviceInstance := range vaultDB.Data {
		l.Debug("Checking instance: %s", instanceID)
		if ss, ok := serviceInstance.(map[string]interface{}); ok {
			service, ok := ss["service_id"].(string)
			if !ok {
				l.Error("Could not parse service_id for instance %s - value type: %T", instanceID, ss["service_id"])
				return nil, errors.New("could not assert service id to string")
			}
			l.Debug("Instance %s - service_id: %s", instanceID, service)

			plan, ok := ss["plan_id"].(string)
			if !ok {
				l.Error("Could not parse plan_id for instance %s - value type: %T", instanceID, ss["plan_id"])
				return nil, errors.New("could not assert plan id to string")
			}
			l.Debug("Instance %s - plan_id: %s", instanceID, plan)

			currentDeployment := plan + "-" + instanceID
			l.Debug("Looking for deployment: %s", currentDeployment)

			//deployments are named as instance.PlanID + "-" + instanceID
			if _, ok := deploymentNames[currentDeployment]; !ok {
				//if the deployment name isn't listed in our director then delete it from vault
				l.Info("Found orphaned instance %s - no BOSH deployment named: %s", instanceID, currentDeployment)
				removedDeploymentNames = append(removedDeploymentNames, currentDeployment)
				l.Info("Removing orphaned service instance %s from vault DB", instanceID)
				if err := b.Vault.Index(instanceID, nil); err != nil {
					l.Error("Failed to remove orphaned service instance '%s' from vault DB: %s", instanceID, err)
				} else {
					l.Debug("Successfully removed orphaned instance %s from vault DB", instanceID)
				}
			} else {
				l.Debug("Instance %s has valid BOSH deployment %s", instanceID, currentDeployment)
			}
		} else {
			l.Error("Could not parse service instance data for %s - unexpected type: %T", instanceID, serviceInstance)
		}
	}
	l.Info("Orphan check complete - removed %d orphaned instances", len(removedDeploymentNames))
	return removedDeploymentNames, nil
}
