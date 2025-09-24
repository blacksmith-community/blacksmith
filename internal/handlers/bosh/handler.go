package bosh

import (
	"net/http"
	"strings"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Handler handles BOSH-related endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	vault    interfaces.Vault
	director interfaces.Director
	broker   interfaces.Broker
}

// Dependencies contains all dependencies needed by the BOSH handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Vault    interfaces.Vault
	Director interfaces.Director
	Broker   interfaces.Broker
}

// NewHandler creates a new BOSH handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:   deps.Logger,
		config:   deps.Config,
		vault:    deps.Vault,
		director: deps.Director,
		broker:   deps.Broker,
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
			"total_connections":   1,
			"active_connections":  0,
			"queued_requests":     0,
			"rejected_requests":   0,
			"total_requests":      0,
			"avg_wait_time_ms":    0,
			"timestamp":           time.Now().Unix(),
		}
		response.HandleJSON(responseWriter, stats, nil)
		return
	}

	stats := map[string]interface{}{
		"total_connections":   poolStats.MaxConnections,
		"active_connections":  poolStats.ActiveConnections,
		"queued_requests":     poolStats.QueuedRequests,
		"rejected_requests":   poolStats.RejectedRequests,
		"total_requests":      poolStats.TotalRequests,
		"avg_wait_time_ms":    poolStats.AvgWaitTime.Milliseconds(),
		"timestamp":           time.Now().Unix(),
	}

	response.HandleJSON(responseWriter, stats, nil)
}

// GetStatus returns BOSH/Blacksmith status information including service instances.
func (h *Handler) GetStatus(responseWriter http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("bosh-status")
	logger.Debug("BOSH status request")

	ctx := req.Context()

	// Attempt to load instance data from Vault index
	instances := make(map[string]interface{})
	if h.vault != nil {
		vaultInstances := make(map[string]interface{})

		exists, err := h.vault.Get(ctx, "db", &vaultInstances)
		if err != nil {
			logger.Error("Failed to fetch instances from Vault: %v", err)
		} else if exists {
			if vaultInstances != nil {
				instances = vaultInstances
			}

			logger.Debug("Loaded %d instances from Vault index", len(instances))
		} else {
			logger.Debug("Vault index 'db' does not exist; falling back to BOSH deployments")
		}
	} else {
		logger.Debug("Vault dependency is not configured; falling back to BOSH deployments")
	}

	// Fallback to BOSH deployments when Vault data is unavailable
	if len(instances) == 0 && h.director != nil {
		deployments, err := h.director.GetDeployments()
		if err != nil {
			logger.Error("Failed to fetch deployments: %v", err)
		} else {
			for _, deployment := range deployments {
				if deployment.Name == "" || deployment.Name == "blacksmith" {
					continue
				}

				instances[deployment.Name] = map[string]interface{}{
					"instance_id":   deployment.Name,
					"service_id":    extractServiceType(deployment.Name),
					"plan_id":       "standard",
					"organization":  "blacksmith",
					"space":         "services",
					"instance_name": deployment.Name,
				}
			}
		}
	}

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

// extractServiceType attempts to extract service type from deployment name.
func extractServiceType(deploymentName string) string {
	// Common patterns: redis-{guid}, rabbitmq-{guid}, postgresql-{guid}
	if strings.HasPrefix(deploymentName, "redis-") {
		return "redis"
	}

	if strings.HasPrefix(deploymentName, "rabbitmq-") {
		return "rabbitmq"
	}

	if strings.HasPrefix(deploymentName, "postgresql-") {
		return "postgresql"
	}
	// Default to first part before hyphen
	parts := strings.Split(deploymentName, "-")
	if len(parts) > 0 {
		return parts[0]
	}

	return "unknown"
}
