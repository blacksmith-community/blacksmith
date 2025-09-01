package reconciler

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// metricsCollector collects metrics for the reconciler
type metricsCollector struct {
	mu sync.RWMutex

	// Counters (using atomic for thread safety)
	totalRuns            int64
	successfulRuns       int64
	failedRuns           int64
	totalDeployments     int64
	totalInstancesFound  int64
	totalInstancesSynced int64
	totalErrors          int64

	// Durations
	durations        []time.Duration
	lastRunDuration  time.Duration
	lastRunStartTime time.Time

	// Error tracking
	recentErrors []errorEntry
	maxErrors    int
}

type errorEntry struct {
	Timestamp time.Time
	Error     error
	Context   string
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() MetricsCollector {
	return &metricsCollector{
		durations:    make([]time.Duration, 0, 100),
		recentErrors: make([]errorEntry, 0, 50),
		maxErrors:    50,
	}
}

// ReconciliationStarted marks the start of a reconciliation run
func (m *metricsCollector) ReconciliationStarted() {
	atomic.AddInt64(&m.totalRuns, 1)
	m.mu.Lock()
	m.lastRunStartTime = time.Now()
	m.mu.Unlock()
	m.logDebug("Reconciliation run %d started", atomic.LoadInt64(&m.totalRuns))
}

// ReconciliationCompleted marks the completion of a reconciliation run
func (m *metricsCollector) ReconciliationCompleted(duration time.Duration) {
	atomic.AddInt64(&m.successfulRuns, 1)

	m.mu.Lock()
	m.lastRunDuration = duration
	m.durations = append(m.durations, duration)

	// Keep only last 100 durations for average calculation
	if len(m.durations) > 100 {
		m.durations = m.durations[len(m.durations)-100:]
	}
	m.mu.Unlock()

	m.logDebug("Reconciliation run completed in %v", duration)
}

// ReconciliationError records a reconciliation error
func (m *metricsCollector) ReconciliationError(err error) {
	atomic.AddInt64(&m.failedRuns, 1)
	atomic.AddInt64(&m.totalErrors, 1)

	m.mu.Lock()
	m.recentErrors = append(m.recentErrors, errorEntry{
		Timestamp: time.Now(),
		Error:     err,
		Context:   "reconciliation",
	})

	// Keep only recent errors
	if len(m.recentErrors) > m.maxErrors {
		m.recentErrors = m.recentErrors[len(m.recentErrors)-m.maxErrors:]
	}
	m.mu.Unlock()

	m.logError("Reconciliation error: %s", err)
}

// ReconciliationSkipped records when a reconciliation is skipped
func (m *metricsCollector) ReconciliationSkipped() {
	m.logDebug("Reconciliation skipped")
}

// DeploymentsScanned records the number of deployments scanned
func (m *metricsCollector) DeploymentsScanned(count int) {
	atomic.AddInt64(&m.totalDeployments, int64(count))
	m.logDebug("Scanned %d deployments", count)
}

// InstancesMatched records the number of instances matched
func (m *metricsCollector) InstancesMatched(count int) {
	atomic.AddInt64(&m.totalInstancesFound, int64(count))
	m.logDebug("Matched %d instances", count)
}

// InstancesUpdated records the number of instances updated
func (m *metricsCollector) InstancesUpdated(count int) {
	atomic.AddInt64(&m.totalInstancesSynced, int64(count))
	m.logDebug("Updated %d instances", count)
}

// Collect collects metrics (implementation required by interface)
func (m *metricsCollector) Collect() {
	// This method can be used to push metrics to external systems
	// For now, it's a no-op as metrics are collected in real-time
	m.logDebug("Metrics collection triggered")
}

// GetMetrics returns the current metrics
func (m *metricsCollector) GetMetrics() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return Metrics{
		ReconciliationRuns:     atomic.LoadInt64(&m.totalRuns),
		ReconciliationFailures: atomic.LoadInt64(&m.failedRuns),
		InstancesProcessed:     atomic.LoadInt64(&m.totalInstancesFound),
		InstancesUpdated:       atomic.LoadInt64(&m.totalInstancesSynced),
		InstancesFailed:        atomic.LoadInt64(&m.totalErrors),
		LastRunTime:            m.lastRunStartTime,
		LastRunDuration:        m.lastRunDuration,
	}
}

// GetRecentErrors returns recent errors
func (m *metricsCollector) GetRecentErrors() []errorEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Return a copy to avoid race conditions
	errors := make([]errorEntry, len(m.recentErrors))
	copy(errors, m.recentErrors)
	return errors
}

