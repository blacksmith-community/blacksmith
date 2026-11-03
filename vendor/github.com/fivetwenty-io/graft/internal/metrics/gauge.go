package metrics

import (
	"math"
	"sync/atomic"
)

// Gauge is a metric that can arbitrarily go up or down.
// Gauges are useful for tracking things like temperatures,
// current memory usage, or number of active connections.
//
// Gauge is safe for concurrent use.
type Gauge struct {
	name   string
	labels Labels
	value  uint64 // stored as uint64 bits of float64
}

// NewGauge creates a new Gauge with the given name and labels.
func NewGauge(name string, labels Labels) *Gauge {
	return &Gauge{
		name:   name,
		labels: labels.Clone(),
		value:  0,
	}
}

// Name returns the gauge name.
func (g *Gauge) Name() string {
	return g.name
}

// Type returns MetricTypeGauge.
func (g *Gauge) Type() MetricType {
	return MetricTypeGauge
}

// Labels returns a copy of the gauge labels.
func (g *Gauge) Labels() Labels {
	return g.labels.Clone()
}

// Value returns the current gauge value.
func (g *Gauge) Value() interface{} {
	return g.Get()
}

// Set sets the gauge to the given value.
func (g *Gauge) Set(value float64) {
	atomic.StoreUint64(&g.value, math.Float64bits(value))
}

// Inc increments the gauge by 1.
func (g *Gauge) Inc() {
	g.Add(1)
}

// Dec decrements the gauge by 1.
func (g *Gauge) Dec() {
	g.Add(-1)
}

// Add adds the given delta to the gauge.
func (g *Gauge) Add(delta float64) {
	for {
		current := atomic.LoadUint64(&g.value)
		currentFloat := math.Float64frombits(current)
		newValue := math.Float64bits(currentFloat + delta)
		if atomic.CompareAndSwapUint64(&g.value, current, newValue) {
			return
		}
	}
}

// Sub subtracts the given delta from the gauge.
func (g *Gauge) Sub(delta float64) {
	g.Add(-delta)
}

// Get returns the current gauge value.
func (g *Gauge) Get() float64 {
	return math.Float64frombits(atomic.LoadUint64(&g.value))
}

// SetToCurrentTime sets the gauge to the current Unix timestamp.
func (g *Gauge) SetToCurrentTime() {
	g.Set(float64(unixNano()) / 1e9)
}

// unixNano returns the current time in nanoseconds since Unix epoch.
// This is a var so it can be mocked in tests.
var unixNano = func() int64 {
	return timeNow().UnixNano()
}

// GaugeVec is a collection of gauges partitioned by labels.
type GaugeVec struct {
	name       string
	labelNames []string
	gauges     *labeledMetrics
}

// NewGaugeVec creates a new GaugeVec with the given label names.
func NewGaugeVec(name string, labelNames []string) *GaugeVec {
	return &GaugeVec{
		name:       name,
		labelNames: labelNames,
		gauges:     newLabeledMetrics(),
	}
}

// WithLabels returns the Gauge for the given labels.
func (gv *GaugeVec) WithLabels(labels Labels) *Gauge {
	key := labels.String()
	metric := gv.gauges.getOrCreate(key, func() Metric {
		return NewGauge(gv.name, labels)
	})
	if g, ok := metric.(*Gauge); ok {
		return g
	}
	return NewGauge(gv.name, labels)
}

// WithLabelValues returns the Gauge for the given label values.
// The values must be provided in the same order as the label names.
func (gv *GaugeVec) WithLabelValues(values ...string) *Gauge {
	if len(values) != len(gv.labelNames) {
		// Return a no-op gauge if label count doesn't match
		return NewGauge(gv.name, nil)
	}

	labels := make(Labels, len(gv.labelNames))
	for i, name := range gv.labelNames {
		//nolint:gosec // len(values) == len(gv.labelNames) is checked above
		labels[name] = values[i]
	}
	return gv.WithLabels(labels)
}

// Reset resets all gauges in the vector to zero.
func (gv *GaugeVec) Reset() {
	gv.gauges.reset()
}

// Collect returns all gauges in the vector.
func (gv *GaugeVec) Collect() []*Gauge {
	metrics := gv.gauges.all()
	gauges := make([]*Gauge, 0, len(metrics))
	for _, m := range metrics {
		if g, ok := m.(*Gauge); ok {
			gauges = append(gauges, g)
		}
	}
	return gauges
}
