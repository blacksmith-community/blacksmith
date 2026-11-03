package parallel

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ScalingRecommendation indicates whether to scale up, down, or hold.
type ScalingRecommendation int

const (
	// ScaleHold indicates the current scale is appropriate.
	ScaleHold ScalingRecommendation = iota
	// ScaleUp indicates more workers should be added.
	ScaleUp
	// ScaleDown indicates workers should be removed.
	ScaleDown
)

// String returns a string representation of the recommendation.
func (s ScalingRecommendation) String() string {
	switch s {
	case ScaleHold:
		return "hold"
	case ScaleUp:
		return "scale_up"
	case ScaleDown:
		return "scale_down"
	}
	return "hold"
}

// MonitorStats contains current monitoring statistics.
type MonitorStats struct {
	// CPU metrics
	NumCPU          int
	NumGoroutine    int
	CPUUtilization  float64
	GoroutineChange int

	// Memory metrics
	HeapAlloc   uint64
	HeapSys     uint64
	HeapInuse   uint64
	HeapIdle    uint64
	StackInuse  uint64
	NumGC       uint32
	GCPauseNs   uint64
	MemPressure float64

	// Pool metrics (if connected)
	ActiveWorkers int
	PendingTasks  int
	QueueCapacity int
	QueueLoad     float64

	// Recommendations
	Recommendation ScalingRecommendation
	Reason         string

	// Timing
	Timestamp time.Time
	Uptime    time.Duration
}

// MonitorConfig configures the monitor behavior.
type MonitorConfig struct {
	// SampleInterval is how often to collect metrics
	SampleInterval time.Duration

	// HistorySize is the number of samples to keep for averaging
	HistorySize int

	// Thresholds for scaling decisions
	CPUHighThreshold    float64 // Scale up if above (0.0-1.0)
	CPULowThreshold     float64 // Scale down if below (0.0-1.0)
	MemHighThreshold    float64 // Memory pressure high (0.0-1.0)
	QueueHighThreshold  float64 // Queue load high (0.0-1.0)
	QueueLowThreshold   float64 // Queue load low (0.0-1.0)
	IdleWorkerThreshold float64 // Scale down if idle ratio above (0.0-1.0)
}

// DefaultMonitorConfig returns sensible default configuration.
func DefaultMonitorConfig() *MonitorConfig {
	return &MonitorConfig{
		SampleInterval:      100 * time.Millisecond,
		HistorySize:         10,
		CPUHighThreshold:    0.8,
		CPULowThreshold:     0.3,
		MemHighThreshold:    0.9,
		QueueHighThreshold:  0.8,
		QueueLowThreshold:   0.2,
		IdleWorkerThreshold: 0.5,
	}
}

// Monitor tracks system and pool health for scaling decisions.
type Monitor struct {
	config *MonitorConfig

	// State
	startTime time.Time
	lastStats runtime.MemStats
	lastGC    uint32
	samples   []MonitorStats
	sampleIdx int

	// Goroutine tracking
	lastGoroutines int

	// Pool connection
	pool          PoolMetrics
	poolMu        sync.RWMutex
	poolConnected atomic.Bool

	// Control
	stopCh  chan struct{}
	stopped atomic.Bool
	mu      sync.RWMutex
	wg      sync.WaitGroup
}

// PoolMetrics interface for getting pool statistics.
type PoolMetrics interface {
	ActiveWorkers() int
	PendingTasks() int
	QueueCapacity() int
}

// NewMonitor creates a new monitor with the given configuration.
func NewMonitor(config *MonitorConfig) *Monitor {
	if config == nil {
		config = DefaultMonitorConfig()
	}

	m := &Monitor{
		config:    config,
		startTime: time.Now(),
		samples:   make([]MonitorStats, config.HistorySize),
		stopCh:    make(chan struct{}),
	}

	// Initialize with current goroutine count
	m.lastGoroutines = runtime.NumGoroutine()

	return m
}

// ConnectPool connects a pool for monitoring.
func (m *Monitor) ConnectPool(pool PoolMetrics) {
	m.poolMu.Lock()
	m.pool = pool
	m.poolMu.Unlock()
	m.poolConnected.Store(true)
}