// Reset resets all metrics
func (m *metricsCollector) Reset() {
	atomic.StoreInt64(&m.totalRuns, 0)
	atomic.StoreInt64(&m.successfulRuns, 0)
	atomic.StoreInt64(&m.failedRuns, 0)
	atomic.StoreInt64(&m.totalDeployments, 0)
	atomic.StoreInt64(&m.totalInstancesFound, 0)
	atomic.StoreInt64(&m.totalInstancesSynced, 0)
	atomic.StoreInt64(&m.totalErrors, 0)

	m.mu.Lock()
	m.durations = make([]time.Duration, 0, 100)
	m.lastRunDuration = 0
	m.recentErrors = make([]errorEntry, 0, m.maxErrors)
	m.mu.Unlock()

	m.logInfo("Metrics reset")
}

// String returns a string representation of the metrics
func (m *metricsCollector) String() string {
	metrics := m.GetMetrics()

	successRate := float64(0)
	if metrics.ReconciliationRuns > 0 {
		successRate = float64(metrics.ReconciliationRuns-metrics.ReconciliationFailures) / float64(metrics.ReconciliationRuns) * 100
	}

	return fmt.Sprintf(
		"Reconciler Metrics:\n"+
			"  Total Runs: %d (Success: %d, Failed: %d, Rate: %.1f%%)\n"+
			"  Last Run Duration: %v\n"+
			"  Last Run Time: %v\n"+
			"  Total Instances Processed: %d\n"+
			"  Total Instances Updated: %d\n"+
			"  Total Instances Failed: %d",
		metrics.ReconciliationRuns,
		metrics.ReconciliationRuns-metrics.ReconciliationFailures,
		metrics.ReconciliationFailures,
		successRate,
		metrics.LastRunDuration,
		metrics.LastRunTime,
		metrics.InstancesProcessed,
		metrics.InstancesUpdated,
		metrics.InstancesFailed,
	)
}

// PrometheusMetrics returns metrics in Prometheus format (for future use)
func (m *metricsCollector) PrometheusMetrics() string {
	metrics := m.GetMetrics()

	return fmt.Sprintf(
		"# HELP blacksmith_reconciler_runs_total Total number of reconciliation runs\n"+
			"# TYPE blacksmith_reconciler_runs_total counter\n"+
			"blacksmith_reconciler_runs_total %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_runs_successful Total number of successful reconciliation runs\n"+
			"# TYPE blacksmith_reconciler_runs_successful counter\n"+
			"blacksmith_reconciler_runs_successful %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_runs_failed Total number of failed reconciliation runs\n"+
			"# TYPE blacksmith_reconciler_runs_failed counter\n"+
			"blacksmith_reconciler_runs_failed %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_last_run_duration_seconds Duration of the last reconciliation run\n"+
			"# TYPE blacksmith_reconciler_last_run_duration_seconds gauge\n"+
			"blacksmith_reconciler_last_run_duration_seconds %f\n"+
			"\n"+
			"# HELP blacksmith_reconciler_deployments_total Total number of deployments scanned\n"+
			"# TYPE blacksmith_reconciler_deployments_total counter\n"+
			"blacksmith_reconciler_deployments_total %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_instances_found_total Total number of instances found\n"+
			"# TYPE blacksmith_reconciler_instances_found_total counter\n"+
			"blacksmith_reconciler_instances_found_total %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_instances_synced_total Total number of instances synced\n"+
			"# TYPE blacksmith_reconciler_instances_synced_total counter\n"+
			"blacksmith_reconciler_instances_synced_total %d\n"+
			"\n"+
			"# HELP blacksmith_reconciler_errors_total Total number of errors\n"+
			"# TYPE blacksmith_reconciler_errors_total counter\n"+
			"blacksmith_reconciler_errors_total %d\n",
		metrics.ReconciliationRuns,
		metrics.ReconciliationRuns-metrics.ReconciliationFailures,
		metrics.ReconciliationFailures,
		metrics.LastRunDuration.Seconds(),
		atomic.LoadInt64(&m.totalDeployments),
		metrics.InstancesProcessed,
		metrics.InstancesUpdated,
		metrics.InstancesFailed,
	)
}

// Logging helper methods - these will be replaced with actual logger calls
func (m *metricsCollector) logDebug(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	if false { // Debug disabled by default
		fmt.Printf("[DEBUG] metrics: "+format+"\n", args...)
	}
}

func (m *metricsCollector) logInfo(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[INFO] metrics: "+format+"\n", args...)
}

func (m *metricsCollector) logError(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[ERROR] metrics: "+format+"\n", args...)
}
