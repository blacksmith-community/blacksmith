// Package metrics provides a lightweight metrics collection system for graft.
//
// The package provides three core metric types:
//   - Counter: monotonically increasing counter
//   - Gauge: arbitrary value that can go up or down
//   - Histogram: distribution of values across configurable buckets
//
// All metrics are thread-safe and use atomic operations where possible
// for optimal performance in concurrent environments.
package metrics

import (
	"sort"
	"strings"
	"time"
)

// MetricType represents the type of metric.
type MetricType string

const (
	// MetricTypeCounter represents a monotonically increasing counter.
	MetricTypeCounter MetricType = "counter"

	// MetricTypeGauge represents a value that can go up or down.
	MetricTypeGauge MetricType = "gauge"

	// MetricTypeHistogram represents a distribution of values.
	MetricTypeHistogram MetricType = "histogram"

	// MetricTypeSummary represents a summary with configurable quantiles.
	MetricTypeSummary MetricType = "summary"
)

// Metric is the base interface for all metric types.
type Metric interface {
	// Name returns the metric name.
	Name() string

	// Type returns the metric type.
	Type() MetricType

	// Labels returns the metric labels.
	Labels() Labels

	// Value returns the current metric value.
	// The concrete type depends on the metric type:
	//   - Counter: float64
	//   - Gauge: float64
	//   - Histogram: HistogramValue
	Value() interface{}
}

// Labels represents a set of key-value pairs for metric dimensions.
type Labels map[string]string

// NewLabels creates a new Labels instance from key-value pairs.
// The arguments should be provided as alternating key, value pairs.
// Example: NewLabels("method", "GET", "status", "200").
func NewLabels(kvs ...string) Labels {
	if len(kvs)%2 != 0 {
		// If odd number of arguments, ignore the last one
		kvs = kvs[:len(kvs)-1]
	}

	labels := make(Labels, len(kvs)/2)
	for i := 0; i < len(kvs); i += 2 {
		labels[kvs[i]] = kvs[i+1]
	}
	return labels
}

// Clone returns a deep copy of the labels.
func (l Labels) Clone() Labels {
	if l == nil {
		return nil
	}

	clone := make(Labels, len(l))
	for k, v := range l {
		clone[k] = v
	}
	return clone
}

// With returns a new Labels instance with the given key-value pair added.
func (l Labels) With(key, value string) Labels {
	clone := l.Clone()
	if clone == nil {
		clone = make(Labels, 1)
	}
	clone[key] = value
	return clone
}

// Merge returns a new Labels instance with all labels from both sets.
// Values from the other Labels take precedence in case of conflicts.
func (l Labels) Merge(other Labels) Labels {
	if l == nil && other == nil {
		return nil
	}

	result := make(Labels, len(l)+len(other))
	for k, v := range l {
		result[k] = v
	}
	for k, v := range other {
		result[k] = v
	}
	return result
}

// Keys returns all label keys in sorted order.
func (l Labels) Keys() []string {
	if l == nil {
		return nil
	}

	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// String returns a string representation of the labels.
// Format: "key1=value1,key2=value2" (keys are sorted).
func (l Labels) String() string {
	if len(l) == 0 {
		return ""
	}

	keys := l.Keys()
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, ",")
}

// Equal returns true if both Labels have the same key-value pairs.
func (l Labels) Equal(other Labels) bool {
	if len(l) != len(other) {
		return false
	}

	for k, v := range l {
		if ov, ok := other[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

// Sample represents a single metric sample with a value and timestamp.
type Sample struct {
	// Value is the metric value.
	Value float64

	// Timestamp is when the sample was recorded.
	Timestamp time.Time

	// Labels are the dimensions for this sample.
	Labels Labels
}

// NewSample creates a new Sample with the current timestamp.
func NewSample(value float64, labels Labels) Sample {
	return Sample{
		Value:     value,
		Timestamp: time.Now(),
		Labels:    labels,
	}
}

// HistogramValue represents the current state of a histogram.
type HistogramValue struct {
	// Count is the total number of observations.
	Count uint64

	// Sum is the sum of all observed values.
	Sum float64

	// Buckets maps upper bounds to cumulative counts.
	Buckets map[float64]uint64
}

// DefaultHistogramBuckets returns the default bucket boundaries for histograms.
// These are suitable for measuring request latencies in seconds.
func DefaultHistogramBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// LinearBuckets creates bucket boundaries with linear spacing.
// start is the first bucket boundary, width is the spacing between buckets,
// and count is the number of buckets.
func LinearBuckets(start, width float64, count int) []float64 {
	if count <= 0 {
		return nil
	}

	buckets := make([]float64, count)
	for i := 0; i < count; i++ {
		buckets[i] = start + float64(i)*width
	}
	return buckets
}

// ExponentialBuckets creates bucket boundaries with exponential spacing.
// start is the first bucket boundary, factor is the growth factor,
// and count is the number of buckets.
func ExponentialBuckets(start, factor float64, count int) []float64 {
	if count <= 0 || start <= 0 || factor <= 1 {
		return nil
	}

	buckets := make([]float64, count)
	buckets[0] = start
	for i := 1; i < count; i++ {
		buckets[i] = buckets[i-1] * factor
	}
	return buckets
}
