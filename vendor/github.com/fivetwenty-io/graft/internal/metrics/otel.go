package metrics

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// OtelExporter exports metrics in OpenTelemetry Protocol (OTLP) compatible JSON format.
// See: https://opentelemetry.io/docs/specs/otlp/
type OtelExporter struct {
	// ResourceAttributes are additional attributes to include in the resource.
	ResourceAttributes map[string]string
}

// NewOtelExporter creates a new OtelExporter.
func NewOtelExporter() *OtelExporter {
	return &OtelExporter{
		ResourceAttributes: make(map[string]string),
	}
}

// NewOtelExporterWithResource creates a new OtelExporter with resource attributes.
func NewOtelExporterWithResource(attrs map[string]string) *OtelExporter {
	return &OtelExporter{
		ResourceAttributes: attrs,
	}
}

// ContentType returns the MIME content type for OTLP JSON format.
func (e *OtelExporter) ContentType() string {
	return contentTypeJSON
}

// Export converts the given metrics to OTLP JSON format.
func (e *OtelExporter) Export(metrics []Metric) ([]byte, error) {
	request := e.buildExportRequest(metrics)
	return json.Marshal(request)
}

// ExportIndent exports metrics with indented JSON for readability.
func (e *OtelExporter) ExportIndent(metrics []Metric) ([]byte, error) {
	request := e.buildExportRequest(metrics)
	return json.MarshalIndent(request, "", "  ")
}

// safeTimestampNano returns the current Unix timestamp in nanoseconds as a uint64.
// If the timestamp is negative (which should never happen in practice), it returns 0.
func safeTimestampNano() uint64 {
	ts := time.Now().UnixNano()
	if ts < 0 {
		return 0
	}
	return uint64(ts)
}

// buildExportRequest builds the OTLP export request structure.
func (e *OtelExporter) buildExportRequest(metrics []Metric) *otelExportMetricsServiceRequest {
	now := safeTimestampNano()

	// Build resource
	resource := &otelResource{
		Attributes: e.buildResourceAttributes(),
	}

	// Build scope metrics
	scopeMetrics := &otelScopeMetrics{
		Scope: &otelInstrumentationScope{
			Name:    "graft",
			Version: "1.0.0",
		},
		Metrics: e.buildMetrics(metrics, now),
	}

	// Build resource metrics
	resourceMetrics := &otelResourceMetrics{
		Resource:     resource,
		ScopeMetrics: []*otelScopeMetrics{scopeMetrics},
	}

	return &otelExportMetricsServiceRequest{
		ResourceMetrics: []*otelResourceMetrics{resourceMetrics},
	}
}

// buildResourceAttributes builds OTLP resource attributes.
func (e *OtelExporter) buildResourceAttributes() []*otelKeyValue {
	// Default attributes
	attrs := make([]*otelKeyValue, 0, 1+len(e.ResourceAttributes))
	attrs = append(attrs, &otelKeyValue{
		Key:   "service.name",
		Value: &otelAnyValue{StringValue: stringPtr("graft")},
	})

	// Add custom attributes
	for k, v := range e.ResourceAttributes {
		attrs = append(attrs, &otelKeyValue{
			Key:   k,
			Value: &otelAnyValue{StringValue: stringPtr(v)},
		})
	}

	return attrs
}

// buildMetrics converts metrics to OTLP metric format.
func (e *OtelExporter) buildMetrics(metrics []Metric, timestamp uint64) []*otelMetric {
	// Group metrics by name
	grouped := groupMetricsByName(metrics)

	// Sort names for consistent output
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]*otelMetric, 0, len(names))
	for _, name := range names {
		group := grouped[name]
		if len(group) == 0 {
			continue
		}

		metric := e.buildMetric(name, group, timestamp)
		if metric != nil {
			result = append(result, metric)
		}
	}

	return result
}

// buildMetric builds a single OTLP metric from a group of metrics with the same name.
func (e *OtelExporter) buildMetric(name string, metrics []Metric, timestamp uint64) *otelMetric {
	if len(metrics) == 0 {
		return nil
	}

	metric := &otelMetric{
		Name:        name,
		Description: name + " metric",
		Unit:        "",
	}

	// Determine type from first metric
	metricType := metrics[0].Type()

	switch metricType {
	case MetricTypeCounter:
		metric.Sum = e.buildSum(metrics, timestamp)
	case MetricTypeGauge:
		metric.Gauge = e.buildGauge(metrics, timestamp)
	case MetricTypeHistogram:
		metric.Histogram = e.buildHistogram(metrics, timestamp)
	case MetricTypeSummary:
		// Summary type not yet implemented, treat as gauge
		metric.Gauge = e.buildGauge(metrics, timestamp)
	}

	return metric
}

// buildSum builds an OTLP Sum from counter metrics.
func (e *OtelExporter) buildSum(metrics []Metric, timestamp uint64) *otelSum {
	dataPoints := make([]*otelNumberDataPoint, 0, len(metrics))

	for _, m := range metrics {
		dp := &otelNumberDataPoint{
			Attributes:        e.labelsToAttributes(m.Labels()),
			TimeUnixNano:      timestamp,
			StartTimeUnixNano: timestamp,
			AsDouble:          metricValue(m),
		}
		dataPoints = append(dataPoints, dp)
	}

	return &otelSum{
		DataPoints:             dataPoints,
		AggregationTemporality: 2, // AGGREGATION_TEMPORALITY_CUMULATIVE
		IsMonotonic:            true,
	}
}

