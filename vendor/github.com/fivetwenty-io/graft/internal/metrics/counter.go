package metrics

import (
	"math"
	"sync/atomic"
)

// Counter is a monotonically increasing metric.
// Counters are useful for tracking things like request counts,
// error counts, or bytes processed.
//
// Counter is safe for concurrent use.
type Counter struct {
	name   string
	labels Labels
	value  uint64 // stored as uint64 bits of float64
}

// NewCounter creates a new Counter with the given name and labels.
func NewCounter(name string, labels Labels) *Counter {
	return &Counter{
		name:   name,
		labels: labels.Clone(),
		value:  0,
	}
}

// Name returns the counter name.
func (c *Counter) Name() string {
	return c.name
}

// Type returns MetricTypeCounter.
func (c *Counter) Type() MetricType {
	return MetricTypeCounter
}

// Labels returns a copy of the counter labels.
func (c *Counter) Labels() Labels {
	return c.labels.Clone()
}

// Value returns the current counter value.
func (c *Counter) Value() interface{} {
	return c.Get()
}

// Inc increments the counter by 1.
func (c *Counter) Inc() {
	c.Add(1)
}

// Add adds the given delta to the counter.
// Delta must be non-negative; negative values are ignored.
func (c *Counter) Add(delta float64) {
	if delta < 0 {
		return
	}

	for {
		current := atomic.LoadUint64(&c.value)
		currentFloat := math.Float64frombits(current)
		newValue := math.Float64bits(currentFloat + delta)
		if atomic.CompareAndSwapUint64(&c.value, current, newValue) {
			return
		}
	}
}

// Get returns the current counter value.
func (c *Counter) Get() float64 {
	return math.Float64frombits(atomic.LoadUint64(&c.value))
}

// Reset resets the counter to zero.
// Note: Counters should generally not be reset in production,
// as this can cause issues with rate calculations.
func (c *Counter) Reset() {
	atomic.StoreUint64(&c.value, 0)
}

// CounterVec is a collection of counters partitioned by labels.
type CounterVec struct {
	name       string
	labelNames []string
	counters   *labeledMetrics
}

// NewCounterVec creates a new CounterVec with the given label names.
func NewCounterVec(name string, labelNames []string) *CounterVec {
	return &CounterVec{
		name:       name,
		labelNames: labelNames,
		counters:   newLabeledMetrics(),
	}
}

// WithLabels returns the Counter for the given label values.
// The label values must match the label names in order.
func (cv *CounterVec) WithLabels(labels Labels) *Counter {
	key := labels.String()
	metric := cv.counters.getOrCreate(key, func() Metric {
		return NewCounter(cv.name, labels)
	})
	if c, ok := metric.(*Counter); ok {
		return c
	}
	return NewCounter(cv.name, labels)
}

// WithLabelValues returns the Counter for the given label values.
// The values must be provided in the same order as the label names.
func (cv *CounterVec) WithLabelValues(values ...string) *Counter {
	if len(values) != len(cv.labelNames) {
		// Return a no-op counter if label count doesn't match
		return NewCounter(cv.name, nil)
	}

	labels := make(Labels, len(cv.labelNames))
	for i, name := range cv.labelNames {
		//nolint:gosec // len(values) == len(cv.labelNames) is checked above
		labels[name] = values[i]
	}
	return cv.WithLabels(labels)
}

// Reset resets all counters in the vector.
func (cv *CounterVec) Reset() {
	cv.counters.reset()
}

// Collect returns all counters in the vector.
func (cv *CounterVec) Collect() []*Counter {
	metrics := cv.counters.all()
	counters := make([]*Counter, 0, len(metrics))
	for _, m := range metrics {
		if c, ok := m.(*Counter); ok {
			counters = append(counters, c)
		}
	}
	return counters
}
