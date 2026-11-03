package metrics

import (
	"fmt"
	"sync"
)

// Registry is a thread-safe container for metrics.
// It provides methods to register, retrieve, and manage metrics.
type Registry struct {
	mu      sync.RWMutex
	metrics map[string]Metric

	// Metric families for vector types
	counterVecs   map[string]*CounterVec
	gaugeVecs     map[string]*GaugeVec
	histogramVecs map[string]*HistogramVec
}

// NewRegistry creates a new metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		metrics:       make(map[string]Metric),
		counterVecs:   make(map[string]*CounterVec),
		gaugeVecs:     make(map[string]*GaugeVec),
		histogramVecs: make(map[string]*HistogramVec),
	}
}

// Register adds a metric to the registry.
// Returns an error if a metric with the same name already exists.
func (r *Registry) Register(name string, metric Metric) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.metrics[name]; exists {
		return fmt.Errorf("metric %q already registered", name)
	}

	r.metrics[name] = metric
	return nil
}

// MustRegister is like Register but panics if registration fails.
func (r *Registry) MustRegister(name string, metric Metric) {
	if err := r.Register(name, metric); err != nil {
		panic(err)
	}
}

// Unregister removes a metric from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.metrics, name)
	delete(r.counterVecs, name)
	delete(r.gaugeVecs, name)
	delete(r.histogramVecs, name)
}

// Get returns the metric with the given name.
// Returns nil if no metric with that name exists.
func (r *Registry) Get(name string) Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.metrics[name]
}

// GetOrCreateCounter returns an existing counter or creates a new one.
func (r *Registry) GetOrCreateCounter(name string, labels Labels) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if a simple counter exists
	if m, exists := r.metrics[name]; exists {
		if counter, ok := m.(*Counter); ok && labels.Equal(counter.labels) {
			return counter
		}
	}

	// Check or create a counter vector
	vec, exists := r.counterVecs[name]
	if !exists {
		vec = NewCounterVec(name, labels.Keys())
		r.counterVecs[name] = vec
	}

	return vec.WithLabels(labels)
}

// GetOrCreateGauge returns an existing gauge or creates a new one.
func (r *Registry) GetOrCreateGauge(name string, labels Labels) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if a simple gauge exists
	if m, exists := r.metrics[name]; exists {
		if gauge, ok := m.(*Gauge); ok && labels.Equal(gauge.labels) {
			return gauge
		}
	}

	// Check or create a gauge vector
	vec, exists := r.gaugeVecs[name]
	if !exists {
		vec = NewGaugeVec(name, labels.Keys())
		r.gaugeVecs[name] = vec
	}

	return vec.WithLabels(labels)
}

// GetOrCreateHistogram returns an existing histogram or creates a new one.
func (r *Registry) GetOrCreateHistogram(name string, labels Labels, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if a simple histogram exists
	if m, exists := r.metrics[name]; exists {
		if hist, ok := m.(*Histogram); ok && labels.Equal(hist.labels) {
			return hist
		}
	}

	// Check or create a histogram vector
	vec, exists := r.histogramVecs[name]
	if !exists {
		vec = NewHistogramVec(name, labels.Keys(), buckets)
		r.histogramVecs[name] = vec
	}

	return vec.WithLabels(labels)
}

// All returns all registered metrics.
func (r *Registry) All() []Metric {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Collect all simple metrics
	metrics := make([]Metric, 0, len(r.metrics))
	for _, m := range r.metrics {
		metrics = append(metrics, m)
	}

	// Collect all metrics from vectors
	for _, cv := range r.counterVecs {
		for _, c := range cv.Collect() {
			metrics = append(metrics, c)
		}
	}
	for _, gv := range r.gaugeVecs {
		for _, g := range gv.Collect() {
			metrics = append(metrics, g)
		}
	}
	for _, hv := range r.histogramVecs {
		for _, h := range hv.Collect() {
			metrics = append(metrics, h)
		}
	}

	return metrics
}

// Reset removes all metrics from the registry.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.metrics = make(map[string]Metric)
	r.counterVecs = make(map[string]*CounterVec)
	r.gaugeVecs = make(map[string]*GaugeVec)
	r.histogramVecs = make(map[string]*HistogramVec)
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (r *Registry) Snapshot() map[string]interface{} {
	metrics := r.All()
	snapshot := make(map[string]interface{}, len(metrics))

	for _, m := range metrics {
		key := m.Name()
		if labels := m.Labels(); len(labels) > 0 {
			key = fmt.Sprintf("%s{%s}", m.Name(), labels.String())
		}
		snapshot[key] = m.Value()
	}

	return snapshot
}

// DefaultRegistry is the global default registry.
var DefaultRegistry = NewRegistry()

// Register adds a metric to the default registry.
func Register(name string, metric Metric) error {
	return DefaultRegistry.Register(name, metric)
}

// MustRegister is like Register but panics if registration fails.
func MustRegister(name string, metric Metric) {
	DefaultRegistry.MustRegister(name, metric)
}

// Unregister removes a metric from the default registry.
func Unregister(name string) {
	DefaultRegistry.Unregister(name)
}

// Get returns the metric with the given name from the default registry.
func Get(name string) Metric {
	return DefaultRegistry.Get(name)
}

// GetOrCreateCounter returns an existing counter or creates a new one
// in the default registry.
func GetOrCreateCounter(name string, labels Labels) *Counter {
	return DefaultRegistry.GetOrCreateCounter(name, labels)
}

// GetOrCreateGauge returns an existing gauge or creates a new one
// in the default registry.
func GetOrCreateGauge(name string, labels Labels) *Gauge {
	return DefaultRegistry.GetOrCreateGauge(name, labels)
}

// GetOrCreateHistogram returns an existing histogram or creates a new one
// in the default registry.
func GetOrCreateHistogram(name string, labels Labels, buckets []float64) *Histogram {
	return DefaultRegistry.GetOrCreateHistogram(name, labels, buckets)
}

// All returns all registered metrics from the default registry.
func All() []Metric {
	return DefaultRegistry.All()
}

// ResetDefaultRegistry resets the default registry.
func ResetDefaultRegistry() {
	DefaultRegistry.Reset()
}
