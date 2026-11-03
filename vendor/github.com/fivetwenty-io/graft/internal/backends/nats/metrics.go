package natsbackend

import (
	"sync"
	"time"
)

// OperationStats tracks statistics for a type of operation.
type OperationStats struct {
	Count         int64
	TotalDuration time.Duration
	Errors        int64
	CacheHits     int64
	LastAccess    time.Time
}

// Metrics provides observability for NATS operations.
type Metrics struct {
	mu         sync.RWMutex
	operations map[string]*OperationStats
	startTime  time.Time
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		operations: make(map[string]*OperationStats),
		startTime:  time.Now(),
	}
}

// RecordOperation records a completed operation.
func (m *Metrics) RecordOperation(operationType string, duration time.Duration, isError bool, isCacheHit bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats, exists := m.operations[operationType]
	if !exists {
		stats = &OperationStats{}
		m.operations[operationType] = stats
	}

	stats.Count++
	stats.TotalDuration += duration
	stats.LastAccess = time.Now()

	if isError {
		stats.Errors++
	}
	if isCacheHit {
		stats.CacheHits++
	}
}

// GetStats returns a copy of all operation statistics.
func (m *Metrics) GetStats() map[string]OperationStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]OperationStats)
	for key, stats := range m.operations {
		result[key] = *stats
	}
	return result
}

// Uptime returns the duration since the metrics were initialized.
func (m *Metrics) Uptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Since(m.startTime)
}

// GlobalMetrics is the global metrics instance for NATS operations.
var GlobalMetrics = NewMetrics()

// GetMetrics returns current NATS operator metrics.
func GetMetrics() map[string]interface{} {
	stats := GlobalMetrics.GetStats()
	result := make(map[string]interface{})

	for opType, opStats := range stats {
		avgDuration := float64(0)
		if opStats.Count > 0 {
			avgDuration = float64(opStats.TotalDuration) / float64(opStats.Count) / float64(time.Millisecond)
		}

		cacheHitRate := float64(0)
		if opStats.Count > 0 {
			cacheHitRate = float64(opStats.CacheHits) / float64(opStats.Count) * 100
		}

		result[opType] = map[string]interface{}{
			"total_operations":   opStats.Count,
			"total_errors":       opStats.Errors,
			"cache_hits":         opStats.CacheHits,
			"cache_hit_rate_pct": cacheHitRate,
			"avg_duration_ms":    avgDuration,
			"last_access":        opStats.LastAccess,
		}
	}

	result["operator_uptime"] = GlobalMetrics.Uptime().String()
	result["cache_size"] = Cache.Len()
	result["pool_connections"] = ConnPool.ConnectionCount()

	return result
}
