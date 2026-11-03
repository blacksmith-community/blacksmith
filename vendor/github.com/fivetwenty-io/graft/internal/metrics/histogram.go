package metrics

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// timeNow is a variable for mocking time in tests.
var timeNow = time.Now

// Histogram tracks the distribution of values across configurable buckets.
// Observations are placed into buckets based on their value, allowing for
// efficient calculation of quantiles without storing all observations.
//
// Histogram is safe for concurrent use.
type Histogram struct {
	name    string
	labels  Labels
	buckets []float64 // upper bounds, sorted

	// Atomic counters for each bucket
	bucketCounts []uint64

	// Total count and sum
	count uint64
	sum   uint64 // stored as uint64 bits of float64

	// Mutex for operations that need consistency across multiple fields
	mu sync.Mutex
}

// NewHistogram creates a new Histogram with the given name, labels, and buckets.
// If buckets is nil or empty, DefaultHistogramBuckets() is used.
// Buckets are automatically sorted and deduplicated.
func NewHistogram(name string, labels Labels, buckets []float64) *Histogram {
	if len(buckets) == 0 {
		buckets = DefaultHistogramBuckets()
	}

	// Sort and deduplicate buckets
	sortedBuckets := make([]float64, len(buckets))
	copy(sortedBuckets, buckets)
	sort.Float64s(sortedBuckets)

	// Deduplicate
	deduped := make([]float64, 0, len(sortedBuckets))
	for i, b := range sortedBuckets {
		if i == 0 || b != sortedBuckets[i-1] {
			deduped = append(deduped, b)
		}
	}

	// Add +Inf bucket if not present
	if len(deduped) == 0 || deduped[len(deduped)-1] != math.Inf(1) {
		deduped = append(deduped, math.Inf(1))
	}

	return &Histogram{
		name:         name,
		labels:       labels.Clone(),
		buckets:      deduped,
		bucketCounts: make([]uint64, len(deduped)),
		count:        0,
		sum:          0,
	}
}

// Name returns the histogram name.
func (h *Histogram) Name() string {
	return h.name
}

// Type returns MetricTypeHistogram.
func (h *Histogram) Type() MetricType {
	return MetricTypeHistogram
}

// Labels returns a copy of the histogram labels.
func (h *Histogram) Labels() Labels {
	return h.labels.Clone()
}

// Value returns the current histogram state as a HistogramValue.
func (h *Histogram) Value() interface{} {
	return HistogramValue{
		Count:   h.Count(),
		Sum:     h.Sum(),
		Buckets: h.Buckets(),
	}
}

// Observe records a value in the histogram.
func (h *Histogram) Observe(value float64) {
	// Find the bucket for this value
	idx := sort.SearchFloat64s(h.buckets, value)
	if idx < len(h.buckets) {
		atomic.AddUint64(&h.bucketCounts[idx], 1)
	}

	// Increment count
	atomic.AddUint64(&h.count, 1)

	// Add to sum using CAS loop
	for {
		current := atomic.LoadUint64(&h.sum)
		currentFloat := math.Float64frombits(current)
		newValue := math.Float64bits(currentFloat + value)
		if atomic.CompareAndSwapUint64(&h.sum, current, newValue) {
			break
		}
	}
}

// ObserveDuration observes the duration since the given start time in seconds.
func (h *Histogram) ObserveDuration(start time.Time) {
	h.Observe(time.Since(start).Seconds())
}

// Timer returns a Timer that will observe the duration when stopped.
func (h *Histogram) Timer() *Timer {
	return &Timer{
		histogram: h,
		start:     timeNow(),
	}
}

// Count returns the total number of observations.
func (h *Histogram) Count() uint64 {
	return atomic.LoadUint64(&h.count)
}

// Sum returns the sum of all observed values.
func (h *Histogram) Sum() float64 {
	return math.Float64frombits(atomic.LoadUint64(&h.sum))
}

// Buckets returns a map of bucket upper bounds to cumulative counts.
// The returned map includes all buckets up to and including +Inf.
func (h *Histogram) Buckets() map[float64]uint64 {
	result := make(map[float64]uint64, len(h.buckets))
	var cumulative uint64

	for i, bound := range h.buckets {
		cumulative += atomic.LoadUint64(&h.bucketCounts[i])
		result[bound] = cumulative
	}

	return result
}