// buildGauge builds an OTLP Gauge from gauge metrics.
func (e *OtelExporter) buildGauge(metrics []Metric, timestamp uint64) *otelGauge {
	dataPoints := make([]*otelNumberDataPoint, 0, len(metrics))

	for _, m := range metrics {
		dp := &otelNumberDataPoint{
			Attributes:   e.labelsToAttributes(m.Labels()),
			TimeUnixNano: timestamp,
			AsDouble:     metricValue(m),
		}
		dataPoints = append(dataPoints, dp)
	}

	return &otelGauge{
		DataPoints: dataPoints,
	}
}

// buildHistogram builds an OTLP Histogram from histogram metrics.
func (e *OtelExporter) buildHistogram(metrics []Metric, timestamp uint64) *otelHistogram {
	dataPoints := make([]*otelHistogramDataPoint, 0, len(metrics))

	for _, m := range metrics {
		hv, ok := m.Value().(HistogramValue)
		if !ok {
			continue
		}

		// Get sorted bucket boundaries (excluding +Inf)
		bounds := make([]float64, 0, len(hv.Buckets))
		for bound := range hv.Buckets {
			if !math.IsInf(bound, 1) {
				bounds = append(bounds, bound)
			}
		}
		sort.Float64s(bounds)

		// Build bucket counts
		bucketCounts := make([]uint64, 0, len(bounds)+1)
		var prevCount uint64
		for _, bound := range bounds {
			count := hv.Buckets[bound]
			bucketCounts = append(bucketCounts, count-prevCount)
			prevCount = count
		}
		// Add final bucket (> last bound)
		if infCount, ok := hv.Buckets[math.Inf(1)]; ok {
			bucketCounts = append(bucketCounts, infCount-prevCount)
		}

		dp := &otelHistogramDataPoint{
			Attributes:        e.labelsToAttributes(m.Labels()),
			TimeUnixNano:      timestamp,
			StartTimeUnixNano: timestamp,
			Count:             hv.Count,
			Sum:               &hv.Sum,
			ExplicitBounds:    bounds,
			BucketCounts:      bucketCounts,
		}
		dataPoints = append(dataPoints, dp)
	}

	return &otelHistogram{
		DataPoints:             dataPoints,
		AggregationTemporality: 2, // AGGREGATION_TEMPORALITY_CUMULATIVE
	}
}

// labelsToAttributes converts metric labels to OTLP attributes.
func (e *OtelExporter) labelsToAttributes(labels Labels) []*otelKeyValue {
	if len(labels) == 0 {
		return nil
	}

	attrs := make([]*otelKeyValue, 0, len(labels))
	for k, v := range labels {
		attrs = append(attrs, &otelKeyValue{
			Key:   k,
			Value: &otelAnyValue{StringValue: stringPtr(v)},
		})
	}
	return attrs
}

// stringPtr returns a pointer to a string.
func stringPtr(s string) *string {
	return &s
}

// OTLP JSON structures

type otelExportMetricsServiceRequest struct {
	ResourceMetrics []*otelResourceMetrics `json:"resourceMetrics"`
}

type otelResourceMetrics struct {
	Resource     *otelResource       `json:"resource"`
	ScopeMetrics []*otelScopeMetrics `json:"scopeMetrics"`
}

type otelResource struct {
	Attributes []*otelKeyValue `json:"attributes"`
}

type otelScopeMetrics struct {
	Scope   *otelInstrumentationScope `json:"scope"`
	Metrics []*otelMetric             `json:"metrics"`
}

type otelInstrumentationScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otelMetric struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Unit        string         `json:"unit,omitempty"`
	Sum         *otelSum       `json:"sum,omitempty"`
	Gauge       *otelGauge     `json:"gauge,omitempty"`
	Histogram   *otelHistogram `json:"histogram,omitempty"`
}

type otelSum struct {
	DataPoints             []*otelNumberDataPoint `json:"dataPoints"`
	AggregationTemporality int                    `json:"aggregationTemporality"`
	IsMonotonic            bool                   `json:"isMonotonic"`
}

type otelGauge struct {
	DataPoints []*otelNumberDataPoint `json:"dataPoints"`
}

type otelHistogram struct {
	DataPoints             []*otelHistogramDataPoint `json:"dataPoints"`
	AggregationTemporality int                       `json:"aggregationTemporality"`
}

type otelNumberDataPoint struct {
	Attributes        []*otelKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano uint64          `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      uint64          `json:"timeUnixNano"`
	AsDouble          float64         `json:"asDouble"`
}

type otelHistogramDataPoint struct {
	Attributes        []*otelKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano uint64          `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      uint64          `json:"timeUnixNano"`
	Count             uint64          `json:"count"`
	Sum               *float64        `json:"sum,omitempty"`
	ExplicitBounds    []float64       `json:"explicitBounds,omitempty"`
	BucketCounts      []uint64        `json:"bucketCounts,omitempty"`
}

type otelKeyValue struct {
	Key   string        `json:"key"`
	Value *otelAnyValue `json:"value"`
}

type otelAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *int64   `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}
