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

// GetMetrics returns the current metrics
func (m *metricsCollector) GetMetrics() Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Calculate average duration
	var totalDuration time.Duration
	for _, d := range m.durations {
		totalDuration += d
	}

	var avgDuration time.Duration
	if len(m.durations) > 0 {
		avgDuration = totalDuration / time.Duration(len(m.durations))
	}

	return Metrics{
		TotalRuns:            atomic.LoadInt64(&m.totalRuns),
		SuccessfulRuns:       atomic.LoadInt64(&m.successfulRuns),
		FailedRuns:           atomic.LoadInt64(&m.failedRuns),
		TotalDuration:        totalDuration,
		AverageDuration:      avgDuration,
		LastRunDuration:      m.lastRunDuration,
		TotalDeployments:     atomic.LoadInt64(&m.totalDeployments),
		TotalInstancesFound:  atomic.LoadInt64(&m.totalInstancesFound),
		TotalInstancesSynced: atomic.LoadInt64(&m.totalInstancesSynced),
		TotalErrors:          atomic.LoadInt64(&m.totalErrors),
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
	if metrics.TotalRuns > 0 {
		successRate = float64(metrics.SuccessfulRuns) / float64(metrics.TotalRuns) * 100
	}

	return fmt.Sprintf(
		"Reconciler Metrics:\n"+
			"  Total Runs: %d (Success: %d, Failed: %d, Rate: %.1f%%)\n"+
			"  Last Run Duration: %v\n"+
			"  Average Duration: %v\n"+
			"  Total Deployments Scanned: %d\n"+
			"  Total Instances Found: %d\n"+
			"  Total Instances Synced: %d\n"+
			"  Total Errors: %d",
		metrics.TotalRuns, metrics.SuccessfulRuns, metrics.FailedRuns, successRate,
		metrics.LastRunDuration,
		metrics.AverageDuration,
		metrics.TotalDeployments,
		metrics.TotalInstancesFound,
		metrics.TotalInstancesSynced,
		metrics.TotalErrors,
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
		metrics.TotalRuns,
		metrics.SuccessfulRuns,
		metrics.FailedRuns,
		metrics.LastRunDuration.Seconds(),
		metrics.TotalDeployments,
		metrics.TotalInstancesFound,
		metrics.TotalInstancesSynced,
		metrics.TotalErrors,
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