// Reset resets the histogram.
func (h *Histogram) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := range h.bucketCounts {
		atomic.StoreUint64(&h.bucketCounts[i], 0)
	}
	atomic.StoreUint64(&h.count, 0)
	atomic.StoreUint64(&h.sum, 0)
}

// Mean returns the mean of all observed values.
// Returns 0 if no observations have been made.
func (h *Histogram) Mean() float64 {
	count := h.Count()
	if count == 0 {
		return 0
	}
	return h.Sum() / float64(count)
}

// Timer is a helper for timing operations.
type Timer struct {
	histogram *Histogram
	start     time.Time
}

// ObserveDuration observes the duration since the timer was created.
func (t *Timer) ObserveDuration() time.Duration {
	duration := timeNow().Sub(t.start)
	t.histogram.Observe(duration.Seconds())
	return duration
}

// HistogramVec is a collection of histograms partitioned by labels.
type HistogramVec struct {
	name       string
	labelNames []string
	buckets    []float64
	histograms *labeledMetrics
}

// NewHistogramVec creates a new HistogramVec with the given label names and buckets.
func NewHistogramVec(name string, labelNames []string, buckets []float64) *HistogramVec {
	return &HistogramVec{
		name:       name,
		labelNames: labelNames,
		buckets:    buckets,
		histograms: newLabeledMetrics(),
	}
}

// WithLabels returns the Histogram for the given labels.
func (hv *HistogramVec) WithLabels(labels Labels) *Histogram {
	key := labels.String()
	metric := hv.histograms.getOrCreate(key, func() Metric {
		return NewHistogram(hv.name, labels, hv.buckets)
	})
	if h, ok := metric.(*Histogram); ok {
		return h
	}
	return NewHistogram(hv.name, labels, hv.buckets)
}

// WithLabelValues returns the Histogram for the given label values.
// The values must be provided in the same order as the label names.
func (hv *HistogramVec) WithLabelValues(values ...string) *Histogram {
	if len(values) != len(hv.labelNames) {
		// Return a no-op histogram if label count doesn't match
		return NewHistogram(hv.name, nil, hv.buckets)
	}

	labels := make(Labels, len(hv.labelNames))
	for i, name := range hv.labelNames {
		//nolint:gosec // len(values) == len(hv.labelNames) is checked above
		labels[name] = values[i]
	}
	return hv.WithLabels(labels)
}

// Reset resets all histograms in the vector.
func (hv *HistogramVec) Reset() {
	hv.histograms.reset()
}

// Collect returns all histograms in the vector.
func (hv *HistogramVec) Collect() []*Histogram {
	metrics := hv.histograms.all()
	histograms := make([]*Histogram, 0, len(metrics))
	for _, m := range metrics {
		if h, ok := m.(*Histogram); ok {
			histograms = append(histograms, h)
		}
	}
	return histograms
}

// labeledMetrics is a thread-safe map of label key to metric.
type labeledMetrics struct {
	mu      sync.RWMutex
	metrics map[string]Metric
}

// newLabeledMetrics creates a new labeledMetrics.
func newLabeledMetrics() *labeledMetrics {
	return &labeledMetrics{
		metrics: make(map[string]Metric),
	}
}

// getOrCreate returns an existing metric or creates a new one.
func (lm *labeledMetrics) getOrCreate(key string, create func() Metric) Metric {
	// Fast path: read lock
	lm.mu.RLock()
	m, ok := lm.metrics[key]
	lm.mu.RUnlock()

	if ok {
		return m
	}

	// Slow path: write lock
	lm.mu.Lock()
	defer lm.mu.Unlock()

	// Double-check after acquiring write lock
	if existing, ok := lm.metrics[key]; ok {
		return existing
	}

	m = create()
	lm.metrics[key] = m
	return m
}

// all returns all metrics.
func (lm *labeledMetrics) all() []Metric {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	metrics := make([]Metric, 0, len(lm.metrics))
	for _, m := range lm.metrics {
		metrics = append(metrics, m)
	}
	return metrics
}

// reset removes all metrics.
func (lm *labeledMetrics) reset() {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.metrics = make(map[string]Metric)
}