// DisconnectPool disconnects the pool.
func (m *Monitor) DisconnectPool() {
	m.poolMu.Lock()
	m.pool = nil
	m.poolMu.Unlock()
	m.poolConnected.Store(false)
}

// Start starts the background monitoring loop.
func (m *Monitor) Start() {
	m.wg.Add(1)
	go m.monitorLoop()
}

// Stop stops the monitor.
func (m *Monitor) Stop() {
	if m.stopped.CompareAndSwap(false, true) {
		close(m.stopCh)
		m.wg.Wait()
	}
}

// Stats returns the current monitoring statistics.
func (m *Monitor) Stats() MonitorStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Get the most recent sample
	idx := m.sampleIdx - 1
	if idx < 0 {
		idx = len(m.samples) - 1
	}

	stats := m.samples[idx]
	if stats.Timestamp.IsZero() {
		// No samples yet, collect one now
		m.mu.RUnlock()
		stats = m.collectStats()
		m.mu.RLock()
	}

	return stats
}

// GetRecommendation returns the current scaling recommendation.
func (m *Monitor) GetRecommendation() (recommendation ScalingRecommendation, reason string) {
	stats := m.Stats()
	return stats.Recommendation, stats.Reason
}

// monitorLoop is the background monitoring loop.
func (m *Monitor) monitorLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(m.config.SampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			stats := m.collectStats()
			m.recordSample(&stats)
		}
	}
}

// collectStats collects current statistics.
func (m *Monitor) collectStats() MonitorStats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	now := time.Now()
	numGoroutines := runtime.NumGoroutine()

	stats := MonitorStats{
		NumCPU:          runtime.NumCPU(),
		NumGoroutine:    numGoroutines,
		GoroutineChange: numGoroutines - m.lastGoroutines,

		HeapAlloc:  memStats.HeapAlloc,
		HeapSys:    memStats.HeapSys,
		HeapInuse:  memStats.HeapInuse,
		HeapIdle:   memStats.HeapIdle,
		StackInuse: memStats.StackInuse,
		NumGC:      memStats.NumGC,

		Timestamp: now,
		Uptime:    now.Sub(m.startTime),
	}

	// Calculate GC pause
	if memStats.NumGC > m.lastGC {
		// Average pause from recent GCs
		pauseIdx := int(memStats.NumGC+255) % 256
		stats.GCPauseNs = memStats.PauseNs[pauseIdx]
	}

	// Calculate memory pressure (heap in use / heap sys)
	if memStats.HeapSys > 0 {
		stats.MemPressure = float64(memStats.HeapInuse) / float64(memStats.HeapSys)
	}

	// Calculate CPU utilization estimate based on goroutines vs CPUs
	// This is a rough estimate since Go doesn't expose CPU time directly
	stats.CPUUtilization = m.estimateCPUUtilization(numGoroutines)

	// Get pool metrics if connected
	m.poolMu.RLock()
	if m.pool != nil {
		stats.ActiveWorkers = m.pool.ActiveWorkers()
		stats.PendingTasks = m.pool.PendingTasks()
		stats.QueueCapacity = m.pool.QueueCapacity()
		if stats.QueueCapacity > 0 {
			stats.QueueLoad = float64(stats.PendingTasks) / float64(stats.QueueCapacity)
		}
	}
	m.poolMu.RUnlock()

	// Determine scaling recommendation
	stats.Recommendation, stats.Reason = m.evaluateScaling(&stats)

	// Update tracking
	m.lastGoroutines = numGoroutines
	m.lastGC = memStats.NumGC
	m.lastStats = memStats

	return stats
}

// estimateCPUUtilization estimates CPU utilization.
// This is a heuristic based on active goroutines relative to CPUs.
func (m *Monitor) estimateCPUUtilization(numGoroutines int) float64 {
	numCPU := runtime.NumCPU()
	gomaxprocs := runtime.GOMAXPROCS(0)

	// Use the smaller of physical CPUs and GOMAXPROCS
	effectiveCPUs := numCPU
	if gomaxprocs < numCPU {
		effectiveCPUs = gomaxprocs
	}

	// Estimate: if goroutines > CPUs, we're likely at high utilization
	// This is a rough estimate since not all goroutines are runnable
	if numGoroutines <= 0 {
		return 0
	}

	ratio := float64(numGoroutines) / float64(effectiveCPUs)
	if ratio > 1.0 {
		// Cap at 1.0 but use a logarithmic curve for high goroutine counts
		return 1.0 - (0.1 / ratio)
	}

	return ratio * 0.8 // Assume not all goroutines are always runnable
}

