package bosh

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Handler handles BOSH-related endpoints.
type Handler struct {
	logger    interfaces.Logger
	config    interfaces.Config
	vault     interfaces.Vault
	director  interfaces.Director
	broker    interfaces.Broker
	vmMonitor interfaces.VMMonitor
}

// Dependencies contains all dependencies needed by the BOSH handler.
type Dependencies struct {
	Logger    interfaces.Logger
	Config    interfaces.Config
	Vault     interfaces.Vault
	Director  interfaces.Director
	Broker    interfaces.Broker
	VMMonitor interfaces.VMMonitor
}

// NewHandler creates a new BOSH handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:    deps.Logger,
		config:    deps.Config,
		vault:     deps.Vault,
		director:  deps.Director,
		broker:    deps.Broker,
		vmMonitor: deps.VMMonitor,
	}
}

// GetPoolStats returns BOSH pool statistics.
func (h *Handler) GetPoolStats(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("bosh-pool-stats")
	logger.Debug("BOSH pool stats request")

	// Get pool statistics from director
	poolStats, err := h.director.GetPoolStats()
	if err != nil {
		logger.Error("Failed to get pool stats: %v", err)
		// Return basic stats on error
		stats := map[string]interface{}{
			"total_connections":  1,
			"active_connections": 0,
			"queued_requests":    0,
			"rejected_requests":  0,
			"total_requests":     0,
			"avg_wait_time_ms":   0,
			"timestamp":          time.Now().Unix(),
		}
		response.HandleJSON(responseWriter, stats, nil)

		return
	}

	stats := map[string]interface{}{
		"total_connections":  poolStats.MaxConnections,
		"active_connections": poolStats.ActiveConnections,
		"queued_requests":    poolStats.QueuedRequests,
		"rejected_requests":  poolStats.RejectedRequests,
		"total_requests":     poolStats.TotalRequests,
		"avg_wait_time_ms":   poolStats.AvgWaitTime.Milliseconds(),
		"timestamp":          time.Now().Unix(),
	}

	response.HandleJSON(responseWriter, stats, nil)
}

// GetStemcells returns available stemcells from the BOSH director.
func (h *Handler) GetStemcells(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("bosh-stemcells")
	logger.Debug("BOSH stemcells request")

	stemcells, err := h.director.GetStemcells()
	if err != nil {
		logger.Error("Failed to get stemcells: %v", err)
		response.HandleJSON(responseWriter, nil, err)
		return
	}

	// Return the 10 most recent stemcells per OS
	result := filterRecentStemcells(stemcells, 10)

	logger.Debug("Returning %d stemcells", len(result))
	response.HandleJSON(responseWriter, result, nil)
}

// filterRecentStemcells returns the N most recent stemcells per OS.
// It filters to only include stemcells from CPIs ending in ".bosh" suffix
// (e.g., "env-name.aws.bosh") to avoid duplicates from legacy CPIs.
func filterRecentStemcells(stemcells []bosh.Stemcell, limit int) []bosh.Stemcell {
	// Group by OS, filtering to only include stemcells with CPI ending in ".bosh"
	byOS := make(map[string][]bosh.Stemcell)
	for _, s := range stemcells {
		// Only include stemcells from CPIs with ".bosh" suffix
		if !strings.HasSuffix(s.CPI, ".bosh") {
			continue
		}
		byOS[s.OS] = append(byOS[s.OS], s)
	}

	// Sort each group by version descending and take top N
	var result []bosh.Stemcell
	for _, group := range byOS {
		// Sort by version descending (string comparison works for semver-like versions)
		sort.Slice(group, func(i, j int) bool {
			return group[i].Version > group[j].Version
		})

		// Take up to limit
		count := limit
		if len(group) < limit {
			count = len(group)
		}
		result = append(result, group[:count]...)
	}

	return result
}

// GetStatus returns BOSH/Blacksmith status information including service instances.
func (h *Handler) GetStatus(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("bosh-status")
	logger.Debug("BOSH status request")

	ctx := req.Context()

	// Service instances are ONLY tracked in Vault 'db' index
	// Do not fall back to BOSH deployments as they may include non-service infrastructure deployments
	instances := h.loadServiceInstances(ctx, logger)

	// Build plan metadata from broker catalog
	plans := make(map[string]map[string]interface{})

	if h.broker != nil {
		for key, plan := range h.broker.GetPlans() {
			planSummary := map[string]interface{}{
				"id":          plan.ID,
				"name":        plan.Name,
				"description": plan.Description,
				"limit":       plan.Limit,
				"type":        plan.Type,
			}

			if plan.Service != nil {
				planSummary["service"] = map[string]interface{}{
					"id":          plan.Service.ID,
					"name":        plan.Service.Name,
					"description": plan.Service.Description,
					"limit":       plan.Service.Limit,
				}
			}

			plans[key] = planSummary
		}
	} else {
		logger.Debug("Broker dependency is not configured; plans data will be empty")
	}

	// Compose the status payload
	environment := h.config.GetEnvironment()
	status := map[string]interface{}{
		"healthy":     true,
		"bosh":        "connected",
		"vault":       "connected",
		"environment": environment,
		"env":         environment,
		"version":     "0.0.0",
		"timestamp":   time.Now().Unix(),
		"instances":   instances,
		"plans":       plans,
	}

	response.HandleJSON(responseWriter, status, nil)
}

