package metrics

import (
	"encoding/json"
	"math"
	"sort"
)

// JSONExporter exports metrics as a simple JSON array.
type JSONExporter struct {
	// PrettyPrint enables indented JSON output.
	PrettyPrint bool
}

// NewJSONExporter creates a new JSONExporter.
func NewJSONExporter(prettyPrint bool) *JSONExporter {
	return &JSONExporter{
		PrettyPrint: prettyPrint,
	}
}

// ContentType returns the MIME content type for JSON format.
func (e *JSONExporter) ContentType() string {
	return contentTypeJSON
}

// Export converts the given metrics to JSON format.
func (e *JSONExporter) Export(metrics []Metric) ([]byte, error) {
	output := e.buildOutput(metrics)

	if e.PrettyPrint {
		return json.MarshalIndent(output, "", "  ")
	}
	return json.Marshal(output)
}

// buildOutput builds the JSON output structure.
func (e *JSONExporter) buildOutput(metrics []Metric) *jsonOutput {
	sortedMetrics := sortMetrics(metrics)
	jsonMetrics := make([]*jsonMetric, 0, len(sortedMetrics))

	for _, m := range sortedMetrics {
		jm := e.buildMetric(m)
		jsonMetrics = append(jsonMetrics, jm)
	}

	return &jsonOutput{
		Metrics: jsonMetrics,
	}
}

// buildMetric builds a single JSON metric.
func (e *JSONExporter) buildMetric(m Metric) *jsonMetric {
	jm := &jsonMetric{
		Name:   m.Name(),
		Type:   string(m.Type()),
		Labels: m.Labels(),
	}

	switch m.Type() {
	case MetricTypeCounter, MetricTypeGauge:
		jm.Value = metricValue(m)
	case MetricTypeHistogram:
		if hv, ok := m.Value().(HistogramValue); ok {
			jm.Histogram = e.buildHistogramValue(hv)
		}
	case MetricTypeSummary:
		// Summary type not yet implemented, treat as simple value
		jm.Value = metricValue(m)
	}

	return jm
}

// buildHistogramValue builds the histogram-specific JSON fields.
func (e *JSONExporter) buildHistogramValue(hv HistogramValue) *jsonHistogramValue {
	// Get sorted bucket boundaries
	bounds := make([]float64, 0, len(hv.Buckets))
	for bound := range hv.Buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)

	buckets := make([]*jsonBucket, 0, len(bounds))
	for _, bound := range bounds {
		bucket := &jsonBucket{
			Count: hv.Buckets[bound],
		}
		// Handle +Inf specially since JSON doesn't support infinity
		if math.IsInf(bound, 1) {
			bucket.LeInf = true
		} else {
			bucket.Le = &bound
		}
		buckets = append(buckets, bucket)
	}

	return &jsonHistogramValue{
		Count:   hv.Count,
		Sum:     hv.Sum,
		Buckets: buckets,
	}
}

// JSON output structures

type jsonOutput struct {
	Metrics []*jsonMetric `json:"metrics"`
}

type jsonMetric struct {
	Name      string              `json:"name"`
	Type      string              `json:"type"`
	Value     float64             `json:"value,omitempty"`
	Labels    Labels              `json:"labels,omitempty"`
	Histogram *jsonHistogramValue `json:"histogram,omitempty"`
}

type jsonHistogramValue struct {
	Count   uint64        `json:"count"`
	Sum     float64       `json:"sum"`
	Buckets []*jsonBucket `json:"buckets"`
}

type jsonBucket struct {
	Le    *float64 `json:"le,omitempty"`
	LeInf bool     `json:"le_inf,omitempty"`
	Count uint64   `json:"count"`
}
