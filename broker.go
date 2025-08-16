package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/shield"
	"github.com/google/uuid"
	"github.com/pivotal-cf/brokerapi/v8/domain"
	"github.com/pivotal-cf/brokerapi/v8/domain/apiresponses"
	"gopkg.in/yaml.v2"
)

type Broker struct {
	Catalog []domain.Service
	Plans   map[string]Plan
	BOSH    bosh.Director
	Vault   *Vault
	Shield  shield.Client
	Config  *Config
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
	err := os.WriteFile(filename, data, 0600)
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
	err = os.WriteFile(filename, b, 0600)
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

	var serviceDirs []string
	var err error

	// If no directories provided and auto-scan is enabled, scan for forge directories
	if len(dir) == 0 && b.Config.Forges.AutoScan {
		l.Info("No service directories provided, using auto-scan for forge directories")
		serviceDirs, err = AutoScanForgeDirectories(b.Config)
		if err != nil {
			l.Error("Auto-scan failed: %s", err)
			return fmt.Errorf("failed to auto-scan forge directories: %s", err)
		}
		if len(serviceDirs) == 0 {
			l.Error("Auto-scan found no forge directories")
			return fmt.Errorf("no forge directories found via auto-scan")
		}
	} else if len(dir) == 0 {
		l.Error("No service directories provided and auto-scan is disabled")
		return fmt.Errorf("no service directories provided")
	} else {
		serviceDirs = dir
	}

	l.Debug("Calling ReadServices to read service definitions from %d directories", len(serviceDirs))
	ss, err := ReadServices(serviceDirs...)
	if err != nil {
		l.Error("Failed to read services: %s", err)
		l.Debug("Error occurred while reading from directories: %v", serviceDirs)
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

	// Check if async is allowed
	if !asyncAllowed {
		l.Error("Async operations required but not allowed by client")
		return spec, fmt.Errorf("this service broker requires async operations")
	}

	l.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)
	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		l.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return spec, err
	}
	l.Debug("Found plan: %s (limit: %d)", plan.Name, plan.Limit)

	// Check service limits
	l.Debug("retrieving vault 'db' index (for tracking service usage)")
	db, err := b.Vault.GetIndex("db")
	if err != nil {
		l.Error("failed to get 'db' index out of the vault: %s", err)
		return spec, err
	}

	l.Info("Checking service limits for plan %s", plan.Name)
	if plan.OverLimit(db) {
		l.Error("Service limit exceeded for %s/%s (limit: %d)", plan.Service.Name, plan.Name, plan.Limit)
		return spec, apiresponses.ErrPlanQuotaExceeded
	}
	l.Debug("Service limit check passed")

	// Store instance in index immediately with requested_at timestamp
	l.Debug("tracking service instance in the vault 'db' index")
	now := time.Now()
	err = b.Vault.Index(instanceID, map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      plan.ID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
	})
	if err != nil {
		l.Error("failed to track new service in the vault index: %s", err)
		return spec, fmt.Errorf("failed to track new service in Vault")
	}

	// Create deployment name
	deploymentName := fmt.Sprintf("%s-%s", details.PlanID, instanceID)
	l.Debug("deployment name: %s", deploymentName)

	// Store details at the deployment name path
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)
	l.Debug("storing details at Vault path: %s", vaultPath)

	// Store only the details object at the deployment path
	err = b.Vault.Put(vaultPath, map[string]interface{}{
		"details": details,
	})
	if err != nil {
		l.Error("failed to store details in the vault at path %s: %s", vaultPath, err)
		// Remove from index since we're failing
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove instance from index: %s", err)
		}
		return spec, fmt.Errorf("failed to store service metadata")
	}

	// Build deployment info to store at instance level
	deploymentInfo := map[string]interface{}{
		"requested_at":      time.Now().Format(time.RFC3339),
		"organization_guid": details.OrganizationGUID,
		"space_guid":        details.SpaceGUID,
		"service_id":        details.ServiceID,
		"plan_id":           details.PlanID,
		"deployment_name":   deploymentName,
		"instance_id":       instanceID,
	}

	// Parse context if available to get additional details
	if len(details.RawContext) > 0 {
		var contextData map[string]interface{}
		if err := json.Unmarshal(details.RawContext, &contextData); err == nil {
			// Add context fields if they exist
			if orgName, ok := contextData["organization_name"].(string); ok {
				deploymentInfo["organization_name"] = orgName
			}
			if spaceName, ok := contextData["space_name"].(string); ok {
				deploymentInfo["space_name"] = spaceName
			}
			if instanceName, ok := contextData["instance_name"].(string); ok {
				deploymentInfo["instance_name"] = instanceName
			}
			if platform, ok := contextData["platform"].(string); ok {
				deploymentInfo["platform"] = platform
			}
		}
	}

	// Store deployment info at instance level
	l.Debug("storing deployment info at instance level: %s/deployment", instanceID)
	err = b.Vault.Put(fmt.Sprintf("%s/deployment", instanceID), deploymentInfo)
	if err != nil {
		l.Error("failed to store deployment info at instance level: %s", err)
		// Continue anyway, this is not fatal
	}

	// Also store flattened data at root instance path for backward compatibility
	l.Debug("storing flattened data at root instance path: %s", instanceID)
	rootData := map[string]interface{}{
		"requested_at":      time.Now().Format(time.RFC3339),
		"organization_guid": details.OrganizationGUID,
		"space_guid":        details.SpaceGUID,
		"service_id":        details.ServiceID,
		"plan_id":           details.PlanID,
		"deployment_name":   deploymentName,
		"instance_id":       instanceID,
	}

	// Add context fields if available
	if len(details.RawContext) > 0 {
		var contextData map[string]interface{}
		if err := json.Unmarshal(details.RawContext, &contextData); err == nil {
			if orgName, ok := contextData["organization_name"].(string); ok {
				rootData["organization_name"] = orgName
			}
			if spaceName, ok := contextData["space_name"].(string); ok {
				rootData["space_name"] = spaceName
			}
			if instanceName, ok := contextData["instance_name"].(string); ok {
				rootData["instance_name"] = instanceName
			}
			if platform, ok := contextData["platform"].(string); ok {
				rootData["platform"] = platform
			}
		}
		rootData["context"] = contextData
	}

	// Add raw parameters if present
	if len(details.RawParameters) > 0 {
		var paramsData interface{}
		if err := json.Unmarshal(details.RawParameters, &paramsData); err == nil {
			rootData["parameters"] = paramsData
		}
	}

	err = b.Vault.Put(instanceID, rootData)
	if err != nil {
		l.Error("failed to store data at root instance path: %s", err)
		// Continue anyway, this is not fatal
	}

	// Write data files for debugging/audit
	if len(details.RawParameters) > 0 {
		if err := WriteDataFile(instanceID, details.RawParameters); err != nil {
			l.Error("failed to write data file for debugging: %s", err)
		}
		if err := WriteYamlFile(instanceID, details.RawParameters); err != nil {
			l.Error("failed to write YAML file for debugging: %s", err)
		}
	}

	// Store plan file SHA256 references for this instance
	l.Debug("storing plan file references for instance %s", instanceID)
	planStorage := NewPlanStorage(b.Vault, b.Config)
	if err := planStorage.StorePlanReferences(instanceID, plan); err != nil {
		l.Error("failed to store plan references: %s", err)
		// Continue anyway, this is not fatal for provisioning
	}

	// Convert details to a map for the async function
	detailsMap := map[string]interface{}{
		"service_id":        details.ServiceID,
		"plan_id":           details.PlanID,
		"organization_guid": details.OrganizationGUID,
		"space_guid":        details.SpaceGUID,
		"raw_parameters":    details.RawParameters,
	}

	// Launch async provisioning in background
	go b.provisionAsync(instanceID, detailsMap, plan)

	l.Info("Accepted provisioning request for service instance %s", instanceID)
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

	// Check if async is allowed
	if !asyncAllowed {
		l.Error("Async operations required but not allowed by client")
		return domain.DeprovisionServiceSpec{}, fmt.Errorf("this service broker requires async operations")
	}

	// Check if instance exists
	instance, exists, err := b.Vault.FindInstance(instanceID)
	if err != nil {
		l.Error("unable to retrieve instance details from vault index: %s", err)
		return domain.DeprovisionServiceSpec{}, err
	}
	if !exists {
		l.Debug("Instance not found in vault index")
		/* return a 410 Gone to the caller */
		return domain.DeprovisionServiceSpec{}, apiresponses.ErrInstanceDoesNotExist
	}

	// Store delete_requested_at timestamp
	l.Debug("storing delete_requested_at timestamp in Vault")
	deleteRequestedAt := time.Now()

	// Get existing metadata
	var metadata map[string]interface{}
	exists, err = b.Vault.Get(fmt.Sprintf("%s/metadata", instanceID), &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add delete_requested_at
	metadata["delete_requested_at"] = deleteRequestedAt.Format(time.RFC3339)

	// Store updated metadata
	err = b.Vault.Put(fmt.Sprintf("%s/metadata", instanceID), metadata)
	if err != nil {
		l.Error("failed to store delete_requested_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Deschedule SHIELD backup early (synchronous but fast)
	l.Debug("descheduling S.H.I.E.L.D. backup")
	err = b.Shield.DeleteSchedule(instanceID, details)
	if err != nil {
		l.Error("failed to deschedule S.H.I.E.L.D. backup for instance %s: %s", instanceID, err)
		// Continue anyway, this is non-fatal
	}

	// Launch async deprovisioning in background
	go b.deprovisionAsync(instanceID, instance)

	l.Info("Accepted deprovisioning request for service instance %s", instanceID)
	return domain.DeprovisionServiceSpec{IsAsync: true}, nil
}

func (b *Broker) OnProvisionCompleted(
	l *Log,
	instanceID string,
) error {
	l.Debug("provision task was successfully completed; scheduling backup in S.H.I.E.L.D. if required")
	l.Debug("fetching instance provision details from Vault")

	// First, update the instance with created_at timestamp
	l.Debug("updating instance with created_at timestamp")
	createdAt := time.Now()

	// Get existing metadata to preserve history and other fields
	var metadata map[string]interface{}
	exists, err := b.Vault.Get(fmt.Sprintf("%s/metadata", instanceID), &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add created_at to existing metadata
	metadata["created_at"] = createdAt.Format(time.RFC3339)

	// Store updated metadata
	err = b.Vault.Put(fmt.Sprintf("%s/metadata", instanceID), metadata)
	if err != nil {
		l.Error("failed to store created_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Update the index with created_at
	l.Debug("updating index with created_at timestamp")
	idx, err := b.Vault.GetIndex("db")
	if err == nil {
		if raw, err := idx.Lookup(instanceID); err == nil {
			if data, ok := raw.(map[string]interface{}); ok {
				data["created_at"] = createdAt.Format(time.RFC3339)
				if err := b.Vault.Index(instanceID, data); err != nil {
					l.Error("failed to index service instance: %s", err)
				}
			}
		}
	}

	// Get the metadata which now includes details wrapped
	// First try to get details from index to construct the deployment name
	instance, exists, err := b.Vault.FindInstance(instanceID)
	if err != nil || !exists {
		l.Error("could not find instance in vault index: %s", err)
		return fmt.Errorf("could not find instance in vault index")
	}

	// Construct the vault path with deployment name
	deploymentName := instance.PlanID + "-" + instanceID
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)

	var detailsMetadata map[string]interface{}
	exists, err = b.Vault.Get(vaultPath, &detailsMetadata)
	if err != nil {
		// Try legacy path for backward compatibility
		l.Debug("failed to fetch from new path, trying legacy path")
		exists, err = b.Vault.Get(instanceID, &detailsMetadata)
		if err != nil {
			l.Error("failed to fetch instance metadata from Vault: %s", err)
			return err
		}
	}
	if !exists {
		return fmt.Errorf("could not find instance metadata in Vault (path: %s)", vaultPath)
	}

	// Extract the details from metadata
	var details domain.ProvisionDetails
	if detailsData, ok := detailsMetadata["details"]; ok {
		// Convert the details back to ProvisionDetails struct
		if detailsMap, ok := detailsData.(map[string]interface{}); ok {
			if serviceID, ok := detailsMap["service_id"].(string); ok {
				details.ServiceID = serviceID
			}
			if planID, ok := detailsMap["plan_id"].(string); ok {
				details.PlanID = planID
			}
		}
	} else {
		// Fallback: try to read as old format
		_, err = b.Vault.Get(instanceID, &details)
		if err != nil {
			return fmt.Errorf("could not parse instance provision details from Vault")
		}
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

	// Store credentials in vault
	l.Debug("storing credentials in vault at %s/credentials", instanceID)
	err = b.Vault.Put(fmt.Sprintf("%s/credentials", instanceID), creds)
	if err != nil {
		l.Error("failed to store credentials in vault: %s", err)
		// Continue anyway as this is non-fatal
	}

	l.Debug("scheduling S.H.I.E.L.D. backup for instance '%s'", instanceID)
	err = b.Shield.CreateSchedule(instanceID, details, vms[0].IPs[0], creds)
	if err != nil {
		l.Error("failed to schedule S.H.I.E.L.D. backup: %s", err)
		return fmt.Errorf("failed to schedule S.H.I.E.L.D. backup")
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

	// Get the deployment name for this instance
	instance, exists, err := b.Vault.FindInstance(instanceID)
	if err != nil {
		l.Error("could not find instance details: %s", err)
		return domain.LastOperation{}, err
	}
	if !exists {
		l.Error("instance %s not found in vault index", instanceID)
		// Return "gone" status for instances that no longer exist
		// This tells CF the operation is complete and the instance is gone
		return domain.LastOperation{
			State:       domain.Succeeded,
			Description: "Instance has been deleted",
		}, nil
	}

	deploymentName := instance.PlanID + "-" + instanceID
	l.Debug("checking deployment: %s", deploymentName)

	// Get the latest task for this deployment from BOSH events
	latestTask, operationType, err := b.GetLatestDeploymentTask(deploymentName)
	if err != nil {
		l.Error("failed to get latest task for deployment %s: %s", deploymentName, err)
		// If we can't get task info, check if deployment exists
		_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
		if deploymentErr != nil {
			// Deployment doesn't exist, assume in progress or failed
			l.Debug("deployment %s does not exist", deploymentName)
			return domain.LastOperation{State: domain.InProgress}, nil
		}
		// Deployment exists but no tasks found - assume succeeded
		l.Debug("deployment %s exists but no recent tasks found", deploymentName)
		return domain.LastOperation{State: domain.Succeeded}, nil
	}

	taskID := latestTask.ID
	typ := operationType
	l.Debug("latest task for deployment %s: task %d, type '%s', state '%s'", deploymentName, taskID, typ, latestTask.State)

	// Handle special task IDs
	if taskID == 0 {
		// Task ID 0 means the task was never properly recorded
		// This can happen with old deployments before the fix
		// Check if the deployment actually exists to determine success
		l.Debug("task ID is 0, checking if deployment exists")

		// Get instance details from vault to find plan ID
		instance, exists, err := b.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("could not find instance details for task ID 0")
			return domain.LastOperation{State: domain.Failed}, nil
		}

		deploymentName := instance.PlanID + "-" + instanceID
		l.Debug("checking for deployment: %s", deploymentName)

		// Check if deployment exists
		_, err = b.BOSH.GetDeployment(deploymentName)
		if err != nil {
			// Deployment doesn't exist, still in progress or failed
			l.Debug("deployment %s does not exist, operation still in progress", deploymentName)
			return domain.LastOperation{State: domain.InProgress}, nil
		}

		// Deployment exists with task ID 0 - this is a completed deployment from before the fix
		l.Info("deployment %s exists with task ID 0, marking as succeeded", deploymentName)

		// Run post-provision hook if this was a provision operation
		if typ == "provision" {
			if err := b.OnProvisionCompleted(l, instanceID); err != nil {
				l.Error("provision succeeded but post-hook failed: %s", err)
				// Don't fail the operation, just log the error
			}
		}

		return domain.LastOperation{State: domain.Succeeded}, nil
	}
	if taskID == -1 {
		// Task failed during initialization
		l.Error("operation failed during initialization")
		return domain.LastOperation{State: domain.Failed}, nil
	}
	if taskID == 1 {
		// Placeholder task for new deployment creation
		// Check if the deployment actually exists now
		l.Debug("checking status of new deployment creation")

		// Get instance details from vault to find plan ID
		instance, exists, err := b.Vault.FindInstance(instanceID)
		if err != nil || !exists {
			l.Error("could not find instance details for placeholder task")
			return domain.LastOperation{State: domain.Failed}, nil
		}

		deploymentName := instance.PlanID + "-" + instanceID
		_, err = b.BOSH.GetDeployment(deploymentName)
		if err != nil {
			// Still being created
			l.Debug("deployment %s still being created", deploymentName)
			return domain.LastOperation{State: domain.InProgress}, nil
		}
		// Deployment exists, mark as succeeded
		l.Debug("deployment %s created successfully", deploymentName)

		// Run post-provision hook
		if err := b.OnProvisionCompleted(l, instanceID); err != nil {
			return domain.LastOperation{}, fmt.Errorf("provision task was successfully completed but the post-hook failed")
		}

		return domain.LastOperation{State: domain.Succeeded}, nil
	}

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

			// Store deleted_at timestamp
			l.Debug("storing deleted_at timestamp in Vault")
			deletedAt := time.Now()

			// Get existing metadata
			var metadata map[string]interface{}
			exists2, err := b.Vault.Get(fmt.Sprintf("%s/metadata", instanceID), &metadata)
			if err != nil || !exists2 {
				metadata = make(map[string]interface{})
			}

			// Add deleted_at
			metadata["deleted_at"] = deletedAt.Format(time.RFC3339)

			// Store updated metadata
			err = b.Vault.Put(fmt.Sprintf("%s/metadata", instanceID), metadata)
			if err != nil {
				l.Error("failed to store deleted_at timestamp: %s", err)
				// Continue anyway, this is non-fatal
			}

			// Update the index with deleted_at (but don't remove it)
			l.Debug("updating index with deleted_at timestamp")
			idx, err := b.Vault.GetIndex("db")
			if err == nil {
				if raw, err := idx.Lookup(instanceID); err == nil {
					if data, ok := raw.(map[string]interface{}); ok {
						data["deleted_at"] = deletedAt.Format(time.RFC3339)
						data["deleted"] = true // Mark as deleted but keep in index
						if err := b.Vault.Index(instanceID, data); err != nil {
							l.Error("failed to update index with deleted status: %s", err)
						}
					}
				}
			}

			l.Debug("keeping secrets in vault for audit purposes")
			// Note: We intentionally do NOT call b.Vault.Clear(instanceID) here
			// to preserve secrets for auditing purposes
			return domain.LastOperation{State: domain.Succeeded}, nil
		}

		if task.State == "error" {
			l.Debug("deprovision operation failed!")
			l.Debug("keeping secrets in vault for audit purposes")
			// Note: We intentionally do NOT call b.Vault.Clear(instanceID) here
			// to preserve secrets for auditing purposes
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
		body, _ := io.ReadAll(resp.Body)
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
		body, _ := io.ReadAll(resp.Body)
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
		body, _ := io.ReadAll(resp.Body)
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

func (b *Broker) GetInstance(ctx context.Context, instanceID string, details domain.FetchInstanceDetails) (domain.GetInstanceDetailsSpec, error) {
	l := Logger.Wrap("GetInstance")
	l.Debug("GetInstance called for instanceID: %s", instanceID)
	l.Info("GetInstance operation not implemented")
	// Not implemented - return empty spec
	return domain.GetInstanceDetailsSpec{}, fmt.Errorf("GetInstance not implemented")
}

func (b *Broker) GetBinding(ctx context.Context, instanceID, bindingID string, details domain.FetchBindingDetails) (domain.GetBindingSpec, error) {
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

// GetLatestDeploymentTask retrieves the most recent task for a deployment from BOSH
func (b *Broker) GetLatestDeploymentTask(deploymentName string) (*bosh.Task, string, error) {
	l := Logger.Wrap("GetLatestDeploymentTask %s", deploymentName)

	// Get events for this deployment to find task IDs
	events, err := b.BOSH.GetEvents(deploymentName)
	if err != nil {
		l.Error("failed to get events for deployment %s: %s", deploymentName, err)
		return nil, "", fmt.Errorf("failed to get events for deployment %s: %w", deploymentName, err)
	}

	// Extract unique task IDs from events and determine the latest one
	taskIDs := make(map[int]bool)
	var maxTaskID int

	for _, event := range events {
		if event.TaskID != "" {
			// Parse task ID from string to int
			if taskID, err := strconv.Atoi(event.TaskID); err == nil {
				taskIDs[taskID] = true
				if taskID > maxTaskID {
					maxTaskID = taskID
				}
			}
		}
	}

	if maxTaskID == 0 {
		l.Debug("no task IDs found in events for deployment %s", deploymentName)
		return nil, "", fmt.Errorf("no tasks found for deployment %s", deploymentName)
	}

	l.Debug("found %d unique task IDs in events, latest is %d", len(taskIDs), maxTaskID)

	// Get the latest task details
	latestTask, err := b.BOSH.GetTask(maxTaskID)
	if err != nil {
		l.Error("failed to get task %d: %s", maxTaskID, err)
		return nil, "", fmt.Errorf("failed to get task %d: %w", maxTaskID, err)
	}

	// Determine operation type from task description
	desc := strings.ToLower(latestTask.Description)
	var operationType string
	if strings.Contains(desc, "delet") || strings.Contains(desc, "deprovision") {
		operationType = "deprovision"
	} else if strings.Contains(desc, "creat") || strings.Contains(desc, "deploy") || strings.Contains(desc, "provision") {
		operationType = "provision"
	} else if strings.Contains(desc, "update") {
		operationType = "update"
	} else {
		// Default to provision for other operations
		operationType = "provision"
	}

	l.Debug("latest task for deployment %s: task %d (%s) - %s", deploymentName, latestTask.ID, operationType, latestTask.Description)

	return latestTask, operationType, nil
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