// loadServiceInstances loads service instances from Vault and enriches them with VM status.
func (h *Handler) loadServiceInstances(ctx context.Context, logger interfaces.Logger) map[string]interface{} {
	instances := make(map[string]interface{})

	if h.vault == nil {
		logger.Debug("Vault dependency is not configured; no service instances available")

		return instances
	}

	vaultInstances, err := h.getInstancesFromVault(ctx)
	if err != nil {
		logger.Error("Failed to fetch instances from Vault: %v", err)

		return instances
	}

	if vaultInstances == nil {
		logger.Debug("Vault index 'db' does not exist; no service instances available")

		return instances
	}

	instances = vaultInstances
	logger.Debug("Loaded %d instances from Vault index", len(instances))

	// Enrich instances with full data including instance_name
	h.enrichInstancesWithFullData(ctx, instances, logger)
	h.enrichInstancesWithVMStatus(ctx, instances, logger)

	return instances
}

// getInstancesFromVault retrieves instances from Vault db index.
func (h *Handler) getInstancesFromVault(ctx context.Context) (map[string]interface{}, error) {
	vaultInstances := make(map[string]interface{})

	exists, err := h.vault.Get(ctx, "db", &vaultInstances)
	if err != nil {
		return nil, fmt.Errorf("failed to get instances from vault: %w", err)
	}

	if !exists {
		return make(map[string]interface{}), nil
	}

	return vaultInstances, nil
}

// enrichInstancesWithFullData enriches instances with full vault data including instance_name.
func (h *Handler) enrichInstancesWithFullData(ctx context.Context, instances map[string]interface{}, logger interfaces.Logger) {
	logger.Debug("Enriching instances with full vault data")

	for instanceID, instanceData := range instances {
		// Get full instance data from vault
		var fullData map[string]interface{}

		exists, err := h.vault.Get(ctx, instanceID, &fullData)
		if err != nil {
			logger.Debug("Failed to get full data for %s: %v", instanceID, err)

			continue
		}

		if !exists {
			logger.Debug("No full data found for instance %s", instanceID)

			continue
		}

		// Merge the full data into the instance data
		if instanceMap, ok := instanceData.(map[string]interface{}); ok {
			// Add instance_name if it exists in full data
			if instanceName, ok := fullData["instance_name"].(string); ok && instanceName != "" {
				instanceMap["instance_name"] = instanceName
				logger.Debug("Added instance_name '%s' for instance %s", instanceName, instanceID)
			}

			// Add any other important fields from full data that might be missing
			if deploymentName, ok := fullData["deployment_name"].(string); ok && deploymentName != "" {
				instanceMap["deployment_name"] = deploymentName
			}
		}
	}
}

// enrichInstancesWithVMStatus adds VM health status to service instances.
func (h *Handler) enrichInstancesWithVMStatus(ctx context.Context, instances map[string]interface{}, logger interfaces.Logger) {
	if h.vmMonitor == nil {
		logger.Debug("VMMonitor not available; skipping VM health enrichment")

		return
	}

	logger.Debug("Enriching instances with VM health status")

	for instanceID, instanceData := range instances {
		vmStatus, err := h.vmMonitor.GetServiceVMStatus(ctx, instanceID)
		if err != nil {
			logger.Debug("No VM status for %s: %v", instanceID, err)

			continue
		}

		if vmStatus == nil {
			continue
		}

		logger.Debug("VM status for %s: %+v", instanceID, vmStatus)

		h.updateInstanceWithVMStatus(instanceID, instanceData, vmStatus, instances)
	}
}

// updateInstanceWithVMStatus updates an instance with VM status information.
func (h *Handler) updateInstanceWithVMStatus(instanceID string, instanceData interface{}, vmStatus interface{}, instances map[string]interface{}) {
	instanceMap, ok := instanceData.(map[string]interface{})
	if !ok {
		return
	}

	// Type assertion for vmStatus - using VMStatus struct from vmmonitor package
	type VMStatus struct {
		Status      string    `json:"status"`
		VMCount     int       `json:"vm_count"`
		HealthyVMs  int       `json:"healthy_vms"`
		LastUpdated time.Time `json:"last_updated"`
	}

	if status, ok := vmStatus.(*VMStatus); ok {
		instanceMap["vm_status"] = status.Status
		instanceMap["vm_count"] = status.VMCount
		instanceMap["vm_healthy"] = status.HealthyVMs
		instanceMap["vm_last_updated"] = status.LastUpdated.Unix()
	}

	instances[instanceID] = instanceMap
}