// evaluateScaling determines the scaling recommendation.
func (m *Monitor) evaluateScaling(stats *MonitorStats) (recommendation ScalingRecommendation, reason string) {
	// Memory pressure takes priority
	if stats.MemPressure > m.config.MemHighThreshold {
		return ScaleDown, "high memory pressure"
	}

	// If pool is connected, use queue-based decisions
	if m.poolConnected.Load() {
		// High queue load - scale up
		if stats.QueueLoad > m.config.QueueHighThreshold {
			return ScaleUp, "high queue load"
		}

		// Low queue load with idle workers - scale down
		if stats.QueueLoad < m.config.QueueLowThreshold && stats.ActiveWorkers > 0 {
			idleRatio := 1.0 - (float64(stats.PendingTasks) / float64(stats.ActiveWorkers+1))
			if idleRatio > m.config.IdleWorkerThreshold {
				return ScaleDown, "low queue load with idle workers"
			}
		}
	}

	// CPU-based decisions
	if stats.CPUUtilization > m.config.CPUHighThreshold {
		return ScaleUp, "high CPU utilization"
	}

	if stats.CPUUtilization < m.config.CPULowThreshold {
		return ScaleDown, "low CPU utilization"
	}

	return ScaleHold, "metrics within acceptable range"
}

// recordSample records a statistics sample.
func (m *Monitor) recordSample(stats *MonitorStats) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.samples[m.sampleIdx] = *stats
	m.sampleIdx = (m.sampleIdx + 1) % len(m.samples)
}

// AverageStats returns averaged statistics over the sample history.
func (m *Monitor) AverageStats() MonitorStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sum MonitorStats
	var count int

	for i := range m.samples {
		s := &m.samples[i]
		if s.Timestamp.IsZero() {
			continue
		}
		count++
		sum.CPUUtilization += s.CPUUtilization
		sum.MemPressure += s.MemPressure
		sum.QueueLoad += s.QueueLoad
		sum.NumGoroutine += s.NumGoroutine
		sum.HeapAlloc += s.HeapAlloc
		sum.ActiveWorkers += s.ActiveWorkers
		sum.PendingTasks += s.PendingTasks
	}

	if count <= 0 {
		return MonitorStats{}
	}

	return MonitorStats{
		NumCPU:         runtime.NumCPU(),
		NumGoroutine:   sum.NumGoroutine / count,
		CPUUtilization: sum.CPUUtilization / float64(count),
		HeapAlloc:      sum.HeapAlloc / uint64(count),
		MemPressure:    sum.MemPressure / float64(count),
		ActiveWorkers:  sum.ActiveWorkers / count,
		PendingTasks:   sum.PendingTasks / count,
		QueueLoad:      sum.QueueLoad / float64(count),
		Timestamp:      time.Now(),
		Uptime:         time.Since(m.startTime),
		Recommendation: m.samples[(m.sampleIdx-1+len(m.samples))%len(m.samples)].Recommendation,
		Reason:         "averaged over sample history",
	}
}

// HealthCheck performs a quick health check and returns issues if any.
func (m *Monitor) HealthCheck() []string {
	stats := m.Stats()
	var issues []string

	if stats.MemPressure > 0.9 {
		issues = append(issues, "critical: memory pressure above 90%")
	} else if stats.MemPressure > 0.8 {
		issues = append(issues, "warning: memory pressure above 80%")
	}

	if stats.NumGoroutine > 10000 {
		issues = append(issues, "warning: high goroutine count")
	}

	if stats.GCPauseNs > uint64(100*time.Millisecond) {
		issues = append(issues, "warning: long GC pauses detected")
	}

	if m.poolConnected.Load() {
		if stats.QueueLoad > 0.95 {
			issues = append(issues, "critical: task queue nearly full")
		} else if stats.QueueLoad > 0.8 {
			issues = append(issues, "warning: task queue load above 80%")
		}
	}

	return issues
}
